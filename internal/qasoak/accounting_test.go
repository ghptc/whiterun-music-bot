package qasoak

import (
	"testing"
	"whiterun/internal/media"
)

func TestSoakEvidenceRejectsVacuousAndLostTracks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		g     *guildRun
		valid bool
	}{
		{"no work", &guildRun{}, false},
		{"never audible", &guildRun{accepted: 1, selected: 1, terminal: map[string]int{"unavailable": 1}}, false},
		{"missing terminal", &guildRun{accepted: 1, selected: 1, announced: 1}, false},
		{"lost track", &guildRun{accepted: 2, selected: 1, announced: 1, terminal: map[string]int{"completed": 1}}, false},
		{"completed and pending", &guildRun{accepted: 2, selected: 1, announced: 1, expected: []media.Track{{Title: "pending"}}, terminal: map[string]int{"completed": 1}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.g.validateAccounting(); (err == nil) != tc.valid {
				t.Fatal("misclassified evidence", err)
			}
		})
	}
}
