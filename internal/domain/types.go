package domain

import "encoding/json"

type MarketStatus string

const (
	MarketStatusOpen      MarketStatus = "OPEN"
	MarketStatusSuspended MarketStatus = "SUSPENDED"
)

type BreakerReason string

const (
	BreakerReasonNone           BreakerReason = ""
	BreakerReasonBetRateSpike   BreakerReason = "BET_RATE_SPIKE"
	BreakerReasonLiabilitySpike BreakerReason = "LIABILITY_SPIKE"
)

type EventType string

const (
	EventTypeMarketUpdated   EventType = "MARKET_UPDATED"
	EventTypeMarketSuspended EventType = "MARKET_SUSPENDED"
	EventTypeMarketReopened  EventType = "MARKET_REOPENED"
)

type MarketState struct {
	MarketID               string        `json:"market_id"`
	Status                 MarketStatus  `json:"status"`
	BetCount30s            int64         `json:"bet_count_30s"`
	StakeSum30sCents       int64         `json:"stake_sum_30s_cents"`
	LiabilityDelta30sCents int64         `json:"liability_delta_30s_cents"`
	CooldownUntilUnixMs    int64         `json:"cooldown_until_unix_ms"`
	LastReason             BreakerReason `json:"last_reason"`
	LastTransitionUnixMs   int64         `json:"last_transition_unix_ms"`
}

type ReplicationEvent struct {
	Seq       int64           `json:"seq"`
	EventID   string          `json:"event_id"`
	TsUnixMs  int64           `json:"ts_unix_ms"`
	EventType EventType       `json:"event_type"`
	MarketID  string          `json:"market_id"`
	Payload   json.RawMessage `json:"payload"`
}

type SnapshotResponse struct {
	LastSeq int64                  `json:"last_seq"`
	Markets map[string]MarketState `json:"markets"`
}

type SignalInput struct {
	MarketID            string `json:"market_id"`
	BetCount            int64  `json:"bet_count"`
	StakeCents          int64  `json:"stake_cents"`
	LiabilityDeltaCents int64  `json:"liability_delta_cents"`
	TsUnixMs            int64  `json:"ts_unix_ms,omitempty"`
}
