package discord

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"

	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

func marathonF(t *testing.T) *marathonSession {
	m := newMarathon(t)
	w := marathonWant{current: "song001", history: []string{"song001"}}
	m.playSong(1, w)
	w.queue = []string{"song002"}
	m.playSong(2, w)
	m.command("queue", nil, 3, w)
	old := m.g.player
	oldConn := m.g.conn.(*lifecycleConn)
	old.Close(context.Background())
	if m.active.Load() != 0 || !oldConn.closed.Load() {
		t.Fatal("old session retained stream/voice")
	}
	if err := m.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(m.path)
	if err != nil {
		t.Fatal(err)
	}
	m.db = db
	m.store = db
	m.b.store = db
	recovered := player.New(m.b.ctx, m.b.log.With("guild_id", "1"), m.lifecycleFixture.play)
	if err := recovered.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	m.g.player = recovered
	m.g.conn = nil
	m.g.channel = 0
	m.b.bindAnnouncements(m.g)
	synctest.Wait()
	if s := recovered.State(); s.Current != nil || !slices.Equal(marathonQueue(s), []string{"song001", "song002"}) || m.active.Load() != 0 {
		t.Fatal("recovery started without voice or reordered metadata")
	}
	m.command("summon", nil, 3, w)
	m.command("resume", nil, 3, w)
	w.queue = append(w.queue, "song003")
	m.playSong(3, w)
	m.command("history", nil, 3, w)
	ledger, err := db.PlaybackHistory(context.Background(), "1", 10)
	if err != nil || len(ledger) != 1 || ledger[0].Title != "song001" {
		t.Fatal("restart lost append-only ledger", ledger, err)
	}
	w.current = "song002"
	w.queue = []string{"song003"}
	w.history = append(w.history, "song002")
	m.eof(w)
	w.current = ""
	w.queue = nil
	w.loop = "off"
	m.command("stop", nil, 3, w)
	conn := m.g.conn.(*lifecycleConn)
	m.command("leave", nil, 3, w)
	if m.g.conn != nil || !conn.closed.Load() {
		t.Fatal("leave retained voice")
	}
	m.finishJourney(w)
	return m
}
func marathonG(t *testing.T) *marathonSession {
	m := newMarathon(t)
	w := marathonWant{current: "song001", history: []string{"song001"}}
	m.playSong(1, w)
	w.queue = []string{"song002"}
	m.playSong(2, w)
	// Provider failures retain the affected occurrence and accepted successors.
	m.finish <- &player.StreamError{Category: "youtube_auth_challenge", Err: errors.New("controlled provider challenge")}
	synctest.Wait()
	w.current = ""
	w.queue = []string{"song001", "song002"}
	m.check(w, "provider challenge retains logical position")
	if !m.g.player.State().ProviderBlocked || m.logs.has("track_unavailable", "reason", "youtube_auth_challenge") {
		t.Fatal("provider rejection consumed queue")
	}
	w.current = "song001"
	w.queue = []string{"song002"}
	m.command("resume", nil, 3, w)
	m.finish <- nil
	synctest.Wait()
	w.current = "song002"
	w.queue = nil
	w.history = append(w.history, "song002")
	m.check(w, "provider recovery resumes once then advances")
	healthy := m.b.resolver.Search
	m.b.resolver.Search = func(context.Context, string) (media.Track, error) {
		return media.Track{}, errors.New("controlled provider outage")
	}
	m.metadata([]media.Track{marathonTrack(4, "song", 1)})
	m.command("play", marathonPlayOptions(4, false), 3, w)
	if !strings.Contains(m.r.response, media.ErrSpotifyMatch.Error()) {
		t.Fatal("provider resolution failure presented as success", m.r.response)
	}
	m.b.resolver.Search = healthy
	w.current = ""
	m.command("skip", nil, 3, w)
	w.current = "song003"
	w.history = append(w.history, "song003")
	m.playSong(3, w)
	w.current = ""
	w.loop = "off"
	m.command("stop", nil, 3, w)
	m.finishJourney(w)
	return m
}
func marathonH(t *testing.T) *marathonSession {
	m := newMarathon(t)
	w := marathonWant{current: "song001", history: []string{"song001"}}
	m.playSong(1, w)
	original := marathonTrack(1, "song", 1)
	key := media.MappingKey(original, nil)
	candidate := media.Candidate{ID: "abcdefghijk", Title: original.Title, Duration: 60}
	if err := m.db.SaveMapping(context.Background(), key, candidate); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary, pids := filepath.Join(dir, "yt-dlp"), filepath.Join(dir, "pids")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$$\" >> '%s'\nprintf '%%s\\n' '{\"id\":\"ZYX98765432\",\"title\":\"manual\",\"duration\":17,\"thumbnail\":\"https://i.ytimg.com/vi/ZYX98765432/default.jpg\"}'\n", strings.ReplaceAll(pids, "'", "'\\''"))
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	m.b.resolver.YouTube = media.Resolver{Binary: binary, Match: &original, Mappings: m.db}
	forced := media.Track{Title: "manual", VideoID: "ZYX98765432", URL: "https://www.youtube.com/watch?v=ZYX98765432", ForcedYouTube: true, RequestedSource: "youtube", Position: 1, Total: 1}
	m.identities[forced.Title] = forced
	w.queue = []string{"manual"}
	m.command("play", []map[string]any{marathonOption("query", 3, "https://youtu.be/ZYX98765432?list=ignored&index=2"), marathonOption("force", 5, true)}, 3, w)
	checkMapping := func() {
		t.Helper()
		saved, hit, err := m.db.LoadMapping(context.Background(), key)
		if err != nil || !hit || !reflect.DeepEqual(saved, candidate) {
			t.Fatal("Force Play contaminated Spotify mapping", saved, err)
		}
	}
	checkMapping()
	m.command("queue", nil, 3, w)
	w.current = "manual"
	w.queue = nil
	w.history = append(w.history, "manual")
	m.command("skip", nil, 3, w)
	if tr := m.g.player.State().Current; tr == nil || tr.SpotifyID != "" || !tr.ForcedYouTube || tr.VideoID != forced.VideoID || tr.URL != forced.URL || tr.RequestedBy != "3" {
		t.Fatal("forced playback changed video/identity", tr)
	}
	w.current = "song001"
	w.queue = []string{"manual"}
	w.history = []string{"song001"}
	m.command("previous", nil, 3, w)
	w.queue = append(w.queue, "song002")
	m.playSong(2, w)
	w.current = "manual"
	w.queue = []string{"song002"}
	w.history = append(w.history, "manual")
	m.eof(w)
	w.current = "song002"
	w.queue = nil
	w.history = append(w.history, "song002")
	m.eof(w)
	if tr := m.g.player.State().Current; tr.ForcedYouTube || tr.RequestedSource != "spotify" || tr.MatchStatus != media.MatchConfirmed {
		t.Fatal("normal Spotify play inherited forced selection", tr)
	}
	checkMapping()
	w.current = ""
	w.loop = "off"
	m.command("stop", nil, 3, w)
	m.finishJourney(w)
	raw, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(text)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatal("metadata subprocess not reaped", pid, err)
		}
	}
	return m
}
