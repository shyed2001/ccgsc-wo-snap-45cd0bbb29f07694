// Path: internal/api/silent_log_R15_test.go | Author: cmdc | 2026-11-22
// R15-FIX-SILENT: the LOG sites of the classification must actually emit a
// warning instead of dropping the failure. These tests capture the handler of
// the Server and assert on the record that is written.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/redisadmin"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
	"ccgsc/poolscore/internal/store/sqlite"
)

// r15Logs collects the records of the logger under test.
type r15Logs struct {
	mu      sync.Mutex
	records []map[string]any
}

func (l *r15Logs) Enabled(context.Context, slog.Level) bool { return true }

func (l *r15Logs) Handle(_ context.Context, r slog.Record) error {
	row := map[string]any{"msg": r.Message, "level": r.Level.String()}
	r.Attrs(func(a slog.Attr) bool {
		row[a.Key] = a.Value.String()
		return true
	})
	l.mu.Lock()
	l.records = append(l.records, row)
	l.mu.Unlock()
	return nil
}

func (l *r15Logs) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *r15Logs) WithGroup(string) slog.Handler      { return l }

// has reports whether one record carries msg, and optionally one attr value.
func (l *r15Logs) has(msg, attr, value string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, row := range l.records {
		if row["msg"] != msg {
			continue
		}
		if attr == "" || row[attr] == value {
			return true
		}
	}
	return false
}

// r15Server builds a Server whose logger is the capture.
func r15Server(t *testing.T) (*Server, *r15Logs) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "r15_log.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logs := &r15Logs{}
	logger := slog.New(logs)
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.New(db, config.Default().JWTSecret, "s1")
	return &Server{Engine: engine, Auth: svc, Log: logger, Config: config.Default()}, logs
}

// TestR15RedisStartupApplyFailureIsLogged covers redis_admin_T91.go:59.
// InitializeRedisAdmin keeps its contract (startup never fails on an apply
// error), but the failure must leave a trace.
func TestR15RedisStartupApplyFailureIsLogged(t *testing.T) {
	s, logs := r15Server(t)
	// A dial that cannot connect fails Configure without a Redis service.
	s.RedisAdmin = redisadmin.New("127.0.0.1:1", "r15-user", "r15-password")
	s.Configs = &r15Configs{version: &pg.ConfigVersion{
		Page:      "thread-pool-admin",
		Version:   1,
		Config:    json.RawMessage(`{"redis.memory.fixed_gb":2}`),
		CreatedAt: time.Unix(10, 0),
	}}
	if err := s.InitializeRedisAdmin(context.Background()); err != nil {
		t.Fatalf("startup must keep serving leases: %v", err)
	}
	if !logs.has("redisadmin.startup_apply_failed", "", "") {
		t.Fatalf("the apply failure was dropped: %+v", logs.records)
	}
}

