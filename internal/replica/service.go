package replica

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novig-take-home/internal/domain"
)

var ErrSeqGap = errors.New("sequence gap detected")
var ErrStreamIdleTimeout = errors.New("stream idle timeout")

type ServiceConfig struct {
	ID                  string
	CoreBaseURL         string
	HTTPClient          *http.Client
	Logger              *slog.Logger
	ReconnectBackoff    time.Duration
	ReconnectBackoffMax time.Duration
	SnapshotTimeout     time.Duration
	StreamIdleTimeout   time.Duration
	HeartbeatInterval   time.Duration
	HeartbeatTimeout    time.Duration
	Now                 func() time.Time
	RandomFloat64       func() float64
}

type Status struct {
	Connected      bool   `json:"connected"`
	Bootstrapped   bool   `json:"bootstrapped"`
	LastAppliedSeq int64  `json:"last_applied_seq"`
	CoreLastSeq    int64  `json:"core_last_seq"`
	LagSeq         int64  `json:"lag_seq"`
	LastSyncAt     int64  `json:"last_sync_at"`
	LastError      string `json:"last_error"`
}

type Service struct {
	mu             sync.RWMutex
	store          *Store
	cfg            ServiceConfig
	markets        map[string]domain.MarketState
	history        map[string][]domain.ReplicationEvent
	connected      bool
	bootstrapped   bool
	lastAppliedSeq int64
	coreLastSeq    int64
	lastSyncAtMs   int64
	lastError      string

	reconnectAttempts int
}

func NewService(store *Store, cfg ServiceConfig) (*Service, error) {
	if cfg.ID == "" {
		cfg.ID = "replica-1"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 0}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ReconnectBackoff == 0 {
		cfg.ReconnectBackoff = 500 * time.Millisecond
	}
	if cfg.ReconnectBackoffMax <= 0 {
		cfg.ReconnectBackoffMax = 5 * time.Second
	}
	if cfg.ReconnectBackoffMax < cfg.ReconnectBackoff {
		cfg.ReconnectBackoffMax = cfg.ReconnectBackoff
	}
	if cfg.SnapshotTimeout <= 0 {
		cfg.SnapshotTimeout = 3 * time.Second
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = 45 * time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 2 * time.Second
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 2 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RandomFloat64 == nil {
		cfg.RandomFloat64 = rand.Float64
	}
	if cfg.CoreBaseURL == "" {
		return nil, errors.New("core base url required")
	}
	checkpoint, err := store.GetCheckpoint()
	if err != nil {
		return nil, err
	}

	return &Service{
		store:          store,
		cfg:            cfg,
		markets:        make(map[string]domain.MarketState),
		history:        make(map[string][]domain.ReplicationEvent),
		lastAppliedSeq: checkpoint,
	}, nil
}

func (s *Service) Start(ctx context.Context) {
	go s.reportHeartbeats(ctx)

	for {
		if ctx.Err() != nil {
			return
		}

		if err := s.ensureBootstrapped(ctx); err != nil {
			s.cfg.Logger.Warn("replica bootstrap failed", "replica_id", s.cfg.ID, "error", err)
			s.setConnected(false)
			s.setLastError(err)
			s.waitBackoff(ctx)
			continue
		}

		if err := s.consumeStream(ctx); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				s.setConnected(false)
				return
			}
			if errors.Is(err, ErrSeqGap) {
				s.cfg.Logger.Warn("sequence gap, refreshing snapshot", "replica_id", s.cfg.ID, "error", err)
				s.setLastError(err)
				if snapErr := s.refreshFromSnapshot(ctx); snapErr != nil {
					s.cfg.Logger.Warn("snapshot refresh failed", "replica_id", s.cfg.ID, "error", snapErr)
					s.setLastError(snapErr)
					s.waitBackoff(ctx)
				}
				continue
			}
			if errors.Is(err, io.EOF) {
				s.cfg.Logger.Info("replica stream ended, reconnecting", "replica_id", s.cfg.ID)
				s.setConnected(false)
				s.waitBackoff(ctx)
				continue
			}

			s.cfg.Logger.Warn("replica stream disconnected", "replica_id", s.cfg.ID, "error", err)
			s.setLastError(err)
			s.setConnected(false)
			s.waitBackoff(ctx)
		}
	}
}

