package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type doctorLevel string

const (
	doctorOK    doctorLevel = "OK"
	doctorInfo  doctorLevel = "INFO"
	doctorWarn  doctorLevel = "WARN"
	doctorError doctorLevel = "ERROR"
)

type doctorFinding struct {
	Level   doctorLevel
	Scope   string
	Message string
}

type doctorReport struct {
	findings []doctorFinding
}

func (r *doctorReport) add(level doctorLevel, scope, format string, args ...any) {
	r.findings = append(r.findings, doctorFinding{
		Level:   level,
		Scope:   scope,
		Message: fmt.Sprintf(format, args...),
	})
}

func (r *doctorReport) count(level doctorLevel) int {
	count := 0
	for _, finding := range r.findings {
		if finding.Level == level {
			count++
		}
	}
	return count
}

func (r *doctorReport) print() {
	for _, finding := range r.findings {
		fmt.Printf("%-5s %s: %s\n", finding.Level, finding.Scope, finding.Message)
	}
	fmt.Printf("\n%d error(s), %d warning(s)\n", r.count(doctorError), r.count(doctorWarn))
}

type clientHealth struct {
	Info           clientInfo
	Status         string
	TrustRemaining time.Duration
}

func cmdDoctor(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	strict := fs.Bool("strict", false, "treat warnings as failures")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("doctor takes no positional arguments")
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return err
	}
	defer store.Close()

	report := &doctorReport{}
	links, err := store.List()
	if err != nil {
		return err
	}

	chains := cfg.namedChains()
	chainByID := make(map[string]namedChain, len(chains))
	for _, chain := range chains {
		chainByID[chain.Config.ChainID] = chain
	}

	reachable := make(map[string]bool, len(chains))
	for _, chain := range chains {
		reachable[chain.Config.ChainID] = report.checkChain(chain)
	}

	if len(links) == 0 {
		report.add(doctorInfo, "links", "no stored links")
	}
	for _, link := range links {
		report.checkLink(store, link, chainByID)
	}
	report.checkOrphans(links, chains, chainByID, reachable)

	report.print()
	errors := report.count(doctorError)
	warnings := report.count(doctorWarn)
	if errors > 0 {
		return fmt.Errorf("%d error(s), %d warning(s)", errors, warnings)
	}
	if *strict && warnings > 0 {
		return fmt.Errorf("%d warning(s)", warnings)
	}
	return nil
}

func (r *doctorReport) checkChain(chain namedChain) bool {
	height, err := latestHeight(chain.Config)
	if err != nil {
		r.add(doctorError, chain.Config.ChainID, "unreachable: %v", err)
		return false
	}

	address, err := relayerAddress(chain.Config)
	if err != nil {
		r.add(doctorError, chain.Config.ChainID, "relayer key: %v", err)
		return true
	}
	coins, denoms, err := relayerBalances(chain.Config, address)
	if err != nil {
		r.add(doctorError, chain.Config.ChainID, "relayer balance: %v", err)
		return true
	}
	if len(coins) == 0 {
		r.add(doctorError, chain.Config.ChainID, "relayer %s has no balance (height %d)", address, height)
	} else {
		r.add(doctorOK, chain.Config.ChainID, "relayer %s balance %s (height %d)", address, strings.Join(coins, ", "), height)
	}

	for _, denom := range feeDenoms(chain.Config) {
		if !denoms[denom] {
			r.add(doctorWarn, chain.Config.ChainID, "relayer has no %s balance for fees", denom)
		}
	}
	return true
}

