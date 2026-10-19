package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

type guild struct {
	mu                  sync.Mutex // voice orchestration only; searches never hold this lock
	player              *player.GuildPlayer
	conn                voice.Conn
	channel             snowflake.ID
	searchCtx           context.Context
	searchCancel        context.CancelFunc
	emptySince          time.Time
	unhealthySince      time.Time
	announcementMu      sync.Mutex // serialize accepted channel changes with first-frame snapshots
	announcementChannel atomic.Uint64
	announcementEpoch   atomic.Uint64
	announcements       chan announcement
}
type Bot struct {
	store     player.Store
	client    *bot.Client
	ctx       context.Context
	cancel    context.CancelFunc
	log       *slog.Logger
	resolver  media.CollectionResolver
	streamer  player.Streamer
	mu        sync.Mutex
	guilds    map[snowflake.ID]*guild
	closing   bool
	workers   sync.WaitGroup
	searches  chan struct{}
	voiceWake chan struct{}
}

func New(ctx context.Context, token string, appID snowflake.ID, log *slog.Logger) (*Bot, error) {
	ctx, cancel := context.WithCancel(ctx)
	b := &Bot{ctx: ctx, cancel: cancel, log: log, resolver: media.CollectionResolver{YouTube: media.Resolver{Binary: "yt-dlp", Cache: &media.SearchCache{}}, Log: log, Limiter: media.NewMatchLimiter()}, streamer: player.Streamer{YTDLP: "yt-dlp", FFmpeg: "ffmpeg"}, guilds: make(map[snowflake.ID]*guild), searches: make(chan struct{}, 4), voiceWake: make(chan struct{}, 1)}
	c, err := disgo.New(token, bot.WithLogger(log),
		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuilds, gateway.IntentGuildVoiceStates)),
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds, cache.FlagVoiceStates, cache.FlagMembers)),
		bot.WithVoiceManagerConfigOpts(voice.WithConnCreateFunc(newVoiceConn)),
		bot.WithEventListenerFunc(b.handle),
		bot.WithEventListenerFunc(b.voiceStateChanged),
	)
	if err != nil {
		cancel()
		return nil, err
	}
	if c.ApplicationID != appID {
		cancel()
		c.Close(context.Background())
		return nil, errors.New("DISCORD_APPLICATION_ID does not match the bot token")
	}
	b.client = c
	return b, nil
}

// ConfigureStore is called before starting the gateway.
func (b *Bot) ConfigureStore(store player.Store) {
	b.store = store
	if mappings, ok := store.(media.MappingStore); ok {
		b.resolver.YouTube.Mappings = mappings
	}
}
func (b *Bot) ConfigureMatchPolicy(policy media.MatchPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	b.resolver.YouTube.Policy = &policy
	return nil
}

// ConfigureSpotify is called before starting the gateway.
func (b *Bot) ConfigureSpotify(s *media.Spotify) { b.resolver.Spotify = s }

func (b *Bot) play(ctx context.Context, track media.Track, voice player.Voice) error {
	if track.RequestedSource == "spotify" {
		if track.URL == "" {
			var err error
			track, err = b.resolver.MatchSpotify(ctx, track, true)
			if err != nil {
				return err
			}
		}
		if err := b.resolver.YouTube.VerifySpotify(ctx, track); err != nil {
			if ctx.Err() == nil {
				b.resolver.YouTube.InvalidateSpotify(ctx, track)
			}
			return err
		}
	}

	if track.VerifyBeforePlay {
		verified, err := b.resolver.YouTube.Resolve(ctx, track.URL)
		if err != nil {
			return err
		}
		verified.Position, verified.Total, verified.Timing = track.Position, track.Total, track.Timing
		verified.RequestedBy = track.RequestedBy
		track = verified
	}
	if metadata, ok := voice.(interface{ SetPlaybackTrack(media.Track) }); ok {
		metadata.SetPlaybackTrack(track)
	}
	err := b.streamer.Play(ctx, track, voice)
	if err != nil && ctx.Err() == nil {
		b.resolver.YouTube.InvalidateSpotify(ctx, track)
	}
	return err
}

func (b *Bot) Start(scope snowflake.ID) error {
	if err := b.SyncCommands(b.ctx, scope); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()
	if err := b.client.OpenGateway(ctx); err != nil {
		return err
	}
	b.workers.Add(1)
	go b.watchVoice()
	b.log.Info("The Bard is connected", "application_id", b.client.ApplicationID, "command_guild", scope)
	return nil
}
func (b *Bot) getGuild(id snowflake.ID) *guild {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		return nil
	}
	g := b.guilds[id]
	if g == nil {
		g = &guild{player: player.New(b.ctx, b.log.With("guild_id", id), b.play)}
		if b.store != nil {
			if err := g.player.ConfigureStore(b.store, id.String()); err != nil {
				b.log.Error("durable_state_load_failed", "guild_id", id, "error", err)
				g.player.Close(context.Background())
				return nil
			}
		}
		if err := b.restoreSettings(id.String(), g); err != nil {
			b.log.Error("guild_settings_load_failed", "guild_id", id, "error", err)
			g.player.Close(context.Background())
			return nil
		}
		g.searchCtx, g.searchCancel = context.WithCancel(b.ctx)
		b.setupAnnouncements(id, g)
		b.guilds[id] = g
	}
	return g
}
func (b *Bot) Close(ctx context.Context) {
	b.mu.Lock()
	b.closing = true
	b.cancel()
	guilds := make([]*guild, 0, len(b.guilds))
	for _, g := range b.guilds {
		guilds = append(guilds, g)
	}
	b.mu.Unlock()
	b.workers.Wait()
	var wg sync.WaitGroup
	for _, g := range guilds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.mu.Lock()
			defer g.mu.Unlock()
			g.emptySince = time.Time{}
			g.player.Close(ctx)
			g.conn = nil
			g.channel = 0
		}()
	}
	wg.Wait()
	b.client.Close(ctx)
}

