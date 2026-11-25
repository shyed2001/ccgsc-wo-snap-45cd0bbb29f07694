// Path: cmd/pools-core/silent_log_R15_test.go | Author: cmdc | 2026-11-22
// R15-FIX-SILENT: main.go:238 dropped the error of a scheduled Redis admin
// apply. Nothing polls Runner.Status on a timer, so without a record the only
// trace of a failed apply was the next status read a human happened to make.
package main

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"ccgsc/poolscore/internal/redisadmin"
)

type r15Records struct {
	mu   sync.Mutex
	msgs []string
}

func (r *r15Records) Enabled(context.Context, slog.Level) bool { return true }

func (r *r15Records) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, rec.Message)
	r.mu.Unlock()
	return nil
}

func (r *r15Records) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *r15Records) WithGroup(string) slog.Handler      { return r }

func (r *r15Records) has(msg string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.msgs {
		if got == msg {
			return true
		}
	}
	return false
}

// TestR15ScheduledRedisApplyFailureIsLogged runs the scheduled tick with an
// unreachable Redis: the apply fails and the tick must say so.
func TestR15ScheduledRedisApplyFailureIsLogged(t *testing.T) {
	rec := &r15Records{}
	previous := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(previous) })

	r := redisadmin.New("127.0.0.1:1", "r15-user", "r15-password")
	cfg := redisadmin.DefaultConfig()
	cfg.Mode = "dynamic"
	if err := r.Configure(context.Background(), cfg); err != nil {
		t.Logf("the immediate apply failed as expected: %v", err)
	}
	rec.msgs = nil
	applyRedisAdminTick(context.Background(), r)
	if !rec.has("redisadmin.scheduled_apply_failed") {
		t.Fatalf("the scheduled apply failure was dropped: %v", rec.msgs)
	}
}

// TestR15DisabledRedisApplyLogsNothing keeps the warning off the disabled path.
func TestR15DisabledRedisApplyLogsNothing(t *testing.T) {
	rec := &r15Records{}
	previous := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(previous) })

	r := redisadmin.New("", "", "")
	applyRedisAdminTick(context.Background(), r)
	if len(rec.msgs) != 0 {
		t.Fatalf("a disabled applier logged: %v", rec.msgs)
	}
}
