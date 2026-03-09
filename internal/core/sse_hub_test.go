package core

import (
	"testing"

	"novig-take-home/internal/domain"
)

func TestSSEHubBroadcastDropsSlowSubscriber(t *testing.T) {
	hub := NewSSEHub()
	ch, _ := hub.Subscribe(1)

	evt1 := domain.ReplicationEvent{Seq: 1, EventID: "evt-1"}
	evt2 := domain.ReplicationEvent{Seq: 2, EventID: "evt-2"}
	hub.Broadcast(evt1)
	hub.Broadcast(evt2)

	first, ok := <-ch
	if !ok {
		t.Fatalf("expected first event before channel closes")
	}
	if first.Seq != 1 {
		t.Fatalf("expected first seq 1, got %d", first.Seq)
	}

	if _, ok := <-ch; ok {
		t.Fatalf("expected channel to be closed after slow-subscriber drop")
	}
}

func TestSSEHubUnsubscribeIsIdempotent(t *testing.T) {
	hub := NewSSEHub()
	_, unsubscribe := hub.Subscribe(1)

	unsubscribe()
	unsubscribe() // should not panic

	if len(hub.subscribers) != 0 {
		t.Fatalf("expected no subscribers after unsubscribe")
	}
}
