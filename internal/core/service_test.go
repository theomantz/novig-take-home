package core

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"novig-take-home/internal/domain"
)

func TestEvaluateAllKeepsAuthoritativeStateWhenPersistFails(t *testing.T) {
	store, err := NewEventStore(InMemoryDSN("core_persist_fail"))
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}

	now := time.UnixMilli(1_000)
	svc, err := NewService(store, ServiceConfig{
		Breaker:         domain.DefaultBreakerConfig(),
		TickInterval:    time.Second,
		WindowDuration:  30 * time.Second,
		DefaultMarketID: "m1",
		Logger:          slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	svc.IngestSignal(domain.SignalInput{
		MarketID: "m1",
		BetCount: svc.cfg.Breaker.BetCountThreshold,
		TsUnixMs: now.UnixMilli(),
	})

	if err := store.Close(); err != nil {
		t.Fatalf("close event store: %v", err)
	}

	svc.evaluateAll(now.UnixMilli())

	snapshot := svc.Snapshot()
	market := snapshot.Markets["m1"]
	if market.Status != domain.MarketStatusOpen {
		t.Fatalf("expected market to stay open when persistence fails, got %s", market.Status)
	}
	if snapshot.LastSeq != 0 {
		t.Fatalf("expected sequence to remain 0 when persistence fails, got %d", snapshot.LastSeq)
	}
}
