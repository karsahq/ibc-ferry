package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type FeeCoin struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

type ChainConfig struct {
	Binary         string    `json:"binary"`
	Home           string    `json:"home"`
	Node           string    `json:"node"`
	RPC            string    `json:"rpc"`
	ChainID        string    `json:"chain_id"`
	KeyringBackend string    `json:"keyring_backend"`
	From           string    `json:"from"`
	GasPrices      string    `json:"gas_prices"`
	Fee            []FeeCoin `json:"fee,omitempty"`
}

type Config struct {
	Src       ChainConfig `json:"src"`
	Dst       ChainConfig `json:"dst"`
	SrcClient string      `json:"src_client"`
	DstClient string      `json:"dst_client"`
	Denom     string      `json:"denom"`
	Amount    string      `json:"amount"`
	Receiver  string      `json:"receiver"`
}

func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if err := validateChain("src", &cfg.Src); err != nil {
		return cfg, err
	}
	if err := validateChain("dst", &cfg.Dst); err != nil {
		return cfg, err
	}
	if cfg.SrcClient == "" {
		cfg.SrcClient = "07-tendermint-0"
	}
	if cfg.DstClient == "" {
		cfg.DstClient = "07-tendermint-0"
	}
	if cfg.Denom == "" || cfg.Amount == "" || cfg.Receiver == "" {
		return cfg, fmt.Errorf("denom, amount and receiver are required")
	}
	return cfg, nil
}

func validateChain(name string, c *ChainConfig) error {
	if c.Binary == "" || c.Home == "" || c.Node == "" || c.RPC == "" || c.ChainID == "" || c.From == "" {
		return fmt.Errorf("%s: binary, home, node, rpc, chain_id and from are required", name)
	}
	if c.KeyringBackend == "" {
		c.KeyringBackend = "test"
	}
	if c.GasPrices == "" {
		c.GasPrices = "0stake"
	}
	if !strings.HasPrefix(c.Node, "tcp://") {
		c.Node = "tcp://" + c.Node
	}
	if !strings.HasPrefix(c.RPC, "http://") && !strings.HasPrefix(c.RPC, "https://") {
		c.RPC = "http://" + c.RPC
	}
	return nil
}
