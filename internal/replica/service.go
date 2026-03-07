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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novig-take-home/internal/domain"
)

var ErrSeqGap = errors.New("sequence gap detected")

type ServiceConfig struct {
	ID                string
	CoreBaseURL       string
	HTTPClient        *http.Client
	Logger            *slog.Logger
	ReconnectBackoff  time.Duration
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	Now               func() time.Time
}

type Status struct {
	Connected      bool  `json:"connected"`
	LastAppliedSeq int64 `json:"last_applied_seq"`
	CoreLastSeq    int64 `json:"core_last_seq"`
	LagSeq         int64 `json:"lag_seq"`
	LastSyncAt     int64 `json:"last_sync_at"`
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
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 2 * time.Second
	}
	if cfg.HeartbeatTimeout == 0 {
		cfg.HeartbeatTimeout = 2 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
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
				if snapErr := s.refreshFromSnapshot(ctx); snapErr != nil {
					s.cfg.Logger.Warn("snapshot refresh failed", "replica_id", s.cfg.ID, "error", snapErr)
					s.waitBackoff(ctx)
				}
				continue
			}

			s.cfg.Logger.Warn("replica stream disconnected", "replica_id", s.cfg.ID, "error", err)
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

	nowMs := s.cfg.Now().UnixMilli()
	if err := s.store.SetCheckpoint(snapshot.LastSeq, nowMs); err != nil {
		return err
	}

	s.mu.Lock()
	s.markets = make(map[string]domain.MarketState, len(snapshot.Markets))
	maps.Copy(s.markets, snapshot.Markets)
	s.history = make(map[string][]domain.ReplicationEvent)
	s.lastAppliedSeq = snapshot.LastSeq
	s.coreLastSeq = snapshot.LastSeq
	s.lastSyncAtMs = nowMs
	s.bootstrapped = true
	s.mu.Unlock()

	s.cfg.Logger.Info("replica snapshot synced",
		"replica_id", s.cfg.ID,
		"last_applied_seq", snapshot.LastSeq,
	)

	return nil
}

func (s *Service) fetchSnapshot(ctx context.Context) (domain.SnapshotResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.CoreBaseURL+"/snapshot", nil)
	if err != nil {
		return domain.SnapshotResponse{}, err
	}

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return domain.SnapshotResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL.String(), nil)
	if err != nil {
		return err
	}

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stream status %d: %s", resp.StatusCode, string(body))
	}

	s.setConnected(true)
	defer s.setConnected(false)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	for scanner.Scan() {
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

	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (s *Service) processEvent(evt domain.ReplicationEvent) error {
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

	var market domain.MarketState
	if err := json.Unmarshal(evt.Payload, &market); err != nil {
		return fmt.Errorf("decode payload: %w", err)
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
	t := time.NewTimer(s.cfg.ReconnectBackoff)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
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
		LastAppliedSeq: s.lastAppliedSeq,
		CoreLastSeq:    s.coreLastSeq,
		LagSeq:         lag,
		LastSyncAt:     s.lastSyncAtMs,
	}
}
