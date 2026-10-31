package main

import (
	"testing"
	"time"
)

func TestGroupPrepared(t *testing.T) {
	chainA := namedChain{Name: "a", Config: ChainConfig{ChainID: "chain-a"}}
	chainB := namedChain{Name: "b", Config: ChainConfig{ChainID: "chain-b"}}
	packet := PacketRecord{LinkID: "link", Direction: directionAToB, Status: PacketPending}
	makePrepared := func(sequence int64, chain namedChain, action relayAction, height int64) preparedPacket {
		packet.Sequence = sequence
		return preparedPacket{
			Packet: packet, Action: action, BroadcastChain: chain,
			ClientID: "client-0", ProofHeight: height,
		}
	}

	prepared := []preparedPacket{
		makePrepared(3, chainB, relayRecv, 10),
		makePrepared(1, chainB, relayRecv, 10),
		makePrepared(2, chainB, relayRecv, 11),
		makePrepared(4, chainA, relayAck, 12),
	}
	groups := groupPrepared(prepared, 10)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	if len(groups[0]) != 1 || groups[0][0].Action != relayAck || groups[0][0].Packet.Sequence != 4 {
		t.Fatalf("unexpected ack group: %+v", groups[0])
	}
	if len(groups[1]) != 2 || groups[1][0].Packet.Sequence != 1 || groups[1][1].Packet.Sequence != 3 {
		t.Fatalf("unexpected recv group: %+v", groups[1])
	}
	if len(groups[2]) != 1 || groups[2][0].Packet.Sequence != 2 {
		t.Fatalf("unexpected second recv group: %+v", groups[2])
	}

	chunks := groupPrepared(prepared[:2], 1)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
}

func TestEmptyBatchTx(t *testing.T) {
	tx := emptyBatchTx([]map[string]any{{"a": 1}, {"b": 2}}, "100")
	body, _ := tx["body"].(map[string]any)
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %d", len(messages))
	}
	auth, _ := tx["auth_info"].(map[string]any)
	fee, _ := auth["fee"].(map[string]any)
	if fee["gas_limit"] != "100" {
		t.Fatalf("gas = %v", fee["gas_limit"])
	}
}

func TestRelayBackoff(t *testing.T) {
	if got := relayBackoff(1); got != 5*time.Second {
		t.Fatalf("backoff(1) = %s", got)
	}
	if got := relayBackoff(10); got != 80*time.Second {
		t.Fatalf("backoff(10) = %s", got)
	}
}
