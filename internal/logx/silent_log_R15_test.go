// Path: internal/logx/silent_log_R15_test.go | Author: cmdc | 2026-11-22
// R15-FIX-SILENT: logx.go:31 closed the previous day's file with `_ = Close`.
// A failed close can lose the audit records still buffered in that file, and the
// rotation is the only place that can report it. The warning goes through
// slog.Default() (stderr), never back into the DailyWriter.
package logx

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

// Microsoft Windows cannot delete an open file, so the rotation close failure is
// provoked by closing the handle behind the writer's back: the follow-up Close
// then answers "file already closed". That is a close failure like any other and
// it must be reported exactly once per rotation.
func TestR15RotationCloseFailureIsLogged(t *testing.T) {
	rec := &r15Records{}
	previous := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(previous) })

	dir, err := os.MkdirTemp("", "r15-logx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	w := &DailyWriter{dir: dir}
	if _, err := w.Write(nil); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "pools-core-"+time.Now().UTC().Format("20060102")+".jsonl")
	if w.file == nil {
		t.Fatal("the writer has no open file")
	}
	if err := w.file.Close(); err != nil {
		t.Fatal(err)
	}
	// A write on another day rotates: the old handle is closed again, which fails.
	w.day = "19990101"
	if _, err := os.Stat(first); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !rec.has("logx.rotate_close_failed") {
		t.Fatalf("the rotation close failure was dropped: %v", rec.msgs)
	}
}
