package domain

import "testing"

func TestApplyBreakerTripsOnBetRateThreshold(t *testing.T) {
	cfg := DefaultBreakerConfig()
	prev := MarketState{MarketID: "m1", Status: MarketStatusOpen}

	next, evt, transitioned := ApplyBreaker(1_000, prev, WindowMetrics{BetCount30s: 120}, cfg)
	if !transitioned {
		t.Fatalf("expected transition")
	}
	if evt != EventTypeMarketSuspended {
		t.Fatalf("expected suspended event, got %s", evt)
	}
	if next.Status != MarketStatusSuspended {
		t.Fatalf("expected suspended status, got %s", next.Status)
	}
	if next.LastReason != BreakerReasonBetRateSpike {
		t.Fatalf("expected bet rate reason, got %s", next.LastReason)
	}
}

func TestApplyBreakerTripsOnLiabilityThreshold(t *testing.T) {
	cfg := DefaultBreakerConfig()
	prev := MarketState{MarketID: "m1", Status: MarketStatusOpen}

	next, evt, transitioned := ApplyBreaker(1_000, prev, WindowMetrics{LiabilityDelta30sCents: 2_000_000}, cfg)
	if !transitioned {
		t.Fatalf("expected transition")
	}
	if evt != EventTypeMarketSuspended {
		t.Fatalf("expected suspended event, got %s", evt)
	}
	if next.Status != MarketStatusSuspended {
		t.Fatalf("expected suspended status, got %s", next.Status)
	}
	if next.LastReason != BreakerReasonLiabilitySpike {
		t.Fatalf("expected liability reason, got %s", next.LastReason)
	}
}

func TestApplyBreakerDoesNotTripBelowThresholds(t *testing.T) {
	cfg := DefaultBreakerConfig()
	prev := MarketState{MarketID: "m1", Status: MarketStatusOpen}

	next, evt, transitioned := ApplyBreaker(1_000, prev, WindowMetrics{BetCount30s: 119, LiabilityDelta30sCents: 1_999_999}, cfg)
	if transitioned {
		t.Fatalf("did not expect transition")
	}
	if evt != EventTypeMarketUpdated {
		t.Fatalf("expected market updated event, got %s", evt)
	}
	if next.Status != MarketStatusOpen {
		t.Fatalf("expected open status, got %s", next.Status)
	}
}

func TestApplyBreakerAutoReopensOnlyAfterCooldownAndNormalization(t *testing.T) {
	cfg := DefaultBreakerConfig()
	prev := MarketState{
		MarketID:            "m1",
		Status:              MarketStatusSuspended,
		CooldownUntilUnixMs: 10_000,
		LastReason:          BreakerReasonBetRateSpike,
	}

	beforeCooldown, evt, transitioned := ApplyBreaker(9_999, prev, WindowMetrics{BetCount30s: 0, LiabilityDelta30sCents: 0}, cfg)
	if transitioned {
		t.Fatalf("expected no transition before cooldown")
	}
	if beforeCooldown.Status != MarketStatusSuspended {
		t.Fatalf("expected suspended before cooldown")
	}
	if evt != EventTypeMarketUpdated {
		t.Fatalf("expected market updated before cooldown, got %s", evt)
	}

	afterCooldownNotNormalized, _, transitioned := ApplyBreaker(10_001, prev, WindowMetrics{BetCount30s: cfg.BetCountThreshold}, cfg)
	if transitioned {
		t.Fatalf("expected no transition when metrics still elevated")
	}
	if afterCooldownNotNormalized.Status != MarketStatusSuspended {
		t.Fatalf("expected suspended while metrics elevated")
	}

	afterCooldownNormalized, evt, transitioned := ApplyBreaker(10_001, prev, WindowMetrics{BetCount30s: 0, LiabilityDelta30sCents: 0}, cfg)
	if !transitioned {
		t.Fatalf("expected transition after cooldown and normalization")
	}
	if evt != EventTypeMarketReopened {
		t.Fatalf("expected reopen event, got %s", evt)
	}
	if afterCooldownNormalized.Status != MarketStatusOpen {
		t.Fatalf("expected open status, got %s", afterCooldownNormalized.Status)
	}
}
