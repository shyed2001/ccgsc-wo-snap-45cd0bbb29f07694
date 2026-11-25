// Path: internal/httpx/silent_log_R15_test.go | Author: cmdc | 2026-11-22
// R15-FIX-SILENT: httpx.go:20 and :25 wrote the body with `_ = Encode`; a
// client that vanished mid-response left no trace anywhere. The package has no
// logger of its own, so the warning goes through slog.Default(), which these
// tests capture.
package httpx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// r15Records collects the records the default logger writes.
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

// r15WithCapture points slog.Default() at the capture for one test.
func r15WithCapture(t *testing.T) *r15Records {
	t.Helper()
	rec := &r15Records{}
	previous := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return rec
}

// r15FailingWriter accepts the status line and then fails the body write, which
// is how a client that vanished looks to the encoder.
type r15FailingWriter struct {
	header http.Header
	status int
}

func (w *r15FailingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *r15FailingWriter) WriteHeader(status int) { w.status = status }
func (w *r15FailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("r15: client gone")
}

func TestR15JSONEncodeFailureIsLogged(t *testing.T) {
	rec := r15WithCapture(t)
	JSON(&r15FailingWriter{}, http.StatusOK, map[string]string{"status": "ok"})
	if !rec.has("httpx.encode_failed") {
		t.Fatalf("the encode failure was dropped: %v", rec.msgs)
	}
}

func TestR15ProblemEncodeFailureIsLogged(t *testing.T) {
	rec := r15WithCapture(t)
	r := httptest.NewRequest(http.MethodGet, "/thread-pool/v1/jobs/x", nil)
	Problem(&r15FailingWriter{}, r, http.StatusBadRequest, "invalid-request", "bad request")
	if !rec.has("httpx.encode_failed") {
		t.Fatalf("the encode failure was dropped: %v", rec.msgs)
	}
}

// TestR15SuccessfulEncodeDoesNotLog keeps the warning off the normal path.
func TestR15SuccessfulEncodeDoesNotLog(t *testing.T) {
	rec := r15WithCapture(t)
	w := httptest.NewRecorder()
	JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	Problem(w, httptest.NewRequest(http.MethodGet, "/x", bytes.NewReader(nil)), http.StatusBadRequest, "invalid-request", "bad")
	if len(rec.msgs) != 0 {
		t.Fatalf("a successful encode logged: %v", rec.msgs)
	}
}
