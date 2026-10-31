// Package storage provides a local SQLite database; no server or voice resources.
package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

//go:embed migrations/*.sql
var migrations embed.FS

type DB struct {
	db        *sql.DB
	alertMu   sync.Mutex
	physical  *physicalQuota
	closeOnce sync.Once
	closeErr  error
}

func Open(path string) (*DB, error) { return OpenWithPhysicalQuota(path, 0) }

func OpenWithPhysicalQuota(path string, limit int64) (*DB, error) {
	quota, err := openPhysicalQuota(path, limit)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			quota.close()
		}
	}()
	uri := url.URL{Scheme: "file", Path: quota.path}
	params := url.Values{"vfs": {quota.name}, "_pragma": {"foreign_keys(ON)", "busy_timeout(3000)", "temp_store(MEMORY)", "mmap_size(0)", "synchronous(FULL)"}}
	uri.RawQuery = params.Encode()
	path = uri.String()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &DB{db: db, physical: quota}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=3000", "PRAGMA journal_mode=WAL"} {
		if _, err = db.ExecContext(ctx, q); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	success = true
	return s, nil
}
func (s *DB) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.db.Close(); s.physical.close() })
	return s.closeErr
}
func (s *DB) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS migrations(version INTEGER PRIMARY KEY)"); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	var newest int
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(version),0) FROM migrations").Scan(&newest); err != nil {
		return err
	}
	if newest > len(entries) {
		return fmt.Errorf("database schema is newer than this binary")
	}
	for i, e := range entries {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM migrations WHERE version=?", i+1).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO migrations(version) VALUES(?)", i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *DB) SaveGuild(ctx context.Context, id string, state player.DurableState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO guilds(guild_id,state) VALUES(?,?) ON CONFLICT(guild_id) DO UPDATE SET state=excluded.state", id, string(data)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM history WHERE guild_id=?", id); err != nil {
		return err
	}
	for i, t := range state.History {
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO history(guild_id,ordinal,metadata) VALUES(?,?,?)", id, i, string(b)); err != nil {
			return err
		}
	}
	tracks := append([]media.Track(nil), state.Queue...)
	if state.Current != nil {
		tracks = append(tracks, *state.Current)
	}
	// Bound statement size and amortize SQL compilation across occurrences.
	// Keep every original UPSERT (including triggers) in the same transaction.
	const trackBatch = 128
	args := make([]any, 0, 2*trackBatch)
	flushTracks := func() error {
		if len(args) == 0 {
			return nil
		}
		values := strings.TrimSuffix(strings.Repeat("(?,?),", len(args)/2), ",")
		_, err := tx.ExecContext(ctx, "INSERT INTO tracks(identity,metadata) VALUES"+values+" ON CONFLICT(identity) DO UPDATE SET metadata=excluded.metadata", args...)
		args = args[:0]
		return err
	}
	for _, t := range tracks {
		// Progress belongs to the guild checkpoint, not catalog identity.
		t.ResumeOffset = 0
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%x", sha256.Sum256(b))
		args = append(args, key, string(b))
		if len(args) == 2*trackBatch {
			if err := flushTracks(); err != nil {
				return err
			}
		}
	}
	if err := flushTracks(); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *DB) LoadGuild(ctx context.Context, id string) (player.DurableState, error) {
	var b string
	var state player.DurableState
	err := s.db.QueryRowContext(ctx, "SELECT state FROM guilds WHERE guild_id=?", id).Scan(&b)
	if err == sql.ErrNoRows {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal([]byte(b), &state)
	return state, err
}
func (s *DB) SaveSettings(ctx context.Context, id string, settings map[string]string) error {
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO guilds(guild_id,state,settings) VALUES(?,'{}',?) ON CONFLICT(guild_id) DO UPDATE SET settings=excluded.settings", id, string(b))
	return err
}
func (s *DB) LoadSettings(ctx context.Context, id string) (map[string]string, error) {
	var b string
	result := map[string]string{}
	err := s.db.QueryRowContext(ctx, "SELECT settings FROM guilds WHERE guild_id=?", id).Scan(&b)
	if err == sql.ErrNoRows {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(b), &result)
	return result, err
}
func (s *DB) SaveMapping(ctx context.Context, key string, c media.Candidate) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO mappings(identity,candidate,expires) VALUES(?,?,?) ON CONFLICT(identity) DO UPDATE SET candidate=excluded.candidate,expires=excluded.expires", key, string(b), time.Now().Add(24*time.Hour).Unix())
	return err
}
func (s *DB) LoadMapping(ctx context.Context, key string) (media.Candidate, bool, error) {
	var b string
	var c media.Candidate
	err := s.db.QueryRowContext(ctx, "SELECT candidate FROM mappings WHERE identity=? AND expires>?", key, time.Now().Unix()).Scan(&b)
	if err == sql.ErrNoRows {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	err = json.Unmarshal([]byte(b), &c)
	return c, err == nil, err
}
func (s *DB) DeleteMapping(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM mappings WHERE identity=?", key)
	return err
}

// RecordPlayback is append-only: navigating backwards must not erase what played.
func (s *DB) RecordPlayback(ctx context.Context, id string, t media.Track) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO playback_events(guild_id,started_at,metadata) VALUES(?,?,?)", id, time.Now().UnixNano(), string(b))
	return err
}
func (s *DB) PlaybackHistory(ctx context.Context, id string, limit int) ([]media.Track, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("history limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT metadata FROM playback_events WHERE guild_id=? ORDER BY id DESC LIMIT ?", id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []media.Track
	for rows.Next() {
		var raw string
		var t media.Track
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}
