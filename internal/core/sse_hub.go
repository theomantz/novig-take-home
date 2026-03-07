package core

import (
	"sync"

	"novig-take-home/internal/domain"
)

type SSEHub struct {
	mu          sync.Mutex
	nextID      int
	subscribers map[int]chan domain.ReplicationEvent
}

func NewSSEHub() *SSEHub {
	return &SSEHub{
		subscribers: make(map[int]chan domain.ReplicationEvent),
	}
}

func (h *SSEHub) Subscribe(buffer int) (chan domain.ReplicationEvent, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id := h.nextID
	h.nextID++
	ch := make(chan domain.ReplicationEvent, buffer)
	h.subscribers[id] = ch

	unsubscribe := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if existing, ok := h.subscribers[id]; ok {
			delete(h.subscribers, id)
			close(existing)
		}
	}

	return ch, unsubscribe
}

func (h *SSEHub) Broadcast(evt domain.ReplicationEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for id, ch := range h.subscribers {
		select {
		case ch <- evt:
		default:
			close(ch)
			delete(h.subscribers, id)
		}
	}
}
