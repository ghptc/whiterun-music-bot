package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"whiterun/internal/media"
)

// SavePlaylist replaces an accessible snapshot atomically. Missing/pending metadata
// is rejected; it must never replace a complete offline snapshot with placeholders.
func (s *DB) SavePlaylist(ctx context.Context, id string, c media.Collection) error {
	if id == "" || len(c.Tracks) == 0 {
		return fmt.Errorf("playlist requires identity and tracks")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE catalog_quota SET monitoring=0 WHERE singleton=1"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_playlists VALUES(?,?,?,?) ON CONFLICT(spotify_id) DO UPDATE SET title=excluded.title,source_url=excluded.source_url,skipped=excluded.skipped`, id, c.Title, c.SourceURL, c.Skipped)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_positions WHERE playlist_id=?", id); err != nil {
		return err
	}
	for i, t := range c.Tracks {
		if err = saveCatalog(ctx, tx, t); err != nil {
			return err
		}
		position, total := t.Position, t.Total
		if position == 0 {
			position = i + 1
		}
		if total == 0 {
			total = len(c.Tracks) + c.Skipped
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_positions VALUES(?,?,?,?,?)", id, i, position, total, t.SpotifyID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE catalog_quota SET monitoring=1,used_bytes=used_bytes WHERE singleton=1"); err != nil {
		return err
	}
	return tx.Commit()
}

// LoadPlaylist uses one SQL statement, so concurrent replacements cannot produce
// a mixture of snapshots. Catalog snapshots do not expire with audio mappings.
func (s *DB) LoadPlaylist(ctx context.Context, id string) (media.Collection, bool, error) {
	c := media.Collection{IsCollection: true}
	rows, err := s.db.QueryContext(ctx, `SELECT p.title,p.source_url,p.skipped,t.metadata,o.position,o.total FROM catalog_playlists p JOIN catalog_positions o ON o.playlist_id=p.spotify_id JOIN music_catalog t ON t.spotify_id=o.track_id WHERE p.spotify_id=? ORDER BY o.ordinal`, id)
	if err != nil {
		return c, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var t media.Track
		if err = rows.Scan(&c.Title, &c.SourceURL, &c.Skipped, &raw, &t.Position, &t.Total); err != nil {
			return c, false, err
		}
		position, total := t.Position, t.Total
		if err = json.Unmarshal([]byte(raw), &t); err != nil {
			return c, false, err
		}
		t.Position, t.Total = position, total
		c.Tracks = append(c.Tracks, t)
	}
	err = rows.Err()
	return c, len(c.Tracks) > 0 && err == nil, err
}
func (s *DB) DeletePlaylist(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM catalog_playlists WHERE spotify_id=?", id)
	return err
}
func (s *DB) DeletePlaylistPosition(ctx context.Context, id string, position int) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM catalog_positions WHERE playlist_id=? AND position=?", id, position)
	return err
}

// DeleteOrphanTrack is explicit, never automatic. Referenced tracks are protected.
func (s *DB) DeleteOrphanTrack(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM music_catalog WHERE spotify_id=?", id)
	return err
}
func (s *DB) CatalogPosition(ctx context.Context, id string, position int) (media.Track, error) {
	var raw string
	var t media.Track
	err := s.db.QueryRowContext(ctx, `SELECT t.metadata,o.total FROM catalog_positions o JOIN music_catalog t ON t.spotify_id=o.track_id WHERE o.playlist_id=? AND o.position=?`, id, position).Scan(&raw, &t.Total)
	if err != nil {
		return t, err
	}
	total := t.Total
	err = json.Unmarshal([]byte(raw), &t)
	t.Position, t.Total = position, total
	return t, err
}
