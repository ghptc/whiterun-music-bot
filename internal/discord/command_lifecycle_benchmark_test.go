package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// Complete dispatch through ACK, permission/snapshot and response adapter;
// HTTP is simulated. BenchmarkEnduranceLocalACK measures SDK over local TCP.
func BenchmarkQA013CommandDispatch(b *testing.B) {
	for _, name := range []string{"help", "queue", "pause"} {
		b.Run(name, func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			r := &recordingRest{}
			service := &Bot{ctx: ctx, cancel: cancel, log: slog.New(slog.NewTextHandler(io.Discard, nil)), client: &bot.Client{ApplicationID: 2, Rest: r, Caches: cache.New(cache.WithCaches(cache.FlagGuilds, cache.FlagVoiceStates, cache.FlagMembers))}, guilds: map[snowflake.ID]*guild{}}
			defer func() {
				cancel()
				service.workers.Wait()
				for _, g := range service.guilds {
					g.player.Close(context.Background())
				}
			}()
			var interaction d.ApplicationCommandInteraction
			raw := fmt.Sprintf(`{"id":"10","application_id":"2","type":2,"token":"fixture","guild_id":"1","user":{"id":"3"},"data":{"id":"4","name":%q,"type":1}}`, name)
			if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
				b.Fatal(err)
			}
			var entered time.Time
			var ack time.Duration
			event := &events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: interaction, Respond: func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error {
				ack += time.Since(entered)
				return nil
			}}
			b.ReportAllocs()
			for b.Loop() {
				entered = time.Now()
				service.command(event)
			}
			b.ReportMetric(float64(ack.Nanoseconds())/float64(b.N), "local_ack_ns/op")
		})
	}
}
