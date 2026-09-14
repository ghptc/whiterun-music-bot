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
	text := c.Title + " " + c.Channel + " " + c.Artist
	tokens := strings.Fields(words(query))
	matched, total := 0, 0
	for _, token := range tokens {
		if token == "the" || token == "and" || token == "official" || token == "audio" || token == "music" || token == "video" {
			continue
		}
		total++
		if tokenMatches(text, token) {
			matched++
		}
	}
	if total == 0 || matched == 0 {
		return 0, false
	}
	score := matched * 300 / total
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
	channel := c.Channel + " " + c.Uploader
	channelRelevant := channelMatchesQuery(query, c)
	switch {
	case c.Verified && c.Artist != "" && has(channel, c.Artist):
		score += 65
	case has(c.Title, "official audio") || has(c.Title, "official video") || has(c.Title, "official music video") || has(c.Title, "official video"):
		score += 55
	case has(channel, "topic") || strings.Contains(strings.ToLower(c.Description), "provided to youtube by"):
		score += 45
	case strings.Contains(strings.ToLower(channel), "vevo"):
		score += 35
	case c.Verified && channelRelevant:
		score += 25
	}
	return score, true
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

func channelMatchesQuery(query string, c Candidate) bool {
	channel := c.Channel + " " + c.Uploader
	for _, token := range strings.Fields(words(query)) {
		if len(token) > 2 && has(channel, token) {
			return true
		}
	}
	return false
}

// tokenMatches tolerates small spelling errors in longer words only.
func tokenMatches(text, token string) bool {
	if has(text, token) {
		return true
	}
	a := []rune(token)
	if len(a) < 5 {
		return false
	}
	for _, word := range strings.Fields(words(text)) {
		b := []rune(word)
		if len(b) < 5 || len(a)-len(b) > 2 || len(b)-len(a) > 2 {
			continue
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
			return true
		}
	}
	return false
}
