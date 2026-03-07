package replica

import (
	"testing"
	"time"
)

func TestNewServiceDefaultsNonPositiveHeartbeatDurations(t *testing.T) {
	store, err := NewStore(InMemoryDSN("service_config_defaults"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, ServiceConfig{
		CoreBaseURL:       "http://core",
		HeartbeatInterval: -1 * time.Second,
		HeartbeatTimeout:  0,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	if svc.cfg.HeartbeatInterval != 2*time.Second {
		t.Fatalf("expected default heartbeat interval 2s, got %s", svc.cfg.HeartbeatInterval)
	}
	if svc.cfg.HeartbeatTimeout != 2*time.Second {
		t.Fatalf("expected default heartbeat timeout 2s, got %s", svc.cfg.HeartbeatTimeout)
	}
}
