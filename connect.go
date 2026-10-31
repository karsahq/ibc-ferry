package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type clientInfo struct {
	ID             string
	ChainID        string
	LatestHeight   int64
	TrustingPeriod time.Duration
}

type clientPair struct {
	A clientInfo
	B clientInfo
}

func cmdConnect(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("connect requires two chain names or chain IDs")
	}

	link, err := connectChains(cfg, statePath, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	printLink(link)
	return nil
}

func cmdPair(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 4 {
		return fmt.Errorf("pair requires <chain-a> <client-a> <chain-b> <client-b>")
	}

	link, err := pairClients(cfg, statePath, fs.Arg(0), fs.Arg(1), fs.Arg(2), fs.Arg(3))
	if err != nil {
		return err
	}
	printLink(link)
	return nil
}

func connectChains(cfg Config, statePath, chainARef, chainBRef string) (Link, error) {
	chainA, chainB, err := resolveLinkChains(cfg, chainARef, chainBRef)
	if err != nil {
		return Link{}, err
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return Link{}, err
	}
	defer store.Close()

	clientA, clientB, err := ensureLinkClients(chainA, chainB)
	if err != nil {
		return Link{}, err
	}
	if err := verifyClientTracks(chainA, clientA, chainB.Config.ChainID); err != nil {
		return Link{}, err
	}
	if err := verifyClientTracks(chainB, clientB, chainA.Config.ChainID); err != nil {
		return Link{}, err
	}
	if err := ensureCounterparty(chainA, clientA, clientB); err != nil {
		return Link{}, err
	}
	if err := ensureCounterparty(chainB, clientB, clientA); err != nil {
		return Link{}, err
	}

	link, err := NewLink(chainA.Config.ChainID, clientA, chainB.Config.ChainID, clientB)
	if err != nil {
		return Link{}, err
	}
	if err := store.Put(link); err != nil {
		return Link{}, err
	}
	return link, nil
}

func pairClients(cfg Config, statePath, chainARef, clientA, chainBRef, clientB string) (Link, error) {
	chainA, chainB, err := resolveLinkChains(cfg, chainARef, chainBRef)
	if err != nil {
		return Link{}, err
	}
	if err := verifyClientTracks(chainA, clientA, chainB.Config.ChainID); err != nil {
		return Link{}, err
	}
	if err := verifyClientTracks(chainB, clientB, chainA.Config.ChainID); err != nil {
		return Link{}, err
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return Link{}, err
	}
	defer store.Close()

	if err := ensureCounterparty(chainA, clientA, clientB); err != nil {
		return Link{}, err
	}
	if err := ensureCounterparty(chainB, clientB, clientA); err != nil {
		return Link{}, err
	}

	link, err := NewLink(chainA.Config.ChainID, clientA, chainB.Config.ChainID, clientB)
	if err != nil {
		return Link{}, err
	}
	if err := store.Put(link); err != nil {
		return Link{}, err
	}
	return link, nil
}

func resolveLinkChains(cfg Config, chainARef, chainBRef string) (namedChain, namedChain, error) {
	chainA, err := cfg.chainByRef(chainARef)
	if err != nil {
		return namedChain{}, namedChain{}, err
	}
	chainB, err := cfg.chainByRef(chainBRef)
	if err != nil {
		return namedChain{}, namedChain{}, err
	}
	if chainA.Config.ChainID == chainB.Config.ChainID {
		return namedChain{}, namedChain{}, fmt.Errorf("cannot link %s to itself", chainA.Config.ChainID)
	}
	return chainA, chainB, nil
}

func ensureLinkClients(chainA, chainB namedChain) (string, string, error) {
	if pair, ok, err := findExistingPair(chainA, chainB); err != nil {
		return "", "", err
	} else if ok {
		return pair.A.ID, pair.B.ID, nil
	}

	clientA, err := ensureClient(chainA, chainB)
	if err != nil {
		return "", "", err
	}
	clientB, err := ensureClient(chainB, chainA)
	if err != nil {
		return "", "", err
	}
	return clientA, clientB, nil
}

