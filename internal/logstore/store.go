// Package logstore is a bounded, best-effort asynchronous slog persistence sink.
// It owns a separate SQLite file and never calls player or operational storage.
package logstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	sqlite "modernc.org/sqlite"
)

const (
	DefaultMaxBytes int64 = 524288000
	MinMaxBytes     int64 = 1 << 20
	applicationID         = 0x424c4f47 // BLOG; reject foreign databases before changing them.
	pageSize        int64 = 4096
	maxRecordBytes        = 16 << 10
)

type Options struct {
	Path            string
	OperationalPath string
	MaxBytes        int64
	Buffer          int
	Batch           int
	FlushInterval   time.Duration
	WriteTimeout    time.Duration
	// Diagnostic must be the console-only logger, never this store's own handler.
	Diagnostic *slog.Logger
	Secrets    []string
}

type counters struct {
	written, removed, failures, overflow, oversized, lost, batches, rows atomic.Uint64
	bytes, flushNS, cleanupNS                                            atomic.Int64
}
type Metrics struct {
	FailureCategory string  `json:"failure_category,omitempty"`
	Bytes           int64   `json:"bytes"`
	Rows            uint64  `json:"rows"`
	Written         uint64  `json:"written"`
	Removed         uint64  `json:"removed"`
	Failures        uint64  `json:"failures"`
	Overflow        uint64  `json:"overflow"`
	Oversized       uint64  `json:"oversized"`
	Lost            uint64  `json:"lost"`
	Batches         uint64  `json:"batches"`
	FlushMS         float64 `json:"flush_ms"`
	CleanupMS       float64 `json:"cleanup_ms"`
}

type Store struct {
	lastFailure atomic.Pointer[string]
	db          *sql.DB
	lock        *os.File
	opt         Options
	mainLimit   int64
	batchLimit  int
	recordLimit int
	queue       chan entry
	done        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.RWMutex // only guards nonblocking enqueue vs channel close; never SQLite.
	closed      bool
	metrics     counters
	scrub       *redactor
}

type entry struct {
	Timestamp                                             int64
	Level, Event                                          string
	Guild, Interaction, Track, Spotify, Video, Collection *string
	Position                                              *int64
	Category                                              *string
	Elapsed                                               *float64
	Payload                                               []byte
}

func Open(opt Options) (_ *Store, err error) {
	if opt.MaxBytes == 0 {
		opt.MaxBytes = DefaultMaxBytes
	}
	if opt.MaxBytes < MinMaxBytes {
		return nil, fmt.Errorf("log quota must be at least %d bytes", MinMaxBytes)
	}
	if opt.Buffer == 0 {
		opt.Buffer = 512
	}
	if opt.Buffer < 1 || opt.Buffer > 8192 {
		return nil, errors.New("invalid log buffer size")
	}
	if opt.Batch == 0 {
		opt.Batch = 64
	}
	if opt.Batch < 1 || opt.Batch > 256 {
		return nil, errors.New("invalid log batch size")
	}
	if opt.FlushInterval == 0 {
		opt.FlushInterval = 250 * time.Millisecond
	}
	if opt.WriteTimeout == 0 {
		opt.WriteTimeout = 2 * time.Second
	}
	if opt.FlushInterval < 0 || opt.WriteTimeout < 0 {
		return nil, errors.New("invalid log timeout")
	}
	path, err := filepath.Abs(opt.Path)
	if err != nil || opt.Path == "" {
		return nil, errors.New("invalid log database path")
	}
	opt.Path = path
	if err = separatePath(path, opt.OperationalPath); err != nil {
		return nil, err
	}
	// No symlinks: do not let a path alias change permissions or acquire a foreign DB.
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("log database already has a writer")
	}
	db, err := sql.Open("sqlite", dsn(path, "rwc"))
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock, opt: opt, mainLimit: (opt.MaxBytes / 5 * 2 / pageSize) * pageSize, queue: make(chan entry, opt.Buffer), done: make(chan struct{}), scrub: newRedactor(opt.Secrets)}
	defer func() {
		if err != nil {
			db.Close()
			lock.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), max(10*time.Second, opt.WriteTimeout))
	defer cancel()
	if err = s.initialize(ctx); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err = os.Chmod(path+suffix, 0600); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	// Index-growth reserve is 8 pages per row. Bound it to 1/4 of the
	// main-file budget, in addition to the existing payload bound (3/16).
	// Tiny records should not force a checkpoint every handful of messages.
	// Large records still flush at the unchanged byte limit in run.
	s.batchLimit = min(opt.Batch, max(1, int(s.mainLimit/(32*pageSize))))
	s.recordLimit = min(maxRecordBytes, int(s.mainLimit/16))
	var rows uint64
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM logs").Scan(&rows); err != nil {
		return nil, err
	}
	s.metrics.rows.Store(rows)
	if err = s.maintain(ctx, 0); err != nil {
		return nil, err
	}

	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.run()
	return s, nil
}

