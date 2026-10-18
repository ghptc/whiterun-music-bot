package media

import (
	"strings"
	"unicode"
)

func words(s string) string {
	return " " + strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }), " ") + " "
}
func has(s, term string) bool { return strings.Contains(words(s), words(term)) }

// IsMusic is deliberately conservative. Metadata is a heuristic, not proof.
func musicShape(c Candidate) bool {
	if c.Title == "" || c.Duration < 45 || c.Duration > 20*60 || c.Live || (c.LiveStatus == "is_upcoming" || c.LiveStatus == "is_live") {
		return false
	}
	text := c.Title + " " + c.Channel + " " + c.Uploader
	for _, term := range []string{"podcast", "interview", "reaction", "reacts", "review", "tutorial", "documentary", "gameplay", "news", "shorts", "short"} {
		if has(text, term) {
			return false
		}
	}
	return true
}

func IsMusic(c Candidate) bool {
	if !musicShape(c) {
		return false
	}
	for _, cat := range c.Categories {
		if strings.EqualFold(cat, "Music") {
			return true
		}
	}
	if c.Artist != "" && c.Track != "" {
		return true
	}
	channel := c.Channel + " " + c.Uploader
	return has(channel, "topic") || strings.HasSuffix(strings.ToLower(strings.TrimSpace(c.Channel)), "vevo") ||
		has(c.Title, "official audio") || has(c.Title, "official video") || has(c.Title, "official music video") ||
		strings.Contains(strings.ToLower(c.Description), "provided to youtube by")
}

var variants = []string{"live", "cover", "remix", "slowed", "reverb", "nightcore", "lyrics", "karaoke", "unplugged", "acoustic"}

func requested(query, variant string) bool {
	if has(query, variant) {
		return true
	}
	return variant == "live" && (has(query, "unplugged") || has(query, "concert"))
}

// Score separates query relevance, version intent, and canonical-source quality.
// Verification alone does not establish an official artist channel.
func Score(query string, c Candidate) (int, bool) {
	if !IsMusic(c) {
		return 0, false
	}
	return scoreMusic(query, c)
}

// scoreMusic is shared by confirmed music and provisional discovery ranking.
// A provisional score never authorizes queue insertion.
func scoreMusic(query string, c Candidate) (int, bool) {
	b, ok := musicScoreBreakdown(query, c)
	return b.FinalScore, ok
}

type scoreBreakdown struct {
	ArtistScore      int
	ExactTokenScore  int
	FuzzyScore       int
	UnmatchedPenalty int
	VersionScore     int
	OfficialBonus    int
	FinalScore       int
}

func musicScoreBreakdown(query string, c Candidate) (scoreBreakdown, bool) {
	breakdown, ok := relevance(query, c)
	if !ok {
		return breakdown, false
	}
	score := breakdown.ArtistScore + breakdown.ExactTokenScore + breakdown.FuzzyScore - breakdown.UnmatchedPenalty
	// Version intent outweighs official-source preference.
	for _, variant := range variants {
		present := has(c.Title, variant)
		if requested(query, variant) {
			if present {
				score += 100
			} else {
				score -= 60
			}
		} else if present {
			score -= 100
		}
	}
	breakdown.VersionScore = score - (breakdown.ArtistScore + breakdown.ExactTokenScore + breakdown.FuzzyScore - breakdown.UnmatchedPenalty)
	beforeBonus := score
	score += sourceBonus(query, c)
	breakdown.OfficialBonus = score - beforeBonus
	breakdown.FinalScore = score
	return breakdown, true
}
func Select(query string, candidates []Candidate) (Candidate, bool) {
	bestScore := -100000
	var best Candidate
	found := false
	for _, c := range candidates {
		if score, ok := Score(query, c); ok && score > bestScore {
			bestScore = score
			best = c
			found = true
		}
	}
	return best, found
}

// Matching weights deliberately separate whole tokens from morphology and typos.
// Case and punctuation are normalized by words before comparison.
func tokenWeight(query, candidate string) int {
	if query == candidate {
		return 100
	}
	if len([]rune(query)) < 5 || len([]rune(candidate)) < 5 {
		return 0
	}
	if strings.HasPrefix(candidate, query) || strings.HasPrefix(query, candidate) {
		return 35
	}
	if strings.Contains(candidate, query) || strings.Contains(query, candidate) {
		return 10
	}
	a, b := []rune(query), []rune(candidate)
	if len(a)-len(b) > 2 || len(b)-len(a) > 2 {
		return 0
	}
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}
	for i, x := range a {
		prev := row[0]
		row[0] = i + 1
		for j, y := range b {
			old := row[j+1]
			cost := 1
			if x == y {
				cost = 0
			}
			row[j+1] = min(row[j+1]+1, row[j]+1, prev+cost)
			prev = old
		}
	}
	if row[len(b)] <= 2 {
		return 60
	}
	return 0
}

