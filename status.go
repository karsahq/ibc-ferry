package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

type relayStats struct {
	mu        sync.Mutex
	started   time.Time
	cycles    uint64
	lastCycle time.Time
	lastError string
	actions   map[string]uint64
	pruned    uint64
}

func newRelayStats() *relayStats {
	return &relayStats{
		started: time.Now().UTC(),
		actions: make(map[string]uint64),
	}
}

func (s *relayStats) recordCycle(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cycles++
	s.lastCycle = time.Now().UTC()
	if err != nil {
		s.lastError = err.Error()
	} else {
		s.lastError = ""
	}
}

func (s *relayStats) addAction(action string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions[action]++
}

func (s *relayStats) addPruned(count int64) {
	if s == nil || count <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruned += uint64(count)
}

type statsSnapshot struct {
	Started   time.Time
	Cycles    uint64
	LastCycle time.Time
	LastError string
	Actions   map[string]uint64
	Pruned    uint64
}

func (s *relayStats) snapshot() statsSnapshot {
	if s == nil {
		return statsSnapshot{Actions: map[string]uint64{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actions := make(map[string]uint64, len(s.actions))
	for action, count := range s.actions {
		actions[action] = count
	}
	return statsSnapshot{
		Started:   s.started,
		Cycles:    s.cycles,
		LastCycle: s.lastCycle,
		LastError: s.lastError,
		Actions:   actions,
		Pruned:    s.pruned,
	}
}

type relayStatus struct {
	Time          time.Time         `json:"time"`
	UptimeSeconds float64           `json:"uptime_seconds"`
	LastCycle     time.Time         `json:"last_cycle,omitempty"`
	Cycles        uint64            `json:"cycles"`
	LastError     string            `json:"last_error,omitempty"`
	Actions       map[string]uint64 `json:"actions"`
	PrunedPackets uint64            `json:"pruned_packets_total"`
	Totals        map[string]int    `json:"packet_totals"`
	Links         []linkStatus      `json:"links"`
}

type linkStatus struct {
	ID          string           `json:"id"`
	Status      string           `json:"status"`
	ChainA      string           `json:"chain_a"`
	ClientA     string           `json:"client_a"`
	ChainB      string           `json:"chain_b"`
	ClientB     string           `json:"client_b"`
	Packets     map[string]int   `json:"packets"`
	Pending     int              `json:"pending"`
	ScanHeights map[string]int64 `json:"scan_heights"`
}

func buildStatus(store *LinkStore, stats *relayStats) (relayStatus, error) {
	links, err := store.List()
	if err != nil {
		return relayStatus{}, err
	}
	snapshot := stats.snapshot()
	status := relayStatus{
		Time:          time.Now().UTC(),
		LastCycle:     snapshot.LastCycle,
		Cycles:        snapshot.Cycles,
		LastError:     snapshot.LastError,
		Actions:       snapshot.Actions,
		PrunedPackets: snapshot.Pruned,
		Totals:        make(map[string]int),
		Links:         make([]linkStatus, 0, len(links)),
	}
	if !snapshot.Started.IsZero() {
		status.UptimeSeconds = time.Since(snapshot.Started).Seconds()
	}

	for _, link := range links {
		counts, err := store.PacketCounts(link.ID)
		if err != nil {
			return relayStatus{}, err
		}
		pending := 0
		for packetStatus, count := range counts {
			status.Totals[packetStatus] += count
			if packetStatus != PacketAcked && packetStatus != PacketTimedOut {
				pending += count
			}
		}
		scanHeights := make(map[string]int64, 2)
		for _, chainID := range []string{link.ChainA, link.ChainB} {
			height, ok, err := store.ScanHeight(link.ID, chainID)
			if err != nil {
				return relayStatus{}, err
			}
			if ok {
				scanHeights[chainID] = height
			}
		}
		status.Links = append(status.Links, linkStatus{
			ID:          link.ID,
			Status:      string(link.Status),
			ChainA:      link.ChainA,
			ClientA:     link.ClientA,
			ChainB:      link.ChainB,
			ClientB:     link.ClientB,
			Packets:     counts,
			Pending:     pending,
			ScanHeights: scanHeights,
		})
	}
	return status, nil
}

func cmdStatus(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("status takes no positional arguments")
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	status, err := buildStatus(store, nil)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(status)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LINK ID\tSTATUS\tPENDING\tACKED\tTIMED_OUT\tSCAN A\tSCAN B")
	for _, link := range status.Links {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
			link.ID, strings.ToUpper(link.Status), link.Pending,
			link.Packets[PacketAcked], link.Packets[PacketTimedOut],
			link.ScanHeights[link.ChainA], link.ScanHeights[link.ChainB],
		)
	}
	return w.Flush()
}

func serveStatus(ctx context.Context, addr string, store *LinkStore, stats *relayStats, interval time.Duration) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("status listen %s: %w", addr, err)
	}
	server := &http.Server{
		Handler:           newStatusMux(store, stats, interval),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("status: %v", err)
		}
	}()
	log.Printf("status: listening on http://%s", addr)
	return nil
}