func separatePath(path, operational string) error {
	if operational == "" {
		return nil
	}
	other, err := filepath.Abs(operational)
	if err != nil {
		return err
	}
	if path == other {
		return errors.New("log and operational databases must be separate")
	}
	a, e1 := os.Stat(path)
	b, e2 := os.Stat(other)
	if e1 == nil && e2 == nil && os.SameFile(a, b) {
		return errors.New("log database aliases operational database")
	}
	return nil
}
func dsn(path, mode string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(100)", "query_only(" + map[bool]string{true: "1", false: "0"}[mode == "ro"] + ")"}}
	u.RawQuery = q.Encode()
	return u.String()
}

// FailureCategory returns only fixed labels; never echo raw SQLite/path/provider errors.
func FailureCategory(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout_or_shutdown"
	}
	var e *sqlite.Error
	if errors.As(err, &e) {
		switch e.Code() & 255 {
		case 13:
			return "disk_full"
		case 5, 6:
			return "busy"
		case 11, 26:
			return "corrupt"
		case 8, 14:
			return "unavailable"
		}
	}
	message := err.Error()
	for _, word := range []string{"quota", "checkpoint", "foreign", "newer", "already has a writer", "corrupt"} {
		if strings.Contains(message, word) {
			return strings.ReplaceAll(word, " ", "_")
		}
	}
	return "unavailable"
}

func (s *Store) initialize(ctx context.Context) error {
	var app, version, tables int
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return err
	}
	if app != applicationID && (app != 0 || tables != 0) {
		return errors.New("refusing foreign SQLite database")
	}
	if version > 1 {
		return errors.New("log database schema is newer than this binary")
	}
	if err := s.lock.Chmod(0600); err != nil {
		return err
	}
	for _, q := range []string{"PRAGMA page_size=4096", "PRAGMA auto_vacuum=INCREMENTAL"} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	var size, auto int
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&auto); err != nil {
		return err
	}
	if size != int(pageSize) || auto != 2 {
		return errors.New("unsupported log database page layout; no automatic VACUUM")
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return errors.New("log database requires WAL")
	}
	for _, q := range []string{"PRAGMA synchronous=NORMAL", "PRAGMA wal_autocheckpoint=0", "PRAGMA cache_size=-4096", "PRAGMA cache_spill=OFF", fmt.Sprintf("PRAGMA journal_size_limit=%d", s.mainLimit), fmt.Sprintf("PRAGMA max_page_count=%d", s.mainLimit/pageSize)} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if version == 0 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, q := range []string{schema, fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=1"} {
			if _, err = tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	var check string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return errors.New("corrupt log database; preserve file for offline recovery")
	}
	return nil
}

