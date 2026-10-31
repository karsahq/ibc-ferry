package main

import (
	"testing"
	"time"
)

func TestGasDenom(t *testing.T) {
	cases := map[string]string{
		"0akra":    "akra",
		"1stake":   "stake",
		"1.5uatom": "uatom",
		"0":        "",
		"":         "",
		" stake":   "stake",
	}
	for input, want := range cases {
		if got := gasDenom(input); got != want {
			t.Fatalf("gasDenom(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:    "1m",
		25 * time.Hour:      "1d1h",
		14 * 24 * time.Hour: "14d0h",
		90 * time.Minute:    "1h30m",
		-time.Hour:          "-1h0m",
	}
	for input, want := range cases {
		if got := formatDuration(input); got != want {
			t.Fatalf("formatDuration(%s) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatPacketCounts(t *testing.T) {
	counts := map[string]int{"pending": 2, "acked": 1}
	if got := formatPacketCounts(counts); got != "acked=1, pending=2" {
		t.Fatalf("formatPacketCounts = %q", got)
	}
}

func TestFeeDenoms(t *testing.T) {
	chain := ChainConfig{
		GasPrices: "1stake",
		Fee:       []FeeCoin{{Denom: "stake"}, {Denom: "uatom"}},
	}
	got := feeDenoms(chain)
	if len(got) != 2 || got[0] != "stake" || got[1] != "uatom" {
		t.Fatalf("feeDenoms = %v", got)
	}
}
