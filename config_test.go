package main

import "testing"

func TestRevisionNumber(t *testing.T) {
	cases := map[string]uint64{
		"karsa_262144-1": 1,
		"gaia-test-1":    1,
		"chain-42":       42,
		"simapp":         0,
		"":               0,
	}
	for chainID, want := range cases {
		if got := revisionNumber(chainID); got != want {
			t.Fatalf("revisionNumber(%q) = %d, want %d", chainID, got, want)
		}
	}
}

func TestValidateChainDefaults(t *testing.T) {
	chain := ChainConfig{
		Binary:  "karsad",
		Home:    "/tmp/karsa",
		Node:    "127.0.0.1:26657",
		RPC:     "127.0.0.1:26657",
		ChainID: "karsa_262144-1",
		From:    "relayer",
	}
	if err := validateChain("src", &chain); err != nil {
		t.Fatal(err)
	}
	if chain.KeyringBackend != "test" {
		t.Fatalf("keyring default = %q", chain.KeyringBackend)
	}
	if chain.Node != "tcp://127.0.0.1:26657" {
		t.Fatalf("node = %q", chain.Node)
	}
	if chain.RPC != "http://127.0.0.1:26657" {
		t.Fatalf("rpc = %q", chain.RPC)
	}
}
