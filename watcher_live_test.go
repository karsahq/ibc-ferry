//go:build live

package main

import (
	"os"
	"testing"
	"time"
)

func TestLiveWatcher(t *testing.T) {
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
	sent, err := sendPacketWithTimeout(src.Config, srcClient, denom, "1000", receiver, 120)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent sequence %d timeout %d", sent.Sequence, sent.Timeout)

	opts := relayOptions{LinkID: link.ID, Once: true, Interval: time.Second, Finality: 0, Lookback: 1000}
	var packet PacketRecord
	for cycle := 0; cycle < 4; cycle++ {
		if err := runRelay(cfg, statePath, opts); err != nil {
			t.Fatal(err)
		}
		packets, err := store.Packets(link.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, candidate := range packets {
			if candidate.Sequence == sent.Sequence && candidate.SourceClient == srcClient {
				packet = candidate
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("packet %d was not ingested", sent.Sequence)
		}
		t.Logf("cycle %d: status=%s recv=%s ack=%s", cycle+1, packet.Status, packet.RecvTxHash, packet.AckTxHash)
		if packet.Status == PacketAcked {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if packet.Status != PacketAcked {
		t.Fatalf("packet ended in status %q", packet.Status)
	}
}

func TestLiveWatcherTimeout(t *testing.T) {
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
	sent, err := sendPacketWithTimeout(src.Config, srcClient, denom, "1000", receiver, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent sequence %d timeout %d; waiting for expiry", sent.Sequence, sent.Timeout)
	time.Sleep(25 * time.Second)

	if err := runRelay(cfg, statePath, relayOptions{
		LinkID: link.ID, Once: true, Interval: time.Second, Finality: 0, Lookback: 1000,
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
		t.Logf("status=%s timeout_tx=%s", packet.Status, packet.TimeoutTxHash)
		if packet.Status != PacketTimedOut || packet.TimeoutTxHash == "" {
			t.Fatalf("packet was not timed out: %+v", packet)
		}
		return
	}
	t.Fatalf("packet %d was not ingested", sent.Sequence)
}

func TestLiveWatcherClientRefresh(t *testing.T) {
	configPath := os.Getenv("FERRY_LIVE_CONFIG")
	statePath := os.Getenv("FERRY_LIVE_STATE")
	if configPath == "" || statePath == "" {
		t.Skip("set FERRY_LIVE_CONFIG and FERRY_LIVE_STATE")
	}

	cfg, err := loadConfig(configPath)
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
	chainA, err := cfg.chainByID(link.ChainA)
	if err != nil {
		t.Fatal(err)
	}
	chainB, err := cfg.chainByID(link.ChainB)
	if err != nil {
		t.Fatal(err)
	}

	_, beforeA, err := clientLatestHeight(chainA.Config, link.ClientA)
	if err != nil {
		t.Fatal(err)
	}
	_, beforeB, err := clientLatestHeight(chainB.Config, link.ClientB)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)

	if err := runRelay(cfg, statePath, relayOptions{
		LinkID:        link.ID,
		Once:          true,
		Interval:      time.Second,
		Finality:      0,
		Lookback:      1000,
		ClientRefresh: time.Nanosecond,
	}); err != nil {
		t.Fatal(err)
	}

	_, afterA, err := clientLatestHeight(chainA.Config, link.ClientA)
	if err != nil {
		t.Fatal(err)
	}
	_, afterB, err := clientLatestHeight(chainB.Config, link.ClientB)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("client heights: A %d -> %d, B %d -> %d", beforeA, afterA, beforeB, afterB)
	if afterA <= beforeA || afterB <= beforeB {
		t.Fatalf("clients were not refreshed: A %d -> %d, B %d -> %d", beforeA, afterA, beforeB, afterB)
	}
}
