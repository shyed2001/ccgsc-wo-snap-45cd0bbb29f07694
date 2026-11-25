package logx

// F5: per-module structured JSON log level selection. This is a filter,
// not a credential-bearing configuration endpoint. It preserves the existing
// global Debug/Info fallback unless an operator explicitly opts into overrides.
import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

var ErrInvalidModuleLevels = errors.New("invalid per-module logging levels")
var moduleNameF5 = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// ParseModuleLevels parses POOLS_LOG_MODULE_LEVELS:
// "admin=warn,recompute=debug,api=error". A module is the prefix of a
// structured event message before its first dot, or its slog "module" field.
func ParseModuleLevels(raw string) (map[string]slog.Level, error) {
	out := make(map[string]slog.Level)
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		parts := strings.Split(strings.TrimSpace(item), "=")
		if len(parts) != 2 {
			return nil, fmt.Errorf("%w: expected module=level", ErrInvalidModuleLevels)
		}
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if !moduleNameF5.MatchString(name) {
			return nil, fmt.Errorf("%w: invalid module identifier", ErrInvalidModuleLevels)
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("%w: duplicate module", ErrInvalidModuleLevels)
		}
		var lv slog.Level
		switch strings.ToLower(strings.TrimSpace(parts[1])) {
		case "trace":
			lv = slog.Level(-8)
		case "debug":
			lv = slog.LevelDebug
		case "info":
			lv = slog.LevelInfo
		case "warn", "warning":
			lv = slog.LevelWarn
		case "error":
			lv = slog.LevelError
		default:
			return nil, fmt.Errorf("%w: unsupported level", ErrInvalidModuleLevels)
		}
		out[name] = lv
	}
	return out, nil
}

type moduleHandlerF5 struct {
	next         slog.Handler
	defaultLevel slog.Level
	overrides    map[string]slog.Level
	fixedModule  string
	minimum      slog.Level
}

// NewModuleHandler wraps a slog handler whose minimum level must be TRACE or
// lower. It never sends a suppressed event to next.Handle. Without a module
// field, the message prefix ("admin.login") is its module.
func NewModuleHandler(next slog.Handler, defaultLevel slog.Level, levels map[string]slog.Level) slog.Handler {
	cp := make(map[string]slog.Level, len(levels))
	min := defaultLevel
	for key, v := range levels {
		cp[strings.ToLower(key)] = v
		if v < min {
			min = v
		}
	}
	return &moduleHandlerF5{next: next, defaultLevel: defaultLevel, overrides: cp, minimum: min}
}
func (h *moduleHandlerF5) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minimum
}
func (h *moduleHandlerF5) Handle(ctx context.Context, rec slog.Record) error {
	module := h.fixedModule
	if module == "" {
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "module" && a.Value.Kind() == slog.KindString {
				module = strings.ToLower(a.Value.String())
				return false
			}
			return true
		})
	}
	if module == "" {
		msg := rec.Message
		if i := strings.IndexByte(msg, '.'); i > 0 {
			msg = msg[:i]
		}
		module = strings.ToLower(msg)
	}
	threshold := h.defaultLevel
	if v, ok := h.overrides[module]; ok {
		threshold = v
	}
	if rec.Level < threshold {
		return nil
	}
	return h.next.Handle(ctx, rec)
}
func (h *moduleHandlerF5) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.next = h.next.WithAttrs(attrs)
	for _, a := range attrs {
		if a.Key == "module" && a.Value.Kind() == slog.KindString {
			next.fixedModule = strings.ToLower(a.Value.String())
		}
	}
	return &next
}
func (h *moduleHandlerF5) WithGroup(name string) slog.Handler {
	next := *h
	next.next = h.next.WithGroup(name)
	return &next
}
