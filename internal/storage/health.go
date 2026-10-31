package storage

import "context"

// CheckHealth verifies a real SQLite read without changing quota or checkpoints.
func (s *DB) CheckHealth(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}
