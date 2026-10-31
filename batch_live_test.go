//go:build live

package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestLiveWatcherBatchPackets(t *testing.T) {
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

	var srcClient string
	switch src.Config.ChainID {
	case link.ChainA:
		srcClient = link.ClientA
	case link.ChainB:
		srcClient = link.ClientB
	default:
		t.Fatalf("source chain %s is not in link %s", src.Config.ChainID, link.ID)
	}

	receiver, err := relayerAddress(dst.Config)
	if err != nil {
		t.Fatal(err)
	}
	const count = 3
	firstSequence, timeout := sendPacketsBatch(t, src.Config, srcClient, denom, "1000", receiver, 120, count)
	t.Logf("sent sequences %d..%d timeout %d", firstSequence, firstSequence+count-1, timeout)

	opts := relayOptions{
		LinkID: link.ID, Once: true, Interval: time.Second,
		Finality: 1, Lookback: 1000, BatchSize: count,
	}
	for cycle := 0; cycle < 6; cycle++ {
		if err := runRelay(cfg, statePath, opts); err != nil {
			t.Fatal(err)
		}
		packets, err := store.Packets(link.ID)
		if err != nil {
			t.Fatal(err)
		}
		statuses := make(map[int64]string)
		acked := 0
		for _, packet := range packets {
			if packet.SourceClient != srcClient || packet.Sequence < firstSequence || packet.Sequence >= firstSequence+count {
				continue
			}
			statuses[packet.Sequence] = packet.Status
			if packet.Status == PacketAcked {
				acked++
			}
		}
		t.Logf("cycle %d: %v", cycle+1, statuses)
		if acked == count {
			return
		}
		if len(statuses) > count {
			t.Fatalf("ingested %d packets, want %d", len(statuses), count)
		}
		if cycle == 5 {
			t.Fatalf("ingested %d packets, want %d", len(statuses), count)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("packets were not all acked")
}

func sendPacketsBatch(t *testing.T, src ChainConfig, srcClient, denom, amount, receiver string, timeoutSeconds int64, count int) (int64, int64) {
	t.Helper()
	height, err := latestHeight(src)
	if err != nil {
		t.Fatal(err)
	}
	header, err := blockAt(src, height)
	if err != nil {
		t.Fatal(err)
	}
	blockTime, err := time.Parse(time.RFC3339Nano, header.Time)
	if err != nil {
		t.Fatal(err)
	}
	timeout := blockTime.Unix() + timeoutSeconds
	signer, err := relayerAddress(src)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{
		"denom": denom, "amount": amount, "sender": signer,
		"receiver": receiver, "memo": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"source_port": "transfer", "destination_port": "transfer",
		"version": "ics20-1", "encoding": "application/json",
		"value": base64.StdEncoding.EncodeToString(raw),
	}
	seqRes, err := queryJSON(src, "ibc", "channelv2", "next-sequence-send", srcClient)
	if err != nil {
		t.Fatal(err)
	}
	first, err := asInt64(seqRes["next_sequence_send"])
	if err != nil {
		t.Fatal(err)
	}
	messages := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		messages = append(messages, map[string]any{
			"@type":             "/ibc.core.channel.v2.MsgSendPacket",
			"source_client":     srcClient,
			"timeout_timestamp": strconv.FormatInt(timeout, 10),
			"payloads":          []any{payload},
			"signer":            signer,
		})
	}
	hash, err := broadcastMessages(src, messages)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitTx(src, hash); err != nil {
		t.Fatal(err)
	}
	return first, timeout
}
