package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const stateVersion = 1

type stateFile struct {
	Version int    `json:"version"`
	Links   []Link `json:"links"`
}

// LinkStore persists links as a small JSON document. The format is versioned
// so a SQLite backend can replace the storage layer without changing commands.
type LinkStore struct {
	Path string
}

func openLinkStore(path string) (*LinkStore, error) {
	if path == "" {
		var err error
		path, err = defaultStatePath()
		if err != nil {
			return nil, err
		}
	}
	return &LinkStore{Path: path}, nil
}

func defaultStatePath() (string, error) {
	if path := os.Getenv("IBC_FERRY_STATE"); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".ibc-ferry", "links.json"), nil
}

func (s *LinkStore) List() ([]Link, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	links := append([]Link(nil), state.Links...)
	sort.Slice(links, func(i, j int) bool { return links[i].ID < links[j].ID })
	return links, nil
}

func (s *LinkStore) Get(id string) (Link, error) {
	state, err := s.load()
	if err != nil {
		return Link{}, err
	}
	for _, link := range state.Links {
		if link.ID == id {
			return link, nil
		}
	}
	return Link{}, fmt.Errorf("link %q not found", id)
}

// Put inserts or replaces a link by ID.
func (s *LinkStore) Put(link Link) error {
	if err := link.Validate(); err != nil {
		return err
	}
	state, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range state.Links {
		if state.Links[i].ID == link.ID {
			state.Links[i] = link
			replaced = true
			break
		}
	}
	if !replaced {
		state.Links = append(state.Links, link)
	}
	return s.save(state)
}

func (s *LinkStore) load() (stateFile, error) {
	state := stateFile{Version: stateVersion}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode state %s: %w", s.Path, err)
	}
	if state.Version != stateVersion {
		return state, fmt.Errorf("state %s has unsupported version %d", s.Path, state.Version)
	}
	seen := make(map[string]struct{}, len(state.Links))
	for _, link := range state.Links {
		if err := link.Validate(); err != nil {
			return state, fmt.Errorf("state %s: invalid link %q: %w", s.Path, link.ID, err)
		}
		if _, ok := seen[link.ID]; ok {
			return state, fmt.Errorf("state %s: duplicate link %q", s.Path, link.ID)
		}
		seen[link.ID] = struct{}{}
	}
	return state, nil
}

func (s *LinkStore) save(state stateFile) error {
	state.Version = stateVersion
	sort.Slice(state.Links, func(i, j int) bool { return state.Links[i].ID < state.Links[j].ID })

	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".links-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

func formatStatus(status LinkStatus) string {
	return strings.ToUpper(string(status))
}
