package logstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type Filter struct {
	Last                                    int
	Level, Guild, Interaction, Track, Event string
	Since                                   time.Time
}

// Record is a JSONL envelope around the sanitized original structured payload.
// Missing correlation fields are omitted, never synthesized.
type Record struct {
	ID          int64           `json:"id"`
	Timestamp   time.Time       `json:"timestamp"`
	Level       string          `json:"level"`
	Event       string          `json:"event"`
	Guild       *string         `json:"guild_id,omitempty"`
	Interaction *string         `json:"interaction_id,omitempty"`
	Track       *string         `json:"track_id,omitempty"`
	Spotify     *string         `json:"spotify_track_id,omitempty"`
	Video       *string         `json:"youtube_video_id,omitempty"`
	Collection  *string         `json:"collection_id,omitempty"`
	Position    *int64          `json:"position,omitempty"`
	Category    *string         `json:"error_category,omitempty"`
	Elapsed     *float64        `json:"elapsed_ms,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

// Read opens no writer and never initializes/migrates/checkpoints a database.
// Select the newest N by event time, then emit chronologically (ID breaks ties).
func Read(ctx context.Context, path string, f Filter, out io.Writer) error {
	if f.Last == 0 {
		f.Last = 100
	}
	if f.Last < 1 || f.Last > 10000 {
		return errors.New("last must be within 1–10000")
	}
	if f.Level != "" {
		f.Level = strings.ToUpper(f.Level)
		switch f.Level {
		case "DEBUG", "INFO", "WARN", "ERROR":
		default:
			return errors.New("level must be DEBUG, INFO, WARN or ERROR")
		}
	}
	db, err := sql.Open("sqlite", dsn(path, "ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var app, version int
	if err = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return err
	}
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if app != applicationID || version != 1 {
		return errors.New("not a compatible Bard log database")
	}
	query, args, err := selectQuery(f)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	for rows.Next() {
		var r Record
		var ns int64
		var raw string
		if err = rows.Scan(&r.ID, &ns, &r.Level, &r.Event, &r.Guild, &r.Interaction, &r.Track, &r.Spotify, &r.Video, &r.Collection, &r.Position, &r.Category, &r.Elapsed, &raw); err != nil {
			return err
		}
		r.Timestamp = time.Unix(0, ns).UTC()
		r.Payload = json.RawMessage(raw)
		if err = encoder.Encode(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
func selectQuery(f Filter) (string, []any, error) {
	where := []string{"1=1"}
	args := []any{}
	for _, pair := range [][2]string{{"level", f.Level}, {"guild_id", f.Guild}, {"interaction_id", f.Interaction}, {"event", f.Event}} {
		if pair[1] != "" {
			where = append(where, pair[0]+"=?")
			args = append(args, pair[1])
		}
	}
	if f.Track != "" {
		where = append(where, "(track_id=? OR spotify_track_id=? OR youtube_video_id=?)")
		args = append(args, f.Track, f.Track, f.Track)
	}
	if !f.Since.IsZero() {
		where = append(where, "timestamp_ns>=?")
		args = append(args, f.Since.UnixNano())
	}
	args = append(args, f.Last)
	return `SELECT * FROM (SELECT id,timestamp_ns,level,event,guild_id,interaction_id,track_id,spotify_track_id,youtube_video_id,collection_id,position,error_category,elapsed_ms,payload FROM logs WHERE ` + strings.Join(where, " AND ") + ` ORDER BY timestamp_ns DESC,id DESC LIMIT ?) ORDER BY timestamp_ns,id`, args, nil
}
