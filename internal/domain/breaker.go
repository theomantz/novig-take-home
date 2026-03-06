package domain

type BreakerConfig struct {
	BetCountThreshold       int64
	LiabilityThresholdCents int64
	CooldownMs              int64
}

type WindowMetrics struct {
	BetCount30s            int64
	StakeSum30sCents       int64
	LiabilityDelta30sCents int64
}

func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		BetCountThreshold:       120,
		LiabilityThresholdCents: 2_000_000,
		CooldownMs:              60_000,
	}
}

func ApplyBreaker(nowUnixMs int64, prev MarketState, metrics WindowMetrics, cfg BreakerConfig) (MarketState, EventType, bool) {
	next := prev
	next.BetCount30s = metrics.BetCount30s
	next.StakeSum30sCents = metrics.StakeSum30sCents
	next.LiabilityDelta30sCents = metrics.LiabilityDelta30sCents

	if next.Status == "" {
		next.Status = MarketStatusOpen
	}

	if next.Status == MarketStatusOpen {
		if metrics.BetCount30s >= cfg.BetCountThreshold {
			next.Status = MarketStatusSuspended
			next.CooldownUntilUnixMs = nowUnixMs + cfg.CooldownMs
			next.LastReason = BreakerReasonBetRateSpike
			next.LastTransitionUnixMs = nowUnixMs
			return next, EventTypeMarketSuspended, true
		}
		if metrics.LiabilityDelta30sCents >= cfg.LiabilityThresholdCents {
			next.Status = MarketStatusSuspended
			next.CooldownUntilUnixMs = nowUnixMs + cfg.CooldownMs
			next.LastReason = BreakerReasonLiabilitySpike
			next.LastTransitionUnixMs = nowUnixMs
			return next, EventTypeMarketSuspended, true
		}
		return next, EventTypeMarketUpdated, false
	}

	if next.Status == MarketStatusSuspended {
		if nowUnixMs >= next.CooldownUntilUnixMs &&
			metrics.BetCount30s < cfg.BetCountThreshold &&
			metrics.LiabilityDelta30sCents < cfg.LiabilityThresholdCents {
			next.Status = MarketStatusOpen
			next.CooldownUntilUnixMs = 0
			next.LastReason = BreakerReasonNone
			next.LastTransitionUnixMs = nowUnixMs
			return next, EventTypeMarketReopened, true
		}
	}

	return next, EventTypeMarketUpdated, false
}
