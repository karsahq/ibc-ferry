package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type FeeCoin struct {
	Denom  string `json:"denom" toml:"denom"`
	Amount string `json:"amount" toml:"amount"`
}

type ChainConfig struct {
	Binary         string    `json:"binary" toml:"binary"`
	Home           string    `json:"home" toml:"home"`
	Node           string    `json:"node" toml:"node"`
	RPC            string    `json:"rpc" toml:"rpc"`
	ChainID        string    `json:"chain_id" toml:"chain_id"`
	KeyringBackend string    `json:"keyring_backend" toml:"keyring_backend"`
	From           string    `json:"from" toml:"from"`
	GasPrices      string    `json:"gas_prices" toml:"gas_prices"`
	Fee            []FeeCoin `json:"fee,omitempty" toml:"fee"`
}

type Config struct {
	// Chains holds named chain entries for the multi-chain CLI commands.
	Chains map[string]ChainConfig `json:"chains,omitempty" toml:"chains"`

	// Src and Dst are the legacy single-transfer configuration. They remain
	// supported so existing one-shot configs keep working.
	Src       ChainConfig `json:"src,omitempty" toml:"src"`
	Dst       ChainConfig `json:"dst,omitempty" toml:"dst"`
	SrcClient string      `json:"src_client,omitempty" toml:"src_client"`
	DstClient string      `json:"dst_client,omitempty" toml:"dst_client"`
	Denom     string      `json:"denom,omitempty" toml:"denom"`
	Amount    string      `json:"amount,omitempty" toml:"amount"`
	Receiver  string      `json:"receiver,omitempty" toml:"receiver"`
	Ack       *bool       `json:"ack,omitempty" toml:"ack"`
}

type namedChain struct {
	Name   string
	Config ChainConfig
}

func (c Config) relayAck() bool {
	return c.Ack == nil || *c.Ack
}

func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := decodeConfig(path, data, &cfg); err != nil {
		return cfg, fmt.Errorf("decode %s: %w", path, err)
	}
	return validateConfig(cfg)
}

func decodeConfig(path string, data []byte, cfg *Config) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("config file is empty")
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return toml.Unmarshal(data, cfg)
	case ".json":
		return json.Unmarshal(data, cfg)
	}
	if trimmed[0] == '{' {
		return json.Unmarshal(data, cfg)
	}
	return toml.Unmarshal(data, cfg)
}

func defaultConfigPath() (string, error) {
	if path := os.Getenv("IBC_FERRY_CONFIG"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config directory: %w", err)
	}
	return filepath.Join(dir, "ibc-ferry", "config.toml"), nil
}

func validateConfig(cfg Config) (Config, error) {
	if len(cfg.Chains) == 0 && cfg.Src.Binary == "" && cfg.Dst.Binary == "" {
		return cfg, fmt.Errorf("no chains configured: add a \"chains\" map or legacy src/dst entries")
	}
	seenIDs := make(map[string]string, len(cfg.Chains))
	for name, chain := range cfg.Chains {
		if strings.TrimSpace(name) == "" {
			return cfg, fmt.Errorf("chains: name cannot be empty")
		}
		if err := validateChain("chains."+name, &chain); err != nil {
			return cfg, err
		}
		if other, ok := seenIDs[chain.ChainID]; ok {
			return cfg, fmt.Errorf("chains.%s and chains.%s both use chain_id %q", other, name, chain.ChainID)
		}
		seenIDs[chain.ChainID] = name
		cfg.Chains[name] = chain
	}
	if cfg.Src.Binary != "" || cfg.Dst.Binary != "" {
		if cfg.Src.Binary == "" || cfg.Dst.Binary == "" {
			return cfg, fmt.Errorf("src and dst must both be configured")
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
	}
	return cfg, nil
}

func (c Config) validateTransfer() error {
	if c.Denom == "" || c.Amount == "" || c.Receiver == "" {
		return fmt.Errorf("denom, amount and receiver are required")
	}
	if c.Src.Binary == "" || c.Dst.Binary == "" {
		return fmt.Errorf("src and dst are required for a one-shot transfer")
	}
	return nil
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

// namedChains returns the configured chains in a stable order. Legacy src/dst
// entries are exposed as "src" and "dst" so the read-only commands work with
// old configs too.
func (c Config) namedChains() []namedChain {
	chains := make([]namedChain, 0, len(c.Chains)+2)
	if len(c.Chains) > 0 {
		for name, chain := range c.Chains {
			chains = append(chains, namedChain{Name: name, Config: chain})
		}
	} else {
		if c.Src.Binary != "" {
			chains = append(chains, namedChain{Name: "src", Config: c.Src})
		}
		if c.Dst.Binary != "" {
			chains = append(chains, namedChain{Name: "dst", Config: c.Dst})
		}
	}
	sort.Slice(chains, func(i, j int) bool { return chains[i].Name < chains[j].Name })
	return chains
}

// chainByRef accepts either a configured name or a chain ID.
func (c Config) chainByRef(ref string) (namedChain, error) {
	for _, chain := range c.namedChains() {
		if chain.Name == ref {
			return chain, nil
		}
	}
	for _, chain := range c.namedChains() {
		if chain.Config.ChainID == ref {
			return chain, nil
		}
	}
	return namedChain{}, fmt.Errorf("chain %q is not configured", ref)
}

func (c Config) chainByID(id string) (namedChain, error) {
	for _, chain := range c.namedChains() {
		if chain.Config.ChainID == id {
			return chain, nil
		}
	}
	return namedChain{}, fmt.Errorf("chain id %q is not configured", id)
}
