package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewLinkCanonicalOrder(t *testing.T) {
	link, err := NewLink("chain-b", "07-tendermint-1", "chain-a", "07-tendermint-0")
	if err != nil {
		t.Fatal(err)
	}
	wantID := "chain-a:07-tendermint-0<->chain-b:07-tendermint-1"
	if link.ID != wantID {
		t.Fatalf("id = %q, want %q", link.ID, wantID)
	}
	if link.ChainA != "chain-a" || link.ClientA != "07-tendermint-0" {
		t.Fatalf("unexpected canonical endpoints: %+v", link)
	}
	if link.Status != LinkActive {
		t.Fatalf("status = %q", link.Status)
	}
	if link.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}
}

func TestLinkValidateRejectsBadState(t *testing.T) {
	cases := map[string]Link{
		"empty client": {
			ID: "a:-<->b:c", ChainA: "a", ChainB: "b", ClientB: "c",
			Status: LinkActive, CreatedAt: time.Now(),
		},
		"bad status": {
			ID: "a:x<->b:y", ChainA: "a", ClientA: "x", ChainB: "b", ClientB: "y",
			Status: "unknown", CreatedAt: time.Now(),
		},
		"wrong id": {
			ID: "a:x<->b:z", ChainA: "a", ClientA: "x", ChainB: "b", ClientB: "y",
			Status: LinkActive, CreatedAt: time.Now(),
		},
		"zero time": {
			ID: "a:x<->b:y", ChainA: "a", ClientA: "x", ChainB: "b", ClientB: "y",
			Status: LinkActive,
		},
	}
	for name, link := range cases {
		t.Run(name, func(t *testing.T) {
			if err := link.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLinkStoreRoundTrip(t *testing.T) {
	store, err := openLinkStore(filepath.Join(t.TempDir(), "state", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	links, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("fresh store has %d links", len(links))
	}

	first, err := NewLink("chain-a", "07-tendermint-0", "chain-b", "07-tendermint-0")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLink("chain-a", "07-tendermint-1", "chain-c", "07-tendermint-0")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(second); err != nil {
		t.Fatal(err)
	}

	first.Status = LinkOrphan
	if err := store.Put(first); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != LinkOrphan {
		t.Fatalf("status = %q, want %q", got.Status, LinkOrphan)
	}

	links, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2", len(links))
	}
	if links[0].ID > links[1].ID {
		t.Fatal("links are not sorted by ID")
	}

	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLinkStoreCreatesPacketSchema(t *testing.T) {
	store, err := openLinkStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, table := range []string{"links", "packets", "scan_state"} {
		var name string
		err := store.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
}

func TestLinkStoreRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := openLinkStore(path); err == nil {
		t.Fatal("expected unsupported schema error")
	}
}

func TestLinkStoreImportsLegacyJSON(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("IBC_FERRY_STATE", "")

	link, err := NewLink("chain-a", "07-tendermint-0", "chain-b", "07-tendermint-0")
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(home, ".ibc-ferry", "links.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(legacyStateFile{Version: legacyStateVersion, Links: []Link{link}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := openLinkStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	wantPath := filepath.Join(stateDir, "ibc-ferry", "state.db")
	if store.Path != wantPath {
		t.Fatalf("path = %q, want %q", store.Path, wantPath)
	}
	got, err := store.Get(link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != link.ID {
		t.Fatalf("id = %q, want %q", got.ID, link.ID)
	}
}
