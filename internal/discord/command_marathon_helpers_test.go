package discord

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

// Only extends the QA-013 fixture: the real command dispatcher, player,
// transport adapter, SQLite, and frame/cancellation gates are unchanged.
type marathonLog struct {
	sync.Mutex
	bytes.Buffer
}

func (l *marathonLog) Write(p []byte) (int, error) {
	l.Lock()
	defer l.Unlock()
	return l.Buffer.Write(p)
}
func (l *marathonLog) records() []map[string]any {
	l.Lock()
	raw := append([]byte{}, l.Bytes()...)
	l.Unlock()
	var rows []map[string]any
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) > 0 {
			var row map[string]any
			if err := json.Unmarshal(line, &row); err != nil {
				panic(err)
			}
			rows = append(rows, row)
		}
	}
	return rows
}
func (l *marathonLog) has(event, key string, value any) bool {
	for _, r := range l.records() {
		name := r["msg"]
		if stage, ok := r["stage"]; ok {
			name = stage
		}
		if name == event && (key == "" || fmt.Sprint(r[key]) == fmt.Sprint(value)) {
			return true
		}
	}
	return false
}

type marathonWant struct {
	current        string
	queue, history []string
	paused         bool
	loop           string
	autoplay       bool
}
type marathonSession struct {
	*lifecycleFixture
	db         *storage.DB
	path       string
	logs       *marathonLog
	identities map[string]media.Track
	seen       map[string]bool
}

