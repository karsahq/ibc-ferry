package main

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestParseWriteAckSearch(t *testing.T) {
	ack := []byte{0x0a, 0x02, 0x6f, 0x6b}
	res := map[string]any{
		"result": map[string]any{
			"txs": []any{
				map[string]any{
					"tx_result": map[string]any{
						"events": []any{
							map[string]any{
								"type": "write_acknowledgement",
								"attributes": []any{
									map[string]any{"key": "packet_dest_client", "value": "07-tendermint-0"},
									map[string]any{"key": "packet_sequence", "value": "7"},
									map[string]any{"key": "encoded_acknowledgement_hex", "value": hex.EncodeToString(ack)},
								},
							},
						},
					},
				},
			},
		},
	}
	got, err := parseWriteAckSearch(res, "07-tendermint-0", 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != base64.StdEncoding.EncodeToString(ack) {
		t.Fatalf("ack = %q", got)
	}
	if _, err := parseWriteAckSearch(res, "07-tendermint-0", 8); err == nil {
		t.Fatal("expected missing sequence error")
	}
}