func newStatusMux(store *LinkStore, stats *relayStats, interval time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		snapshot := stats.snapshot()
		if snapshot.LastCycle.IsZero() {
			if time.Since(snapshot.Started) < 5*time.Minute {
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, "starting")
				return
			}
			http.Error(w, "no relay cycle completed", http.StatusServiceUnavailable)
			return
		}
		maxAge := 3 * interval
		if maxAge < 30*time.Second {
			maxAge = 30 * time.Second
		}
		if time.Since(snapshot.LastCycle) > maxAge {
			http.Error(w, "relay cycle is stale", http.StatusServiceUnavailable)
			return
		}
		if snapshot.LastError != "" {
			http.Error(w, snapshot.LastError, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		status, err := buildStatus(store, stats)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(status)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		status, err := buildStatus(store, stats)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		writeMetrics(w, status)
	})
	return mux
}

func writeMetrics(w io.Writer, status relayStatus) {
	fmt.Fprintln(w, "# HELP ferry_links_total Number of stored links.")
	fmt.Fprintln(w, "# TYPE ferry_links_total gauge")
	fmt.Fprintf(w, "ferry_links_total %d\n", len(status.Links))

	fmt.Fprintln(w, "# HELP ferry_packets Number of packets by link and status.")
	fmt.Fprintln(w, "# TYPE ferry_packets gauge")
	for _, link := range status.Links {
		statuses := make([]string, 0, len(link.Packets))
		for packetStatus := range link.Packets {
			statuses = append(statuses, packetStatus)
		}
		sort.Strings(statuses)
		for _, packetStatus := range statuses {
			fmt.Fprintf(w, "ferry_packets{link=\"%s\",status=\"%s\"} %d\n",
				promLabel(link.ID), promLabel(packetStatus), link.Packets[packetStatus])
		}
	}

	fmt.Fprintln(w, "# HELP ferry_pending_packets Number of packets that are not acked or timed out.")
	fmt.Fprintln(w, "# TYPE ferry_pending_packets gauge")
	for _, link := range status.Links {
		fmt.Fprintf(w, "ferry_pending_packets{link=\"%s\"} %d\n", promLabel(link.ID), link.Pending)
	}

	fmt.Fprintln(w, "# HELP ferry_scan_height Last scanned height by link and chain.")
	fmt.Fprintln(w, "# TYPE ferry_scan_height gauge")
	for _, link := range status.Links {
		for chainID, height := range link.ScanHeights {
			fmt.Fprintf(w, "ferry_scan_height{link=\"%s\",chain=\"%s\"} %d\n",
				promLabel(link.ID), promLabel(chainID), height)
		}
	}

	fmt.Fprintln(w, "# HELP ferry_relay_cycles_total Completed relay cycles.")
	fmt.Fprintln(w, "# TYPE ferry_relay_cycles_total counter")
	fmt.Fprintf(w, "ferry_relay_cycles_total %d\n", status.Cycles)

	fmt.Fprintln(w, "# HELP ferry_relay_actions_total Successful relay actions.")
	fmt.Fprintln(w, "# TYPE ferry_relay_actions_total counter")
	actions := make([]string, 0, len(status.Actions))
	for action := range status.Actions {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	for _, action := range actions {
		fmt.Fprintf(w, "ferry_relay_actions_total{action=\"%s\"} %d\n", promLabel(action), status.Actions[action])
	}

	fmt.Fprintln(w, "# HELP ferry_relay_last_cycle_timestamp_seconds Unix time of the last completed cycle.")
	fmt.Fprintln(w, "# TYPE ferry_relay_last_cycle_timestamp_seconds gauge")
	if status.LastCycle.IsZero() {
		fmt.Fprintln(w, "ferry_relay_last_cycle_timestamp_seconds 0")
	} else {
		fmt.Fprintf(w, "ferry_relay_last_cycle_timestamp_seconds %d\n", status.LastCycle.Unix())
	}

	fmt.Fprintln(w, "# HELP ferry_pruned_packets_total Terminal packets removed by retention.")
	fmt.Fprintln(w, "# TYPE ferry_pruned_packets_total counter")
	fmt.Fprintf(w, "ferry_pruned_packets_total %d\n", status.PrunedPackets)

	fmt.Fprintln(w, "# HELP ferry_relay_last_cycle_error Whether the last cycle failed.")
	fmt.Fprintln(w, "# TYPE ferry_relay_last_cycle_error gauge")
	if status.LastError == "" {
		fmt.Fprintln(w, "ferry_relay_last_cycle_error 0")
	} else {
		fmt.Fprintln(w, "ferry_relay_last_cycle_error 1")
	}
}

func promLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}
