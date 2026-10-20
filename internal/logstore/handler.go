package logstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type boundAttr struct {
	groups []string
	attr   slog.Attr
}
type handler struct {
	console slog.Handler
	store   *Store
	attrs   []boundAttr
	groups  []string
}

func NewHandler(console slog.Handler, store *Store) slog.Handler {
	return &handler{console: console, store: store}
}
func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.console.Enabled(ctx, level)
}
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copy := *h
	copy.console = h.console.WithAttrs(attrs)
	copy.attrs = append([]boundAttr(nil), h.attrs...)
	for _, a := range attrs {
		if len(copy.attrs) >= 64 {
			break
		}
		copy.attrs = append(copy.attrs, boundAttr{append([]string(nil), h.groups...), a})
	}
	return &copy
}
func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	copy := *h
	copy.console = h.console.WithGroup(name)
	copy.groups = append(append([]string(nil), h.groups...), name)
	return &copy
}
func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	err := h.console.Handle(ctx, r)
	h.store.capture(r, h.attrs, h.groups)
	return err
}
func (s *Store) capture(r slog.Record, attrs []boundAttr, groups []string) {
	// A malformed diagnostic value must not take down the caller/player.
	defer func() {
		if recover() != nil {
			s.metrics.lost.Add(1)
			s.metrics.failures.Add(1)
			category := "sanitization"
			s.lastFailure.Store(&category)
		}
	}()

	fields := map[string]any{}
	budget := valueBudget{nodes: 256, bytes: 32 << 10}
	count := 0
	var add func(map[string]any, []string, slog.Attr, int)
	add = func(dst map[string]any, groups []string, a slog.Attr, depth int) {
		if count >= 64 || depth > 4 || len(groups) > 4 || len(a.Key) > 128 {
			return
		}
		count++
		for _, group := range groups {
			if len(group) > 128 || sensitiveKey(group) {
				return
			}
			nested, ok := dst[group].(map[string]any)
			if !ok {
				nested = map[string]any{}
				dst[group] = nested
			}
			dst = nested
		}
		a.Value = a.Value.Resolve()
		if a.Equal(slog.Attr{}) {
			return
		}
		if sensitiveKey(a.Key) {
			dst[a.Key] = "[redacted]"
			return
		}
		if a.Value.Kind() == slog.KindGroup {
			target := dst
			if a.Key != "" {
				target = map[string]any{}
				dst[a.Key] = target
			}
			for _, v := range a.Value.Group() {
				if count >= 64 {
					break
				}
				add(target, nil, v, depth+1)
			}
			return
		}
		dst[a.Key] = s.scrub.value(a.Value.Any(), depth, &budget)
	}
	for _, a := range attrs {
		add(fields, a.groups, a.attr, 0)
	}
	r.Attrs(func(a slog.Attr) bool { add(fields, groups, a, 0); return count < 64 })
	timestamp := r.Time
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	// Reserved envelope keys cannot be overwritten by arbitrary provider attrs.
	fields["time"] = timestamp.UTC().Format(time.RFC3339Nano)
	fields["level"] = r.Level.String()
	fields["msg"] = s.scrub.text(r.Message, 1024)
	raw, err := json.Marshal(fields)
	if err != nil || len(raw) > s.recordLimit {
		s.metrics.oversized.Add(1)
		return
	}
	stringField := func(names ...string) *string {
		for _, name := range names {
			if value, ok := fields[name]; ok && value != nil && value != "[redacted]" {
				text := fmt.Sprint(value)
				if text != "" {
					return &text
				}
			}
		}
		return nil
	}
	event := strings.Clone(s.scrub.text(r.Message, 1024))
	if stage := stringField("stage"); stage != nil {
		event = *stage
	}
	e := entry{Timestamp: timestamp.UnixNano(), Level: r.Level.String(), Event: event, Guild: stringField("guild_id"), Interaction: stringField("interaction_id"), Track: stringField("track_id"), Spotify: stringField("spotify_track_id", "spotify_id"), Video: stringField("youtube_video_id", "video_id", "selected_video_id"), Collection: stringField("collection_id"), Category: stringField("error_category", "category"), Payload: raw}
	if n := number(fields["position"]); n != nil && *n == math.Trunc(*n) && *n >= math.MinInt64 && *n < math.MaxInt64 {
		v := int64(*n)
		e.Position = &v
	}
	e.Elapsed = number(fields["elapsed_ms"])
	if e.Elapsed == nil {
		if n := number(fields["elapsed_us"]); n != nil {
			v := *n / 1000
			e.Elapsed = &v
		}
	}
	s.enqueue(e)
}
func number(value any) *float64 {
	if value == nil {
		return nil
	}
	n, err := strconv.ParseFloat(fmt.Sprint(value), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}

type redactor struct{ secrets []string }

func newRedactor(secrets []string) *redactor {
	r := &redactor{}
	for _, s := range secrets {
		if len(s) > 0 {
			r.secrets = append(r.secrets, s)
		}
	}
	return r
}
func sensitiveKey(s string) bool {
	k := strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(s))
	for _, word := range []string{"token", "secret", "password", "passwd", "cookie", "authorization", "credential", "api_key", "apikey", "private_key", "client_id"} {
		if strings.Contains(k, word) {
			return true
		}
	}
	switch k {
	case "message", "data", "request", "response", "raw_payload", "provider_response", "content", "message_content", "discord_message", "raw_message", "raw", "body", "headers", "request_body", "response_body", "provider_payload", "payload":
		return true
	}
	return false
}

var urlPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
var quotedCredentialPattern = regexp.MustCompile(`(?i)["'](?:access_token|refresh_token|password|client_secret|client_id|authorization|cookie|token)["']\s*:\s*["'][^"']*["']`)
var credentialPattern = regexp.MustCompile(`(?i)(bearer\s+|basic\s+|(?:access_token|refresh_token|password|client_secret|client_id|authorization|cookie|token)\s*[=:]\s*(?:(?:bearer|basic)\s+)?)[^\s,;"']+`)
var jwtPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)

func (r *redactor) text(s string, limit int) string {
	// Bound work on untrusted diagnostic strings, then redact before truncating.
	if len(s) > 32768 {
		return "[omitted: oversized string]"
	}
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	s = quotedCredentialPattern.ReplaceAllString(s, "[redacted]")
	s = credentialPattern.ReplaceAllString(s, "[redacted]")
	s = jwtPattern.ReplaceAllString(s, "[redacted]")
	s = urlPattern.ReplaceAllStringFunc(s, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[redacted URL]"
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		u.RawFragment = ""
		path := strings.ToLower(u.Path)
		if strings.Contains(path, "/webhooks/") || strings.Contains(path, "/token/") {
			u.Path = "/[redacted]"
			u.RawPath = ""
		}
		return u.String()
	})
	if len(s) > limit {
		runes := []rune(s)
		if len(runes) > limit/4 {
			s = string(runes[:limit/4]) + "[truncated]"
		}
	}
	return s
}

type valueBudget struct{ nodes, bytes int }

func (b *valueBudget) text(r *redactor, s string) string {
	text := r.text(s, 4096)
	if len(text) > b.bytes {
		b.bytes = 0
		return "[omitted: value budget]"
	}
	b.bytes -= len(text)
	return strings.Clone(text)
}
func (r *redactor) value(value any, depth int, budget *valueBudget) any {
	if budget.nodes <= 0 || budget.bytes <= 0 {
		return "[omitted: value budget]"
	}
	budget.nodes--
	if depth > 4 {
		return "[omitted: depth]"
	}
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return budget.text(r, v)
	case bool:
		return v
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano)
	case time.Duration:
		return v.String()
	case json.RawMessage:
		return "[omitted: raw JSON]"
	case error:
		return budget.text(r, v.Error())
	case fmt.Stringer:
		return budget.text(r, v.String())

	}
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		n := v.Float()
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil
		}
		return n
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return r.value(v.Elem().Interface(), depth+1, budget)
	case reflect.Slice, reflect.Array:
		// Never persist raw byte buffers (provider bodies, cookies or message content).
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return "[omitted: bytes]"
		}
		out := make([]any, 0, min(v.Len(), 32))
		for i := 0; i < min(v.Len(), 32) && budget.nodes > 0 && budget.bytes > 0; i++ {
			out = append(out, r.value(v.Index(i).Interface(), depth+1, budget))
		}
		return out
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return "[omitted: map]"
		}
		out := map[string]any{}
		iter := v.MapRange()
		for n := 0; n < 32 && budget.nodes > 0 && budget.bytes > 0 && iter.Next(); n++ {
			key := iter.Key().String()
			if len(key) > 128 {
				out["[omitted: oversized key]"] = true
				continue
			}
			if sensitiveKey(key) {
				out[key] = "[redacted]"
			} else {
				out[key] = r.value(iter.Value().Interface(), depth+1, budget)
			}
		}
		return out
	}
	return "[omitted: unsupported value]"
}
