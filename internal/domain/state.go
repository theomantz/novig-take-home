package domain

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

func (m MarketState) IsEqual(other MarketState) bool {
	return m.MarketID == other.MarketID &&
		m.Status == other.Status &&
		m.BetCount30s == other.BetCount30s &&
		m.StakeSum30sCents == other.StakeSum30sCents &&
		m.LiabilityDelta30sCents == other.LiabilityDelta30sCents &&
		m.CooldownUntilUnixMs == other.CooldownUntilUnixMs &&
		m.LastReason == other.LastReason &&
		m.LastTransitionUnixMs == other.LastTransitionUnixMs
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
