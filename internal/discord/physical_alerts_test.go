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

func TestDB022PhysicalQuotaDiscordHTTP(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	path := filepath.Join(t.TempDir(), "physical-alerts.db")
	db, err := storage.OpenWithPhysicalQuota(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	if err = db.RecordCatalogCommand(ctx, "valid-command", "1", "30", now); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var contents, nonces []string
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			var body struct {
				Content, Nonce string
				Enforce        bool `json:"enforce_nonce"`
			}
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Error(e)
			}
			if !body.Enforce {
				t.Error("dedup missing")
			}
			contents = append(contents, body.Content)
			nonces = append(nonces, body.Nonce)
			if fail {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"code":0,"message":"injected delivery failure"}`)
				return
			}
			fmt.Fprint(w, `{"id":"900","channel_id":"30","timestamp":"2026-10-21T00:00:00Z","author":{"id":"99","username":"bot"}}`)
			return
		}
		switch r.URL.Path {
		case "/channels/30":
			fmt.Fprint(w, `{"id":"30","guild_id":"1","type":0,"permission_overwrites":[]}`)
		case "/guilds/1/members/99":
			fmt.Fprint(w, `{"user":{"id":"99","username":"bot"},"roles":[]}`)
		case "/guilds/1/roles":
			fmt.Fprint(w, `[{"id":"1","permissions":"3072"}]`)
		default:
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := rest.NewClient("fixture-token", rest.WithURL(server.URL), rest.WithHTTPClient(server.Client()))
	defer client.Close(ctx)
	notifier := CatalogDiscordNotifier{REST: rest.New(client), UserID: 99}
	monitor := storage.CatalogAlertMonitor{DB: db, Notifier: notifier}
	if err = monitor.Poll(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(contents) != 0 {
		t.Fatal("warning below 90%")
	}
	warning := false
	blocked := false
	for i := range 500 {
		track := media.Track{SpotifyID: fmt.Sprintf("%022d", i), RequestedSource: "spotify", Title: strings.Repeat("x", 512)}
		writeErr := db.SaveCatalog(ctx, track)
		u, e := db.PhysicalUsage(ctx)
		if e != nil || u.Files > u.Limit || u.Peak > u.Limit {
			t.Fatal(u, e)
		}
		if !warning && u.Reserved >= u.Limit*9/10 {
			if e = monitor.Poll(ctx, now); e != nil {
				t.Fatal(e)
			}
			if len(contents) != 1 || !strings.Contains(contents[0], "90%") {
				t.Fatal(contents)
			}
			warning = true
		}
		if writeErr != nil {
			blocked = true
			break
		}
	}
	if !warning || !blocked {
		t.Fatal("fixture did not cross thresholds", warning, blocked)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if err = monitor.Poll(ctx, now); err == nil {
		t.Fatal("delivery failure lost")
	}
	if len(contents) != 2 || !strings.Contains(contents[1], "100%") || !strings.Contains(contents[1], "blocked") {
		t.Fatal(contents)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	monitor = storage.CatalogAlertMonitor{DB: db, Notifier: notifier}
	if err = monitor.Poll(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(contents) != 2 {
		t.Fatal("retry backoff lost")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if err = monitor.Poll(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(contents) != 3 || nonces[1] != nonces[2] {
		t.Fatal(contents, nonces)
	}
	if err = monitor.Poll(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(contents) != 3 {
		t.Fatal("confirmed alert repeated")
	}

	recovered, e := db.PhysicalUsage(ctx)
	if e != nil || recovered.Reserved >= recovered.Limit*85/100 {
		t.Fatal("checkpoint did not restore headroom", recovered, e)
	}
	rearmed := false
	for i := 1000; i < 1500; i++ {
		track := media.Track{SpotifyID: fmt.Sprintf("%022d", i), RequestedSource: "spotify", Title: strings.Repeat("x", 512)}
		if e = db.SaveCatalog(ctx, track); e != nil {
			t.Fatal("new warning should precede next rejection", e)
		}
		u, e := db.PhysicalUsage(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if u.Reserved >= u.Limit*9/10 {
			if e = monitor.Poll(ctx, now.Add(3*time.Second)); e != nil {
				t.Fatal(e)
			}
			if len(contents) != 4 || nonces[3] == nonces[0] || !strings.Contains(contents[3], "90%") {
				t.Fatal(contents, nonces)
			}
			rearmed = true
			break
		}
	}
	if !rearmed {
		t.Fatal("physical warning did not rearm after recovery")
	}
	t.Logf("physical thresholds: warning POST=1; critical failure=1; retry=1; stable nonce=%s; restart read successful; below85 rearmed; next warning=1", nonces[2])
}
