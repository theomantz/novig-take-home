package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/google/uuid"

	"novig-take-home/internal/domain"
)

type ServiceConfig struct {
	Breaker         domain.BreakerConfig
	TickInterval    time.Duration
	WindowDuration  time.Duration
	DefaultMarketID string
	Logger          *slog.Logger
	Now             func() time.Time
}

type signalPoint struct {
	TsUnixMs            int64
	BetCount            int64
	StakeCents          int64
	LiabilityDeltaCents int64
}

type Service struct {
	mu           sync.RWMutex
	store        *EventStore
	hub          *SSEHub
	cfg          ServiceConfig
	lastSeq      int64
	markets      map[string]domain.MarketState
	signalWindow map[string][]signalPoint
}

func NewService(store *EventStore, cfg ServiceConfig) (*Service, error) {
	if cfg.Breaker.BetCountThreshold == 0 {
		cfg.Breaker = domain.DefaultBreakerConfig()
	}
	if cfg.TickInterval == 0 {
		cfg.TickInterval = time.Second
	}
	if cfg.WindowDuration == 0 {
		cfg.WindowDuration = 30 * time.Second
	}
	if cfg.DefaultMarketID == "" {
		cfg.DefaultMarketID = "market-news-001"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	lastSeq, err := store.LastSeq()
	if err != nil {
		return nil, fmt.Errorf("load last seq: %w", err)
	}

	nowMs := cfg.Now().UnixMilli()
	svc := &Service{
		store:   store,
		hub:     NewSSEHub(),
		cfg:     cfg,
		lastSeq: lastSeq,
		markets: map[string]domain.MarketState{
			cfg.DefaultMarketID: {
				MarketID:             cfg.DefaultMarketID,
				Status:               domain.MarketStatusOpen,
				LastTransitionUnixMs: nowMs,
			},
		},
		signalWindow: make(map[string][]signalPoint),
	}

	return svc, nil
}

func (s *Service) Start(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.evaluateAll(now.UnixMilli())
		}
	}
}

func (s *Service) IngestSignal(in domain.SignalInput) {
	nowMs := s.cfg.Now().UnixMilli()
	if in.TsUnixMs == 0 {
		in.TsUnixMs = nowMs
	}
	if in.MarketID == "" {
		in.MarketID = s.cfg.DefaultMarketID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureMarketLocked(in.MarketID, nowMs)
	s.signalWindow[in.MarketID] = append(s.signalWindow[in.MarketID], signalPoint{
		TsUnixMs:            in.TsUnixMs,
		BetCount:            in.BetCount,
		StakeCents:          in.StakeCents,
		LiabilityDeltaCents: in.LiabilityDeltaCents,
	})
	s.pruneSignalsLocked(in.MarketID, nowMs)
}

func (s *Service) ClearSignals(marketID string) {
	nowMs := s.cfg.Now().UnixMilli()
	if marketID == "" {
		marketID = s.cfg.DefaultMarketID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureMarketLocked(marketID, nowMs)
	s.signalWindow[marketID] = nil
}

func (s *Service) Snapshot() domain.SnapshotResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	markets := make(map[string]domain.MarketState, len(s.markets))
	maps.Copy(markets, s.markets)

	return domain.SnapshotResponse{
		LastSeq: s.lastSeq,
		Markets: markets,
	}
}

func (s *Service) LastSeq() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSeq
}

func (s *Service) EventsFromSeq(fromSeq int64) ([]domain.ReplicationEvent, error) {
	return s.store.EventsFromSeq(fromSeq)
}

func (s *Service) Subscribe(buffer int) (chan domain.ReplicationEvent, func()) {
	return s.hub.Subscribe(buffer)
}

func (s *Service) evaluateAll(nowMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	marketIDs := make(map[string]struct{}, len(s.markets)+len(s.signalWindow))
	for id := range s.markets {
		marketIDs[id] = struct{}{}
	}
	for id := range s.signalWindow {
		marketIDs[id] = struct{}{}
	}

	for marketID := range marketIDs {
		s.ensureMarketLocked(marketID, nowMs)
		s.pruneSignalsLocked(marketID, nowMs)

		metrics := s.metricsLocked(marketID)
		prev := s.markets[marketID]
		next, transitionType, transitioned := domain.ApplyBreaker(nowMs, prev, metrics, s.cfg.Breaker)

		if prev.IsEqual(next) {
			continue
		}

		eventType := domain.EventTypeMarketUpdated
		if transitioned {
			eventType = transitionType
		}
		if s.emitEventLocked(eventType, marketID, next) {
			s.markets[marketID] = next
		}
	}
}

func (s *Service) metricsLocked(marketID string) domain.WindowMetrics {
	points := s.signalWindow[marketID]
	var out domain.WindowMetrics
	for i := range points {
		out.BetCount30s += points[i].BetCount
		out.StakeSum30sCents += points[i].StakeCents
		out.LiabilityDelta30sCents += points[i].LiabilityDeltaCents
	}
	return out
}

func (s *Service) ensureMarketLocked(marketID string, nowMs int64) {
	if _, ok := s.markets[marketID]; !ok {
		s.markets[marketID] = domain.MarketState{
			MarketID:             marketID,
			Status:               domain.MarketStatusOpen,
			LastTransitionUnixMs: nowMs,
		}
	}
}

func (s *Service) pruneSignalsLocked(marketID string, nowMs int64) {
	points := s.signalWindow[marketID]
	if len(points) == 0 {
		return
	}
	cutoff := nowMs - s.cfg.WindowDuration.Milliseconds()
	filtered := make([]signalPoint, 0, len(points))
	for _, point := range points {
		if point.TsUnixMs >= cutoff {
			filtered = append(filtered, point)
		}
	}
	s.signalWindow[marketID] = filtered
}

func (s *Service) emitEventLocked(eventType domain.EventType, marketID string, state domain.MarketState) bool {
	payload, err := json.Marshal(state)
	if err != nil {
		s.cfg.Logger.Error("marshal payload", "error", err)
		return false
	}

	prevSeq := s.lastSeq
	s.lastSeq++
	evt := domain.ReplicationEvent{
		Seq:       s.lastSeq,
		EventID:   uuid.NewString(),
		TsUnixMs:  s.cfg.Now().UnixMilli(),
		EventType: eventType,
		MarketID:  marketID,
		Payload:   payload,
	}

	if err := s.store.Insert(evt); err != nil {
		s.cfg.Logger.Error("persist event failed", "seq", evt.Seq, "event_id", evt.EventID, "error", err)
		s.lastSeq = prevSeq
		if lastSeq, seqErr := s.store.LastSeq(); seqErr == nil {
			s.lastSeq = lastSeq
		}
		return false
	}

	s.cfg.Logger.Info("core emitted event",
		"seq", evt.Seq,
		"event_id", evt.EventID,
		"market_id", evt.MarketID,
		"event_type", evt.EventType,
	)
	s.hub.Broadcast(evt)
	return true
}
