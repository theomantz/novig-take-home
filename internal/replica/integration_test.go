package replica_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"novig-take-home/internal/core"
	"novig-take-home/internal/domain"
	"novig-take-home/internal/replica"
)

const testMarketID = "market-news-001"

func TestSingleReplicaConvergesWithCore(t *testing.T) {
	coreURL, shutdownCore := startCoreTestServer(t)
	defer shutdownCore()

	replicaSvc, _, shutdownReplica := startReplicaTestServer(t, "single", coreURL)
	defer shutdownReplica()

	waitFor(t, 3*time.Second, func() bool {
		status := replicaSvc.Status()
		return status.Connected
	}, "replica failed to connect")

	postScenario(t, coreURL, "spike")

	waitFor(t, 4*time.Second, func() bool {
		market, ok := replicaSvc.MarketByID(testMarketID)
		return ok && market.Status == domain.MarketStatusSuspended
	}, "replica did not converge to suspended state")
}

func TestTwoReplicasConvergeToSameState(t *testing.T) {
	coreURL, shutdownCore := startCoreTestServer(t)
	defer shutdownCore()

	replica1, _, shutdownReplica1 := startReplicaTestServer(t, "r1", coreURL)
	defer shutdownReplica1()

	replica2, _, shutdownReplica2 := startReplicaTestServer(t, "r2", coreURL)
	defer shutdownReplica2()

	waitFor(t, 3*time.Second, func() bool {
		return replica1.Status().Connected && replica2.Status().Connected
	}, "replicas failed to connect")

	postScenario(t, coreURL, "spike")

	waitFor(t, 4*time.Second, func() bool {
		m1, ok1 := replica1.MarketByID(testMarketID)
		m2, ok2 := replica2.MarketByID(testMarketID)
		return ok1 && ok2 && m1.Status == domain.MarketStatusSuspended && m1.Status == m2.Status
	}, "replicas did not converge to same suspended state")
}

