package replica

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

const inMemoryDSNOptions = "?mode=memory&cache=shared"

func InMemoryDSN(replicaID string) string {
	if replicaID == "" {
		replicaID = "replica"
	}
	return fmt.Sprintf("file:replica_%s%s", url.PathEscape(replicaID), inMemoryDSNOptions)
}

type Store struct {
	db *sql.DB
}

func NewStore(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	store := &Store{db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) init() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS applied_events (
			event_id TEXT PRIMARY KEY,
			seq INTEGER NOT NULL,
			applied_at_ms INTEGER NOT NULL
		);
		`,
		`
		CREATE TABLE IF NOT EXISTS consumer_checkpoint (
			id INTEGER PRIMARY KEY CHECK (id=1),
			last_applied_seq INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		);
		`,
		`INSERT OR IGNORE INTO consumer_checkpoint(id, last_applied_seq, updated_at_ms) VALUES (1, 0, 0);`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init replica sqlite: %w", err)
		}
	}
	return nil
}

func (s *Store) GetCheckpoint() (int64, error) {
	var seq int64
	if err := s.db.QueryRow(`SELECT last_applied_seq FROM consumer_checkpoint WHERE id=1`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("get checkpoint: %w", err)
	}
	return seq, nil
}

func (s *Store) SetCheckpoint(seq int64, updatedAtMs int64) error {
	_, err := s.db.Exec(`UPDATE consumer_checkpoint SET last_applied_seq=?, updated_at_ms=? WHERE id=1`, seq, updatedAtMs)
	if err != nil {
		return fmt.Errorf("set checkpoint: %w", err)
	}
	return nil
}

func (s *Store) IsEventApplied(eventID string) (bool, error) {
	var count int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM applied_events WHERE event_id=?`, eventID).Scan(&count); err != nil {
		return false, fmt.Errorf("is event applied: %w", err)
	}
	return count > 0, nil
}

func (s *Store) CommitAppliedEvent(eventID string, seq int64, tsMs int64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT OR IGNORE INTO applied_events(event_id, seq, applied_at_ms) VALUES (?, ?, ?)`, eventID, seq, tsMs)
	if err != nil {
		return false, fmt.Errorf("insert applied event: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit duplicate tx: %w", err)
		}
		return false, nil
	}

	if _, err := tx.Exec(`UPDATE consumer_checkpoint SET last_applied_seq=?, updated_at_ms=? WHERE id=1`, seq, tsMs); err != nil {
		return false, fmt.Errorf("update checkpoint: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit apply tx: %w", err)
	}
	return true, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
