package discord

import (
	"github.com/disgoorg/snowflake/v2"
	"testing"
	"time"
	"whiterun/internal/media"
)

func TestOPT026SharedProcessAdmissionWiring(t *testing.T) {
	b, _ := testBot(t)
	if err := b.ConfigureYouTubeEfficiency(media.DefaultProviderAdmissionConfig(), 8*time.Minute); err != nil {
		t.Fatal(err)
	}
	first := b.youtubeAdmission
	if first == nil || b.resolver.YouTube.Admission != first || b.streamer.Admission != first {
		t.Fatal("extractors do not share admission")
	}
	for _, id := range []snowflake.ID{1, 2} {
		if b.getGuild(id) == nil {
			t.Fatal("guild missing")
		}
	}
	if b.youtubeAdmission != first {
		t.Fatal("guild initialization replaced global recovery")
	}
	if err := b.ConfigureYouTubeEfficiency(media.DefaultProviderAdmissionConfig(), 8*time.Minute); err == nil {
		t.Fatal("live reconfiguration accepted")
	}
}