const schema = `CREATE TABLE logs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 timestamp_ns INTEGER NOT NULL,
 level TEXT NOT NULL,
 event TEXT NOT NULL,
 guild_id TEXT, interaction_id TEXT, track_id TEXT,
 spotify_track_id TEXT, youtube_video_id TEXT, collection_id TEXT,
 position INTEGER, error_category TEXT, elapsed_ms REAL,
 payload TEXT NOT NULL CHECK(json_valid(payload))
);
CREATE INDEX logs_time ON logs(timestamp_ns,id);
CREATE INDEX logs_guild ON logs(guild_id,timestamp_ns,id);
CREATE INDEX logs_event ON logs(event,timestamp_ns,id);
CREATE INDEX logs_track ON logs(track_id,timestamp_ns,id);
CREATE INDEX logs_spotify ON logs(spotify_track_id,timestamp_ns,id);
CREATE INDEX logs_video ON logs(youtube_video_id,timestamp_ns,id);
CREATE INDEX logs_level ON logs(level,timestamp_ns,id);`

func (s *Store) enqueue(e entry) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.metrics.lost.Add(1)
		return
	}
	select {
	case s.queue <- e:
	default:
		s.metrics.overflow.Add(1)
	} // drop newest; console already emitted.
}
func (s *Store) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	}
}
func (s *Store) Metrics() Metrics {
	m := &s.metrics
	category := ""
	if ptr := s.lastFailure.Load(); ptr != nil {
		category = *ptr
	}
	return Metrics{FailureCategory: category, Bytes: m.bytes.Load(), Rows: m.rows.Load(), Written: m.written.Load(), Removed: m.removed.Load(), Failures: m.failures.Load(), Overflow: m.overflow.Load(), Oversized: m.oversized.Load(), Lost: m.lost.Load(), Batches: m.batches.Load(), FlushMS: float64(m.flushNS.Load()) / 1e6, CleanupMS: float64(m.cleanupNS.Load()) / 1e6}
}