func newMarathon(t *testing.T) *marathonSession {
	path := filepath.Join(t.TempDir(), "journey.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	logs := &marathonLog{}
	m := &marathonSession{db: db, path: path, logs: logs, identities: map[string]media.Track{}, seen: map[string]bool{}}
	t.Cleanup(func() { _ = m.db.Close() })
	m.lifecycleFixture = newLifecycleFixtureLog(t, "idle", db, slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	m.g.announcementChannel.Store(9)
	return m
}
func marathonTrack(n int, prefix string, total int) media.Track {
	position := 1
	if total > 1 {
		position = n
	}
	id := n
	if prefix == "p" {
		id += 1000
	}
	return media.Track{Title: fmt.Sprintf("%s%03d", prefix, n), SpotifyID: fmt.Sprintf("%022d", id), RequestedSource: "spotify", Position: position, Total: total, Duration: time.Minute, SpotifyArtworkURL: "https://i.scdn.co/image/fixture"}
}
func (m *marathonSession) metadata(tracks []media.Track) {
	for _, tr := range tracks {
		m.identities[tr.Title] = tr
	}
	m.b.resolver.Spotify = hardeningSpotify{collection: media.Collection{Tracks: tracks}}
}
func marathonOption(name string, typ int, value any) map[string]any {
	return map[string]any{"name": name, "type": typ, "value": value}
}
func marathonPlayOptions(n int, playlist bool) []map[string]any {
	kind := "track"
	if playlist {
		kind = "playlist"
	}
	return []map[string]any{marathonOption("query", 3, "https://open.spotify.com/"+kind+"/"+fmt.Sprintf("%022d", n))}
}
func marathonNames(tracks []media.Track) []string { return lifecycleTitles(tracks) }
func marathonQueue(s player.State) []string {
	var out []string
	for _, e := range s.Entries {
		out = append(out, e.Track.Title)
	}
	return out
}
func (m *marathonSession) check(w marathonWant, stage string) {
	m.t.Helper()
	s := m.g.player.State()
	current := ""
	if s.Current != nil {
		current = s.Current.Title
	}
	if current != w.current || !slices.Equal(marathonQueue(s), w.queue) || !slices.Equal(marathonNames(m.g.player.History()), w.history) || s.Paused != w.paused || s.Loop != w.loop || s.Autoplay != w.autoplay || s.CurrentStarted != (w.current != "") || s.PersistenceError != "" {
		m.t.Fatalf("%s: current=%q queue=%v history=%v phase=%s paused=%v loop=%q want=%+v", stage, current, marathonQueue(s), marathonNames(m.g.player.History()), s.PlaybackState, s.Paused, s.Loop, w)
	}
	active := int64(0)
	if current != "" {
		active = 1
	}
	if m.active.Load() != active || m.overlap.Load() {
		m.t.Fatal(stage, "stream resources overlap or remain active")
	}
	tracks := append([]media.Track{}, m.g.player.History()...)
	for _, e := range s.Entries {
		tracks = append(tracks, e.Track)
	}
	if s.Current != nil {
		tracks = append(tracks, *s.Current)
	}
	for _, tr := range tracks {
		want, ok := m.identities[tr.Title]
		if !ok || tr.SpotifyID != want.SpotifyID || tr.Position != want.Position || tr.Total != want.Total || tr.ForcedYouTube != want.ForcedYouTube || tr.SpotifyArtworkURL != want.SpotifyArtworkURL {
			m.t.Fatal(stage, "recording identity changed", tr)
		}
		if tr.ForcedYouTube && (tr.VideoID != want.VideoID || tr.URL != want.URL || tr.RequestedSource != "youtube") {
			m.t.Fatal("forced locator changed", tr)
		}
	}
	m.checkPersisted(s, stage)
}
func (m *marathonSession) checkPersisted(s player.State, stage string) {
	m.t.Helper()
	want := player.DurableState{Loop: s.Loop, Autoplay: s.Autoplay, CurrentStarted: s.CurrentStarted, Current: s.Current}
	for _, e := range s.Entries {
		want.Queue = append(want.Queue, e.Track)
	}
	for _, tr := range m.g.player.History() {
		want.History = append(want.History, tr)
	}
	saved, err := m.db.LoadGuild(context.Background(), "1")
	if err != nil {
		m.t.Fatal(err)
	}
	// Confirmed audio progress is independent of the metadata snapshot.
	// Its own crash/checkpoint tests verify the saved offset.
	if saved.Current != nil {
		saved.Current.ResumeOffset = want.Current.ResumeOffset
	}
	// Timing is intentionally absent from the durable contract.
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(saved)
	if !bytes.Equal(a, b) {
		m.t.Fatalf("%s: SQLite differs from accepted state", stage)
	}
}
func (m *marathonSession) command(name string, opts []map[string]any, user snowflake.ID, w marathonWant) marathonWant {
	m.t.Helper()
	e := m.interactionFor(name, 1, user)
	if opts != nil {
		data := e.Data.(d.SlashCommandInteractionData)
		data.Options = map[string]d.SlashCommandOption{}
		raw, _ := json.Marshal(opts)
		var parsed []d.SlashCommandOption
		if err := json.Unmarshal(raw, &parsed); err != nil {
			m.t.Fatal(err)
		}
		for _, o := range parsed {
			data.Options[o.Name] = o
		}
		e.Data = data
	}
	before := m.g.player.State()
	logStart := len(m.logs.records())
	hadVoice := m.g.conn != nil
	generation := m.g.player.Ticket()
	m.r.mu.Lock()
	m.r.update = d.MessageUpdate{}
	m.r.response = ""
	m.r.mu.Unlock()
	ack := 0
	e.Respond = func(typ d.InteractionResponseType, _ d.InteractionResponseData, _ ...rest.RequestOpt) error {
		ack++
		expected := d.InteractionResponseTypeDeferredCreateMessage
		if name == "play" {
			expected = d.InteractionResponseTypeCreateMessage
		}
		if typ != expected || !reflect.DeepEqual(before, m.g.player.State()) {
			m.t.Fatal(name, "invalid ACK or mutation before acknowledgement")
		}
		return nil
	}
	beforeCurrent := ""
	if before.Current != nil {
		beforeCurrent = before.Current.Title
	}
	m.t.Logf("step=%d command=%s user=%s options=%v before=%s upcoming=%d", m.serial, name, user, opts, beforeCurrent, len(before.Entries))
	m.b.command(e)
	synctest.Wait()
	if ack != 1 {
		m.t.Fatal(name, "ACK count", ack)
	}
	if name == "shuffle" {
		old, next := marathonQueue(before), marathonQueue(m.g.player.State())
		a, b := slices.Clone(old), slices.Clone(next)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			m.t.Fatal("shuffle lost/duplicated a slot")
		}
		w.queue = next
		m.t.Logf("accepted shuffle permutation=%v", next)
	}
	if name == "queue" || name == "now" || name == "history" || name == "help" || name == "lyrics" || name == "seek" {
		if !reflect.DeepEqual(before, m.g.player.State()) || generation != m.g.player.Ticket() {
			m.t.Fatal(name, "query/rejected seek mutated playback")
		}
	}
	m.check(w, name)

	expectedGeneration := generation
	if name == "stop" || name == "clear" || name == "leave" || ((name == "summon" || name == "play") && !hadVoice) {
		expectedGeneration++
	}
	if m.g.player.Ticket() != expectedGeneration {
		m.t.Fatal(name, "unexpected generation change")
	}
	// Inspect only this command's events, not a similarly named event earlier
	// in the journey. Existing logging intentionally coalesces scheduler wakeups.
	recent := m.logs.records()[logStart:]
	action := ""
	switch name {
	case "move", "remove", "shuffle", "jump", "clear", "stop":
		action = name
	}
	if action != "" {
		found := false
		for _, row := range recent {
			if row["msg"] == "queue_mutation" && row["action"] == action {
				found = true
			}
		}
		if !found {
			m.t.Fatal(name, "mutation event missing")
		}
	}
	if before.Paused != w.paused {
		found := false
		for _, row := range recent {
			if row["msg"] == "playback_pause_changed" && row["paused"] == w.paused {
				found = true
			}
		}
		if (name == "pause" || name == "resume") && !found {
			m.t.Fatal(name, "pause transition event missing")
		}
	}
	m.r.mu.Lock()
	response := m.r.update.Content != nil || m.r.update.Embeds != nil || m.r.update.Components != nil
	m.r.mu.Unlock()
	if !response {
		m.t.Fatal(name, "missing Discord result")
	}
	if !m.logs.has("command_received", "interaction_id", e.ID()) || !m.logs.has("command_ack_complete", "interaction_id", e.ID()) || !m.logs.has("command_total", "interaction_id", e.ID()) {
		m.t.Fatal(name, "missing correlated lifecycle events")
	}
	m.seen[name] = true
	return w
}
func (m *marathonSession) playSong(n int, w marathonWant) {
	m.t.Helper()
	m.metadata([]media.Track{marathonTrack(n, "song", 1)})
	m.command("play", marathonPlayOptions(n, false), 3, w)
}
func (m *marathonSession) eof(w marathonWant) {
	m.t.Helper()
	m.finish <- nil
	synctest.Wait()
	m.check(w, "EOF")
	if !m.logs.has("track_playback_end", "category", "eof") {
		m.t.Fatal("EOF event missing")
	}
}
func (m *marathonSession) finishJourney(w marathonWant) {
	m.t.Helper()
	m.check(w, "journey end")
	if w.current != "" || len(w.queue) != 0 {
		m.t.Fatal("journey did not explicitly stop/leave")
	}
	m.g.player.Close(context.Background())
	m.b.cancel()
	m.b.workers.Wait()
	synctest.Wait()
	if len(m.g.player.State().Resolving) != 0 || m.active.Load() != 0 || m.overlap.Load() {
		m.t.Fatal("owned workers/resources retained")
	}
	sqlDB, err := sql.Open("sqlite", m.path)
	if err != nil {
		m.t.Fatal(err)
	}
	defer sqlDB.Close()
	var result string
	if err := sqlDB.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		m.t.Fatal("SQLite integrity", result, err)
	}
	if err := m.db.Close(); err != nil {
		m.t.Fatal(err)
	}
}
