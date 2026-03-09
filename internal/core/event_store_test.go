package core

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"novig-take-home/internal/domain"
)

func TestEventsFromSeqReturnsAscendingSubset(t *testing.T) {
	store, err := NewEventStore(InMemoryDSN(fmt.Sprintf("event_store_from_seq_%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	for i := int64(1); i <= 3; i++ {
		payload, _ := json.Marshal(domain.MarketState{MarketID: "m1", Status: domain.MarketStatusOpen})
		evt := domain.ReplicationEvent{
			Seq:       i,
			EventID:   fmt.Sprintf("evt-%d", i),
			TsUnixMs:  1_000 + i,
			EventType: domain.EventTypeMarketUpdated,
			MarketID:  "m1",
			Payload:   payload,
		}
		if err := store.Insert(evt); err != nil {
			t.Fatalf("insert seq %d: %v", i, err)
		}
	}

	events, err := store.EventsFromSeq(2)
	if err != nil {
		t.Fatalf("events from seq: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Seq != 2 || events[1].Seq != 3 {
		t.Fatalf("expected seqs [2,3], got [%d,%d]", events[0].Seq, events[1].Seq)
	}
}
