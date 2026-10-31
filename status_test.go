package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildStatusAndMetrics(t *testing.T) {
	store, err := openLinkStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	link, err := NewLink("chain-a", "07-tendermint-0", "chain-b", "07-tendermint-0")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(link); err != nil {
		t.Fatal(err)
	}
	for _, record := range []PacketRecord{
		{LinkID: link.ID, Direction: directionAToB, Sequence: 1, SourceClient: link.ClientA, DestinationClient: link.ClientB, Status: PacketPending},
		{LinkID: link.ID, Direction: directionAToB, Sequence: 2, SourceClient: link.ClientA, DestinationClient: link.ClientB, Status: PacketAcked},
	} {
		if _, err := store.InsertPacket(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetScanHeight(link.ID, link.ChainA, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.SetScanHeight(link.ID, link.ChainB, 20); err != nil {
		t.Fatal(err)
	}

	stats := newRelayStats()
	stats.addAction("recv")
	stats.recordCycle(nil)
	status, err := buildStatus(store, stats)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Links) != 1 {
		t.Fatalf("links = %d", len(status.Links))
	}
	if status.Links[0].Pending != 1 || status.Links[0].Packets[PacketAcked] != 1 {
		t.Fatalf("unexpected link status: %+v", status.Links[0])
	}
	if status.Links[0].ScanHeights[link.ChainA] != 10 || status.Links[0].ScanHeights[link.ChainB] != 20 {
		t.Fatalf("unexpected scan heights: %+v", status.Links[0].ScanHeights)
	}
	if status.Actions["recv"] != 1 || status.Cycles != 1 {
		t.Fatalf("unexpected stats: %+v", status)
	}

	var metrics bytes.Buffer
	writeMetrics(&metrics, status)
	output := metrics.String()
	for _, want := range []string{
		"ferry_links_total 1",
		`ferry_packets{link="` + link.ID + `",status="pending"} 1`,
		`ferry_packets{link="` + link.ID + `",status="acked"} 1`,
		`ferry_scan_height{link="` + link.ID + `",chain="` + link.ChainA + `"} 10`,
		`ferry_relay_actions_total{action="recv"} 1`,
		"ferry_relay_cycles_total 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metrics missing %q:\n%s", want, output)
		}
	}
}

func TestStatusHTTP(t *testing.T) {
	store, err := openLinkStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	link, err := NewLink("chain-a", "07-tendermint-0", "chain-b", "07-tendermint-0")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(link); err != nil {
		t.Fatal(err)
	}

	stats := newRelayStats()
	stats.recordCycle(nil)
	mux := newStatusMux(store, stats, 5*time.Second)

	health := httptest.NewRecorder()
	mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), "ok") {
		t.Fatalf("health = %d %q", health.Code, health.Body.String())
	}

	status := httptest.NewRecorder()
	mux.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status code = %d", status.Code)
	}
	var payload relayStatus
	if err := json.Unmarshal(status.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Links) != 1 || payload.Links[0].ID != link.ID {
		t.Fatalf("unexpected status payload: %+v", payload)
	}

	metrics := httptest.NewRecorder()
	mux.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "ferry_links_total 1") {
		t.Fatalf("metrics = %d %q", metrics.Code, metrics.Body.String())
	}

	stats.mu.Lock()
	stats.lastCycle = time.Now().Add(-time.Hour)
	stats.mu.Unlock()
	stale := httptest.NewRecorder()
	mux.ServeHTTP(stale, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if stale.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale health code = %d", stale.Code)
	}

	stats.recordCycle(errors.New("chain unavailable"))
	failed := httptest.NewRecorder()
	mux.ServeHTTP(failed, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed-cycle health code = %d", failed.Code)
	}
}
