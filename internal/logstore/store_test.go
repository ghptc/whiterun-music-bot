package logstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

func openTest(t *testing.T, opt Options) *Store {
	t.Helper()
	if opt.Path == "" {
		opt.Path = filepath.Join(t.TempDir(), "bard-logs.db")
	}
	if opt.MaxBytes == 0 {
		opt.MaxBytes = 4 << 20
	}
	if opt.FlushInterval == 0 {
		opt.FlushInterval = 10 * time.Millisecond
	}
	s, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}
func logger(s *Store) *slog.Logger {
	return slog.New(NewHandler(slog.NewJSONHandler(io.Discard, nil), s))
}
func closeTest(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
func records(t *testing.T, path string, f Filter) []Record {
	t.Helper()
	var out bytes.Buffer
	if err := Read(context.Background(), path, f, &out); err != nil {
		t.Fatal(err)
	}
	result := []Record{}
	decoder := json.NewDecoder(&out)
	for decoder.More() {
		var r Record
		if err := decoder.Decode(&r); err != nil {
			t.Fatal(err)
		}
		result = append(result, r)
	}
	return result
}
func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func event(i, size int) entry {
	return entry{Timestamp: time.Unix(int64(i), 0).UnixNano(), Level: "INFO", Event: "retention", Payload: []byte(fmt.Sprintf(`{"sequence":%d,"text":%q}`, i, strings.Repeat("x", size)))}
}

func TestPersistenceRestartFieldsAndQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	s := openTest(t, Options{Path: path})
	log := logger(s).With("guild_id", "g1", "interaction_id", "i1")
	log.Info("Spotify metadata diagnostic", "stage", "spotify_metadata", "spotify_id", "3hWu4Z5IkREyWNT4zuuXM5", "video_id", "mhXmqO7hCfA", "collection_id", 7, "position", 4, "elapsed_us", 1234, "category", "provider", "title", "Auto Pilot 🎵")
	logger(s).Error("other", "guild_id", "g2", "track_id", "track2")
	logger(s).Info("optional")
	closeTest(t, s)
	s2 := openTest(t, Options{Path: path})
	logger(s2).Info("restarted")
	closeTest(t, s2)
	all := records(t, path, Filter{})
	if len(all) != 4 || all[0].Spotify == nil || *all[0].Spotify != "3hWu4Z5IkREyWNT4zuuXM5" || all[0].Collection == nil || *all[0].Collection != "7" || all[0].Position == nil || *all[0].Position != 4 || all[0].Elapsed == nil || *all[0].Elapsed != 1.234 || all[0].Timestamp.Location() != time.UTC {
		t.Fatalf("%+v", all)
	}
	if all[2].Guild != nil || all[2].Position != nil || all[2].Elapsed != nil {
		t.Fatal("optional fields synthesized")
	}
	for i, r := range all {
		if !json.Valid(r.Payload) || !bytes.Contains(all[0].Payload, []byte("Auto Pilot 🎵")) || (i > 0 && r.ID <= all[i-1].ID) {
			t.Fatal(r)
		}
	}
	for _, f := range []Filter{{Event: "spotify_metadata"}, {Guild: "g1"}, {Interaction: "i1"}, {Track: "3hWu4Z5IkREyWNT4zuuXM5"}, {Track: "mhXmqO7hCfA"}, {Track: "track2"}, {Level: "error"}, {Last: 1}, {Since: all[3].Timestamp}} {
		if got := records(t, path, f); len(got) != 1 {
			t.Fatal(f, got)
		}
	}
	if got := records(t, path, Filter{Guild: "g1", Track: "track2"}); len(got) != 0 {
		t.Fatal("guild isolation", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}
func TestTemporalOrderingAndStableIDs(t *testing.T) {
	s := openTest(t, Options{})
	base := time.Now()
	h := NewHandler(slog.NewJSONHandler(io.Discard, nil), s)
	for _, offset := range []int{3, 1, 2, 2} {
		r := slog.NewRecord(base.Add(time.Duration(offset)*time.Second), slog.LevelInfo, "time", 0)
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	closeTest(t, s)
	got := records(t, s.opt.Path, Filter{Last: 3})
	if len(got) != 3 {
		t.Fatal(got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Fatal(got)
		}
	}
	if got[0].ID != 3 || got[1].ID != 4 || got[2].ID != 1 {
		t.Fatal("time order and tie break", got)
	}
}
func TestConcurrentWritesBatchAndFinalFlush(t *testing.T) {
	s := openTest(t, Options{Buffer: 4096, FlushInterval: time.Hour})
	log := logger(s)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Go(func() {
			for i := 0; i < 100; i++ {
				log.Info("concurrent", "position", i)
			}
		})
	}
	wg.Wait()
	closeTest(t, s)
	m := s.Metrics()
	if m.Written != 800 || m.Rows != 800 || m.Overflow != 0 || m.Batches >= 800 || m.Batches < 2 {
		t.Fatal(m)
	}
	if len(records(t, s.opt.Path, Filter{Last: 1000})) != 800 {
		t.Fatal(m)
	}
}
func TestOverflowAndClosedSinkNeverBlockConsole(t *testing.T) {
	var out bytes.Buffer
	s := &Store{queue: make(chan entry, 2), recordLimit: maxRecordBytes, scrub: newRedactor(nil)}
	log := slog.New(NewHandler(slog.NewJSONHandler(&out, nil), s))
	start := time.Now()
	for i := 0; i < 1000; i++ {
		log.Info("buffer_full", "n", i)
	}
	if time.Since(start) > time.Second || len(s.queue) != 2 || s.Metrics().Overflow != 998 || bytes.Count(out.Bytes(), []byte("buffer_full")) != 1000 {
		t.Fatal(s.Metrics())
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	log.Info("after_close")
	if s.Metrics().Lost != 1 || !bytes.Contains(out.Bytes(), []byte("after_close")) {
		t.Fatal(s.Metrics())
	}
}
func TestUnavailableAndFullDiskRecoverWithoutConsoleLoss(t *testing.T) {
	for _, kind := range []string{"unavailable", "full"} {
		t.Run(kind, func(t *testing.T) {
			var out bytes.Buffer
			s := openTest(t, Options{Batch: 1})
			log := slog.New(NewHandler(slog.NewJSONHandler(&out, nil), s))
			if kind == "unavailable" {
				if err := s.db.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				// Force real SQLITE_FULL inside the production batch transaction. It is
				// SQLite's page allocator, not an invented error string or mocked DB.
				_, err := s.db.Exec(`CREATE TABLE pressure(data BLOB); CREATE TRIGGER disk_full BEFORE INSERT ON logs BEGIN INSERT INTO pressure VALUES(zeroblob(8388608)); END;`)
				if err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			log.Error("still_on_console")
			if time.Since(start) > 100*time.Millisecond {
				t.Fatal("logger waited for SQLite")
			}
			eventually(t, func() bool { return s.Metrics().Failures > 0 })
			if s.Metrics().Lost != 1 || !bytes.Contains(out.Bytes(), []byte("still_on_console")) {
				t.Fatal(s.Metrics())
			}
			if kind == "full" {
				// Independently establish that this fixture generates SQLITE_FULL (13).
				_, err := s.db.Exec("INSERT INTO logs(timestamp_ns,level,event,payload) VALUES(1,'INFO','full','{}')")
				sqlErr, ok := err.(*sqlite.Error)
				if !ok || sqlErr.Code()&255 != 13 {
					t.Fatalf("not SQLITE_FULL: %T %v", err, err)
				}
				if _, err = s.db.Exec("DROP TRIGGER disk_full; DROP TABLE pressure"); err != nil {
					t.Fatal(err)
				}
				log.Info("recovered")
				eventually(t, func() bool { return s.Metrics().Written == 1 })
			}
		})
	}
}
func TestWALReaderStallsPersistenceNotProducer(t *testing.T) {
	s := openTest(t, Options{Batch: 1, WriteTimeout: 300 * time.Millisecond})
	log := logger(s)
	log.Info("first")
	eventually(t, func() bool { return s.Metrics().Written == 1 })
	reader, err := sql.Open("sqlite", dsn(s.opt.Path, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRow("SELECT count(*) FROM logs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	log.Info("committed_before_checkpoint")
	eventually(t, func() bool { return s.Metrics().Failures >= 1 })
	before, err := PhysicalBytes(s.opt.Path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 50; i++ {
		log.Info("reader_held")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("producer blocked")
	}
	eventually(t, func() bool { return s.Metrics().Failures >= 2 })
	after, err := PhysicalBytes(s.opt.Path)
	if err != nil || after > before || after > s.opt.MaxBytes {
		t.Fatal(before, after, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	closeTest(t, s)
	// Committed events are not reported as lost merely because checkpoint failed.
	if s.Metrics().Written+s.Metrics().Lost+s.Metrics().Overflow != 52 {
		t.Fatal(s.Metrics())
	}
}
func TestRetentionContinuousFIFOReuseAndReducedQuota(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	s := openTest(t, Options{Path: path, MaxBytes: 1 << 20})
	ctx := context.Background()
	maximum := int64(0)
	for i := 0; i < 240; i++ {
		if err := s.write(ctx, []entry{event(i, 12000)}); err != nil {
			t.Fatal(i, err)
		}
		n, err := PhysicalBytes(path)
		if err != nil || n > s.opt.MaxBytes {
			t.Fatal(n, err)
		}
		maximum = max(maximum, n)
	}
	m := s.Metrics()
	if m.Removed == 0 || m.Rows >= 240 || m.Rows < 2 || maximum >= s.opt.MaxBytes {
		t.Fatal(m, maximum)
	}
	all := records(t, path, Filter{Last: 1000})
	if all[len(all)-1].ID != 240 || all[0].ID <= 1 {
		t.Fatal("newest lost or oldest retained", all[0].ID, all[len(all)-1].ID)
	}
	var free, pages int
	if err := s.db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if int64(pages)*pageSize > s.mainLimit {
		t.Fatal(pages)
	}
	closeTest(t, s)
	s2 := openTest(t, Options{Path: path, MaxBytes: 1 << 20})
	if err := s2.write(ctx, []entry{event(241, 12000)}); err != nil {
		t.Fatal(err)
	}
	closeTest(t, s2)
	got := records(t, path, Filter{Last: 1})
	if got[0].ID != 241 {
		t.Fatal(got)
	}
}
func TestCleanupFailureDoesNotGrowWALAndCanRecover(t *testing.T) {
	s := openTest(t, Options{MaxBytes: 1 << 20})
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := s.write(ctx, []entry{event(i, 12000)}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.db.Exec(`CREATE TRIGGER refuse_cleanup BEFORE DELETE ON logs BEGIN SELECT RAISE(ABORT,'cleanup fault'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for i := 10; i < 100; i++ {
		if err = s.write(ctx, []entry{event(i, 12000)}); err != nil {
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("cleanup fault not exercised")
	}
	n, _ := PhysicalBytes(s.opt.Path)
	if n > s.opt.MaxBytes {
		t.Fatal(n)
	}
	if _, err = s.db.Exec("DROP TRIGGER refuse_cleanup"); err != nil {
		t.Fatal(err)
	}
	if err = s.write(ctx, []entry{event(101, 12000)}); err != nil {
		t.Fatal(err)
	}
	if s.Metrics().Removed == 0 {
		t.Fatal("retention did not recover")
	}
}
func TestDefault500MiBRepresentative(t *testing.T) {
	s := openTest(t, Options{MaxBytes: DefaultMaxBytes})
	if s.opt.MaxBytes != 524288000 || s.mainLimit > DefaultMaxBytes*2/5 {
		t.Fatal(s.opt)
	}
	var maximum int64
	if err := s.db.QueryRow("PRAGMA max_page_count").Scan(&maximum); err != nil {
		t.Fatal(err)
	}
	if maximum*pageSize > s.mainLimit {
		t.Fatal(maximum)
	}
	for i := 0; i < 512; i += 64 {
		batch := []entry{}
		for j := i; j < i+64; j++ {
			batch = append(batch, event(j, 12000))
		}
		if err := s.write(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
	if s.Metrics().Rows != 512 || s.Metrics().Removed != 0 || s.Metrics().Bytes > DefaultMaxBytes {
		t.Fatal(s.Metrics())
	}
}
func TestDefault500MiBSaturation(t *testing.T) {
	if os.Getenv("BARD_LOG_FULL_QUOTA_TEST") != "1" {
		t.Skip("opt-in real 500 MiB quota saturation; see persistent-logging.md")
	}
	s := openTest(t, Options{MaxBytes: DefaultMaxBytes})
	for i := 0; i < 12000; i += 64 {
		batch := []entry{}
		for j := i; j < min(i+64, 12000); j++ {
			batch = append(batch, event(j, 14000))
		}
		if err := s.write(context.Background(), batch); err != nil {
			t.Fatal(i, err)
		}
	}
	if s.Metrics().Removed == 0 || s.Metrics().Bytes > DefaultMaxBytes {
		t.Fatal(s.Metrics())
	}
	all := records(t, s.opt.Path, Filter{Last: 1})
	if all[0].ID != 12000 {
		t.Fatal(all)
	}
	t.Logf("real default-quota metrics: %+v", s.Metrics())
}

func TestRefuseOperationalForeignCorruptAndFutureDB(t *testing.T) {
	dir := t.TempDir()
	op := filepath.Join(dir, "operational.db")
	db, err := sql.Open("sqlite", op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE important(value TEXT); INSERT INTO important VALUES('music')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(op)
	if err != nil {
		t.Fatal(err)
	}
	for _, opt := range []Options{{Path: op, OperationalPath: op}, {Path: op}} {
		if _, err := Open(opt); err == nil {
			t.Fatal("foreign DB opened")
		}
	}
	alias := filepath.Join(dir, "alias.db")
	if err = os.Link(op, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Path: alias, OperationalPath: op}); err == nil {
		t.Fatal("hardlink accepted")
	}
	after, _ := os.ReadFile(op)
	if !bytes.Equal(before, after) {
		t.Fatal("operational DB modified")
	}
	db, err = sql.Open("sqlite", op)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var check string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatal(check, err)
	}
	corrupt := filepath.Join(dir, "corrupt.db")
	raw := []byte(strings.Repeat("corrupt log bytes", 100))
	if err = os.WriteFile(corrupt, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(Options{Path: corrupt}); err == nil {
		t.Fatal("corruption accepted")
	}
	got, _ := os.ReadFile(corrupt)
	if !bytes.Equal(got, raw) {
		t.Fatal("corruption overwritten")
	}
	// An operator can choose a fresh path while preserving corrupt evidence.
	fresh := openTest(t, Options{Path: filepath.Join(dir, "fresh.db")})
	closeTest(t, fresh)
	s := openTest(t, Options{})
	path := s.opt.Path
	closeTest(t, s)
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db2.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	db2.Close()
	if _, err = Open(Options{Path: path}); err == nil {
		t.Fatal("newer schema accepted")
	}
}
func TestReadOnlyIndexesAndTimeout(t *testing.T) {
	s := openTest(t, Options{})
	logger(s).Info("read", "guild_id", "g")
	closeTest(t, s)
	before, _ := os.ReadFile(s.opt.Path)
	if len(records(t, s.opt.Path, Filter{Guild: "g"})) != 1 {
		t.Fatal("query failed")
	}
	after, _ := os.ReadFile(s.opt.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("read modified main DB")
	}
	db, err := sql.Open("sqlite", dsn(s.opt.Path, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("DELETE FROM logs"); err == nil {
		t.Fatal("reader wrote")
	}
	for _, column := range []string{"guild_id", "event", "spotify_track_id", "youtube_video_id", "track_id", "level"} {
		rows, err := db.Query("EXPLAIN QUERY PLAN SELECT id FROM logs WHERE "+column+"=? ORDER BY id LIMIT 100", "g")
		if err != nil {
			t.Fatal(err)
		}
		plan := ""
		for rows.Next() {
			var a, b, c int
			var detail string
			if err = rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail
		}
		rows.Close()
		if !strings.Contains(plan, "INDEX") {
			t.Fatal(column, plan)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Read(ctx, s.opt.Path, Filter{}, io.Discard); err == nil {
		t.Fatal("cancelled query ran")
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if err := Read(context.Background(), missing, Filter{}, io.Discard); err == nil {
		t.Fatal("created missing DB")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// A separate OS process models a container that exits without flushing/closing.
func TestCrashRestartWAL(t *testing.T) {
	if path := os.Getenv("BARD_LOG_CRASH_HELPER"); path != "" {
		s, err := Open(Options{Path: path, MaxBytes: 4 << 20})
		if err != nil {
			os.Exit(2)
		}
		if err = s.write(context.Background(), []entry{event(1, 1000)}); err != nil {
			os.Exit(3)
		}
		if _, err = s.db.Exec("INSERT INTO logs(timestamp_ns,level,event,payload) VALUES(2,'ERROR','before_crash','{}')"); err != nil {
			os.Exit(4)
		}
		os.Exit(0) // deliberately bypass cleanup: committed WAL must survive.
	}
	path := filepath.Join(t.TempDir(), "crash.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRestartWAL$")
	cmd.Env = append(os.Environ(), "BARD_LOG_CRASH_HELPER="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatal("no uncheckpointed WAL", info, err)
	}
	s := openTest(t, Options{Path: path})
	closeTest(t, s)
	if got := records(t, path, Filter{}); len(got) != 2 {
		t.Fatal(got)
	}
}
