package main

import "testing"

func TestClientSequence(t *testing.T) {
	cases := map[string]int{
		"07-tendermint-0":  0,
		"07-tendermint-10": 10,
		"client-42":        42,
		"07-tendermint-":   -1,
		"no-sequence":      -1,
		"":                 -1,
	}
	for clientID, want := range cases {
		if got := clientSequence(clientID); got != want {
			t.Fatalf("clientSequence(%q) = %d, want %d", clientID, got, want)
		}
	}
}

func TestParseClientStates(t *testing.T) {
	res := map[string]any{
		"client_states": []any{
			map[string]any{
				"client_id": "07-tendermint-1",
				"client_state": map[string]any{
					"chain_id":      "chain-b-1",
					"latest_height": map[string]any{"revision_height": "42"},
				},
			},
			map[string]any{
				"client_id": "07-tendermint-0",
				"client_state": map[string]any{
					"chain_id": "chain-a-1",
				},
			},
			map[string]any{
				"client_id": "unusable",
			},
		},
	}
	clients := parseClientStates(res)
	if len(clients) != 2 {
		t.Fatalf("got %d clients, want 2", len(clients))
	}
	if clients[0].ID != "07-tendermint-1" || clients[0].ChainID != "chain-b-1" || clients[0].LatestHeight != 42 {
		t.Fatalf("unexpected first client: %+v", clients[0])
	}
	if clients[1].ID != "07-tendermint-0" || clients[1].LatestHeight != 0 {
		t.Fatalf("unexpected second client: %+v", clients[1])
	}
}
