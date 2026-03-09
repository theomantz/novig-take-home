package replica

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSendHeartbeatPostsReplicaStatus verifies sendHeartbeat posts the expected replica status payload.
func TestSendHeartbeatPostsReplicaStatus(t *testing.T) {
	received := make(chan heartbeatRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/replicas/heartbeat" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var in heartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- in
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"accepted"}`)
	}))
	defer server.Close()

	store, err := NewStore(InMemoryDSN("heartbeat_send"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, ServiceConfig{
		ID:               "replica-heartbeat",
		CoreBaseURL:      server.URL,
		HeartbeatTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	svc.mu.Lock()
	svc.connected = true
	svc.lastAppliedSeq = 21
	svc.coreLastSeq = 23
	svc.lastSyncAtMs = 111_222
	svc.mu.Unlock()

	if err := svc.sendHeartbeat(context.Background()); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}

	select {
	case got := <-received:
		if got.ReplicaID != "replica-heartbeat" {
			t.Fatalf("expected replica id replica-heartbeat, got %s", got.ReplicaID)
		}
		if !got.Connected {
			t.Fatalf("expected connected=true")
		}
		if got.LastAppliedSeq != 21 {
			t.Fatalf("expected last_applied_seq=21, got %d", got.LastAppliedSeq)
		}
		if got.CoreLastSeq != 23 {
			t.Fatalf("expected core_last_seq=23, got %d", got.CoreLastSeq)
		}
		if got.LastSyncAt != 111_222 {
			t.Fatalf("expected last_sync_at=111222, got %d", got.LastSyncAt)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for heartbeat payload")
	}
}

// TestSendHeartbeatReturnsErrorOnNonSuccessStatus verifies non-2xx heartbeat responses surface as errors.
func TestSendHeartbeatReturnsErrorOnNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer server.Close()

	store, err := NewStore(InMemoryDSN("heartbeat_failure"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, ServiceConfig{
		ID:               "replica-heartbeat",
		CoreBaseURL:      server.URL,
		HeartbeatTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	err = svc.sendHeartbeat(context.Background())
	if err == nil {
		t.Fatalf("expected heartbeat error on 500 response")
	}
	if !strings.Contains(err.Error(), "heartbeat status 500") {
		t.Fatalf("unexpected error: %v", err)
	}
}
