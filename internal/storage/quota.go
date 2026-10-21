package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type CatalogUsage struct{ Used, Limit int64 }

func (s *DB) CatalogUsage(ctx context.Context) (u CatalogUsage, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT used_bytes,limit_bytes FROM catalog_quota WHERE singleton=1").Scan(&u.Used, &u.Limit)
	return
}

// SetCatalogQuota bounds persisted UTF-8 payload. SQLite files require a separate
// filesystem quota; this logical accounting does not bound page/WAL overhead.
func (s *DB) SetCatalogQuota(ctx context.Context, limit int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE catalog_quota SET limit_bytes=? WHERE singleton=1", limit)
	return err
}
func (s *DB) RecordCatalogCommand(ctx context.Context, command, guild, channel string, at time.Time) error {
	if command == "" || guild == "" || channel == "" {
		return fmt.Errorf("command identity required")
	}
	_, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO catalog_activity VALUES(?,?,?,?)", command, guild, channel, at.Unix())
	return err
}

type CatalogChannel struct {
	Guild, Channel string
	Commands       int
}

func (s *DB) CatalogChannels(ctx context.Context, at time.Time) ([]CatalogChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT guild_id,channel_id,count(*) FROM catalog_activity WHERE occurred_at>? AND occurred_at<=? GROUP BY guild_id,channel_id ORDER BY count(*) DESC,guild_id,channel_id`, at.Add(-30*24*time.Hour).Unix(), at.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CatalogChannel
	for rows.Next() {
		var c CatalogChannel
		if err = rows.Scan(&c.Guild, &c.Channel, &c.Commands); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// CatalogNotifier must reject missing channels and channels without permission.
// Send uses the stable event key for external deduplication when supported.
type CatalogNotifier interface {
	Eligible(context.Context, CatalogChannel) (bool, error)
	Send(context.Context, CatalogChannel, string, string) error
}

// CatalogAlertMonitor is single-consumer per database. Retry state survives a
// restart. A crash after HTTP success and before confirmation may redeliver;
// external APIs cannot participate in the SQLite commit.
type CatalogAlertMonitor struct {
	DB       *DB
	Notifier CatalogNotifier
	Log      *slog.Logger
}

func (m *CatalogAlertMonitor) Poll(ctx context.Context, now time.Time) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	m.DB.alertMu.Lock()
	defer m.DB.alertMu.Unlock()
	var id, episode, threshold, attempts int
	var used, limit int64
	var targetGuild, targetChannel sql.NullString
	err := m.DB.db.QueryRowContext(ctx, `SELECT id,episode,threshold,used_bytes,limit_bytes,attempts,target_guild,target_channel FROM catalog_alerts WHERE delivered=0 AND retry_at<=? ORDER BY id LIMIT 1`, now.Unix()).Scan(&id, &episode, &threshold, &used, &limit, &attempts, &targetGuild, &targetChannel)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	// Persist the next attempt before contacting an external service, including
	// failures to choose a channel. Backoff is bounded at one hour.
	delay := time.Second * time.Duration(1<<min(attempts, 12))
	if delay > time.Hour {
		delay = time.Hour
	}

	// Claim a lease atomically across connections/processes. Network operations
	// have a shorter deadline than the lease. Crashes leave a recoverable claim.
	claimed, err := m.DB.db.ExecContext(ctx, "UPDATE catalog_alerts SET attempts=attempts+1,retry_at=? WHERE id=? AND delivered=0 AND retry_at<=?", now.Add(time.Minute).Unix(), id, now.Unix())
	if err != nil {
		return err
	}
	count, err := claimed.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if m.Log != nil {
		level := slog.LevelWarn
		if threshold == 100 {
			level = slog.LevelError
		}
		m.Log.Log(ctx, level, "catalog_storage_threshold", "event_id", id, "threshold", threshold, "used_bytes", used, "limit_bytes", limit, "attempt", attempts+1)
	}
	defer func() {
		if resultErr != nil {
			// If the caller timed out, retain the crash lease instead of making an
			// unbounded background database call during shutdown.
			if ctx.Err() == nil {
				_, e := m.DB.db.ExecContext(ctx, "UPDATE catalog_alerts SET retry_at=? WHERE id=? AND delivered=0", now.Add(delay).Unix(), id)
				if e != nil {
					resultErr = errors.Join(resultErr, e)
				}
			}
		}
	}()
	channels, err := m.DB.CatalogChannels(ctx, now)
	if err != nil {
		return err
	}

	if targetGuild.Valid && targetChannel.Valid {
		target := CatalogChannel{Guild: targetGuild.String, Channel: targetChannel.String}
		// Keep an ambiguous delivery on the original channel, even if rankings change.
		filtered := []CatalogChannel{target}
		for _, c := range channels {
			if c.Guild != target.Guild || c.Channel != target.Channel {
				filtered = append(filtered, c)
			}
		}
		channels = filtered
	}
	for _, channel := range channels {
		eligible, e := m.Notifier.Eligible(ctx, channel)
		if e != nil {
			return e
		}
		if !eligible {
			continue
		}

		if _, err = m.DB.db.ExecContext(ctx, "UPDATE catalog_alerts SET target_guild=?,target_channel=? WHERE id=?", channel.Guild, channel.Channel, id); err != nil {
			return err
		}
		severity := "warning"
		if threshold == 100 {
			severity = "critical: catalog writes blocked"
		}
		message := fmt.Sprintf("Music storage %s: %d%% (%d/%d bytes). Existing music is preserved.", severity, threshold, used, limit)
		key := fmt.Sprintf("catalog-%d-%d", episode, threshold)
		if err = m.Notifier.Send(ctx, channel, key, message); err != nil {
			return err
		}
		_, err = m.DB.db.ExecContext(ctx, "UPDATE catalog_alerts SET delivered=1 WHERE id=?", id)
		return err
	}
	return fmt.Errorf("no eligible catalog alert channel")
}