// findExistingPair looks for a complete link first. If one side has a
// counterparty pointing at a client on the other side, that pair is reused and
// the missing counterparty is filled in by ensureCounterparty.
func findExistingPair(chainA, chainB namedChain) (clientPair, bool, error) {
	sides := []struct {
		from namedChain
		to   namedChain
	}{
		{from: chainA, to: chainB},
		{from: chainB, to: chainA},
	}
	for _, side := range sides {
		clients, err := listClientStates(side.from.Config)
		if err != nil {
			return clientPair{}, false, err
		}
		for _, fromClient := range clients {
			if fromClient.ChainID != side.to.Config.ChainID {
				continue
			}
			counterpartyID, set, err := clientCounterparty(side.from.Config, fromClient.ID)
			if err != nil {
				return clientPair{}, false, err
			}
			if !set {
				continue
			}
			toClient, err := clientStateInfo(side.to.Config, counterpartyID)
			if err != nil {
				if isNotFound(err) {
					continue
				}
				return clientPair{}, false, err
			}
			if toClient.ChainID != side.from.Config.ChainID {
				continue
			}
			toCounterpartyID, toSet, err := clientCounterparty(side.to.Config, toClient.ID)
			if err != nil {
				return clientPair{}, false, err
			}
			if toSet && toCounterpartyID != fromClient.ID {
				continue
			}
			if side.from.Config.ChainID == chainA.Config.ChainID {
				return clientPair{A: fromClient, B: toClient}, true, nil
			}
			return clientPair{A: toClient, B: fromClient}, true, nil
		}
	}
	return clientPair{}, false, nil
}

// ensureClient reuses an existing client that tracks the counterparty and has
// no counterparty registered yet. A client with a counterparty is left alone:
// it either belongs to an existing pair or is an orphan that ferry doctor can
// report later.
func ensureClient(chain, counterparty namedChain) (string, error) {
	clients, err := listClientStates(chain.Config)
	if err != nil {
		return "", err
	}
	for _, client := range clients {
		if client.ChainID != counterparty.Config.ChainID {
			continue
		}
		_, set, err := clientCounterparty(chain.Config, client.ID)
		if err != nil {
			return "", err
		}
		if !set {
			return client.ID, nil
		}
	}
	clientID, _, err := createClient(chain.Config, counterparty.Config, 0)
	return clientID, err
}

func ensureCounterparty(chain namedChain, clientID, counterpartyClientID string) error {
	current, set, err := clientCounterparty(chain.Config, clientID)
	if err != nil {
		return err
	}
	if set {
		if current == counterpartyClientID {
			return nil
		}
		return fmt.Errorf(
			"client %s on %s already has counterparty %s; refusing to overwrite it with %s",
			clientID, chain.Config.ChainID, current, counterpartyClientID,
		)
	}

	if err := addCounterparty(chain.Config, clientID, counterpartyClientID); err != nil {
		// A concurrent relay may have registered it after our query.
		current, set, queryErr := clientCounterparty(chain.Config, clientID)
		if queryErr == nil && set && current == counterpartyClientID {
			return nil
		}
		return err
	}

	current, set, err = clientCounterparty(chain.Config, clientID)
	if err != nil {
		return err
	}
	if !set || current != counterpartyClientID {
		return fmt.Errorf(
			"counterparty verification failed for %s on %s: got %q",
			clientID, chain.Config.ChainID, current,
		)
	}
	return nil
}

func verifyClientTracks(chain namedChain, clientID, expectedChainID string) error {
	client, err := clientStateInfo(chain.Config, clientID)
	if err != nil {
		return err
	}
	if client.ChainID != expectedChainID {
		return fmt.Errorf(
			"client %s on %s tracks %s, not %s",
			clientID, chain.Config.ChainID, client.ChainID, expectedChainID,
		)
	}
	return nil
}

