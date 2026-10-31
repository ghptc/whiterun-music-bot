package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type accessProbeMappings struct {
	mu    sync.Mutex
	items map[string]Candidate
}

func (s *accessProbeMappings) LoadMapping(_ context.Context, k string) (Candidate, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[k]
	return v, ok, nil
}
func (s *accessProbeMappings) SaveMapping(_ context.Context, k string, v Candidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[k] = v
	return nil
}
func (s *accessProbeMappings) DeleteMapping(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, k)
	return nil
}

func accessProbe(t *testing.T, challenge bool) (CollectionResolver, string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "provider-mock")
	calls := filepath.Join(dir, "calls")
	script := `#!/usr/bin/env python3
import sys,json,re,time
args=sys.argv[1:];target=args[-1];flat='--flat-playlist' in args
with open(` + fmt.Sprintf("%q", calls) + `, 'a') as f:f.write(('search' if flat else 'verification')+'\n')
if flat:time.sleep(.01)
m=re.search(r'Song(\d{11})',target)
id=m.group(1) if m else target.rsplit('=',1)[-1]
c={'id':id,'title':'Song'+id,'track':'Song'+id,'channel':'Artist - Topic','artist':'Artist','artists':['Artist'],'album':'Album','duration':210,'categories':['Music']}
if not flat and ` + map[bool]string{true: "True", false: "False"}[challenge] + `:
 print("ERROR: Sign in to confirm you're not a bot",file=sys.stderr);sys.exit(1)
print(json.dumps({'entries':[c]} if flat else c))
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return CollectionResolver{YouTube: Resolver{Binary: binary, Cache: &SearchCache{}, Mappings: &accessProbeMappings{items: map[string]Candidate{}}}, Limiter: NewMatchLimiter()}, calls
}
func accessTrack(i int) Track {
	id := fmt.Sprintf("%011d", i)
	return Track{SpotifyID: fmt.Sprintf("%022d", i), Title: "Song" + id, Artist: "Artist", Artists: []string{"Artist"}, Album: "Album", Duration: 210 * time.Second, RequestedSource: "spotify", SourceURL: "https://open.spotify.com/track/" + fmt.Sprintf("%022d", i)}
}
func accessCounts(t *testing.T, p string) (int, int) {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return 0, 0
	}
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	return strings.Count(s, "search\n"), strings.Count(s, "verification\n")
}
func accessMetric(t *testing.T, name string, data map[string]any) {
	t.Helper()
	data["scenario"] = name
	b, _ := json.Marshal(data)
	t.Log("MEASUREMENT " + string(b))
}
