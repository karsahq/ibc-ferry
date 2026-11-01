package main

import "testing"

func TestFeeForGas(t *testing.T) {
	cases := []struct {
		name      string
		gasPrices string
		gasLimit  int64
		denom     string
		amount    string
		ok        bool
	}{
		{
			name:      "integer price",
			gasPrices: "20000000000akra",
			gasLimit:  8_300_000,
			denom:     "akra",
			amount:    "166000000000000000",
			ok:        true,
		},
		{
			name:      "fractional price rounds up",
			gasPrices: "0.025stake",
			gasLimit:  1001,
			denom:     "stake",
			amount:    "26",
			ok:        true,
		},
		{
			name:      "exact fractional price",
			gasPrices: "0.025stake",
			gasLimit:  1000,
			denom:     "stake",
			amount:    "25",
			ok:        true,
		},
		{
			name:      "zero price is ignored",
			gasPrices: "0stake",
			gasLimit:  1000,
			ok:        false,
		},
		{
			name:      "empty price is ignored",
			gasPrices: "",
			gasLimit:  1000,
			ok:        false,
		},
		{
			name:      "price list is ignored",
			gasPrices: "0.025stake,0.1uatom",
			gasLimit:  1000,
			ok:        false,
		},
		{
			name:      "missing denom is ignored",
			gasPrices: "0.025",
			gasLimit:  1000,
			ok:        false,
		},
		{
			name:      "zero gas limit is ignored",
			gasPrices: "0.025stake",
			gasLimit:  0,
			ok:        false,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			coin, ok := feeForGas(test.gasPrices, test.gasLimit)
			if ok != test.ok {
				t.Fatalf("ok = %v, want %v", ok, test.ok)
			}
			if !ok {
				return
			}
			if coin.Denom != test.denom || coin.Amount != test.amount {
				t.Fatalf("coin = %s%s, want %s%s", coin.Amount, coin.Denom, test.amount, test.denom)
			}
		})
	}
}

func TestApplyFee(t *testing.T) {
	cases := []struct {
		name      string
		chain     ChainConfig
		gasLimit  string
		wantCoins []FeeCoin
	}{
		{
			name: "raises a fee that is too small for the batch",
			chain: ChainConfig{
				GasPrices: "20000000000akra",
				Fee:       []FeeCoin{{Denom: "akra", Amount: "100000000000000000"}},
			},
			gasLimit:  "8300000",
			wantCoins: []FeeCoin{{Denom: "akra", Amount: "166000000000000000"}},
		},
		{
			name: "keeps a fee that already covers the gas",
			chain: ChainConfig{
				GasPrices: "20000000000akra",
				Fee:       []FeeCoin{{Denom: "akra", Amount: "900000000000000000"}},
			},
			gasLimit:  "600000",
			wantCoins: []FeeCoin{{Denom: "akra", Amount: "900000000000000000"}},
		},
		{
			name: "derives a fee when none is configured",
			chain: ChainConfig{
				GasPrices: "20000000000akra",
			},
			gasLimit:  "700000",
			wantCoins: []FeeCoin{{Denom: "akra", Amount: "14000000000000000"}},
		},
		{
			name: "adds the gas denom when the configured fee uses another denom",
			chain: ChainConfig{
				GasPrices: "1stake",
				Fee:       []FeeCoin{{Denom: "uatom", Amount: "5000"}},
			},
			gasLimit:  "400000",
			wantCoins: []FeeCoin{{Denom: "uatom", Amount: "5000"}, {Denom: "stake", Amount: "400000"}},
		},
		{
			name: "keeps the configured fee without a gas price",
			chain: ChainConfig{
				Fee: []FeeCoin{{Denom: "stake", Amount: "5000000"}},
			},
			gasLimit:  "400000",
			wantCoins: []FeeCoin{{Denom: "stake", Amount: "5000000"}},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tx := emptyTx(map[string]any{"@type": "/test.Msg"}, test.gasLimit)
			applyFee(tx, test.chain)

			fee := tx["auth_info"].(map[string]any)["fee"].(map[string]any)
			raw, _ := fee["amount"].([]any)
			if len(raw) != len(test.wantCoins) {
				t.Fatalf("fee coins = %v, want %v", raw, test.wantCoins)
			}
			for index, want := range test.wantCoins {
				coin := raw[index].(map[string]any)
				if coin["denom"] != want.Denom || coin["amount"] != want.Amount {
					t.Fatalf("coin %d = %v, want %s%s", index, coin, want.Amount, want.Denom)
				}
			}
		})
	}
}
