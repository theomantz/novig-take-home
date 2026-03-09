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

// DefaultBreakerConfig returns production defaults for trip thresholds and cooldown duration.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		BetCountThreshold:       120,
		LiabilityThresholdCents: 2_000_000,
		CooldownMs:              60_000,
	}
}

// ApplyBreaker updates metrics and returns the next state, emitted event type, and whether a transition occurred.
func ApplyBreaker(nowUnixMs int64, prev MarketState, metrics WindowMetrics, cfg BreakerConfig) (MarketState, EventType, bool) {
	next := prev
	next.BetCount30s = metrics.BetCount30s
	next.StakeSum30sCents = metrics.StakeSum30sCents
	next.LiabilityDelta30sCents = metrics.LiabilityDelta30sCents

	if next.Status == "" {
		next.Status = MarketStatusOpen
	}

	if next.Status == MarketStatusOpen {
		if reason, shouldSuspend := suspensionReason(metrics, cfg); shouldSuspend {
			next = suspendMarket(next, nowUnixMs, reason, cfg)
			return next, EventTypeMarketSuspended, true
		}
		return next, EventTypeMarketUpdated, false
	}

	if next.Status == MarketStatusSuspended && canReopenMarket(nowUnixMs, next, metrics, cfg) {
		next.Status = MarketStatusOpen
		next.CooldownUntilUnixMs = 0
		next.LastReason = BreakerReasonNone
		next.LastTransitionUnixMs = nowUnixMs
		return next, EventTypeMarketReopened, true
	}

	return next, EventTypeMarketUpdated, false
}

// suspensionReason determines whether current metrics require a suspension and records the first trigger reason.
func suspensionReason(metrics WindowMetrics, cfg BreakerConfig) (BreakerReason, bool) {
	if metrics.BetCount30s >= cfg.BetCountThreshold {
		return BreakerReasonBetRateSpike, true
	}
	if metrics.LiabilityDelta30sCents >= cfg.LiabilityThresholdCents {
		return BreakerReasonLiabilitySpike, true
	}
	return BreakerReasonNone, false
}

// suspendMarket applies suspension fields, including cooldown and transition metadata.
func suspendMarket(state MarketState, nowUnixMs int64, reason BreakerReason, cfg BreakerConfig) MarketState {
	state.Status = MarketStatusSuspended
	state.CooldownUntilUnixMs = nowUnixMs + cfg.CooldownMs
	state.LastReason = reason
	state.LastTransitionUnixMs = nowUnixMs
	return state
}

// canReopenMarket reports whether cooldown elapsed and both breaker inputs are back below thresholds.
func canReopenMarket(nowUnixMs int64, state MarketState, metrics WindowMetrics, cfg BreakerConfig) bool {
	cooldownElapsed := nowUnixMs >= state.CooldownUntilUnixMs
	signalsNormalized := metrics.BetCount30s < cfg.BetCountThreshold &&
		metrics.LiabilityDelta30sCents < cfg.LiabilityThresholdCents
	return cooldownElapsed && signalsNormalized
}
