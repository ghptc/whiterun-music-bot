package media

import "strings"
import "unicode"

func words(s string) string {
	return " " + strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }), " ") + " "
}
func has(s, term string) bool { return strings.Contains(words(s), words(term)) }

// IsMusic is deliberately conservative. Metadata is a heuristic, not proof.
func IsMusic(c Candidate) bool {
	if c.Title == "" || c.Duration < 45 || c.Duration > 20*60 || c.Live || c.LiveStatus == "is_upcoming" {
		return false
	}
	text := c.Title + " " + c.Channel + " " + c.Uploader
	for _, term := range []string{"podcast", "interview", "reaction", "reacts", "review", "tutorial", "documentary", "gameplay", "news", "shorts", "short"} {
		if has(text, term) {
			return false
		}
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
		has(c.Title, "official audio") || has(c.Title, "official music video") ||
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
	text := c.Title + " " + c.Channel + " " + c.Artist
	tokens := strings.Fields(words(query))
	matched, total := 0, 0
	for _, token := range tokens {
		if token == "the" || token == "and" || token == "official" || token == "audio" || token == "music" || token == "video" {
			continue
		}
		total++
		if has(text, token) {
			matched++
		}
	}
	if total == 0 || matched*100/total < 60 {
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
	channelRelevant := false
	for _, token := range tokens {
		if len(token) > 2 && has(channel, token) {
			channelRelevant = true
		}
	}
	switch {
	case c.Verified && c.Artist != "" && has(channel, c.Artist):
		score += 65
	case has(c.Title, "official audio") || has(c.Title, "official music video") || has(c.Title, "official video"):
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