func (r *doctorReport) checkLink(store *LinkStore, link Link, chainByID map[string]namedChain) {
	chainA, okA := chainByID[link.ChainA]
	chainB, okB := chainByID[link.ChainB]
	if !okA || !okB {
		if !okA {
			r.add(doctorError, link.ID, "chain %s is not configured", link.ChainA)
		}
		if !okB {
			r.add(doctorError, link.ID, "chain %s is not configured", link.ChainB)
		}
		return
	}

	healthy := true
	notes := make([]string, 0, 2)

	healthA, err := inspectClient(chainA, link.ClientA, link.ChainB, link.ClientB)
	if err != nil {
		r.add(doctorError, link.ID, "client %s on %s: %v", link.ClientA, link.ChainA, err)
		healthy = false
	} else {
		switch {
		case !strings.EqualFold(healthA.Status, "Active"):
			r.add(doctorError, link.ID, "client %s on %s is %s", link.ClientA, link.ChainA, healthA.Status)
			healthy = false
		case healthA.TrustRemaining <= 0:
			r.add(doctorError, link.ID, "client %s on %s trust period expired", link.ClientA, link.ChainA)
			healthy = false
		case healthA.TrustRemaining < 24*time.Hour:
			r.add(doctorWarn, link.ID, "client %s on %s trust period ends in %s", link.ClientA, link.ChainA, formatDuration(healthA.TrustRemaining))
		default:
			notes = append(notes, fmt.Sprintf("%s trust %s", link.ClientA, formatDuration(healthA.TrustRemaining)))
		}
	}

	healthB, err := inspectClient(chainB, link.ClientB, link.ChainA, link.ClientA)
	if err != nil {
		r.add(doctorError, link.ID, "client %s on %s: %v", link.ClientB, link.ChainB, err)
		healthy = false
	} else {
		switch {
		case !strings.EqualFold(healthB.Status, "Active"):
			r.add(doctorError, link.ID, "client %s on %s is %s", link.ClientB, link.ChainB, healthB.Status)
			healthy = false
		case healthB.TrustRemaining <= 0:
			r.add(doctorError, link.ID, "client %s on %s trust period expired", link.ClientB, link.ChainB)
			healthy = false
		case healthB.TrustRemaining < 24*time.Hour:
			r.add(doctorWarn, link.ID, "client %s on %s trust period ends in %s", link.ClientB, link.ChainB, formatDuration(healthB.TrustRemaining))
		default:
			notes = append(notes, fmt.Sprintf("%s trust %s", link.ClientB, formatDuration(healthB.TrustRemaining)))
		}
	}

	if healthy {
		r.add(doctorOK, link.ID, "both clients active; %s", strings.Join(notes, "; "))
	}

	counts, err := store.PacketCounts(link.ID)
	if err != nil {
		r.add(doctorError, link.ID, "packet state: %v", err)
	} else {
		pending := 0
		for status, count := range counts {
			if status != "acked" && status != "timed_out" {
				pending += count
			}
		}
		if pending == 0 {
			r.add(doctorInfo, link.ID, "no pending packets")
		} else {
			r.add(doctorInfo, link.ID, "pending packets: %d (%s)", pending, formatPacketCounts(counts))
		}
	}

	for _, chainID := range []string{link.ChainA, link.ChainB} {
		if height, ok, err := store.ScanHeight(link.ID, chainID); err != nil {
			r.add(doctorError, link.ID, "scan state for %s: %v", chainID, err)
		} else if ok {
			r.add(doctorInfo, link.ID, "last scanned %s height %d", chainID, height)
		} else {
			r.add(doctorInfo, link.ID, "no scan height for %s", chainID)
		}
	}
}

func (r *doctorReport) checkOrphans(links []Link, chains []namedChain, chainByID map[string]namedChain, reachable map[string]bool) {
	linked := make(map[string]bool, len(links)*2)
	for _, link := range links {
		linked[clientKey(link.ChainA, link.ClientA)] = true
		linked[clientKey(link.ChainB, link.ClientB)] = true
	}

	seenPairs := make(map[string]bool)
	for _, chain := range chains {
		if !reachable[chain.Config.ChainID] {
			continue
		}
		clients, err := listClientStates(chain.Config)
		if err != nil {
			r.add(doctorError, chain.Config.ChainID, "list clients: %v", err)
			continue
		}
		for _, client := range clients {
			if linked[clientKey(chain.Config.ChainID, client.ID)] {
				continue
			}
			target, ok := chainByID[client.ChainID]
			if !ok {
				continue
			}
			counterpartyID, set, err := clientCounterparty(chain.Config, client.ID)
			if err != nil {
				r.add(doctorError, chain.Config.ChainID+":"+client.ID, "counterparty query: %v", err)
				continue
			}
			if !set {
				r.add(doctorError, chain.Config.ChainID+":"+client.ID, "orphan: no counterparty registered")
				continue
			}
			peer, err := clientStateInfo(target.Config, counterpartyID)
			if err != nil {
				if isNotFound(err) {
					r.add(doctorError, chain.Config.ChainID+":"+client.ID, "orphan: counterparty %s does not exist on %s", counterpartyID, target.Config.ChainID)
				} else {
					r.add(doctorError, chain.Config.ChainID+":"+client.ID, "counterparty %s: %v", counterpartyID, err)
				}
				continue
			}
			if peer.ChainID != chain.Config.ChainID {
				r.add(doctorError, chain.Config.ChainID+":"+client.ID, "orphan: counterparty %s tracks %s, not %s", counterpartyID, peer.ChainID, chain.Config.ChainID)
				continue
			}
			peerCounterpartyID, peerSet, err := clientCounterparty(target.Config, counterpartyID)
			if err != nil {
				r.add(doctorError, chain.Config.ChainID+":"+client.ID, "counterparty %s query: %v", counterpartyID, err)
				continue
			}
			if !peerSet {
				r.add(doctorError, chain.Config.ChainID+":"+client.ID, "orphan: counterparty %s has no counterparty", counterpartyID)
				continue
			}
			if peerCounterpartyID != client.ID {
				r.add(doctorError, chain.Config.ChainID+":"+client.ID, "orphan: counterparty %s points to %s, not %s", counterpartyID, peerCounterpartyID, client.ID)
				continue
			}

			key := canonicalPairKey(chain.Config.ChainID, client.ID, target.Config.ChainID, counterpartyID)
			if seenPairs[key] {
				continue
			}
			seenPairs[key] = true
			r.add(doctorWarn, chain.Config.ChainID+":"+client.ID, "unlinked pair with %s:%s; run ferry connect or pair", target.Config.ChainID, counterpartyID)
		}
	}
}