func TestForcedSequenceGapTriggersSnapshotRecovery(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	var snapshotCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/snapshot":
			call := snapshotCalls.Add(1)
			if call == 1 {
				_ = json.NewEncoder(w).Encode(domain.SnapshotResponse{
					LastSeq: 1,
					Markets: map[string]domain.MarketState{testMarketID: {
						MarketID: testMarketID,
						Status:   domain.MarketStatusOpen,
					}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(domain.SnapshotResponse{
				LastSeq: 3,
				Markets: map[string]domain.MarketState{testMarketID: {
					MarketID:            testMarketID,
					Status:              domain.MarketStatusSuspended,
					LastReason:          domain.BreakerReasonBetRateSpike,
					CooldownUntilUnixMs: time.Now().Add(1 * time.Second).UnixMilli(),
				}},
			})
		case r.URL.Path == "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fromSeq := r.URL.Query().Get("from_seq")
			if fromSeq != "2" && fromSeq != "4" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, "unexpected from_seq")
				return
			}
			if fromSeq == "2" {
				evt := domain.ReplicationEvent{
					Seq:       3,
					EventID:   "gap-event",
					TsUnixMs:  time.Now().UnixMilli(),
					EventType: domain.EventTypeMarketUpdated,
					MarketID:  testMarketID,
					Payload:   mustJSON(t, domain.MarketState{MarketID: testMarketID, Status: domain.MarketStatusSuspended}),
				}
				data, _ := json.Marshal(evt)
				_, _ = io.WriteString(w, "data: "+string(data)+"\n\n")
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				return
			}
			select {
			case <-r.Context().Done():
			case <-time.After(500 * time.Millisecond):
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store, err := replica.NewStore(replica.InMemoryDSN("gap_test"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := replica.NewService(store, replica.ServiceConfig{
		ID:               "gap",
		CoreBaseURL:      server.URL,
		Logger:           logger,
		ReconnectBackoff: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx)

	waitFor(t, 2*time.Second, func() bool {
		market, ok := svc.MarketByID(testMarketID)
		return ok && svc.LastAppliedSeq() == 3 && market.Status == domain.MarketStatusSuspended
	}, "replica did not recover from forced gap via snapshot")

	if snapshotCalls.Load() < 2 {
		t.Fatalf("expected at least 2 snapshot calls, got %d", snapshotCalls.Load())
	}
}

func TestReplicaReconnectResumesFromCheckpoint(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	var snapshotCalls atomic.Int32
	var streamCalls atomic.Int32
	var firstFromSeq atomic.Int64
	var secondFromSeq atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot":
			snapshotCalls.Add(1)
			_ = json.NewEncoder(w).Encode(domain.SnapshotResponse{
				LastSeq: 0,
				Markets: map[string]domain.MarketState{testMarketID: {
					MarketID: testMarketID,
					Status:   domain.MarketStatusOpen,
				}},
			})
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fromSeq, _ := strconv.ParseInt(r.URL.Query().Get("from_seq"), 10, 64)
			call := streamCalls.Add(1)
			if call == 1 {
				firstFromSeq.Store(fromSeq)
				evt := domain.ReplicationEvent{
					Seq:       1,
					EventID:   "resume-1",
					TsUnixMs:  time.Now().UnixMilli(),
					EventType: domain.EventTypeMarketUpdated,
					MarketID:  testMarketID,
					Payload:   mustJSON(t, domain.MarketState{MarketID: testMarketID, Status: domain.MarketStatusOpen}),
				}
				data, _ := json.Marshal(evt)
				_, _ = io.WriteString(w, "data: "+string(data)+"\n\n")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				return
			}

			secondFromSeq.Store(fromSeq)
			evt := domain.ReplicationEvent{
				Seq:       2,
				EventID:   "resume-2",
				TsUnixMs:  time.Now().UnixMilli(),
				EventType: domain.EventTypeMarketUpdated,
				MarketID:  testMarketID,
				Payload:   mustJSON(t, domain.MarketState{MarketID: testMarketID, Status: domain.MarketStatusSuspended}),
			}
			data, _ := json.Marshal(evt)
			_, _ = io.WriteString(w, "data: "+string(data)+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store, err := replica.NewStore(replica.InMemoryDSN("resume_checkpoint"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	svc, err := replica.NewService(store, replica.ServiceConfig{
		ID:               "resume",
		CoreBaseURL:      server.URL,
		Logger:           logger,
		ReconnectBackoff: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx)

	waitFor(t, 2*time.Second, func() bool {
		return svc.LastAppliedSeq() >= 2
	}, "replica did not resume and apply second event")

	if got := firstFromSeq.Load(); got != 1 {
		t.Fatalf("expected first stream from_seq=1, got %d", got)
	}
	if got := secondFromSeq.Load(); got != 2 {
		t.Fatalf("expected reconnect stream from_seq=2, got %d", got)
	}
	if snapshotCalls.Load() != 1 {
		t.Fatalf("expected exactly one bootstrap snapshot call, got %d", snapshotCalls.Load())
	}
}

func TestReplicaRestartBootstrapsFromSnapshotAndConverges(t *testing.T) {
	coreURL, shutdownCore := startCoreTestServer(t)
	defer shutdownCore()

	postScenario(t, coreURL, "spike")

	restartedReplica, _, shutdownReplica := startReplicaTestServer(t, "restart", coreURL)
	defer shutdownReplica()

	waitFor(t, 4*time.Second, func() bool {
		market, ok := restartedReplica.MarketByID(testMarketID)
		return ok && market.Status == domain.MarketStatusSuspended && restartedReplica.LastAppliedSeq() > 0
	}, "restarted replica did not bootstrap from snapshot")

	postScenario(t, coreURL, "normalize")
	waitFor(t, 6*time.Second, func() bool {
		market, ok := restartedReplica.MarketByID(testMarketID)
		return ok && market.Status == domain.MarketStatusOpen
	}, "restarted replica did not converge to reopen")
}

func startCoreTestServer(t *testing.T) (string, func()) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	dsn := core.InMemoryDSN(fmt.Sprintf("core_test_%d", time.Now().UnixNano()))
	store, err := core.NewEventStore(dsn)
	if err != nil {
		t.Fatalf("new core store: %v", err)
	}

	svc, err := core.NewService(store, core.ServiceConfig{
		Breaker: domain.BreakerConfig{
			BetCountThreshold:       120,
			LiabilityThresholdCents: 2_000_000,
			CooldownMs:              250,
		},
		TickInterval:    50 * time.Millisecond,
		WindowDuration:  30 * time.Second,
		DefaultMarketID: testMarketID,
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("new core service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go svc.Start(ctx)

	server := httptest.NewServer(core.NewHandler(svc, logger))

	shutdown := func() {
		cancel()
		server.Close()
		_ = store.Close()
	}

	return server.URL, shutdown
}

func startReplicaTestServer(t *testing.T, id string, coreURL string) (*replica.Service, string, func()) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	dsn := replica.InMemoryDSN(fmt.Sprintf("test_%s_%d", id, time.Now().UnixNano()))
	store, err := replica.NewStore(dsn)
	if err != nil {
		t.Fatalf("new replica store: %v", err)
	}

	svc, err := replica.NewService(store, replica.ServiceConfig{
		ID:               id,
		CoreBaseURL:      coreURL,
		Logger:           logger,
		ReconnectBackoff: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new replica service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go svc.Start(ctx)

	server := httptest.NewServer(replica.NewHandler(svc))

	stopOnce := atomic.Bool{}
	shutdown := func() {
		if stopOnce.Swap(true) {
			return
		}
		cancel()
		server.Close()
		_ = store.Close()
	}

	return svc, server.URL, shutdown
}

func postScenario(t *testing.T, coreURL string, name string) {
	t.Helper()
	resp, err := http.Post(coreURL+"/internal/scenarios/"+name, "application/json", nil)
	if err != nil {
		t.Fatalf("post scenario: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("scenario status=%d body=%s", resp.StatusCode, string(body))
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return payload
}