// TestR15LogoutRevokeFailureIsLogged covers admin_auth.go:287. The cookies are
// always cleared, so the surviving server-side session is what an operator has
// to hear about.
func TestR15LogoutRevokeFailureIsLogged(t *testing.T) {
	s, logs := r15Server(t)
	// The auth service owns its own store handle; a store that rejects the
	// session write is the failure to report.
	s.Auth.Store = r15FailingStore{}
	r := httptest.NewRequest(http.MethodPost, "/pools/auth/logout", nil)
	r.RemoteAddr = "203.0.113.7:1234"
	r.AddCookie(&http.Cookie{Name: "ccgsc_admin_session", Value: "r15-session"})
	r.AddCookie(&http.Cookie{Name: "ccgsc_csrf", Value: "r15-csrf"})
	r.Header.Set("X-CSRF-Token", "r15-csrf")
	w := httptest.NewRecorder()
	s.logoutEndpoint(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("logout = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !logs.has("admin.logout_revoke_failed", "ip", "203.0.113.7") {
		t.Fatalf("the failed revoke was dropped: %+v", logs.records)
	}
}

// TestR15MalformedLoginFormIsLogged covers admin_auth.go:197. A form body that
// cannot be parsed must not reach the credential check as a plain 401.
func TestR15MalformedLoginFormIsLogged(t *testing.T) {
	s, logs := r15Server(t)
	r := httptest.NewRequest(http.MethodPost, "/pools/auth/login", nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The form decoder reads the body; a broken reader is exactly the malformed
	// form this site has to report.
	r.Body = r15BrokenBody{}
	w := httptest.NewRecorder()
	s.loginEndpoint(w, r)
	if !logs.has("admin.login_form_invalid", "", "") {
		t.Fatalf("the malformed form was dropped: %+v", logs.records)
	}
}

// r15BrokenBody fails every read, which is what ParseForm reports.
type r15BrokenBody struct{}

func (r15BrokenBody) Read([]byte) (int, error) { return 0, errR15Store }
func (r15BrokenBody) Close() error             { return nil }

// TestR15RedisSavedApplyFailureIsLogged covers admin_config_F5.go:206. The PG
// version is committed and the 201 reply carries `redis` state; the apply error
// only has to be logged, never turned into a failed save.
func TestR15RedisSavedApplyFailureIsLogged(t *testing.T) {
	s, logs := r15Server(t)
	s.RedisAdmin = redisadmin.New("127.0.0.1:1", "r15-user", "r15-password")
	configs := &r15WriteConfigs{}
	s.Configs = configs
	session, err := s.Auth.CreateSession(context.Background(), &auth.AdminAccount{
		Username: "r15-admin", AccountStore: "A", Roles: []string{"admin"},
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims := auth.Claims{KeyID: "r15-key", User: auth.User{ID: "r15-admin", AccountStore: "A", Roles: []string{"admin"}}, Scopes: []string{"*"}}
	r := httptest.NewRequest(http.MethodPost, "/admin/v1/config/thread-pool-admin", strings.NewReader(`{"settings":{"redis.memory.fixed_gb":2},"reason":"r15"}`))
	r.SetPathValue("page", "thread-pool-admin")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "ccgsc_admin_session", Value: session.ID})
	r.Header.Set("X-CSRF-Token", session.CSRFToken)
	w := httptest.NewRecorder()
	s.writeAdminConfig(w, r, claims, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("save = %d, want 201 (the PG version is committed): %s", w.Code, w.Body.String())
	}
	if !logs.has("admin.redis_apply_failed", "page", "thread-pool-admin") {
		t.Fatalf("the Redis apply failure was dropped: %+v", logs.records)
	}
}

// r15WriteConfigs commits every save, so the apply branch is reached.
type r15WriteConfigs struct{ version *pg.ConfigVersion }

func (c *r15WriteConfigs) AdminAuthor(context.Context, string) (int64, error) { return 42, nil }
func (c *r15WriteConfigs) Latest(context.Context, string) (*pg.ConfigVersion, error) {
	return nil, pg.ErrVersionNotFound
}
func (c *r15WriteConfigs) Get(context.Context, string, int) (*pg.ConfigVersion, error) {
	return nil, pg.ErrVersionNotFound
}
func (c *r15WriteConfigs) List(context.Context, string, int) ([]pg.ConfigVersion, error) {
	return nil, nil
}
func (c *r15WriteConfigs) WriteAdminConfig(_ context.Context, page string, q pg.AdminConfigWrite, author int64, validate func(json.RawMessage) error) (*pg.ConfigVersion, error) {
	if err := validate(q.Settings); err != nil {
		return nil, err
	}
	c.version = &pg.ConfigVersion{Page: page, Version: 1, Config: q.Settings, AuthorID: author, Reason: q.Reason}
	return c.version, nil
}
func (c *r15WriteConfigs) Pin(context.Context, string, int, string, int64) error { return nil }
func (c *r15WriteConfigs) Unpin(context.Context, string, int, int64) error       { return nil }

// r15Configs serves one config version for the Redis startup path.
type r15Configs struct {
	version *pg.ConfigVersion
}

func (c *r15Configs) AdminAuthor(context.Context, string) (int64, error) { return 0, nil }
func (c *r15Configs) Get(context.Context, string, int) (*pg.ConfigVersion, error) {
	return nil, pg.ErrVersionNotFound
}
func (c *r15Configs) List(context.Context, string, int) ([]pg.ConfigVersion, error) {
	return nil, nil
}
func (c *r15Configs) WriteAdminConfig(context.Context, string, pg.AdminConfigWrite, int64, func(json.RawMessage) error) (*pg.ConfigVersion, error) {
	return nil, store.ErrNotImplemented
}
func (c *r15Configs) Pin(context.Context, string, int, string, int64) error { return nil }
func (c *r15Configs) Unpin(context.Context, string, int, int64) error       { return nil }

func (c *r15Configs) Latest(_ context.Context, page string) (*pg.ConfigVersion, error) {
	if c.version != nil && c.version.Page == page {
		return c.version, nil
	}
	return nil, pg.ErrVersionNotFound
}

// r15FailingStore rejects every write, which is how the revoke fails here.
type r15FailingStore struct{}

func (r15FailingStore) View(_ context.Context, fn func(store.Reader) error) error {
	return fn(r15FailingTx{})
}
func (r15FailingStore) Update(context.Context, func(store.Tx) error) error { return errR15Store }
func (r15FailingStore) Close() error                                       { return nil }

type r15FailingTx struct{}

func (r15FailingTx) Get(string, string, any) error       { return errR15Store }
func (r15FailingTx) List(string) ([]store.Record, error) { return nil, errR15Store }
func (r15FailingTx) Put(string, string, any) error       { return errR15Store }

var errR15Store = errors.New("r15: store unavailable")
