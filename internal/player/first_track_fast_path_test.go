package player

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
	"whiterun/internal/media"
)

type deferredSpotify struct{ release <-chan struct{} }

func (s deferredSpotify) Collection(context.Context, string, string) (media.Collection, error) {
	panic("unexpected collection fetch")
}
func (s deferredSpotify) TrackMetadata(ctx context.Context, id string) (media.Track, error) {
	select {
	case <-s.release:
	case <-ctx.Done():
		return media.Track{}, ctx.Err()
	}
	return media.Track{SpotifyID: id, SourceURL: "https://open.spotify.com/track/" + id, RequestedSource: "spotify", Title: "Hydrated " + id, Artist: "Artist"}, nil
}
func deferredTracks() []media.Track {
	tracks := make([]media.Track, 500)
	for i := range tracks {
		id := fmt.Sprintf("%022d", i+1)
		tracks[i] = media.Track{SpotifyID: id, SourceURL: "https://open.spotify.com/track/" + id, RequestedSource: "spotify", Title: "Loading Spotify metadata…", MetadataPending: true, Position: i + 1, Total: 500}
	}
	tracks[0].MetadataPending = false
	tracks[0].Title = "First"
	tracks[0].Artist = "Artist"
	return tracks
}

func TestPERF018Deferred500ControlsAndPersistence(t *testing.T) {
	for _, action := range []string{"shuffle", "stop", "cancel", "restart"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				resolver := media.CollectionResolver{Spotify: deferredSpotify{release}, Limiter: media.NewMatchLimiter(), Search: func(context.Context, string) (media.Track, error) {
					return media.Track{URL: "https://www.youtube.com/watch?v=abcdefghijk"}, nil
				}}
				p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				j := begin(t, p, parent, deferredTracks(), resolver.MatchSpotify)
				first := firstResult(t, j)
				if first.Err != nil || first.Position != 1 {
					t.Fatal(first)
				}
				synctest.Wait()
				state := p.State()
				if state.Current == nil || state.Current.MetadataPending || len(state.Entries) != 499 {
					t.Fatal("first track blocked by tail", len(state.Entries))
				}
				for _, e := range state.Entries {
					if !e.Pending || !e.Track.MetadataPending {
						t.Fatal("metadata exposed as ready")
					}
				}
				switch action {
				case "shuffle":
					if err := p.shuffle(func(n int, swap func(int, int)) { swap(0, n-1) }); err != nil {
						t.Fatal(err)
					}
					close(release)
					synctest.Wait()
					state = p.State()
					if state.Entries[0].Track.Position != 500 || state.Entries[0].Pending || state.Entries[0].Track.MetadataPending {
						t.Fatal("shuffled logical head did not hydrate")
					}
					seen := map[string]bool{}
					for _, e := range state.Entries {
						if seen[e.Track.SpotifyID] {
							t.Fatal("duplicate occurrence")
						}
						seen[e.Track.SpotifyID] = true
					}
				case "stop":
					p.Stop()
					synctest.Wait()
					close(release)
					synctest.Wait()
					if p.Length() != 0 || p.State().Current != nil {
						t.Fatal("stopped import resurrected")
					}
				case "cancel":
					cancel()
					synctest.Wait()
					close(release)
					synctest.Wait()
					if p.Length() != 0 {
						t.Fatal("canceled pending entries survived")
					}
				case "restart":
					p.mu.Lock()
					saved := p.durableLocked()
					p.mu.Unlock()
					cancel()
					synctest.Wait()
					restored := qualityDetached(t)
					if err := restored.ConfigureStore(&qualityStore{state: saved}, "1"); err != nil {
						t.Fatal(err)
					}
					snapshot := restored.State()
					if len(snapshot.Queued) != 500 || !snapshot.Queued[499].MetadataPending || snapshot.Queued[499].SpotifyID != fmt.Sprintf("%022d", 500) {
						t.Fatal("restart lost deferred occurrences")
					}
					close(release)
					tr, err := resolver.MatchSpotify(context.Background(), snapshot.Queued[499], true)
					if err != nil || tr.MetadataPending || tr.Position != 500 {
						t.Fatal("restored entry cannot hydrate", err)
					}
				}
			})
		})
	}
}

func TestPERF018DeferredMetadataTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resolver := media.CollectionResolver{Spotify: deferredSpotify{make(chan struct{})}, Limiter: media.NewMatchLimiter(), Search: func(context.Context, string) (media.Track, error) {
			t.Error("unhydrated identity searched")
			return media.Track{}, nil
		}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := resolver.MatchSpotify(ctx, deferredTracks()[499], false)
		if err != context.DeadlineExceeded {
			t.Fatal("provider timeout hidden", err)
		}
	})
}
