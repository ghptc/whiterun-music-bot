package logstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactionCredentialsPrivateMessagesAndProviderBuffers(t *testing.T) {
	secret := "example-bot-secret-not-to-persist"
	client := "spotify-client-identifier"
	s := openTest(t, Options{Secrets: []string{secret, client}})
	log := logger(s).With("authorization", "Bearer hidden-header", "spotify_client_id", client)
	log.Error("provider failure: "+secret, "error", fmt.Errorf("failed https://user:password@api.example/path?access_token=hidden-query#private-fragment Bearer hidden-bearer token=hidden-token"), "cookies", []string{"hidden-cookie"}, "body", `{"private":"hidden-body"}`, "message_content", "hidden-private-message", "message", "hidden-private-message-2", "provider_payload", map[string]any{"text": "hidden-provider"}, "raw_json", json.RawMessage(`{"text":"hidden-json"}`), "raw_bytes", []byte("hidden-bytes"), "nested", map[string]any{"refresh_token": "hidden-refresh", "safe": "artist", "key": secret}, "jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.secret_signature", "url", "https://api.example/safe?signature=hidden-signature", "webhook", "https://discord.com/api/webhooks/123/hidden-webhook-token")
	log.Error("quoted_credentials", "error", errors.New(`provider response {"access_token":"hidden-json-token","client_secret":"hidden-json-secret"} Authorization: Bearer hidden-auth-header`))
	log.WithGroup("details").Info("grouped", "password", "hidden-password", "safe", "music")
	closeTest(t, s)
	got := records(t, s.opt.Path, Filter{})
	if len(got) != 3 {
		t.Fatal(got)
	}
	raw, _ := json.Marshal(got)
	for _, bad := range []string{secret, client, "hidden-header", "hidden-query", "private-fragment", "hidden-bearer", "hidden-token", "hidden-cookie", "hidden-body", "hidden-private-message", "hidden-provider", "hidden-json", "hidden-bytes", "hidden-refresh", "eyJhbGci", "secret_signature", "hidden-signature", "hidden-webhook-token", "hidden-password", "user:password", "hidden-json-token", "hidden-json-secret", "hidden-auth-header"} {
		if bytes.Contains(raw, []byte(bad)) {
			t.Fatalf("secret leaked: %s: %s", bad, raw)
		}
	}
	if !bytes.Contains(raw, []byte("artist")) || !bytes.Contains(raw, []byte("https://api.example/safe")) {
		t.Fatal("safe diagnostics lost", string(raw))
	}
}
func TestGroupsAttrsOptionalValuesAndLimits(t *testing.T) {
	s := openTest(t, Options{})
	log := logger(s)
	log.With("guild_id", "outer").WithGroup("details").With("stage", "nested").Info("groups", slog.Group("nested", slog.String("safe", "yes")), "zero", 0, "nil", nil, "duration", time.Second, "error", errors.New("problem"))
	log.Info("oversized", "a", strings.Repeat("x", 35000)) // omitted oversized string, not copied in full.
	// Multiple individually bounded strings exceed the complete record budget.
	attrs := []any{}
	for i := 0; i < 16; i++ {
		attrs = append(attrs, fmt.Sprint(i), strings.Repeat("y", 3000))
	}
	log.Info("drop_oversized", attrs...)
	closeTest(t, s)
	if s.Metrics().Oversized != 1 {
		t.Fatal(s.Metrics())
	}
	got := records(t, s.opt.Path, Filter{})
	if len(got) != 2 || got[0].Guild == nil || *got[0].Guild != "outer" || got[0].Event != "groups" {
		t.Fatal(got)
	}
	var payload map[string]any
	if err := json.Unmarshal(got[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	details := payload["details"].(map[string]any)
	if details["safe"] != nil || details["stage"] != "nested" || details["duration"] != "1s" {
		t.Fatal(payload)
	}
	if bytes.Contains(got[1].Payload, []byte(strings.Repeat("x", 100))) {
		t.Fatal("oversized string retained")
	}
}
func TestShutdownDeadlineCancelsActiveWriteAndConcurrentClose(t *testing.T) {
	s := openTest(t, Options{Batch: 1, WriteTimeout: time.Second})
	log := logger(s)
	blocker, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	} // occupies the single connection.
	log.Info("blocked_worker")
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err = s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("shutdown exceeded deadline")
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Once SQLite releases its connection, the canceled worker exits; no goroutine
	// is abandoned to keep writing after shutdown.
	eventually(t, func() bool {
		select {
		case <-s.done:
			return true
		default:
			return false
		}
	})
	log.Info("stdout_still_works_after_close")
	if s.Metrics().Lost < 1 {
		t.Fatal(s.Metrics())
	}
}
func TestReadErrorsAndInvalidOptions(t *testing.T) {
	for _, n := range []int64{-1, 1024} {
		if _, err := Open(Options{Path: filepath.Join(t.TempDir(), "logs.db"), MaxBytes: n}); err == nil {
			t.Fatal(n)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent", "logs.db")
	if _, err := Open(Options{Path: missing}); err == nil {
		t.Fatal("missing directory opened")
	}
	for _, filter := range []Filter{{Last: -1}, {Last: 10001}, {Level: "INVALID"}} {
		if err := Read(context.Background(), "missing.db", filter, io.Discard); err == nil {
			t.Fatal(filter)
		}
	}
	s := openTest(t, Options{})
	if _, err := Open(Options{Path: s.opt.Path}); err == nil {
		t.Fatal("second writer allowed")
	}
	symlink := filepath.Join(t.TempDir(), "link.db")
	if err := os.Symlink(s.opt.Path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Path: symlink}); err == nil {
		t.Fatal("writer followed symlink")
	}
}
func TestLowerQuotaAfterRestartReclaimsPhysicalPages(t *testing.T) {
	s := openTest(t, Options{MaxBytes: 8 << 20})
	path := s.opt.Path
	for i := 0; i < 200; i++ {
		if err := s.write(context.Background(), []entry{event(i, 12000)}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := PhysicalBytes(path)
	closeTest(t, s)
	if before < 1<<20 {
		t.Fatal("fixture did not exceed new quota", before)
	}
	s2 := openTest(t, Options{Path: path, MaxBytes: 1 << 20})
	after, _ := PhysicalBytes(path)
	if after >= 1<<20 || after >= before || s2.Metrics().Removed == 0 {
		t.Fatal(before, after, s2.Metrics())
	}
	got := records(t, path, Filter{Last: 1})
	if len(got) != 1 || got[0].ID != 200 {
		t.Fatal("newest lost on quota reduction", got)
	}
}

type panickingValue struct{}

func (panickingValue) String() string { panic("malformed diagnostic") }
func TestMalformedValueCannotCrashProducer(t *testing.T) {
	s := openTest(t, Options{})
	logger(s).Info("bad_value", "value", panickingValue{})
	logger(s).Info("next_event")
	closeTest(t, s)
	if s.Metrics().FailureCategory != "sanitization" || s.Metrics().Lost != 1 || s.Metrics().Written != 1 {
		t.Fatal(s.Metrics())
	}
}

func TestAggregateSanitizationBudget(t *testing.T) {
	// A shared tree would expand to 32^4 strings if bounds applied independently
	// to each branch. Only a fixed number of nodes may be copied for one event.
	var tree any = strings.Repeat("x", 1000)
	for depth := 0; depth < 4; depth++ {
		children := make([]any, 32)
		for i := range children {
			children[i] = tree
		}
		tree = children
	}
	budget := valueBudget{nodes: 256, bytes: 32 << 10}
	value := newRedactor(nil).value(tree, 0, &budget)
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if budget.nodes < 0 || budget.bytes < 0 || len(raw) > 40<<10 {
		t.Fatal("unbounded traversal", budget, len(raw))
	}
}
