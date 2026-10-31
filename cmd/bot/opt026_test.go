package main

import (
	"testing"
	"whiterun/internal/discord"
)

func TestOPT026Configuration(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		ok          bool
	}{
		{"YOUTUBE_MAX_CONCURRENT", "1", true}, {"YOUTUBE_MAX_CONCURRENT", "0", false}, {"YOUTUBE_MAX_CONCURRENT", "33", false},
		{"YOUTUBE_ADMISSION_INTERVAL", "1ms", true}, {"YOUTUBE_ADMISSION_INTERVAL", "0", false},
		{"YOUTUBE_PREFETCH_HORIZON", "2m", true}, {"YOUTUBE_PREFETCH_HORIZON", "9m", false}, {"YOUTUBE_PREFETCH_HORIZON", "0", false},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			for _, key := range []string{"YOUTUBE_MAX_CONCURRENT", "YOUTUBE_ADMISSION_INTERVAL", "YOUTUBE_PREFETCH_HORIZON"} {
				t.Setenv(key, "")
			}
			t.Setenv(tc.name, tc.value)
			err := configureYouTubeEfficiency(&discord.Bot{})
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
		})
	}
}
