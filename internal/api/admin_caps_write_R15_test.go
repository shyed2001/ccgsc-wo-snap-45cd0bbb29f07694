// Path: internal/api/admin_caps_write_R15_test.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS Addendum (G-R15-3) - the caps must be EDITABLE in the admin
// panel (the server pools settings section), not only read-only.
//
// The rules of the addendum:
//   - the admin role only (a developer is refused),
//   - the values are validated and clamped (never a silent failure),
//   - the change is audit-logged,
//   - maxProcs and memLimitMB apply LIVE (runtime.GOMAXPROCS /
//     debug.SetMemoryLimit),
//   - workerPool and dbMaxOpenConns may need a restart, so the reply says so.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"ccgsc/poolscore/internal/store"
)

// capsWriteCall writes the caps and returns the recorded response.
func capsWriteCall(t *testing.T, handler http.Handler, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "http://s/admin/v1/caps", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestAdminCapsWriteIsBehindTheAdminRole pins the role gate of the write: an
// anonymous caller and a developer are refused, only an admin writes.
func TestAdminCapsWriteIsBehindTheAdminRole(t *testing.T) {
	handler, authService := capsHarness(t)
	// Anonymous.
	if rec := capsWriteCall(t, handler, "", `{"maxProcs":2}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}
	// Developer (a developer key lives in store B).
	developer := bearerKey(t, authService, "B", "developer")
	if rec := capsWriteCall(t, handler, developer, `{"maxProcs":2}`); rec.Code != http.StatusForbidden {
		t.Fatalf("developer status = %d, want 403", rec.Code)
	}
}

// TestAdminCapsWriteAppliesMaxProcsLive pins that maxProcs applies live: the
// runtime really changes, and the reply carries the new value.
func TestAdminCapsWriteAppliesMaxProcsLive(t *testing.T) {
	handler, authService := capsHarness(t)
	old := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	admin := bearerKey(t, authService, "A", "admin")
	rec := capsWriteCall(t, handler, admin, `{"maxProcs":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := runtime.GOMAXPROCS(0); got != 2 {
		t.Fatalf("GOMAXPROCS = %d, want 2 (the write applies live)", got)
	}
	var body struct {
		MaxProcs int  `json:"maxProcs"`
		Applied  bool `json:"applied"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.MaxProcs != 2 {
		t.Fatalf("maxProcs = %d, want 2", body.MaxProcs)
	}
	if !body.Applied {
		t.Fatal("the reply does not say that the write applied")
	}
}

// TestAdminCapsWriteAppliesMemLimitLive pins that memLimitMB applies live through
// debug.SetMemoryLimit.
func TestAdminCapsWriteAppliesMemLimitLive(t *testing.T) {
	handler, authService := capsHarness(t)
	old := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(old) })
	admin := bearerKey(t, authService, "A", "admin")
	rec := capsWriteCall(t, handler, admin, `{"memLimitMB":512}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := debug.SetMemoryLimit(-1); got != int64(512)<<20 {
		t.Fatalf("the memory limit = %d, want %d (the write applies live)", got, int64(512)<<20)
	}
}

// TestAdminCapsWriteClampsTheValues pins the clamp rule of the write: a value that
// would take every core is clamped, never refused, and the reply carries the
// clamped value.
func TestAdminCapsWriteClampsTheValues(t *testing.T) {
	handler, authService := capsHarness(t)
	old := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	admin := bearerKey(t, authService, "A", "admin")
	n := runtime.NumCPU()
	rec := capsWriteCall(t, handler, admin, `{"maxProcs":9999}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		MaxProcs int `json:"maxProcs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.MaxProcs >= n && n > 1 {
		t.Fatalf("maxProcs = %d takes every core of %d", body.MaxProcs, n)
	}
	if body.MaxProcs < 1 {
		t.Fatalf("maxProcs = %d, want at least 1", body.MaxProcs)
	}
}

// TestAdminCapsWriteRefusesInvalidValues pins that an invalid value is a clear
// error and never a silent fallback.
func TestAdminCapsWriteRefusesInvalidValues(t *testing.T) {
	handler, authService := capsHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	for _, tc := range []struct {
		name string
		body string
	}{
		{"a negative maxProcs", `{"maxProcs":-1}`},
		{"a negative memLimitMB", `{"memLimitMB":-1}`},
		{"a negative workerPool", `{"workerPool":-1}`},
		{"a negative dbMaxOpenConns", `{"dbMaxOpenConns":-1}`},
		{"a non numeric maxProcs", `{"maxProcs":"many"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := capsWriteCall(t, handler, admin, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdminCapsWriteSaysWhatNeedsARestart pins the addendum rule: workerPool and
// dbMaxOpenConns may need a restart, so the reply says so.
func TestAdminCapsWriteSaysWhatNeedsARestart(t *testing.T) {
	handler, authService := capsHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	rec := capsWriteCall(t, handler, admin, `{"workerPool":3,"dbMaxOpenConns":9}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Applied     bool     `json:"applied"`
		PendingList []string `json:"pendingRestart"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.PendingList) == 0 {
		t.Fatal("the reply does not name the fields that need a restart")
	}
	joined := strings.Join(body.PendingList, ",")
	if !strings.Contains(joined, "workerPool") || !strings.Contains(joined, "dbMaxOpenConns") {
		t.Fatalf("the pending restart list = %v, want workerPool and dbMaxOpenConns", body.PendingList)
	}
}

// TestAdminCapsWriteIsAuditLogged pins that every write leaves an audit row with
// the actor, so the operator can see who changed the caps.
func TestAdminCapsWriteIsAuditLogged(t *testing.T) {
	handler, authService, engine := capsHarnessWithEngine(t)
	admin := bearerKey(t, authService, "A", "admin")
	rec := capsWriteCall(t, handler, admin, `{"maxProcs":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var found bool
	if err := engine.Store.View(t.Context(), func(r store.Reader) error {
		rows, err := r.List(store.Audit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			raw := row.Value
			if strings.Contains(string(raw), "caps") && strings.Contains(string(raw), "admin-user") {
				found = true
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read the audit bucket: %v", err)
	}
	if !found {
		t.Fatal("no audit row carries the caps write and its actor")
	}
}

// TestAdminCapsWriteRefusesAnEmptyBody pins that a write without any known field
// changes nothing.
func TestAdminCapsWriteRefusesAnEmptyBody(t *testing.T) {
	handler, authService := capsHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	rec := capsWriteCall(t, handler, admin, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
