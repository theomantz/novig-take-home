package core

import (
	"bufio"
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

func TestStreamRejectsInvalidFromSeq(t *testing.T) {
	svc, store := newCoreStreamTestService(t)
	defer store.Close()

	server := httptest.NewServer(NewHandler(svc, slog.New(slog.NewJSONHandler(io.Discard, nil))))
	defer server.Close()

	resp, err := http.Get(server.URL + "/stream?from_seq=0")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestStreamWritesConnectedAndKeepaliveFrames(t *testing.T) {
	svc, store := newCoreStreamTestService(t)
	defer store.Close()

	prevHeartbeat := streamHeartbeatInterval
	streamHeartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		streamHeartbeatInterval = prevHeartbeat
	})

	server := httptest.NewServer(NewHandler(svc, slog.New(slog.NewJSONHandler(io.Discard, nil))))
	defer server.Close()

	resp, err := http.Get(server.URL + "/stream?from_seq=1")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	line, err := readLineWithTimeout(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("read connected line: %v", err)
	}
	if strings.TrimSpace(line) != ":connected" {
		t.Fatalf("expected :connected, got %q", line)
	}

	_, err = readLineWithTimeout(reader, 2*time.Second) // consume blank line after :connected
	if err != nil {
		t.Fatalf("read connected frame separator: %v", err)
	}

	foundKeepalive := false
	deadline := time.Now().Add(2 * time.Second)
	for !foundKeepalive && time.Now().Before(deadline) {
		line, err := readLineWithTimeout(reader, 2*time.Second)
		if err != nil {
			t.Fatalf("read keepalive line: %v", err)
		}
		if strings.TrimSpace(line) == ":keepalive" {
			foundKeepalive = true
		}
	}
	if !foundKeepalive {
		t.Fatalf("expected :keepalive frame")
	}
}

func TestStreamBacklogAndLiveEventsRemainAscending(t *testing.T) {
	svc, store := newCoreStreamTestService(t)
	defer store.Close()

	marketID := "m1"
	evt1 := testEvent(t, 1, "evt-1", domain.EventTypeMarketUpdated, marketID, domain.MarketStatusOpen)
	evt2 := testEvent(t, 2, "evt-2", domain.EventTypeMarketSuspended, marketID, domain.MarketStatusSuspended)
	if err := store.Insert(evt1); err != nil {
		t.Fatalf("insert evt1: %v", err)
	}
	if err := store.Insert(evt2); err != nil {
		t.Fatalf("insert evt2: %v", err)
	}
	svc.mu.Lock()
	svc.lastSeq = 2
	svc.mu.Unlock()

	server := httptest.NewServer(NewHandler(svc, slog.New(slog.NewJSONHandler(io.Discard, nil))))
	defer server.Close()

	resp, err := http.Get(server.URL + "/stream?from_seq=1")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	consumeConnectedFrame(t, reader)

	got1 := readSSEDataEvent(t, reader, 2*time.Second)
	got2 := readSSEDataEvent(t, reader, 2*time.Second)
	if got1.Seq != 1 || got2.Seq != 2 {
		t.Fatalf("expected seqs [1,2], got [%d,%d]", got1.Seq, got2.Seq)
	}

	evt3 := testEvent(t, 3, "evt-3", domain.EventTypeMarketReopened, marketID, domain.MarketStatusOpen)
	if err := store.Insert(evt3); err != nil {
		t.Fatalf("insert evt3: %v", err)
	}
	svc.mu.Lock()
	svc.lastSeq = 3
	svc.mu.Unlock()
	svc.hub.Broadcast(evt3)

	got3 := readSSEDataEvent(t, reader, 2*time.Second)
	if got3.Seq != 3 {
		t.Fatalf("expected seq 3, got %d", got3.Seq)
	}
}

func TestStreamGapFillQueriesStoreAndReplaysMissingEvents(t *testing.T) {
	svc, store := newCoreStreamTestService(t)
	defer store.Close()

	server := httptest.NewServer(NewHandler(svc, slog.New(slog.NewJSONHandler(io.Discard, nil))))
	defer server.Close()

	resp, err := http.Get(server.URL + "/stream?from_seq=1")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	consumeConnectedFrame(t, reader)

	marketID := "m1"
	evt1 := testEvent(t, 1, "gap-1", domain.EventTypeMarketUpdated, marketID, domain.MarketStatusOpen)
	evt2 := testEvent(t, 2, "gap-2", domain.EventTypeMarketSuspended, marketID, domain.MarketStatusSuspended)
	evt3 := testEvent(t, 3, "gap-3", domain.EventTypeMarketReopened, marketID, domain.MarketStatusOpen)
	for _, evt := range []domain.ReplicationEvent{evt1, evt2, evt3} {
		if err := store.Insert(evt); err != nil {
			t.Fatalf("insert seq %d: %v", evt.Seq, err)
		}
	}

	svc.mu.Lock()
	svc.lastSeq = 3
	svc.mu.Unlock()
	// Broadcast only seq=3; handler must gap-fill seq=1,2 from store first.
	svc.hub.Broadcast(evt3)

	got := []int64{
		readSSEDataEvent(t, reader, 2*time.Second).Seq,
		readSSEDataEvent(t, reader, 2*time.Second).Seq,
		readSSEDataEvent(t, reader, 2*time.Second).Seq,
	}
	if got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("expected gap fill seqs [1,2,3], got %v", got)
	}
}

func newCoreStreamTestService(t *testing.T) (*Service, *EventStore) {
	t.Helper()

	store, err := NewEventStore(InMemoryDSN(fmt.Sprintf("core_stream_%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}

	svc, err := NewService(store, ServiceConfig{
		DefaultMarketID: "m1",
		Logger:          slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("new service: %v", err)
	}
	return svc, store
}

func testEvent(t *testing.T, seq int64, id string, eventType domain.EventType, marketID string, status domain.MarketStatus) domain.ReplicationEvent {
	t.Helper()
	payload, err := json.Marshal(domain.MarketState{MarketID: marketID, Status: status})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return domain.ReplicationEvent{
		Seq:       seq,
		EventID:   id,
		TsUnixMs:  1_000 + seq,
		EventType: eventType,
		MarketID:  marketID,
		Payload:   payload,
	}
}

type lineResult struct {
	line string
	err  error
}

func readLineWithTimeout(reader *bufio.Reader, timeout time.Duration) (string, error) {
	ch := make(chan lineResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ch <- lineResult{line: line, err: err}
	}()

	select {
	case result := <-ch:
		return result.line, result.err
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out waiting for stream line")
	}
}

func consumeConnectedFrame(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	line, err := readLineWithTimeout(reader, 2*time.Second)
	if err != nil {
		t.Fatalf("read connected line: %v", err)
	}
	if strings.TrimSpace(line) != ":connected" {
		t.Fatalf("expected :connected, got %q", line)
	}
	if _, err := readLineWithTimeout(reader, 2*time.Second); err != nil {
		t.Fatalf("read connected separator: %v", err)
	}
}

func readSSEDataEvent(t *testing.T, reader *bufio.Reader, timeout time.Duration) domain.ReplicationEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		line, err := readLineWithTimeout(reader, timeout)
		if err != nil {
			t.Fatalf("read stream line: %v", err)
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data: ") {
			continue
		}

		var evt domain.ReplicationEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(trimmed, "data: ")), &evt); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		return evt
	}
	t.Fatalf("timed out waiting for SSE data event")
	return domain.ReplicationEvent{}
}
