package main

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestDecodePacket(t *testing.T) {
	payload := append(fieldBytes(1, []byte("transfer")),
		fieldBytes(2, []byte("transfer"))...)
	payload = append(payload, fieldBytes(3, []byte("ics20-1"))...)
	payload = append(payload, fieldBytes(4, []byte("application/json"))...)
	payload = append(payload, fieldBytes(5, []byte("hello"))...)

	raw := fieldVarint(1, 7)
	raw = append(raw, fieldBytes(2, []byte("07-tendermint-0"))...)
	raw = append(raw, fieldBytes(3, []byte("07-tendermint-1"))...)
	raw = append(raw, fieldVarint(4, 1234)...)
	raw = append(raw, fieldBytes(5, payload)...)

	packet, err := decodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Sequence != 7 || packet.SourceClient != "07-tendermint-0" ||
		packet.DestinationClient != "07-tendermint-1" || packet.TimeoutTimestamp != 1234 {
		t.Fatalf("unexpected packet: %+v", packet)
	}
	if len(packet.Payloads) != 1 {
		t.Fatalf("payloads = %d", len(packet.Payloads))
	}
	got := packet.Payloads[0]
	if got.SourcePort != "transfer" || got.DestinationPort != "transfer" ||
		got.Version != "ics20-1" || got.Encoding != "application/json" {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if got.Value != base64.StdEncoding.EncodeToString([]byte("hello")) {
		t.Fatalf("value = %q", got.Value)
	}
}

func TestDecodePacketHex(t *testing.T) {
	raw := fieldVarint(1, 1)
	packet, err := decodePacketHex(hex.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if packet.Sequence != 1 {
		t.Fatalf("sequence = %d", packet.Sequence)
	}
}

func fieldVarint(field int, value uint64) []byte {
	out := appendVarint(nil, uint64(field<<3))
	return appendVarint(out, value)
}

func fieldBytes(field int, value []byte) []byte {
	out := appendVarint(nil, uint64(field<<3|2))
	out = appendVarint(out, uint64(len(value)))
	return append(out, value...)
}

func appendVarint(out []byte, value uint64) []byte {
	for value >= 0x80 {
		out = append(out, byte(value)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}
