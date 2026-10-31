package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"whiterun/internal/media"
)

type catalogWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *DB) SaveCatalog(ctx context.Context, t media.Track) error { return saveCatalog(ctx, s.db, t) }

// SaveCatalogBatch commits collection metadata once, without changing catalog semantics.
func (s *DB) SaveCatalogBatch(ctx context.Context, tracks []media.Track) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE catalog_quota SET monitoring=0 WHERE singleton=1"); err != nil {
		return err
	}
	const batchSize = 128
	args := make([]any, 0, 3*batchSize)
	flush := func() error {
		if len(args) == 0 {
			return nil
		}
		values := strings.TrimSuffix(strings.Repeat("(?,?,?),", len(args)/3), ",")
		_, err := tx.ExecContext(ctx, "INSERT INTO music_catalog(spotify_id,metadata,updated_at) VALUES"+values+catalogConflict, args...)
		args = args[:0]
		return err
	}
	for _, t := range tracks {
		raw, err := catalogMetadata(t)
		if err != nil {
			return err
		}
		args = append(args, t.SpotifyID, string(raw), time.Now().Unix())
		if len(args) == 3*batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE catalog_quota SET monitoring=1,used_bytes=used_bytes WHERE singleton=1"); err != nil {
		return err
	}
	return tx.Commit()
}

func catalogMetadata(t media.Track) ([]byte, error) {
	if t.SpotifyID == "" || t.RequestedSource != "spotify" || t.MetadataPending {
		return nil, fmt.Errorf("catalog requires Spotify metadata")
	}
	// Whitelist source metadata; never persist requester/queue/session fields here.
	clean := media.Track{SpotifyID: t.SpotifyID, RequestedSource: "spotify", SourceURL: "https://open.spotify.com/track/" + t.SpotifyID, Title: t.Title, Artist: t.Artist, Artists: t.Artists, Album: t.Album, Duration: t.Duration, ISRC: t.ISRC, Explicit: t.Explicit, SpotifyArtworkURL: media.SafeArtworkURL(t.SpotifyArtworkURL), YouTubeThumbnailURL: media.SafeArtworkURL(t.YouTubeThumbnailURL)}
	return json.Marshal(clean)
}

const catalogConflict = ` ON CONFLICT(spotify_id) DO UPDATE SET metadata=CASE
 WHEN coalesce(json_extract(excluded.metadata,'$.SpotifyArtworkURL'),'')='' THEN
 json_set(excluded.metadata,'$.SpotifyArtworkURL',coalesce(json_extract(music_catalog.metadata,'$.SpotifyArtworkURL'),''))
 ELSE excluded.metadata END,updated_at=excluded.updated_at`

func saveCatalog(ctx context.Context, writer catalogWriter, t media.Track) error {
	raw, err := catalogMetadata(t)
	if err != nil {
		return err
	}
	_, err = writer.ExecContext(ctx, "INSERT INTO music_catalog(spotify_id,metadata,updated_at) VALUES(?,?,?)"+catalogConflict, t.SpotifyID, string(raw), time.Now().Unix())
	return err
}
func (s *DB) LoadCatalog(ctx context.Context, id string) (media.Track, bool, error) {
	var raw string
	var t media.Track
	err := s.db.QueryRowContext(ctx, `SELECT metadata FROM music_catalog WHERE spotify_id=? AND updated_at>?`, id, time.Now().Add(-24*time.Hour).Unix()).Scan(&raw)
	if err == sql.ErrNoRows {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	err = json.Unmarshal([]byte(raw), &t)
	return t, err == nil, err
}

// Do not evict a replacement mapping when an older stream fails concurrently.
func (s *DB) DeleteMappingForVideo(ctx context.Context, key, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM mappings WHERE identity=? AND json_extract(candidate,'$.id')=?", key, id)
	return err
}