func (s *Service) ensureBootstrapped(ctx context.Context) error {
	s.mu.RLock()
	bootstrapped := s.bootstrapped
	s.mu.RUnlock()
	if bootstrapped {
		return nil
	}

	return s.refreshFromSnapshot(ctx)
}

func (s *Service) refreshFromSnapshot(ctx context.Context) error {
	snapshot, err := s.fetchSnapshot(ctx)
	if err != nil {
		return err
	}

	currentSeq := s.LastAppliedSeq()
	validatedMarkets, err := validateSnapshot(snapshot, currentSeq)
	if err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}

	nowMs := s.cfg.Now().UnixMilli()
	if err := s.store.SetCheckpoint(snapshot.LastSeq, nowMs); err != nil {
		return err
	}

	s.mu.Lock()
	s.markets = make(map[string]domain.MarketState, len(validatedMarkets))
	maps.Copy(s.markets, validatedMarkets)
	s.history = make(map[string][]domain.ReplicationEvent)
	s.lastAppliedSeq = snapshot.LastSeq
	s.coreLastSeq = snapshot.LastSeq
	s.lastSyncAtMs = nowMs
	s.bootstrapped = true
	s.lastError = ""
	s.reconnectAttempts = 0
	s.mu.Unlock()

	s.cfg.Logger.Info("replica snapshot synced",
		"replica_id", s.cfg.ID,
		"last_applied_seq", snapshot.LastSeq,
	)

	return nil
}

func (s *Service) fetchSnapshot(ctx context.Context) (domain.SnapshotResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.SnapshotTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, s.cfg.CoreBaseURL+"/snapshot", nil)
	if err != nil {
		return domain.SnapshotResponse{}, err
	}

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return domain.SnapshotResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return domain.SnapshotResponse{}, fmt.Errorf("snapshot status %d: %s", resp.StatusCode, string(body))
	}

	var snapshot domain.SnapshotResponse
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return domain.SnapshotResponse{}, err
	}
	if snapshot.Markets == nil {
		snapshot.Markets = make(map[string]domain.MarketState)
	}
	return snapshot, nil
}

func (s *Service) consumeStream(ctx context.Context) error {
	fromSeq := s.LastAppliedSeq() + 1

	streamURL, err := url.Parse(s.cfg.CoreBaseURL + "/stream")
	if err != nil {
		return err
	}
	q := streamURL.Query()
	q.Set("from_seq", strconv.FormatInt(fromSeq, 10))
	streamURL.RawQuery = q.Encode()

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, streamURL.String(), nil)
	if err != nil {
		return err
	}

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return fmt.Errorf("stream status %d: %s", resp.StatusCode, string(body))
	}

	s.setConnected(true)
	s.resetReconnectBackoff()
	s.clearLastError()
	defer s.setConnected(false)

	idleReset := make(chan struct{}, 1)
	idleErr := make(chan error, 1)
	go s.monitorStreamIdle(streamCtx, cancel, idleReset, idleErr)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		select {
		case idleReset <- struct{}{}:
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var evt domain.ReplicationEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		if err := s.processEvent(evt); err != nil {
			return err
		}
	}

	select {
	case idleTimeoutErr := <-idleErr:
		return idleTimeoutErr
	default:
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (s *Service) processEvent(evt domain.ReplicationEvent) error {
	if err := validateEventEnvelope(evt); err != nil {
		return err
	}

	alreadyApplied, err := s.store.IsEventApplied(evt.EventID)
	if err != nil {
		return err
	}
	if alreadyApplied {
		s.updateCoreLastSeq(evt.Seq)
		return nil
	}

	expected := s.LastAppliedSeq() + 1
	if evt.Seq != expected {
		return fmt.Errorf("%w: expected=%d got=%d", ErrSeqGap, expected, evt.Seq)
	}

	market, err := decodeAndValidateEventPayload(evt)
	if err != nil {
		return err
	}

	nowMs := s.cfg.Now().UnixMilli()
	committed, err := s.store.CommitAppliedEvent(evt.EventID, evt.Seq, nowMs)
	if err != nil {
		return err
	}
	if !committed {
		s.updateCoreLastSeq(evt.Seq)
		return nil
	}

	s.mu.Lock()
	s.markets[market.MarketID] = market
	s.history[market.MarketID] = append(s.history[market.MarketID], evt)
	s.lastAppliedSeq = evt.Seq
	if evt.Seq > s.coreLastSeq {
		s.coreLastSeq = evt.Seq
	}
	s.lastSyncAtMs = nowMs
	s.mu.Unlock()

	s.cfg.Logger.Info("replica applied event",
		"replica_id", s.cfg.ID,
		"seq", evt.Seq,
		"event_id", evt.EventID,
		"market_id", evt.MarketID,
		"event_type", evt.EventType,
	)

	return nil
}

func (s *Service) updateCoreLastSeq(seq int64) {
	s.mu.Lock()
	if seq > s.coreLastSeq {
		s.coreLastSeq = seq
	}
	s.mu.Unlock()
}

func (s *Service) waitBackoff(ctx context.Context) {
	t := time.NewTimer(s.nextReconnectDelay())
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (s *Service) monitorStreamIdle(ctx context.Context, cancel context.CancelFunc, reset <-chan struct{}, idleErr chan<- error) {
	timer := time.NewTimer(s.cfg.StreamIdleTimeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-reset:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.cfg.StreamIdleTimeout)
		case <-timer.C:
			select {
			case idleErr <- fmt.Errorf("%w after %s", ErrStreamIdleTimeout, s.cfg.StreamIdleTimeout):
			default:
			}
			cancel()
			return
		}
	}
}

