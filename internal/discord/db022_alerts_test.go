package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/rest"
	"whiterun/internal/media"
	"whiterun/internal/storage"
)

type capturedCatalogMessage struct {
	Path, Content, Nonce string
	Enforce              bool
}

func TestDB022CatalogAlertHTTPPipeline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "alerts.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	now := time.Now().Truncate(time.Second)
	var mu sync.Mutex
	var messages []capturedCatalogMessage
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bot fixture-token" {
			t.Error("missing real adapter authorization")
		}
		switch {
		case r.Method == "POST":
			var body struct {
				Content, Nonce string
				Enforce        bool `json:"enforce_nonce"`
			}
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Error(e)
			}
			messages = append(messages, capturedCatalogMessage{r.URL.Path, body.Content, body.Nonce, body.Enforce})
			if fail {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"code":0,"message":"temporary fixture failure"}`)
				return
			}
			fmt.Fprint(w, `{"id":"900","channel_id":"30","content":"sent","author":{"id":"99","username":"bot","discriminator":"0"},"timestamp":"2026-10-21T00:00:00Z"}`)
		case r.URL.Path == "/channels/10":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"code":10003,"message":"removed"}`)
		case r.URL.Path == "/channels/20":
			fmt.Fprint(w, `{"id":"20","guild_id":"1","type":0,"permission_overwrites":[{"id":"99","type":1,"deny":"2048","allow":"0"}]}`)
		case r.URL.Path == "/channels/30":
			fmt.Fprint(w, `{"id":"30","guild_id":"2","type":0,"permission_overwrites":[]}`)
		case strings.HasSuffix(r.URL.Path, "/members/99"):
			fmt.Fprint(w, `{"user":{"id":"99","username":"bot","discriminator":"0"},"roles":[]}`)
		case r.URL.Path == "/guilds/1/roles":
			fmt.Fprint(w, `[{"id":"1","permissions":"3072"}]`)
		case r.URL.Path == "/guilds/2/roles":
			fmt.Fprint(w, `[{"id":"2","permissions":"3072"}]`)
		default:
			t.Error("unexpected Discord request", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := rest.NewClient("fixture-token", rest.WithURL(server.URL), rest.WithHTTPClient(server.Client()))
	defer client.Close(ctx)
	notifier := CatalogDiscordNotifier{REST: rest.New(client), UserID: 99}
	monitor := &storage.CatalogAlertMonitor{DB: db, Notifier: notifier}
	for channel, count := range map[string]int{"10": 5, "20": 4, "30": 3} {
		guild := "1"
		if channel == "30" {
			guild = "2"
		}
		for i := range count {
			if err = db.RecordCatalogCommand(ctx, fmt.Sprintf("%s-%d", channel, i), guild, channel, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Duplicate interaction and events outside the rolling window do not count.
	if err = db.RecordCatalogCommand(ctx, "30-0", "2", "30", now); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(-30 * 24 * time.Hour), now.Add(time.Second)} {
		if err = db.RecordCatalogCommand(ctx, at.String(), "2", "30", at); err != nil {
			t.Fatal(err)
		}
	}
	channels, err := db.CatalogChannels(ctx, now)
	if err != nil || len(channels) != 3 || channels[0].Channel != "10" || channels[2].Commands != 3 {
		t.Fatal(channels, err)
	}
	track := func(i int) media.Track {
		return media.Track{SpotifyID: fmt.Sprintf("%022d", i), RequestedSource: "spotify", Title: "Música"}
	}
	if err = db.SaveCatalog(ctx, track(0)); err != nil {
		t.Fatal(err)
	}
	unit, err := db.CatalogUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetCatalogQuota(ctx, unit.Used*10); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 8; i++ {
		if err = db.SaveCatalog(ctx, track(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = monitor.Poll(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatal("alert below 90%")
	}
	if err = db.SaveCatalog(ctx, track(8)); err != nil {
		t.Fatal(err)
	}
	if err = monitor.Poll(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Path != "/channels/30/messages" || !strings.Contains(messages[0].Content, "90%") || !messages[0].Enforce {
		t.Fatal(messages)
	}
	if err = monitor.Poll(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatal("duplicate warning")
	}
	if err = db.SaveCatalog(ctx, track(9)); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveCatalog(ctx, track(10)); err == nil {
		t.Fatal("write above 100% allowed")
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if err = monitor.Poll(ctx, now); err == nil {
		t.Fatal("Discord error lost")
	}
	if len(messages) != 2 || !strings.Contains(messages[1].Content, "100%") || !strings.Contains(messages[1].Content, "blocked") {
		t.Fatal(messages)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	monitor = &storage.CatalogAlertMonitor{DB: db, Notifier: notifier}
	if err = monitor.Poll(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatal("retry ignored persisted backoff")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if err = monitor.Poll(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[1].Nonce != messages[2].Nonce {
		t.Fatal("unstable retry deduplication key", messages)
	}
	if err = monitor.Poll(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatal("confirmed alert replayed")
	}
	for i := 8; i < 10; i++ {
		if err = db.DeleteOrphanTrack(ctx, track(i).SpotifyID); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.SaveCatalog(ctx, track(8)); err != nil {
		t.Fatal(err)
	}
	if err = monitor.Poll(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[3].Nonce == messages[0].Nonce {
		t.Fatal("recovery failed to rearm", messages)
	}
	t.Logf("HTTP requests: warning=1 critical_failed=1 critical_retry=1 rearmed_warning=1; eligible channel=30; stable retry nonce=%s", messages[2].Nonce)
}
