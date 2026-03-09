package domain

import "testing"

// TestMarketStateIsEqual confirms IsEqual compares every replicated MarketState field.
func TestMarketStateIsEqual(t *testing.T) {
	base := MarketState{
		MarketID:               "market-1",
		Status:                 MarketStatusSuspended,
		BetCount30s:            10,
		StakeSum30sCents:       20,
		LiabilityDelta30sCents: 30,
		CooldownUntilUnixMs:    40,
		LastReason:             BreakerReasonBetRateSpike,
		LastTransitionUnixMs:   50,
	}
	if !base.IsEqual(base) {
		t.Fatalf("expected identical states to be equal")
	}

	cases := []struct {
		name   string
		mutate func(*MarketState)
	}{
		{
			name: "market id",
			mutate: func(m *MarketState) {
				m.MarketID = "market-2"
			},
		},
		{
			name: "status",
			mutate: func(m *MarketState) {
				m.Status = MarketStatusOpen
			},
		},
		{
			name: "bet count",
			mutate: func(m *MarketState) {
				m.BetCount30s++
			},
		},
		{
			name: "stake sum",
			mutate: func(m *MarketState) {
				m.StakeSum30sCents++
			},
		},
		{
			name: "liability delta",
			mutate: func(m *MarketState) {
				m.LiabilityDelta30sCents++
			},
		},
		{
			name: "cooldown",
			mutate: func(m *MarketState) {
				m.CooldownUntilUnixMs++
			},
		},
		{
			name: "reason",
			mutate: func(m *MarketState) {
				m.LastReason = BreakerReasonLiabilitySpike
			},
		},
		{
			name: "last transition",
			mutate: func(m *MarketState) {
				m.LastTransitionUnixMs++
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other := base
			tc.mutate(&other)
			if base.IsEqual(other) {
				t.Fatalf("expected states to differ on %s", tc.name)
			}
		})
	}
}
