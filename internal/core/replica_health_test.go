package core

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestReplicaStatusesClassifyHealthyStaleOffline verifies health class transitions and lag math from heartbeat age.
func TestReplicaStatusesClassifyHealthyStaleOffline(t *testing.T) {
	store, err := NewEventStore(InMemoryDSN("replica_health_classification"))
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	defer store.Close()

	nowMs := int64(1_700_000_000_000)
	svc, err := NewService(store, ServiceConfig{
		DefaultMarketID:     "m1",
		ReplicaStaleAfter:   5 * time.Second,
		ReplicaOfflineAfter: 15 * time.Second,
		Logger:              slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Now:                 func() time.Time { return time.UnixMilli(nowMs) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	svc.mu.Lock()
	svc.lastSeq = 10
	svc.mu.Unlock()

	svc.RecordReplicaHeartbeat(ReplicaHeartbeat{
		ReplicaID:      "r1",
		Connected:      true,
		LastAppliedSeq: 7,
		CoreLastSeq:    9,
		LastSyncAt:     nowMs - 100,
	})

	resp := svc.ReplicaStatuses()
	if len(resp.Replicas) != 1 {
		t.Fatalf("expected 1 replica, got %d", len(resp.Replicas))
	}
	if got := resp.Replicas[0].Health; got != ReplicaHealthHealthy {
		t.Fatalf("expected healthy, got %s", got)
	}
	if got := resp.Replicas[0].LagSeq; got != 3 {
		t.Fatalf("expected lag 3, got %d", got)
	}
	if got := resp.Replicas[0].LastSeenAt; got != nowMs {
		t.Fatalf("expected last seen %d, got %d", nowMs, got)
	}

	nowMs += 6_000
	resp = svc.ReplicaStatuses()
	if got := resp.Replicas[0].Health; got != ReplicaHealthStale {
		t.Fatalf("expected stale after threshold, got %s", got)
	}

	nowMs += 10_000
	resp = svc.ReplicaStatuses()
	if got := resp.Replicas[0].Health; got != ReplicaHealthOffline {
		t.Fatalf("expected offline after threshold, got %s", got)
	}

	svc.RecordReplicaHeartbeat(ReplicaHeartbeat{
		ReplicaID:      "r1",
		Connected:      false,
		LastAppliedSeq: 8,
		CoreLastSeq:    10,
		LastSyncAt:     nowMs,
	})
	resp = svc.ReplicaStatuses()
	if got := resp.Replicas[0].Health; got != ReplicaHealthOffline {
		t.Fatalf("expected disconnected replica to be offline, got %s", got)
	}
}

// TestReplicaStatusEndpointsAcceptHeartbeatAndExposeStatus validates heartbeat ingestion and status API output.
func TestReplicaStatusEndpointsAcceptHeartbeatAndExposeStatus(t *testing.T) {
	store, err := NewEventStore(InMemoryDSN("replica_health_http"))
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	defer store.Close()

	nowMs := int64(2_000_000)
	svc, err := NewService(store, ServiceConfig{
		DefaultMarketID:     "m1",
		ReplicaStaleAfter:   5 * time.Second,
		ReplicaOfflineAfter: 15 * time.Second,
		Logger:              slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Now:                 func() time.Time { return time.UnixMilli(nowMs) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	svc.mu.Lock()
	svc.lastSeq = 12
	svc.mu.Unlock()

	server := httptest.NewServer(NewHandler(svc, slog.New(slog.NewJSONHandler(io.Discard, nil))))
	defer server.Close()

	payload := ReplicaHeartbeat{
		ReplicaID:      "replica-a",
		Connected:      true,
		LastAppliedSeq: 10,
		CoreLastSeq:    11,
		LastSyncAt:     nowMs,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}

	resp, err := http.Post(server.URL+"/internal/replicas/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post heartbeat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	statusResp, err := http.Get(server.URL + "/replicas/status")
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", statusResp.StatusCode)
	}

	var out ReplicasStatusResponse
	if err := json.NewDecoder(statusResp.Body).Decode(&out); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if len(out.Replicas) != 1 {
		t.Fatalf("expected 1 replica in status response, got %d", len(out.Replicas))
	}
	got := out.Replicas[0]
	if got.ReplicaID != "replica-a" {
		t.Fatalf("expected replica-a, got %s", got.ReplicaID)
	}
	if got.Health != ReplicaHealthHealthy {
		t.Fatalf("expected healthy, got %s", got.Health)
	}
	if got.LagSeq != 2 {
		t.Fatalf("expected lag 2, got %d", got.LagSeq)
	}
}
