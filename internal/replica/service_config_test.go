package replica

import (
	"fmt"
	"testing"
	"time"
)

func TestNewServiceDefaultsNonPositiveDurations(t *testing.T) {
	store, err := NewStore(InMemoryDSN(fmt.Sprintf("service_config_defaults_%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, ServiceConfig{
		CoreBaseURL:         "http://core",
		ReconnectBackoff:    0,
		ReconnectBackoffMax: -1 * time.Second,
		SnapshotTimeout:     0,
		StreamIdleTimeout:   -1 * time.Second,
		HeartbeatInterval:   -1 * time.Second,
		HeartbeatTimeout:    0,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	if svc.cfg.ReconnectBackoff != 500*time.Millisecond {
		t.Fatalf("expected default reconnect backoff 500ms, got %s", svc.cfg.ReconnectBackoff)
	}
	if svc.cfg.ReconnectBackoffMax != 5*time.Second {
		t.Fatalf("expected default reconnect backoff max 5s, got %s", svc.cfg.ReconnectBackoffMax)
	}
	if svc.cfg.SnapshotTimeout != 3*time.Second {
		t.Fatalf("expected default snapshot timeout 3s, got %s", svc.cfg.SnapshotTimeout)
	}
	if svc.cfg.StreamIdleTimeout != 45*time.Second {
		t.Fatalf("expected default stream idle timeout 45s, got %s", svc.cfg.StreamIdleTimeout)
	}
	if svc.cfg.HeartbeatInterval != 2*time.Second {
		t.Fatalf("expected default heartbeat interval 2s, got %s", svc.cfg.HeartbeatInterval)
	}
	if svc.cfg.HeartbeatTimeout != 2*time.Second {
		t.Fatalf("expected default heartbeat timeout 2s, got %s", svc.cfg.HeartbeatTimeout)
	}
}
