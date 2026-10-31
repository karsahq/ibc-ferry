//go:build live

package main

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestLiveTimeout(t *testing.T) {
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

	var srcClient, dstClient string
	switch src.Config.ChainID {
	case link.ChainA:
		srcClient, dstClient = link.ClientA, link.ClientB
	case link.ChainB:
		srcClient, dstClient = link.ClientB, link.ClientA
	default:
		t.Fatalf("source chain %s is not in link %s", src.Config.ChainID, link.ID)
	}

	receiver, err := relayerAddress(dst.Config)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := sendPacketWithTimeout(src.Config, srcClient, denom, "1000", receiver, 15)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent sequence %d timeout %d", sent.Sequence, sent.Timeout)

	deadline := time.Now().Add(45 * time.Second)
	for {
		proof, proofHeight, height, err := getTimeoutProof(dst.Config, dstClient, sent.Sequence, sent.Timeout)
		if err == nil {
			t.Logf("timeout proof height %d length %d", height, len(proof))
			hash, err := timeoutPacket(
				src.Config, dst.Config, srcClient, dstClient,
				sent.Sequence, sent.Timeout, sent.Payload, proof, proofHeight,
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("timeout tx %s", hash)
			return
		}
		if !errors.Is(err, errTimeoutNotReached) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout proof did not become available: %v", err)
		}
		time.Sleep(time.Second)
	}
}
