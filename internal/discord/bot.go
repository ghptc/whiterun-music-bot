package discord

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

type guild struct {
	mu           sync.Mutex // voice orchestration only; searches never hold this lock
	player       *player.GuildPlayer
	conn         voice.Conn
	channel      snowflake.ID
	searchCtx    context.Context
	searchCancel context.CancelFunc
}
type Bot struct {
	client   *bot.Client
	ctx      context.Context
	cancel   context.CancelFunc
	log      *slog.Logger
	resolver media.Resolver
	streamer player.Streamer
	mu       sync.Mutex
	guilds   map[snowflake.ID]*guild
	closing  bool
	workers  sync.WaitGroup
	searches chan struct{}
}

func New(ctx context.Context, token string, appID snowflake.ID, log *slog.Logger) (*Bot, error) {
	ctx, cancel := context.WithCancel(ctx)
	b := &Bot{ctx: ctx, cancel: cancel, log: log, resolver: media.Resolver{Binary: "yt-dlp", Cache: &media.SearchCache{}}, streamer: player.Streamer{YTDLP: "yt-dlp", FFmpeg: "ffmpeg"}, guilds: make(map[snowflake.ID]*guild), searches: make(chan struct{}, 4)}
	c, err := disgo.New(token, bot.WithLogger(log),
		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuilds, gateway.IntentGuildVoiceStates)),
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds, cache.FlagVoiceStates)),
		bot.WithVoiceManagerConfigOpts(voice.WithConnCreateFunc(newVoiceConn)),
		bot.WithEventListenerFunc(b.handle),
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
func (b *Bot) Start(scope snowflake.ID) error {
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()
	var err error
	if scope != 0 {
		_, err = b.client.Rest.SetGuildCommands(b.client.ApplicationID, scope, commands(), rest.WithCtx(ctx))
	} else {
		_, err = b.client.Rest.SetGlobalCommands(b.client.ApplicationID, commands(), rest.WithCtx(ctx))
	}
	if err != nil {
		return err
	}
	if err = b.client.OpenGateway(ctx); err != nil {
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
		g = &guild{player: player.New(b.ctx, b.log.With("guild_id", id), b.streamer.Play)}
		g.searchCtx, g.searchCancel = context.WithCancel(b.ctx)
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
				if b.client.Gateway.Status() != gateway.StatusReady || !ok || state.ChannelID == nil || *state.ChannelID != g.channel || g.conn.Gateway().Status() != voice.StatusReady {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					g.resetSearches(b.ctx)
					g.player.Leave(ctx)
					cancel()
					g.conn = nil
					g.channel = 0
					b.log.Info("voice disconnected; cleared playback", "guild_id", id)
				}
			}
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
	if c, ok := v.conn.(*voiceConn); ok && c.dave.ShouldHoldFrames() {
		wait, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := c.dave.WaitReady(wait)
		cancel()
		if err != nil {
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
