package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
	"whiterun/internal/media"
)

func (s *DB) SaveCatalog(ctx context.Context, t media.Track) error {
	if t.SpotifyID == "" || t.RequestedSource != "spotify" {
		return fmt.Errorf("catalog requires Spotify metadata")
	}
	// Whitelist source metadata; never persist requester/queue/session fields here.
	clean := media.Track{SpotifyID: t.SpotifyID, RequestedSource: "spotify", SourceURL: "https://open.spotify.com/track/" + t.SpotifyID, Title: t.Title, Artist: t.Artist, Artists: t.Artists, Album: t.Album, Duration: t.Duration, ISRC: t.ISRC, Explicit: t.Explicit, SpotifyArtworkURL: media.SafeArtworkURL(t.SpotifyArtworkURL), YouTubeThumbnailURL: media.SafeArtworkURL(t.YouTubeThumbnailURL)}
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO music_catalog(spotify_id,metadata,updated_at) VALUES(?,?,?) ON CONFLICT(spotify_id) DO UPDATE SET metadata=CASE
 WHEN coalesce(json_extract(excluded.metadata,'$.SpotifyArtworkURL'),'')='' THEN
 json_set(excluded.metadata,'$.SpotifyArtworkURL',coalesce(json_extract(music_catalog.metadata,'$.SpotifyArtworkURL'),''))
 ELSE excluded.metadata END,updated_at=excluded.updated_at`, t.SpotifyID, string(raw), time.Now().Unix())
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
