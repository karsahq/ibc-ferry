package main

import (
	"fmt"
	"strings"
	"time"
)

// LinkStatus describes the health of a link as known by the local relayer.
type LinkStatus string

const (
	LinkActive   LinkStatus = "active"
	LinkOrphan   LinkStatus = "orphan"
	LinkExpired  LinkStatus = "expired"
	LinkDisabled LinkStatus = "disabled"
)

// Link is a bidirectional pair of IBC v2 clients. IBC v2 has no channels or
// connections, so a link is identified by the two client IDs alone.
type Link struct {
	ID        string     `json:"id"`
	ChainA    string     `json:"chain_a"`
	ClientA   string     `json:"client_a"`
	ChainB    string     `json:"chain_b"`
	ClientB   string     `json:"client_b"`
	Status    LinkStatus `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
}

func linkID(chainA, clientA, chainB, clientB string) string {
	return fmt.Sprintf("%s:%s<->%s:%s", chainA, clientA, chainB, clientB)
}

// NewLink builds a link in a canonical order so that pairing the same two
// clients in either argument order produces the same link ID.
func NewLink(chainA, clientA, chainB, clientB string) (Link, error) {
	if strings.Compare(chainA, chainB) > 0 ||
		(chainA == chainB && strings.Compare(clientA, clientB) > 0) {
		chainA, clientA, chainB, clientB = chainB, clientB, chainA, clientA
	}
	l := Link{
		ID:        linkID(chainA, clientA, chainB, clientB),
		ChainA:    chainA,
		ClientA:   clientA,
		ChainB:    chainB,
		ClientB:   clientB,
		Status:    LinkActive,
		CreatedAt: time.Now().UTC(),
	}
	if err := l.Validate(); err != nil {
		return Link{}, err
	}
	return l, nil
}

// Validate checks the stored form of a link. It deliberately rejects malformed
// state rather than letting a later relay command operate on it.
func (l Link) Validate() error {
	for name, value := range map[string]string{
		"chain_a":  l.ChainA,
		"client_a": l.ClientA,
		"chain_b":  l.ChainB,
		"client_b": l.ClientB,
	} {
		if value == "" {
			return fmt.Errorf("%s is empty", name)
		}
		if strings.TrimSpace(value) != value {
			return fmt.Errorf("%s contains leading or trailing whitespace", name)
		}
	}
	if l.ID != linkID(l.ChainA, l.ClientA, l.ChainB, l.ClientB) {
		return fmt.Errorf("id %q does not match link endpoints", l.ID)
	}
	switch l.Status {
	case LinkActive, LinkOrphan, LinkExpired, LinkDisabled:
	default:
		return fmt.Errorf("unknown status %q", l.Status)
	}
	if l.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is empty")
	}
	return nil
}
