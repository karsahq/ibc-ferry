package main

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestLoadConfigWithNamedChains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{
		"chains": {
			"karsa": {
				"binary": "karsad", "home": "/tmp/karsa", "node": "127.0.0.1:26657",
				"rpc": "127.0.0.1:26657", "chain_id": "karsa_262144-1",
				"from": "relayer"
			},
			"gaia": {
				"binary": "gaiad", "home": "/tmp/gaia", "node": "127.0.0.1:26658",
				"rpc": "127.0.0.1:26658", "chain_id": "gaia-test-1",
				"keyring_backend": "test", "from": "relayer", "gas_prices": "1stake"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	chains := cfg.namedChains()
	if len(chains) != 2 || chains[0].Name != "gaia" || chains[1].Name != "karsa" {
		t.Fatalf("unexpected chains: %+v", chains)
	}
	if chains[1].Config.Node != "tcp://127.0.0.1:26657" {
		t.Fatalf("node default = %q", chains[1].Config.Node)
	}
	if chains[1].Config.KeyringBackend != "test" {
		t.Fatalf("keyring default = %q", chains[1].Config.KeyringBackend)
	}

	byName, err := cfg.chainByRef("karsa")
	if err != nil {
		t.Fatal(err)
	}
	if byName.Config.ChainID != "karsa_262144-1" {
		t.Fatalf("chain id = %q", byName.Config.ChainID)
	}
	byID, err := cfg.chainByRef("gaia-test-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.Name != "gaia" {
		t.Fatalf("name = %q", byID.Name)
	}
	if err := cfg.validateTransfer(); err == nil {
		t.Fatal("named-chain config must not be usable as a one-shot transfer")
	}
}

func TestLoadConfigRejectsHalfLegacyPair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"src":{"binary":"karsad","home":"/tmp/a","node":"127.0.0.1:1","rpc":"127.0.0.1:1","chain_id":"a-1","from":"relayer"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected missing dst error")
	}
}

func TestLoadConfigTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := `
[chains.karsa]
binary = "karsad"
home = "/tmp/karsa"
node = "127.0.0.1:26657"
rpc = "127.0.0.1:26657"
chain_id = "karsa_262144-1"
from = "relayer"

[chains.gaia]
binary = "gaiad"
home = "/tmp/gaia"
node = "127.0.0.1:26658"
rpc = "127.0.0.1:26658"
chain_id = "gaia-test-1"
from = "relayer"
gas_prices = "1stake"

[[chains.gaia.fee]]
denom = "stake"
amount = "5000000"
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	gaia, err := cfg.chainByRef("gaia")
	if err != nil {
		t.Fatal(err)
	}
	if gaia.Config.ChainID != "gaia-test-1" {
		t.Fatalf("chain id = %q", gaia.Config.ChainID)
	}
	if len(gaia.Config.Fee) != 1 || gaia.Config.Fee[0].Amount != "5000000" {
		t.Fatalf("fee = %+v", gaia.Config.Fee)
	}
	if gaia.Config.KeyringBackend != "test" {
		t.Fatalf("keyring default = %q", gaia.Config.KeyringBackend)
	}
}

func TestDecodeConfigSniffsTOML(t *testing.T) {
	var cfg Config
	data := []byte("[chains.karsa]\nbinary = \"karsad\"\nhome = \"/tmp/karsa\"\nnode = \"127.0.0.1:26657\"\nrpc = \"127.0.0.1:26657\"\nchain_id = \"karsa_262144-1\"\nfrom = \"relayer\"\n")
	if err := decodeConfig("config.conf", data, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IBC_FERRY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", dir)
	path, err := defaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "ibc-ferry", "config.toml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}
