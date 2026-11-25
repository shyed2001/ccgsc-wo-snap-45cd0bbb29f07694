package logx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestF5ParsePerModuleVerbosity(t *testing.T) {
	got, err := ParseModuleLevels("admin=warn,recompute=DEBUG,api=error")
	if err != nil {
		t.Fatal(err)
	}
	if got["admin"] != slog.LevelWarn || got["recompute"] != slog.LevelDebug || got["api"] != slog.LevelError {
		t.Fatalf("module levels not normalized: %#v", got)
	}
	for _, bad := range []string{"admin=unknown", "api", "auth=trace,auth=debug", "../unsafe=warn", "admin="} {
		if _, err := ParseModuleLevels(bad); err == nil {
			t.Errorf("accepted invalid %q", bad)
		}
	}
}

func TestF5ModuleVerbositySeparatesEvents(t *testing.T) {
	var out bytes.Buffer
	sink := slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.Level(-8)})
	h := NewModuleHandler(sink, slog.LevelInfo, map[string]slog.Level{"admin": slog.LevelWarn, "recompute": slog.LevelDebug})
	l := slog.New(h)
	l.Info("admin.login")
	l.Warn("admin.loginFailure")
	l.Debug("recompute.notify")
	l.Debug("api.request")
	l.Info("api.request")
	if err := l.Handler().Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelDebug, "admin.hidden", 0)); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"admin.loginFailure", "recompute.notify", "api.request"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s: %s", want, s)
		}
	}
	for _, notWant := range []string{`"msg":"admin.login"`, "admin.hidden"} {
		if strings.Contains(s, notWant) {
			t.Errorf("unexpected %s", notWant)
		}
	}
}

func TestF5MalformedModuleLevelFailsLoggerStartup(t *testing.T) {
	t.Setenv("POOLS_LOG_MODULE_LEVELS", "admin=not-a-level")
	_, _, err := New(t.TempDir(), false)
	if !errors.Is(err, ErrInvalidModuleLevels) {
		t.Fatalf("malformed level did not fail closed: %v", err)
	}
}