func listClientStates(chain ChainConfig) ([]clientInfo, error) {
	res, err := queryJSON(chain, "ibc", "client", "states", "--limit", "1000")
	if err != nil {
		return nil, err
	}
	clients := parseClientStates(res)
	sort.Slice(clients, func(i, j int) bool {
		left, right := clientSequence(clients[i].ID), clientSequence(clients[j].ID)
		if left != right {
			return left < right
		}
		return clients[i].ID < clients[j].ID
	})
	return clients, nil
}

func parseClientStates(res map[string]any) []clientInfo {
	rawStates, _ := res["client_states"].([]any)
	clients := make([]clientInfo, 0, len(rawStates))
	for _, rawState := range rawStates {
		state, _ := rawState.(map[string]any)
		clientID := str(state["client_id"])
		clientState, _ := state["client_state"].(map[string]any)
		if clientID == "" || clientState == nil {
			continue
		}
		trustingPeriod, _ := time.ParseDuration(str(clientState["trusting_period"]))
		client := clientInfo{
			ID:             clientID,
			ChainID:        str(clientState["chain_id"]),
			TrustingPeriod: trustingPeriod,
		}
		if latest, ok := clientState["latest_height"].(map[string]any); ok {
			if height, err := asInt64(latest["revision_height"]); err == nil {
				client.LatestHeight = height
			}
		}
		clients = append(clients, client)
	}
	return clients
}

func clientStateInfo(chain ChainConfig, clientID string) (clientInfo, error) {
	res, err := queryJSON(chain, "ibc", "client", "state", clientID)
	if err != nil {
		if isNotFound(err) {
			return clientInfo{}, fmt.Errorf("client %s not found", clientID)
		}
		return clientInfo{}, err
	}
	clientState, _ := res["client_state"].(map[string]any)
	if clientState == nil {
		return clientInfo{}, fmt.Errorf("client %s not found on %s", clientID, chain.ChainID)
	}
	chainID := str(clientState["chain_id"])
	if chainID == "" {
		return clientInfo{}, fmt.Errorf("client %s on %s has no chain_id", clientID, chain.ChainID)
	}
	trustingPeriod, _ := time.ParseDuration(str(clientState["trusting_period"]))
	client := clientInfo{
		ID:             clientID,
		ChainID:        chainID,
		TrustingPeriod: trustingPeriod,
	}
	if latest, ok := clientState["latest_height"].(map[string]any); ok {
		if height, err := asInt64(latest["revision_height"]); err == nil {
			client.LatestHeight = height
		}
	}
	return client, nil
}

func clientCounterparty(chain ChainConfig, clientID string) (string, bool, error) {
	res, err := queryJSON(chain, "ibc", "client", "counterparty-info", clientID)
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	info, _ := res["counterparty_info"].(map[string]any)
	if info == nil {
		return "", false, nil
	}
	counterpartyID := str(info["client_id"])
	if counterpartyID == "" {
		return "", false, nil
	}
	return counterpartyID, true, nil
}

func isNotFound(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not found") ||
		strings.Contains(message, "notfound") ||
		strings.Contains(message, "code = 5")
}

func clientSequence(clientID string) int {
	index := strings.LastIndex(clientID, "-")
	if index < 0 || index == len(clientID)-1 {
		return -1
	}
	sequence, err := strconv.Atoi(clientID[index+1:])
	if err != nil {
		return -1
	}
	return sequence
}

func printLink(link Link) {
	fmt.Printf("link:      %s\n", link.ID)
	fmt.Printf("status:    %s\n", formatStatus(link.Status))
	fmt.Printf("chain-a:   %s\n", link.ChainA)
	fmt.Printf("client-a:  %s\n", link.ClientA)
	fmt.Printf("chain-b:   %s\n", link.ChainB)
	fmt.Printf("client-b:  %s\n", link.ClientB)
}
