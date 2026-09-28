package command

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// newLogger returns a logger that never writes secret, even when an error
// message happens to contain it.
func newLogger(level, format, secret string, w io.Writer) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, Usagef("--log-level must be debug, info, warn, or error")
	}
	opts := &slog.HandlerOptions{Level: l}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "text":
		h = slog.NewTextHandler(w, opts)
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		return nil, Usagef("--log-format must be text or json")
	}
	if secret != "" {
		h = redactHandler{h, secret}
	}
	return slog.New(h), nil
}

// redactHandler replaces a secret in log messages and string attributes.
type redactHandler struct {
	slog.Handler
	secret string
}

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, h.scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.attr(a))
		return true
	})
	return h.Handler.Handle(ctx, clean)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.attr(a)
	}
	return redactHandler{h.Handler.WithAttrs(clean), h.secret}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{h.Handler.WithGroup(name), h.secret}
}

func (h redactHandler) scrub(s string) string { return strings.ReplaceAll(s, h.secret, "[REDACTED]") }

func (h redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.scrub(v.String()))
	case slog.KindGroup:
		group := v.Group()
		clean := make([]any, len(group))
		for i, g := range group {
			clean[i] = h.attr(g)
		}
		return slog.Group(a.Key, clean...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, h.scrub(err.Error()))
		}
	}
	return a
}
