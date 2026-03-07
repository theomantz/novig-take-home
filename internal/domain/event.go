package domain

import "encoding/json"

type EventType string

const (
	EventTypeMarketUpdated   EventType = "MARKET_UPDATED"
	EventTypeMarketSuspended EventType = "MARKET_SUSPENDED"
	EventTypeMarketReopened  EventType = "MARKET_REOPENED"
)

type ReplicationEvent struct {
	Seq       int64           `json:"seq"`
	EventID   string          `json:"event_id"`
	TsUnixMs  int64           `json:"ts_unix_ms"`
	EventType EventType       `json:"event_type"`
	MarketID  string          `json:"market_id"`
	Payload   json.RawMessage `json:"payload"`
}