func inspectClient(chain namedChain, clientID, peerChainID, peerClientID string) (clientHealth, error) {
	info, err := clientStateInfo(chain.Config, clientID)
	if err != nil {
		return clientHealth{}, err
	}
	if info.ChainID != peerChainID {
		return clientHealth{}, fmt.Errorf("tracks %s, not %s", info.ChainID, peerChainID)
	}
	if info.TrustingPeriod <= 0 {
		return clientHealth{}, fmt.Errorf("missing or invalid trusting period")
	}

	counterpartyID, set, err := clientCounterparty(chain.Config, clientID)
	if err != nil {
		return clientHealth{}, err
	}
	if !set {
		return clientHealth{}, fmt.Errorf("no counterparty registered")
	}
	if counterpartyID != peerClientID {
		return clientHealth{}, fmt.Errorf("counterparty is %s, not %s", counterpartyID, peerClientID)
	}

	status, err := clientStatus(chain.Config, clientID)
	if err != nil {
		return clientHealth{}, err
	}
	timestamp, err := consensusTimestamp(chain.Config, clientID)
	if err != nil {
		return clientHealth{}, err
	}
	return clientHealth{
		Info:           info,
		Status:         status,
		TrustRemaining: time.Until(timestamp.Add(info.TrustingPeriod)),
	}, nil
}

func clientStatus(chain ChainConfig, clientID string) (string, error) {
	res, err := queryJSON(chain, "ibc", "client", "status", clientID)
	if err != nil {
		return "", err
	}
	status := str(res["status"])
	if status == "" {
		return "", fmt.Errorf("empty client status")
	}
	return status, nil
}

func consensusTimestamp(chain ChainConfig, clientID string) (time.Time, error) {
	res, err := queryJSON(chain, "ibc", "client", "consensus-state", clientID, "--latest-height", "--prove=false")
	if err != nil {
		return time.Time{}, err
	}
	consensusState, _ := res["consensus_state"].(map[string]any)
	if consensusState == nil {
		return time.Time{}, fmt.Errorf("consensus state not found")
	}
	timestamp := str(consensusState["timestamp"])
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse consensus timestamp %q: %w", timestamp, err)
	}
	return parsed, nil
}

func relayerBalances(chain ChainConfig, address string) ([]string, map[string]bool, error) {
	res, err := queryJSON(chain, "bank", "balances", address)
	if err != nil {
		return nil, nil, err
	}
	rawBalances, _ := res["balances"].([]any)
	coins := make([]string, 0, len(rawBalances))
	denoms := make(map[string]bool, len(rawBalances))
	for _, rawBalance := range rawBalances {
		balance, _ := rawBalance.(map[string]any)
		denom := str(balance["denom"])
		amount := str(balance["amount"])
		if denom == "" || amount == "" {
			continue
		}
		coins = append(coins, amount+denom)
		denoms[denom] = true
	}
	sort.Strings(coins)
	return coins, denoms, nil
}

func feeDenoms(chain ChainConfig) []string {
	set := make(map[string]bool)
	if denom := gasDenom(chain.GasPrices); denom != "" {
		set[denom] = true
	}
	for _, fee := range chain.Fee {
		if fee.Denom != "" {
			set[fee.Denom] = true
		}
	}
	denoms := make([]string, 0, len(set))
	for denom := range set {
		denoms = append(denoms, denom)
	}
	sort.Strings(denoms)
	return denoms
}

func gasDenom(gasPrices string) string {
	value := strings.TrimSpace(gasPrices)
	index := 0
	for index < len(value) {
		character := value[index]
		if (character < '0' || character > '9') && character != '.' {
			break
		}
		index++
	}
	return value[index:]
}

func formatDuration(duration time.Duration) string {
	negative := duration < 0
	if negative {
		duration = -duration
	}
	duration = duration.Round(time.Minute)
	days := int(duration / (24 * time.Hour))
	duration -= time.Duration(days) * 24 * time.Hour
	hours := int(duration / time.Hour)
	duration -= time.Duration(hours) * time.Hour
	minutes := int(duration / time.Minute)

	var result string
	switch {
	case days > 0:
		result = fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		result = fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		result = fmt.Sprintf("%dm", minutes)
	}
	if negative {
		return "-" + result
	}
	return result
}

func formatPacketCounts(counts map[string]int) string {
	statuses := make([]string, 0, len(counts))
	for status := range counts {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
		parts = append(parts, fmt.Sprintf("%s=%d", status, counts[status]))
	}
	return strings.Join(parts, ", ")
}

func clientKey(chainID, clientID string) string {
	return chainID + "\x00" + clientID
}

func canonicalPairKey(chainA, clientA, chainB, clientB string) string {
	if chainA > chainB || (chainA == chainB && clientA > clientB) {
		chainA, clientA, chainB, clientB = chainB, clientB, chainA, clientA
	}
	return linkID(chainA, clientA, chainB, clientB)
}
