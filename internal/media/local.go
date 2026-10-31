package media

import (
	"errors"
	"regexp"
	"time"
)

var localAudioID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// LocalTrack is an operator-only source. No Discord resolver accepts local IDs.
// URL carries a basename, never a filesystem path; Streamer validates it again.
func LocalTrack(id string, duration time.Duration) (Track, error) {
	if !ValidLocalAudioID(id) || duration <= 0 {
		return Track{}, errors.New("invalid local audio descriptor")
	}
	return Track{RequestedSource: "local", URL: id, Title: "Local audio validation", Duration: duration}, nil
}

func ValidLocalAudioID(id string) bool { return localAudioID.MatchString(id) }
