package replica

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"novig-take-home/internal/domain"
)

func TestDuplicateEventIDIsIdempotentNoOp(t *testing.T) {
	svc, store := newReplicaProcessEventTestService(t)
	defer store.Close()

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

func TestProcessEventRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		evt         domain.ReplicationEvent
		errContains string
	}{
		{
			name:        "missing event id",
			evt:         domain.ReplicationEvent{Seq: 1, EventID: "", EventType: domain.EventTypeMarketUpdated, MarketID: "m1", Payload: mustMarketPayload(t, "m1", domain.MarketStatusOpen)},
			errContains: "event_id is required",
		},
		{
			name:        "invalid sequence",
			evt:         domain.ReplicationEvent{Seq: 0, EventID: "evt-1", EventType: domain.EventTypeMarketUpdated, MarketID: "m1", Payload: mustMarketPayload(t, "m1", domain.MarketStatusOpen)},
			errContains: "seq must be >= 1",
		},
		{
			name:        "missing market id",
			evt:         domain.ReplicationEvent{Seq: 1, EventID: "evt-1", EventType: domain.EventTypeMarketUpdated, MarketID: "", Payload: mustMarketPayload(t, "m1", domain.MarketStatusOpen)},
			errContains: "market_id is required",
		},
		{
			name:        "unsupported event type",
			evt:         domain.ReplicationEvent{Seq: 1, EventID: "evt-1", EventType: domain.EventType("UNKNOWN"), MarketID: "m1", Payload: mustMarketPayload(t, "m1", domain.MarketStatusOpen)},
			errContains: "unsupported event_type",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newReplicaProcessEventTestService(t)
			defer store.Close()

			err := svc.processEvent(tc.evt)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
			if got := svc.LastAppliedSeq(); got != 0 {
				t.Fatalf("expected last applied seq to remain 0, got %d", got)
			}
		})
	}
}

func TestProcessEventRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name        string
		evt         domain.ReplicationEvent
		errContains string
	}{
		{
			name:        "invalid json",
			evt:         domain.ReplicationEvent{Seq: 1, EventID: "evt-1", EventType: domain.EventTypeMarketUpdated, MarketID: "m1", Payload: []byte("{")},
			errContains: "decode payload",
		},
		{
			name:        "payload market id mismatch",
			evt:         domain.ReplicationEvent{Seq: 1, EventID: "evt-1", EventType: domain.EventTypeMarketUpdated, MarketID: "m1", Payload: mustMarketPayload(t, "m2", domain.MarketStatusOpen)},
			errContains: "payload market_id mismatch",
		},
		{
			name:        "invalid payload status",
			evt:         domain.ReplicationEvent{Seq: 1, EventID: "evt-1", EventType: domain.EventTypeMarketUpdated, MarketID: "m1", Payload: mustInvalidStatusPayload(t, "m1", "BROKEN")},
			errContains: "unsupported market status",
		},
		{
			name:        "suspended event with open status",
			evt:         domain.ReplicationEvent{Seq: 1, EventID: "evt-1", EventType: domain.EventTypeMarketSuspended, MarketID: "m1", Payload: mustMarketPayload(t, "m1", domain.MarketStatusOpen)},
			errContains: "requires payload status=SUSPENDED",
		},
		{
			name:        "reopened event with suspended status",
			evt:         domain.ReplicationEvent{Seq: 1, EventID: "evt-1", EventType: domain.EventTypeMarketReopened, MarketID: "m1", Payload: mustMarketPayload(t, "m1", domain.MarketStatusSuspended)},
			errContains: "requires payload status=OPEN",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newReplicaProcessEventTestService(t)
			defer store.Close()

			err := svc.processEvent(tc.evt)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
			if got := svc.LastAppliedSeq(); got != 0 {
				t.Fatalf("expected last applied seq to remain 0, got %d", got)
			}
		})
	}
}

func newReplicaProcessEventTestService(t *testing.T) (*Service, *Store) {
	t.Helper()

	store, err := NewStore(InMemoryDSN(fmt.Sprintf("test_process_event_%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	svc, err := NewService(store, ServiceConfig{
		ID:          "replica-test",
		CoreBaseURL: "http://core",
		Now:         func() time.Time { return time.UnixMilli(10_000) },
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("new service: %v", err)
	}

	return svc, store
}

func mustMarketPayload(t *testing.T, marketID string, status domain.MarketStatus) []byte {
	t.Helper()
	payload, err := json.Marshal(domain.MarketState{MarketID: marketID, Status: status})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return payload
}

func mustInvalidStatusPayload(t *testing.T, marketID string, status string) []byte {
	t.Helper()
	payload, err := json.Marshal(struct {
		MarketID string `json:"market_id"`
		Status   string `json:"status"`
	}{
		MarketID: marketID,
		Status:   status,
	})
	if err != nil {
		t.Fatalf("marshal invalid status payload: %v", err)
	}
	return payload
}