func (s *Service) nextReconnectDelay() time.Duration {
	s.mu.Lock()
	attempt := s.reconnectAttempts
	s.reconnectAttempts++
	s.mu.Unlock()

	baseDelay := exponentialBackoffDuration(s.cfg.ReconnectBackoff, s.cfg.ReconnectBackoffMax, attempt)
	jitter := 0.5 + s.cfg.RandomFloat64()
	if jitter < 0.1 {
		jitter = 0.1
	}
	if jitter > 2.0 {
		jitter = 2.0
	}

	delay := time.Duration(float64(baseDelay) * jitter)
	if delay > s.cfg.ReconnectBackoffMax {
		return s.cfg.ReconnectBackoffMax
	}
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}

func exponentialBackoffDuration(initial time.Duration, cap time.Duration, attempt int) time.Duration {
	if initial <= 0 {
		initial = 500 * time.Millisecond
	}
	if cap <= 0 || cap < initial {
		cap = initial
	}
	if attempt <= 0 {
		return initial
	}

	multiplier := math.Pow(2, float64(attempt))
	delay := time.Duration(float64(initial) * multiplier)
	if delay > cap {
		return cap
	}
	return delay
}

func validateSnapshot(snapshot domain.SnapshotResponse, minSeq int64) (map[string]domain.MarketState, error) {
	if snapshot.LastSeq < 0 {
		return nil, fmt.Errorf("snapshot last_seq must be >= 0: %d", snapshot.LastSeq)
	}
	if snapshot.LastSeq < minSeq {
		return nil, fmt.Errorf("snapshot last_seq regressed: current=%d snapshot=%d", minSeq, snapshot.LastSeq)
	}
	if snapshot.Markets == nil {
		return map[string]domain.MarketState{}, nil
	}

	validated := make(map[string]domain.MarketState, len(snapshot.Markets))
	for marketID, market := range snapshot.Markets {
		if marketID == "" {
			return nil, errors.New("snapshot market_id key must be non-empty")
		}
		if market.MarketID != marketID {
			return nil, fmt.Errorf("snapshot market_id mismatch for key=%q payload=%q", marketID, market.MarketID)
		}
		if err := validateMarketState(market); err != nil {
			return nil, fmt.Errorf("invalid snapshot market %q: %w", marketID, err)
		}
		validated[marketID] = market
	}
	return validated, nil
}

func validateEventEnvelope(evt domain.ReplicationEvent) error {
	if evt.EventID == "" {
		return errors.New("event_id is required")
	}
	if evt.Seq <= 0 {
		return fmt.Errorf("seq must be >= 1: %d", evt.Seq)
	}
	if evt.MarketID == "" {
		return errors.New("market_id is required")
	}
	switch evt.EventType {
	case domain.EventTypeMarketUpdated, domain.EventTypeMarketSuspended, domain.EventTypeMarketReopened:
	default:
		return fmt.Errorf("unsupported event_type %q", evt.EventType)
	}
	return nil
}

