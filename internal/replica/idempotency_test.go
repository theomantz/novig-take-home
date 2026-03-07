package replica

import (
	"encoding/json"
	"testing"
	"time"

	"novig-take-home/internal/domain"
)

func TestDuplicateEventIDIsIdempotentNoOp(t *testing.T) {
	store, err := NewStore(InMemoryDSN("test_dup"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, ServiceConfig{
		ID:          "replica-test",
		CoreBaseURL: "http://core",
		Now:         func() time.Time { return time.UnixMilli(10_000) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	payload, _ := json.Marshal(domain.MarketState{MarketID: "m1", Status: domain.MarketStatusOpen})
	evt := domain.ReplicationEvent{Seq: 1, EventID: "evt-1", MarketID: "m1", EventType: domain.EventTypeMarketUpdated, Payload: payload}

	if err := svc.processEvent(evt); err != nil {
		t.Fatalf("apply event: %v", err)
	}
	if got := svc.LastAppliedSeq(); got != 1 {
		t.Fatalf("expected seq 1, got %d", got)
	}

	if err := svc.processEvent(evt); err != nil {
		t.Fatalf("duplicate event should be no-op: %v", err)
	}
	if got := svc.LastAppliedSeq(); got != 1 {
		t.Fatalf("expected seq to remain 1, got %d", got)
	}
}