func (s *Store) run() {
	defer close(s.done)
	defer s.lock.Close()
	defer s.db.Close()
	defer s.cancel()
	ticker := time.NewTicker(s.opt.FlushInterval)
	defer ticker.Stop()
	report := time.NewTicker(30 * time.Second)
	defer report.Stop()
	batch := make([]entry, 0, s.batchLimit)
	bytes := 0
	flush := func() {
		if len(batch) == 0 {
			return
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(s.ctx, s.opt.WriteTimeout)
		before := s.metrics.written.Load()
		err := s.write(ctx, batch)
		cancel()
		s.metrics.flushNS.Store(time.Since(start).Nanoseconds())
		if err != nil {
			s.metrics.failures.Add(1)
			category := FailureCategory(err)
			s.lastFailure.Store(&category)
			s.metrics.lost.Add(uint64(len(batch)) - (s.metrics.written.Load() - before))
		}
		clear(batch)
		batch = batch[:0]
		bytes = 0
	}
	lastWritten := uint64(0)
	reportAt := time.Now()
	for {
		select {
		case <-s.ctx.Done():
			s.metrics.lost.Add(uint64(len(batch) + len(s.queue)))
			s.report(lastWritten, time.Since(reportAt))
			return
		case e, ok := <-s.queue:
			if !ok {
				flush()
				s.report(lastWritten, time.Since(reportAt))
				return
			}
			if bytes+len(e.Payload) > int(s.mainLimit/16) {
				flush()
			}
			batch = append(batch, e)
			bytes += len(e.Payload)
			if len(batch) >= s.batchLimit {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-report.C:
			s.report(lastWritten, time.Since(reportAt))
			lastWritten = s.metrics.written.Load()
			reportAt = time.Now()
		}
	}
}
func (s *Store) report(previous uint64, elapsed time.Duration) {
	if s.opt.Diagnostic == nil {
		return
	}
	m := s.Metrics()
	s.opt.Diagnostic.Info("log_persistence_metrics", "failure_category", m.FailureCategory, "bytes_used", m.Bytes, "records", m.Rows, "written", m.Written, "removed", m.Removed, "failures", m.Failures, "overflow", m.Overflow, "oversized", m.Oversized, "lost", m.Lost, "batches", m.Batches, "flush_ms", m.FlushMS, "cleanup_ms", m.CleanupMS, "writes_per_second", float64(m.Written-previous)/max(elapsed.Seconds(), 0.001))
}
func (s *Store) checkpoint(ctx context.Context) error {
	var busy, frames, done int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("log checkpoint blocked; writes suspended")
	}
	return nil
}

// PhysicalBytes conservatively counts file length AND allocated blocks, including
// SQLite sidecars. It may overcount sparse files; it never discounts their length.
func PhysicalBytes(path string) (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		n := info.Size()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			n = max(n, stat.Blocks*512)
		}
		total += n
	}
	return total, nil
}
func (s *Store) maintain(ctx context.Context, reserve int64) error {
	start := time.Now()
	defer func() { s.metrics.cleanupNS.Store(time.Since(start).Nanoseconds()) }()
	// Do not write another transaction while any prior WAL cannot be truncated.
	if err := s.checkpoint(ctx); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var pages, free int64
		if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
			return err
		}
		if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
			return err
		}
		used := (pages - free) * pageSize
		bytes, err := PhysicalBytes(s.opt.Path)
		if err != nil {
			return err
		}
		s.metrics.bytes.Store(bytes)
		if used+reserve <= s.mainLimit/4*3 && pages*pageSize <= s.mainLimit && bytes < s.opt.MaxBytes/10*9 {
			break
		}
		var n int64
		if used+reserve > s.mainLimit/4*3 {
			remove := min(64, max(1, int(s.metrics.rows.Load()/10)))
			result, err := s.db.ExecContext(ctx, "DELETE FROM logs WHERE id IN (SELECT id FROM logs WHERE id < (SELECT max(id) FROM logs) ORDER BY id LIMIT ?)", remove)
			if err != nil {
				return err
			}
			n, err = result.RowsAffected()
			if err != nil {
				return err
			}
			s.metrics.removed.Add(uint64(n))
			s.metrics.rows.Add(0 - uint64(n))
		}

		if err = s.checkpoint(ctx); err != nil {
			return err
		}
		// Incremental maintenance, never VACUUM/rebuild. Each step is bounded and
		// freed pages left behind are reusable by future inserts.
		if _, err = s.db.ExecContext(ctx, "PRAGMA incremental_vacuum(64)"); err != nil {
			return err
		}
		if err = s.checkpoint(ctx); err != nil {
			return err
		}
		if n == 0 && free == 0 {
			return errors.New("log quota too small for schema and batch")
		}
	}
	var maxPages int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", s.mainLimit/pageSize)).Scan(&maxPages); err != nil {
		return err
	}
	if maxPages > s.mainLimit/pageSize {
		return errors.New("log page quota could not be enforced")
	}
	return nil
}
func (s *Store) write(ctx context.Context, batch []entry) error {
	reserve := int64(0)
	for _, e := range batch {
		reserve += int64(len(e.Payload))*3 + 8*pageSize
	}
	if err := s.maintain(ctx, reserve); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO logs(timestamp_ns,level,event,guild_id,interaction_id,track_id,spotify_track_id,youtube_video_id,collection_id,position,error_category,elapsed_ms,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range batch {
		if _, err = stmt.ExecContext(ctx, e.Timestamp, e.Level, e.Event, e.Guild, e.Interaction, e.Track, e.Spotify, e.Video, e.Collection, e.Position, e.Category, e.Elapsed, string(e.Payload)); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.metrics.written.Add(uint64(len(batch)))
	s.metrics.rows.Add(uint64(len(batch)))
	s.metrics.batches.Add(1)
	if err = s.checkpoint(ctx); err != nil {
		return err
	}
	bytes, err := PhysicalBytes(s.opt.Path)
	if err != nil {
		return err
	}
	s.metrics.bytes.Store(bytes)
	if bytes > s.opt.MaxBytes {
		return errors.New("log physical quota exceeded; writes suspended")
	}
	return nil
}
