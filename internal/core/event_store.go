package core

import (
	"database/sql"
	"fmt"
	"net/url"

	"novig-take-home/internal/domain"

	_ "modernc.org/sqlite"
)

const inMemoryDSNOptions = "?mode=memory&cache=shared"

func InMemoryDSN(dbName string) string {
	if dbName == "" {
		dbName = "core_events"
	}
	return fmt.Sprintf("file:%s%s", url.PathEscape(dbName), inMemoryDSNOptions)
}

type EventStore struct {
	db *sql.DB
}

func NewEventStore(dsn string) (*EventStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	store := &EventStore{db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *EventStore) init() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS event_log (
			seq INTEGER PRIMARY KEY,
			event_id TEXT UNIQUE NOT NULL,
			ts_ms INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			market_id TEXT NOT NULL,
			payload_json TEXT NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("create event_log: %w", err)
	}
	return nil
}

func (s *EventStore) Insert(event domain.ReplicationEvent) error {
	_, err := s.db.Exec(`
		INSERT INTO event_log(seq, event_id, ts_ms, event_type, market_id, payload_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, event.Seq, event.EventID, event.TsUnixMs, string(event.EventType), event.MarketID, string(event.Payload))
	if err != nil {
		return fmt.Errorf("insert event_log: %w", err)
	}
	return nil
}

func (s *EventStore) EventsFromSeq(fromSeq int64) ([]domain.ReplicationEvent, error) {
	rows, err := s.db.Query(`
		SELECT seq, event_id, ts_ms, event_type, market_id, payload_json
		FROM event_log
		WHERE seq >= ?
		ORDER BY seq ASC
	`, fromSeq)
	if err != nil {
		return nil, fmt.Errorf("query event_log: %w", err)
	}
	defer rows.Close()

	out := make([]domain.ReplicationEvent, 0)
	for rows.Next() {
		var evt domain.ReplicationEvent
		var eventType string
		var payload string
		if err := rows.Scan(&evt.Seq, &evt.EventID, &evt.TsUnixMs, &eventType, &evt.MarketID, &payload); err != nil {
			return nil, fmt.Errorf("scan event_log: %w", err)
		}
		evt.EventType = domain.EventType(eventType)
		evt.Payload = []byte(payload)
		out = append(out, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event_log: %w", err)
	}
	return out, nil
}

func (s *EventStore) LastSeq() (int64, error) {
	var last sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM event_log`).Scan(&last); err != nil {
		return 0, fmt.Errorf("query last seq: %w", err)
	}
	if !last.Valid {
		return 0, nil
	}
	return last.Int64, nil
}

func (s *EventStore) Close() error {
	return s.db.Close()
}