func semanticTokens(text string) []string {
	var result []string
	for _, token := range strings.Fields(words(text)) {
		switch token {
		case "the", "and", "official", "audio", "video", "music", "lyrics", "remastered", "hd", "4k":
			continue
		}
		result = append(result, token)
	}
	return result
}

// Infer artist tokens from explicit metadata or the conventional Artist - Title
// prefix. Channel tokens are artist evidence only when also present in query.
func relevance(query string, c Candidate) (scoreBreakdown, bool) {
	var b scoreBreakdown
	artist := c.Artist
	if artist == "" {
		if prefix, _, ok := strings.Cut(c.Title, " - "); ok {
			artist = prefix
		} else {
			artist = c.Channel + " " + c.Uploader
		}
	}
	queryTokens := semanticTokens(query)
	titleTokens := semanticTokens(c.Title)
	songCount, artistCount, matched := 0, 0, 0
	for _, q := range queryTokens {
		if has(artist, q) {
			artistCount++
			b.ArtistScore += 100
			matched++
			continue
		}
		songCount++
		weight := 0
		for _, t := range titleTokens {
			weight = max(weight, tokenWeight(q, t))
		}
		if weight > 0 {
			matched++
		}
		if weight == 100 {
			b.ExactTokenScore += 300
		} else {
			b.FuzzyScore += weight * 3
		}
	}
	if artistCount > 0 {
		b.ArtistScore /= artistCount
	}
	if songCount > 0 {
		b.ExactTokenScore /= songCount
		b.FuzzyScore /= songCount
		if artistCount == 0 {
			b.ExactTokenScore = b.ExactTokenScore * 4 / 3
			b.FuzzyScore = b.FuzzyScore * 4 / 3
		}
	}
	for _, t := range titleTokens {
		if has(artist, t) {
			continue
		}
		relevant := false
		for _, q := range queryTokens {
			if tokenWeight(q, t) > 0 {
				relevant = true
				break
			}
		}
		if !relevant {
			b.UnmatchedPenalty += 8
		}
	}
	b.UnmatchedPenalty = min(b.UnmatchedPenalty, 64)
	return b, matched > 0
}

// Normalize the entire channel identity, not a single overlapping artist token.
// "Artist Archive" and unrelated verified music aggregators are not the artist.
func channelIdentity(s string) string {
	s = strings.TrimSpace(words(s))
	s = strings.TrimPrefix(s, "official ")
	s = strings.TrimSuffix(s, " official")
	s = strings.TrimSuffix(s, " topic")
	s = strings.TrimSuffix(s, "vevo")
	return strings.ReplaceAll(s, " ", "")
}
func artistChannel(c Candidate) bool {
	artist := c.Artist
	if artist == "" {
		artist, _, _ = strings.Cut(c.Title, " - ")
	}
	if artist == "" {
		return false
	}
	return channelIdentity(artist) == channelIdentity(c.Channel) || channelIdentity(artist) == channelIdentity(c.Uploader)
}
func sourceBonus(query string, c Candidate) int {
	channel := c.Channel + " " + c.Uploader
	auto := has(channel, "topic") || strings.Contains(strings.ToLower(c.Description), "provided to youtube by")
	video := has(c.Title, "video") && !has(query, "video")
	bonus := 0
	switch {
	case c.Verified && artistChannel(c):
		bonus = 85
	case auto:
		bonus = 75
	case has(c.Title, "official audio"):
		bonus = 60
	case artistChannel(c) && strings.Contains(strings.ToLower(channel), "vevo"):
		bonus = 50
	case has(c.Title, "official video") || has(c.Title, "official music video"):
		bonus = 35
	case artistChannel(c):
		bonus = 25
	}
	if video {
		// Official video remains a credible fallback, below canonical audio.
		if bonus >= 35 {
			bonus = 20
		} else {
			bonus -= 20
		}
	}
	return bonus
}

// Spotify metadata is a constraint, not merely extra query tokens. Require
// title and artist evidence independently, reject unsolicited versions, and
// reject major duration mismatches when duration is available on both sides.
func matchesMetadata(t *Track, c Candidate) bool {
	if t == nil {
		return true
	}
	title := c.Track
	if title == "" {
		title = c.Title
	}
	for _, token := range semanticTokens(t.Title) {
		if !has(title, token) {
			return false
		}
	}
	artist := c.Channel + " " + c.Uploader + " " + c.Title
	if c.Artist != "" {
		artist = c.Artist
	}
	for _, token := range semanticTokens(t.Artist) {
		if !has(artist, token) && !(c.Artist == "" && channelIdentity(c.Channel) == channelIdentity(t.Artist)) {
			return false
		}
	}
	for _, variant := range variants {
		if has(c.Title, variant) && !requested(t.Title+" "+t.Album, variant) {
			return false
		}
	}
	if t.Duration > 0 && c.Duration > 0 {
		expected := t.Duration.Seconds()
		delta := c.Duration - expected
		if delta < 0 {
			delta = -delta
		}
		if delta > max(15, expected*0.1) {
			return false
		}
	}
	return true
}
