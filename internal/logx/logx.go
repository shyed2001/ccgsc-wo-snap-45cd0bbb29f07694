// Path: internal/logx/logx.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: always-on JSON security/request logging, daily rotation; WWPP-R25.1, R41.1.
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type DailyWriter struct {
	mu       sync.Mutex
	dir, day string
	file     *os.File
}

func (w *DailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	day := time.Now().UTC().Format("20060102")
	if w.file == nil || w.day != day {
		f, e := os.OpenFile(filepath.Join(w.dir, fmt.Sprintf("pools-core-%s.jsonl", day)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if e != nil {
			return 0, e
		}
		if w.file != nil {
			// A failed close of yesterday's file can drop the records still
			// buffered in it. slog.Default() writes to stderr, never back into
			// this writer, so the warning cannot recurse.
			if err := w.file.Close(); err != nil {
				slog.Default().Warn("logx.rotate_close_failed", "day", w.day, "error", err)
			}
		}
		w.file = f
		w.day = day
	}
	return w.file.Write(p)
}
func (w *DailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}
func New(dir string, debug bool) (*slog.Logger, io.Closer, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, nil, e
	}
	w := &DailyWriter{dir: dir}
	if _, e := w.Write(nil); e != nil {
		return nil, nil, e
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	configured, err := ParseModuleLevels(os.Getenv("POOLS_LOG_MODULE_LEVELS"))
	if err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	// The inner handler admits TRACE; the outer per-module handler enforces
	// the global fallback and each explicitly configured module override.
	jsonHandler := slog.NewJSONHandler(io.MultiWriter(os.Stdout, w), &slog.HandlerOptions{Level: slog.Level(-8)})
	return slog.New(NewModuleHandler(jsonHandler, level, configured)), w, nil
}
