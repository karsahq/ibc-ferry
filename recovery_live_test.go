//go:build live

package main

import (
	"os"
	"testing"
	"time"
)

func TestLiveAckRecovery(t *testing.T) {
	configPath := os.Getenv("FERRY_LIVE_CONFIG")
	statePath := os.Getenv("FERRY_LIVE_STATE")
	srcName := os.Getenv("FERRY_LIVE_SRC")
	dstName := os.Getenv("FERRY_LIVE_DST")
	denom := os.Getenv("FERRY_LIVE_DENOM")
	if configPath == "" || statePath == "" || srcName == "" || dstName == "" || denom == "" {
		t.Skip("set FERRY_LIVE_CONFIG, FERRY_LIVE_STATE, FERRY_LIVE_SRC, FERRY_LIVE_DST and FERRY_LIVE_DENOM")
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	src, err := cfg.chainByRef(srcName)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := cfg.chainByRef(dstName)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openLinkStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	links, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("expected exactly one link, got %d", len(links))
	}
	link := links[0]

	var srcClient, dstClient, direction string
	switch src.Config.ChainID {
	case link.ChainA:
		srcClient, dstClient, direction = link.ClientA, link.ClientB, directionAToB
	case link.ChainB:
		srcClient, dstClient, direction = link.ClientB, link.ClientA, directionBToA
	default:
		t.Fatalf("source chain %s is not in link %s", src.Config.ChainID, link.ID)
	}

	receiver, err := relayerAddress(dst.Config)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := sendPacketWithTimeout(src.Config, srcClient, denom, "1000", receiver, 120)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent sequence %d", sent.Sequence)

	var commitmentProof proof
	deadline := time.Now().Add(20 * time.Second)
	for {
		commitmentProof, err = getProof(src.Config, srcClient, sent.Sequence)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	if err := ensureClientAtHeight(dst.Config, src.Config, dstClient, commitmentProof.HeightNum); err != nil {
		t.Fatal(err)
	}
	recvHash, _, err := recvPacketWithPayloads(
		dst.Config, dstClient, srcClient,
		sent.Sequence, sent.Timeout, []map[string]any{sent.Payload},
		commitmentProof.Height, commitmentProof.Commitment,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("external recv tx %s", recvHash)

	// Move scan heights past the recv block so the watcher cannot just read the
	// write_acknowledgement event from a block scan.
	srcLatest, err := latestHeight(src.Config)
	if err != nil {
		t.Fatal(err)
	}
	dstLatest, err := latestHeight(dst.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetScanHeight(link.ID, src.Config.ChainID, srcLatest); err != nil {
		t.Fatal(err)
	}
	if err := store.SetScanHeight(link.ID, dst.Config.ChainID, dstLatest); err != nil {
		t.Fatal(err)
	}

	payload := packetPayload{
		SourcePort:      str(sent.Payload["source_port"]),
		DestinationPort: str(sent.Payload["destination_port"]),
		Version:         str(sent.Payload["version"]),
		Encoding:        str(sent.Payload["encoding"]),
		Value:           str(sent.Payload["value"]),
	}
	if _, err := store.InsertPacket(PacketRecord{
		LinkID:            link.ID,
		Direction:         direction,
		Sequence:          sent.Sequence,
		SourceClient:      srcClient,
		DestinationClient: dstClient,
		Payloads:          []packetPayload{payload},
		TimeoutTimestamp:  sent.Timeout,
		Status:            PacketRecvSent,
	}); err != nil {
		t.Fatal(err)
	}

	if err := runRelay(cfg, statePath, relayOptions{
		LinkID: link.ID, Once: true, Interval: time.Second,
		Finality: 0, Lookback: 1, BatchSize: 10,
	}); err != nil {
		t.Fatal(err)
	}
	packets, err := store.Packets(link.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, packet := range packets {
		if packet.Sequence != sent.Sequence || packet.SourceClient != srcClient {
			continue
		}
		t.Logf("status=%s ack_tx=%s", packet.Status, packet.AckTxHash)
		if packet.Status != PacketAcked || packet.AckTxHash == "" {
			t.Fatalf("ack was not recovered and relayed: %+v", packet)
		}
		return
	}
	t.Fatalf("packet %d was not found", sent.Sequence)
}
