//go:build live

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestLiveCallbackGas relays a packet whose memo asks for an EVM destination
// callback and checks that the receive transaction carries the reserved gas.
// The callback target has no code, so the chain answers with an error
// acknowledgement; the point here is the gas limit the relayer submits, not a
// successful callback execution.
func TestLiveCallbackGas(t *testing.T) {
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
	// FERRY_LIVE_RECEIVER and FERRY_LIVE_MEMO let the harness point the packet
	// at a deployed callback contract and the isolated receiver address the
	// chain derives for it. The memo must still ask for callbackGas.
	if override := os.Getenv("FERRY_LIVE_RECEIVER"); override != "" {
		receiver = override
	}
	const callbackGas = 1_000_000
	memo := fmt.Sprintf(
		`{"dest_callback":{"address":"0x1234567890123456789012345678901234567890","gas_limit":"%d"}}`,
		callbackGas,
	)
	if override := os.Getenv("FERRY_LIVE_MEMO"); override != "" {
		memo = override
	}
	sequence, err := sendPacketWithMemo(t, src.Config, srcClient, denom, "1000", receiver, 120, memo)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent sequence %d with a %d gas destination callback", sequence, callbackGas)

	opts := relayOptions{
		LinkID: link.ID, Once: true, Interval: time.Second,
		Finality: 1, Lookback: 1000, BatchSize: 4,
	}
	deadline := time.Now().Add(3 * time.Minute)
	var record PacketRecord
	for time.Now().Before(deadline) {
		if err := runRelay(cfg, statePath, opts); err != nil {
			t.Fatal(err)
		}
		packets, err := store.Packets(link.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, packet := range packets {
			if packet.Sequence == sequence && packet.SourceClient == srcClient {
				record = packet
			}
		}
		if record.Status == PacketAcked && record.RecvTxHash != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if record.RecvTxHash == "" {
		t.Fatalf("packet %d was not relayed: %+v", sequence, record)
	}

	tx, err := queryJSON(dst.Config, "tx", record.RecvTxHash)
	if err != nil {
		t.Fatal(err)
	}
	gasWanted, err := asInt64(tx["gas_wanted"])
	if err != nil {
		t.Fatalf("gas_wanted: %v", err)
	}
	gasUsed, _ := asInt64(tx["gas_used"])
	// A single packet is not a batch, so the transaction carries the
	// single-message cost plus the callback gas.
	want := int64(singleRelayGas + callbackGas)
	if gasWanted != want {
		t.Fatalf("recv gas_wanted = %d, want %d", gasWanted, want)
	}
	if gasUsed > gasWanted {
		t.Fatalf("recv gas_used %d exceeds gas_wanted %d", gasUsed, gasWanted)
	}
	if raw, ok := tx["tx"].(map[string]any); ok {
		if auth, ok := raw["auth_info"].(map[string]any); ok {
			if fee, ok := auth["fee"].(map[string]any); ok {
				t.Logf("recv fee %v", fee["amount"])
			}
		}
	}
	t.Logf("recv gas_wanted %d gas_used %d status %s", gasWanted, gasUsed, record.Status)
}

func sendPacketWithMemo(t *testing.T, src ChainConfig, srcClient, denom, amount, receiver string, timeoutSeconds int64, memo string) (int64, error) {
	t.Helper()
	height, err := latestHeight(src)
	if err != nil {
		return 0, err
	}
	header, err := blockAt(src, height)
	if err != nil {
		return 0, err
	}
	blockTime, err := time.Parse(time.RFC3339Nano, header.Time)
	if err != nil {
		return 0, err
	}
	timeout := blockTime.Unix() + timeoutSeconds
	signer, err := relayerAddress(src)
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(map[string]string{
		"denom": denom, "amount": amount, "sender": signer,
		"receiver": receiver, "memo": memo,
	})
	if err != nil {
		return 0, err
	}
	payload := map[string]any{
		"source_port": "transfer", "destination_port": "transfer",
		"version": "ics20-1", "encoding": "application/json",
		"value": base64.StdEncoding.EncodeToString(raw),
	}
	seqRes, err := queryJSON(src, "ibc", "channelv2", "next-sequence-send", srcClient)
	if err != nil {
		return 0, err
	}
	sequence, err := asInt64(seqRes["next_sequence_send"])
	if err != nil {
		return 0, err
	}
	message := map[string]any{
		"@type":             "/ibc.core.channel.v2.MsgSendPacket",
		"source_client":     srcClient,
		"timeout_timestamp": strconv.FormatInt(timeout, 10),
		"payloads":          []any{payload},
		"signer":            signer,
	}
	hash, err := broadcastMessages(src, []map[string]any{message})
	if err != nil {
		return 0, err
	}
	if err := waitTx(src, hash); err != nil {
		return 0, err
	}
	return sequence, nil
}
