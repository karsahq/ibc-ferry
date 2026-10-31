package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	schemaVersion       = 1
	legacyStateVersion  = 1
	legacyStateFileName = "links.json"
)

type legacyStateFile struct {
	Version int    `json:"version"`
	Links   []Link `json:"links"`
}

// LinkStore persists links and relay work in SQLite. Chain definitions stay in
// the config file; this database is runtime state only.
type LinkStore struct {
	Path string
	db   *sql.DB
}

func openLinkStore(path string) (*LinkStore, error) {
	var legacyPath string
	if path == "" {
		path = os.Getenv("IBC_FERRY_STATE")
		if path == "" {
			var err error
			path, err = defaultStatePath()
			if err != nil {
				return nil, err
			}
			legacyPath = legacyStatePath()
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection keeps the per-connection PRAGMAs in effect and serializes
	// writers. The relayer is a single process and does not need a pool yet.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &LinkStore{Path: path, db: db}
	if err := store.configure(); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)

	if legacyPath != "" {
		if err := store.importLegacyJSON(legacyPath); err != nil {
			db.Close()
			return nil, err
		}
	}
	return store, nil
}

func defaultStatePath() (string, error) {
	if path := os.Getenv("IBC_FERRY_STATE"); path != "" {
		return path, nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "ibc-ferry", "state.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "ibc-ferry", "state.db"), nil
}

func legacyStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ibc-ferry", legacyStateFileName)
}

func (s *LinkStore) configure() error {
	for _, stmt := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("sqlite %s: %w", stmt, err)
		}
	}
	return nil
}

func (s *LinkStore) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("state %s has unsupported schema version %d", s.Path, version)
	}
	if version == schemaVersion {
		return nil
	}

	for _, stmt := range schemaStatements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate state %s: %w", s.Path, err)
		}
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS links (
		id TEXT PRIMARY KEY,
		chain_a TEXT NOT NULL,
		client_a TEXT NOT NULL,
		chain_b TEXT NOT NULL,
		client_b TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS packets (
		link_id TEXT NOT NULL,
		direction TEXT NOT NULL,
		sequence INTEGER NOT NULL,
		source_client TEXT NOT NULL,
		destination_client TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		timeout_timestamp INTEGER NOT NULL,
		status TEXT NOT NULL,
		send_tx_hash TEXT,
		recv_tx_hash TEXT,
		ack_tx_hash TEXT,
		timeout_tx_hash TEXT,
		proof_height INTEGER,
		ack_proof_height INTEGER,
		attempts INTEGER NOT NULL DEFAULT 0,
		next_attempt_at TEXT,
		last_error TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (link_id, direction, sequence),
		FOREIGN KEY (link_id) REFERENCES links(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS scan_state (
		link_id TEXT PRIMARY KEY,
		last_scanned_height INTEGER NOT NULL,
		updated_at TEXT NOT NULL,
		FOREIGN KEY (link_id) REFERENCES links(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS packets_pending_idx ON packets(status, next_attempt_at)`,
}

func (s *LinkStore) Close() error {
	return s.db.Close()
}

func (s *LinkStore) List() ([]Link, error) {
	rows, err := s.db.Query(`SELECT id, chain_a, client_a, chain_b, client_b, status, created_at
		FROM links ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	links := []Link{}
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(links, func(i, j int) bool { return links[i].ID < links[j].ID })
	return links, nil
}

func (s *LinkStore) Get(id string) (Link, error) {
	row := s.db.QueryRow(`SELECT id, chain_a, client_a, chain_b, client_b, status, created_at
		FROM links WHERE id = ?`, id)
	link, err := scanLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, fmt.Errorf("link %q not found", id)
	}
	return link, err
}

// Put inserts or replaces a link by ID.
func (s *LinkStore) Put(link Link) error {
	if err := link.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO links
		(id, chain_a, client_a, chain_b, client_b, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			chain_a = excluded.chain_a,
			client_a = excluded.client_a,
			chain_b = excluded.chain_b,
			client_b = excluded.client_b,
			status = excluded.status,
			updated_at = excluded.updated_at`,
		link.ID, link.ChainA, link.ClientA, link.ChainB, link.ClientB,
		string(link.Status), link.CreatedAt.UTC().Format(time.RFC3339Nano), now,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanLink(row rowScanner) (Link, error) {
	var link Link
	var status, created string
	if err := row.Scan(
		&link.ID, &link.ChainA, &link.ClientA, &link.ChainB, &link.ClientB,
		&status, &created,
	); err != nil {
		return Link{}, err
	}
	link.Status = LinkStatus(status)
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Link{}, fmt.Errorf("parse created_at for link %q: %w", link.ID, err)
	}
	link.CreatedAt = createdAt
	if err := link.Validate(); err != nil {
		return Link{}, fmt.Errorf("invalid link %q in state: %w", link.ID, err)
	}
	return link, nil
}

func (s *LinkStore) importLegacyJSON(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state legacyStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode legacy state %s: %w", path, err)
	}
	if state.Version != legacyStateVersion {
		return fmt.Errorf("legacy state %s has unsupported version %d", path, state.Version)
	}
	if len(state.Links) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO links
		(id, chain_a, client_a, chain_b, client_b, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	seen := make(map[string]struct{}, len(state.Links))
	for _, link := range state.Links {
		if err := link.Validate(); err != nil {
			return fmt.Errorf("legacy state %s: invalid link %q: %w", path, link.ID, err)
		}
		if _, ok := seen[link.ID]; ok {
			return fmt.Errorf("legacy state %s: duplicate link %q", path, link.ID)
		}
		seen[link.ID] = struct{}{}
		if _, err := stmt.Exec(
			link.ID, link.ChainA, link.ClientA, link.ChainB, link.ClientB,
			string(link.Status), link.CreatedAt.UTC().Format(time.RFC3339Nano), now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func formatStatus(status LinkStatus) string {
	return strings.ToUpper(string(status))
}

// PacketCounts returns packet counts grouped by status for a link.
func (s *LinkStore) PacketCounts(linkID string) (map[string]int, error) {
	rows, err := s.db.Query(
		`SELECT status, COUNT(*) FROM packets WHERE link_id = ? GROUP BY status`,
		linkID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// ScanHeight returns the last scanned height for a link, if one is recorded.
func (s *LinkStore) ScanHeight(linkID string) (int64, bool, error) {
	var height int64
	err := s.db.QueryRow(
		`SELECT last_scanned_height FROM scan_state WHERE link_id = ?`, linkID,
	).Scan(&height)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return height, true, nil
}