func decodeAndValidateEventPayload(evt domain.ReplicationEvent) (domain.MarketState, error) {
	var market domain.MarketState
	if err := json.Unmarshal(evt.Payload, &market); err != nil {
		return domain.MarketState{}, fmt.Errorf("decode payload: %w", err)
	}
	if err := validateMarketState(market); err != nil {
		return domain.MarketState{}, fmt.Errorf("invalid payload market state: %w", err)
	}
	if market.MarketID != evt.MarketID {
		return domain.MarketState{}, fmt.Errorf("payload market_id mismatch: envelope=%q payload=%q", evt.MarketID, market.MarketID)
	}

	switch evt.EventType {
	case domain.EventTypeMarketSuspended:
		if market.Status != domain.MarketStatusSuspended {
			return domain.MarketState{}, fmt.Errorf("event_type=%s requires payload status=%s", evt.EventType, domain.MarketStatusSuspended)
		}
	case domain.EventTypeMarketReopened:
		if market.Status != domain.MarketStatusOpen {
			return domain.MarketState{}, fmt.Errorf("event_type=%s requires payload status=%s", evt.EventType, domain.MarketStatusOpen)
		}
	}

	return market, nil
}

func validateMarketState(market domain.MarketState) error {
	if market.MarketID == "" {
		return errors.New("market_id is required")
	}
	switch market.Status {
	case domain.MarketStatusOpen, domain.MarketStatusSuspended:
	default:
		return fmt.Errorf("unsupported market status %q", market.Status)
	}
	return nil
}

func (s *Service) setLastError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
}

func (s *Service) clearLastError() {
	s.mu.Lock()
	s.lastError = ""
	s.mu.Unlock()
}

func (s *Service) resetReconnectBackoff() {
	s.mu.Lock()
	s.reconnectAttempts = 0
	s.mu.Unlock()
}

func (s *Service) setConnected(v bool) {
	s.mu.Lock()
	s.connected = v
	s.mu.Unlock()
}

func (s *Service) reportHeartbeats(ctx context.Context) {
	if err := s.sendHeartbeat(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.cfg.Logger.Warn("replica heartbeat failed", "replica_id", s.cfg.ID, "error", err)
	}

	t := time.NewTicker(s.cfg.HeartbeatInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.sendHeartbeat(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.cfg.Logger.Warn("replica heartbeat failed", "replica_id", s.cfg.ID, "error", err)
			}
		}
	}
}

type heartbeatRequest struct {
	ReplicaID      string `json:"replica_id"`
	Connected      bool   `json:"connected"`
	LastAppliedSeq int64  `json:"last_applied_seq"`
	CoreLastSeq    int64  `json:"core_last_seq"`
	LastSyncAt     int64  `json:"last_sync_at"`
}

func (s *Service) sendHeartbeat(ctx context.Context) error {
	status := s.Status()
	payload, err := json.Marshal(heartbeatRequest{
		ReplicaID:      s.cfg.ID,
		Connected:      status.Connected,
		LastAppliedSeq: status.LastAppliedSeq,
		CoreLastSeq:    status.CoreLastSeq,
		LastSyncAt:     status.LastSyncAt,
	})
	if err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.HeartbeatTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.cfg.CoreBaseURL+"/internal/replicas/heartbeat", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return fmt.Errorf("heartbeat status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

func (s *Service) Markets() []domain.MarketState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.MarketState, 0, len(s.markets))
	for _, market := range s.markets {
		out = append(out, market)
	}
	return out
}

func (s *Service) MarketByID(id string) (domain.MarketState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	market, ok := s.markets[id]
	return market, ok
}

func (s *Service) MarketHistory(id string) []domain.ReplicationEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	history := s.history[id]
	out := make([]domain.ReplicationEvent, len(history))
	copy(out, history)
	return out
}

func (s *Service) LastAppliedSeq() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastAppliedSeq
}

func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lag := s.coreLastSeq - s.lastAppliedSeq
	if lag < 0 {
		lag = 0
	}
	return Status{
		Connected:      s.connected,
		Bootstrapped:   s.bootstrapped,
		LastAppliedSeq: s.lastAppliedSeq,
		CoreLastSeq:    s.coreLastSeq,
		LagSeq:         lag,
		LastSyncAt:     s.lastSyncAtMs,
		LastError:      s.lastError,
	}
}
