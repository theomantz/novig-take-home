package replica

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novig-take-home/internal/domain"
)

func TestRefreshFromSnapshotRejectsRegressedSequence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshot" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(domain.SnapshotResponse{
			LastSeq: 4,
			Markets: map[string]domain.MarketState{
				"m1": {MarketID: "m1", Status: domain.MarketStatusOpen},
			},
		})
	}))
	defer server.Close()

	store, err := NewStore(InMemoryDSN(fmt.Sprintf("refresh_snapshot_regressed_%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	nowMs := int64(50_000)
	if err := store.SetCheckpoint(5, nowMs); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	svc, err := NewService(store, ServiceConfig{
		ID:              "replica-1",
		CoreBaseURL:     server.URL,
		Logger:          slog.New(slog.NewJSONHandler(io.Discard, nil)),
		SnapshotTimeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	err = svc.refreshFromSnapshot(context.Background())
	if err == nil {
		t.Fatalf("expected stale snapshot error")
	}
	if !strings.Contains(err.Error(), "snapshot last_seq regressed") {
		t.Fatalf("unexpected error: %v", err)
	}

	checkpoint, err := store.GetCheckpoint()
	if err != nil {
		t.Fatalf("get checkpoint: %v", err)
	}
	if checkpoint != 5 {
		t.Fatalf("expected checkpoint to remain 5, got %d", checkpoint)
	}
}

func TestRefreshFromSnapshotRejectsInvalidMarketPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshot" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(domain.SnapshotResponse{
			LastSeq: 1,
			Markets: map[string]domain.MarketState{
				"m1": {MarketID: "wrong-id", Status: domain.MarketStatusOpen},
			},
		})
	}))
	defer server.Close()

	store, err := NewStore(InMemoryDSN(fmt.Sprintf("refresh_snapshot_invalid_%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, ServiceConfig{
		ID:          "replica-1",
		CoreBaseURL: server.URL,
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	err = svc.refreshFromSnapshot(context.Background())
	if err == nil {
		t.Fatalf("expected invalid snapshot error")
	}
	if !strings.Contains(err.Error(), "snapshot market_id mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExponentialBackoffDurationCapsAtConfiguredMaximum(t *testing.T) {
	initial := 100 * time.Millisecond
	capDelay := 1600 * time.Millisecond

	if got := exponentialBackoffDuration(initial, capDelay, 0); got != initial {
		t.Fatalf("expected attempt0=%s, got %s", initial, got)
	}
	if got := exponentialBackoffDuration(initial, capDelay, 1); got != 200*time.Millisecond {
		t.Fatalf("expected attempt1=200ms, got %s", got)
	}
	if got := exponentialBackoffDuration(initial, capDelay, 4); got != capDelay {
		t.Fatalf("expected cap at %s, got %s", capDelay, got)
	}
	if got := exponentialBackoffDuration(initial, capDelay, 8); got != capDelay {
		t.Fatalf("expected cap to hold at %s, got %s", capDelay, got)
	}
}

func TestStatusIncludesBootstrappedAndLastError(t *testing.T) {
	store, err := NewStore(InMemoryDSN(fmt.Sprintf("status_last_error_%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, ServiceConfig{CoreBaseURL: "http://core"})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	svc.mu.Lock()
	svc.connected = true
	svc.bootstrapped = true
	svc.lastAppliedSeq = 10
	svc.coreLastSeq = 12
	svc.lastSyncAtMs = 12345
	svc.lastError = "stream idle timeout"
	svc.mu.Unlock()

	status := svc.Status()
	if !status.Bootstrapped {
		t.Fatalf("expected bootstrapped=true")
	}
	if status.LastError != "stream idle timeout" {
		t.Fatalf("expected last_error to be propagated, got %q", status.LastError)
	}
}