// A voice kick, move, or failed voice gateway invalidates the old player session.
// Polling also catches voice transport loss without a main-gateway leave event.
func (b *Bot) watchVoice() {
	defer b.workers.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-tick.C:
		case <-b.voiceWake:
		}
		b.mu.Lock()
		guilds := make(map[snowflake.ID]*guild, len(b.guilds))
		for id, g := range b.guilds {
			guilds[id] = g
		}
		b.mu.Unlock()
		for id, g := range guilds {
			if !g.mu.TryLock() {
				continue
			}
			if g.conn != nil {
				state, ok := b.client.Caches.VoiceState(id, b.client.ID())
				mainReady := b.client.Gateway.Status() == gateway.StatusReady
				voiceReady := mainReady && ok && state.ChannelID != nil && *state.ChannelID == g.channel && g.conn.Gateway().Status() == voice.StatusReady
				sameChannel := ok && state.ChannelID != nil && *state.ChannelID == g.channel
				if b.checkVoiceHealth(id, g, time.Now(), mainReady, voiceReady, ok, sameChannel) {
					g.mu.Unlock()
					continue
				}
			}
			b.checkEmpty(id, g, time.Now())
			g.mu.Unlock()
		}
	}
}

type transport struct{ conn voice.Conn }

// Retain the DAVE session to hold audio during encryption handshakes.
type voiceConn struct {
	voice.Conn
	dave *session.Session
}

func newVoiceConn(guildID, userID snowflake.ID, update voice.StateUpdateFunc, remove func(), opts ...voice.ConnConfigOpt) voice.Conn {
	c := &voiceConn{}
	opts = append(opts, voice.WithConnDaveSessionCreateFunc(func(log *slog.Logger, id godave.UserID, callbacks godave.Callbacks) godave.Session {
		c.dave = session.New(id, callbacks, session.WithLogger(log))
		return c.dave
	}))
	c.Conn = voice.NewConn(guildID, userID, update, remove, opts...)
	return c
}
func (c *voiceConn) Close(ctx context.Context) {
	c.Conn.Close(ctx)
	_ = c.dave.Close()
}
func (v transport) WriteFrame(ctx context.Context, frame []byte) error {
	if c, ok := v.conn.(*voiceConn); ok {
		if err := waitVoiceReady(ctx, c.dave, 60*time.Second); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := v.conn.UDP().SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	_, err := v.conn.UDP().Write(frame)
	if err == nil {
		media.TimingFrom(ctx).FirstDiscordSend()
	}
	return err
}
func (v transport) Speaking(ctx context.Context, on bool) error {
	flags := voice.SpeakingFlagNone
	if on {
		flags = voice.SpeakingFlagMicrophone
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return v.conn.SetSpeaking(ctx, flags)
}
func (v transport) Close(ctx context.Context) { v.conn.Close(ctx) }

// Keep event handling short enough to acknowledge within Discord's deadline.
func (b *Bot) handle(e *events.ApplicationCommandInteractionCreate) {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return
	}
	b.workers.Add(1)
	b.mu.Unlock()
	go func() { defer b.workers.Done(); b.command(e) }()
}

// Caller holds g.mu. Cancel pending resolutions as well as invalidating tickets.
func (g *guild) resetSearches(parent context.Context) {
	if g.searchCancel != nil {
		g.searchCancel()
	}
	g.searchCtx, g.searchCancel = context.WithCancel(parent)
}

// DAVE's recovery watchdog starts after 15s and may need a second cycle.
// This deadline bounds only readiness waiting, never the track's processes.
type voiceReadiness interface {
	ShouldHoldFrames() bool
	WaitReady(context.Context) (time.Duration, error)
}

func waitVoiceReady(ctx context.Context, ready voiceReadiness, timeout time.Duration) error {
	if !ready.ShouldHoldFrames() {
		return nil
	}
	started := time.Now()
	timing := media.TimingFrom(ctx)
	timing.Event("dave_ready_wait_start", started)
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := ready.WaitReady(wait)
	timing.Event("dave_ready_wait_complete", started, "error", err)
	if err != nil {
		return fmt.Errorf("DAVE readiness: %w", err)
	}
	return nil
}
