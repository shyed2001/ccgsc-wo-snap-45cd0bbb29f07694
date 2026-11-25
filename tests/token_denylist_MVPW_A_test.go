// Path: tests/token_denylist_MVPW_A_test.go | Author: cmdc for Claude Opus 5.5 | 2026-11-19
// What/why: MVPW slice A acceptance -- a denied token is refused on HTTP and gRPC,
// an unrevoked token still works, an expired entry is pruned, and the deny-list
// survives a restart on SQLite.
package tests

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
)

// quietLogger is the sink the shared fixture uses, kept here so the restart test
// can build an identical service without depending on core_test.go internals.
func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// denyRevoke POSTs the admin route and returns the decoded response.
func (f *fixture) denyRevoke(t *testing.T, admin, tokenID, reason string, want int) map[string]any {
	t.Helper()
	body := map[string]any{"tokenId": tokenID}
	if reason != "" {
		body["reason"] = reason
	}
	return decode[map[string]any](t, f.request(t, "POST", "/admin/v1/tokens:revoke", admin, body, want, nil))
}

// tokenInfo exchanges a raw key and returns the token plus its jti and kid.
func (f *fixture) tokenInfo(t *testing.T, raw string) (auth.Token, auth.Claims) {
	t.Helper()
	tok, e := f.auth.Exchange(context.Background(), raw, nil)
	if e != nil {
		t.Fatal(e)
	}
	c, e := f.auth.Validate(context.Background(), tok.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	return tok, c
}

// A denied jti is 401 over HTTP and the identical token becomes unusable
// everywhere at once, because both transports validate through auth.Validate.
func TestMVPWADeniedTokenIsRefusedHTTPAndGRPC(t *testing.T) {
	f := setup(t)
	raw, _ := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	tok, c := f.tokenInfo(t, raw)

	// The token works before the deny.
	f.request(t, "GET", "/thread-pool/v1/capabilities", tok.AccessToken, nil, 200, nil)

	got := f.denyRevoke(t, admin, c.ID, "leaked in a screenshot", 201)
	if got["tokenId"] != c.ID {
		t.Fatalf("revoked tokenId = %v, want %s", got["tokenId"], c.ID)
	}
	if got["reason"] != "leaked in a screenshot" {
		t.Fatalf("reason not echoed: %v", got["reason"])
	}

	// HTTP: the bearer branch of secure() calls Validate, so it is refused.
	f.request(t, "GET", "/thread-pool/v1/capabilities", tok.AccessToken, nil, 401, nil)

	// gRPC: claimsFromMetadata also calls Validate, so the same token is
	// Unauthenticated there without any gRPC-specific deny code.
	if _, e := f.auth.Validate(context.Background(), tok.AccessToken); e == nil {
		t.Fatal("denied token still validated: the gRPC path shares this call")
	}
}

// Denying one token id must not disturb any other token, even one issued to the
// same user from the same key.
func TestMVPWAUnrevokedTokenStillWorks(t *testing.T) {
	f := setup(t)
	raw, _ := f.key(t, "developer")
	_, admin := f.key(t, "admin")

	first, firstClaims := f.tokenInfo(t, raw)
	second, secondClaims := f.tokenInfo(t, raw)
	if firstClaims.ID == secondClaims.ID {
		t.Fatal("two exchanges produced the same jti")
	}

	f.denyRevoke(t, admin, firstClaims.ID, "one leak", 201)

	f.request(t, "GET", "/thread-pool/v1/capabilities", first.AccessToken, nil, 401, nil)
	f.request(t, "GET", "/thread-pool/v1/capabilities", second.AccessToken, nil, 200, nil)

	// The underlying key is untouched: exchanging again yields a usable token.
	fresh, _ := f.tokenInfo(t, raw)
	f.request(t, "GET", "/thread-pool/v1/capabilities", fresh.AccessToken, nil, 200, nil)
}

// An entry past its expiry neither refuses the token nor survives a prune.
func TestMVPWAExpiredEntryIsPruned(t *testing.T) {
	f := setup(t)
	raw, _ := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	tok, c := f.tokenInfo(t, raw)

	f.denyRevoke(t, admin, c.ID, "short-lived deny", 201)
	f.request(t, "GET", "/thread-pool/v1/capabilities", tok.AccessToken, nil, 401, nil)

	// Move the clock past the token's own expiry: both the token and the entry
	// are then dead, which is why the entry may be forgotten safely.
	// The admin token expires on the same 15-minute clock, so mint a fresh one
	// at the new time rather than reusing the stale one.
	f.auth.Now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	_, adminLater := f.key(t, "admin")

	// The listing with includeExpired shows it, flagged as expired.
	all := decode[struct {
		Items []struct {
			TokenID string `json:"tokenId"`
			Expired bool   `json:"expired"`
		} `json:"items"`
	}](t, f.request(t, "GET", "/admin/v1/tokens/revoked?includeExpired=true", adminLater, nil, 200, nil))
	found := false
	for _, item := range all.Items {
		if item.TokenID == c.ID {
			found = true
			if !item.Expired {
				t.Fatal("expired entry not flagged expired")
			}
		}
	}
	if !found {
		t.Fatal("expired entry missing from includeExpired listing")
	}

	// The default listing omits it: pending entries only.
	pending := decode[struct {
		Items []struct {
			TokenID string `json:"tokenId"`
		} `json:"items"`
	}](t, f.request(t, "GET", "/admin/v1/tokens/revoked", adminLater, nil, 200, nil))
	for _, item := range pending.Items {
		if item.TokenID == c.ID {
			t.Fatal("expired entry still listed as pending")
		}
	}

	pruned, e := f.auth.PruneDenylist(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if pruned < 1 {
		t.Fatalf("prune removed %d entries, want at least the one expired row", pruned)
	}

	// The row is physically gone, not merely hidden.
	var rows []store.Record
	if e := f.db.View(context.Background(), func(r store.Reader) error {
		var e error
		rows, e = r.List(store.TokenDenylist)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 0 {
		t.Fatalf("%d deny rows remain after prune, want 0", len(rows))
	}
}

// A prune must not remove an entry that is still in force.
func TestMVPWAPruneKeepsLiveEntries(t *testing.T) {
	f := setup(t)
	raw, _ := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	tok, c := f.tokenInfo(t, raw)

	f.denyRevoke(t, admin, c.ID, "still live", 201)
	pruned, e := f.auth.PruneDenylist(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if pruned != 0 {
		t.Fatalf("prune removed %d live entries, want 0", pruned)
	}
	f.request(t, "GET", "/thread-pool/v1/capabilities", tok.AccessToken, nil, 401, nil)
}

// The deny-list is persisted, so a restart against the same database file keeps it.
func TestMVPWADenylistSurvivesRestartSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")

	db, e := sqlite.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	a := auth.New(db, config.Default().JWTSecret, "s1")
	engine, e := pool.New(db, quietLogger(), "s1", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	_ = engine
	raw, _, e := a.CreateKey(context.Background(), auth.User{ID: "dev", AccountStore: "B", Roles: []string{"developer"}}, []string{"*"})
	if e != nil {
		t.Fatal(e)
	}
	tok, e := a.Exchange(context.Background(), raw, nil)
	if e != nil {
		t.Fatal(e)
	}
	c, e := a.Validate(context.Background(), tok.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.DenyToken(context.Background(), c.ID, "persist me", "tester", time.Time{}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Validate(context.Background(), tok.AccessToken); e == nil {
		t.Fatal("denied token validated before restart")
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}

	// A fresh process view of the same file.
	db2, e := sqlite.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer db2.Close()
	a2 := auth.New(db2, config.Default().JWTSecret, "s1")
	if _, e = a2.Validate(context.Background(), tok.AccessToken); e == nil {
		t.Fatal("deny-list did not survive restart: token valid again after reopen")
	}
	entries, e := a2.RevokedTokens(context.Background(), false)
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 1 || entries[0].TokenID != c.ID {
		t.Fatalf("entries after restart = %#v, want the one denied id", entries)
	}
	if entries[0].Reason != "persist me" {
		t.Fatalf("reason after restart = %q", entries[0].Reason)
	}
}

// Denying by key id (kid) also refuses every token minted from that key, which is
// what an operator reaches for when they only have the key, not the jti.
func TestMVPWADenyingKeyIDRefusesItsTokens(t *testing.T) {
	f := setup(t)
	raw, developer := f.key(t, "developer")
	_, admin := f.key(t, "admin")

	// Take the kid from the claims before denying: after the deny, Validate is
	// exactly the call that refuses, so it can no longer be used to read claims.
	c, e := f.auth.Validate(context.Background(), developer)
	if e != nil {
		t.Fatal(e)
	}
	kid := c.KeyID
	if kid == "" {
		t.Fatal("developer token has no kid")
	}

	f.denyRevoke(t, admin, kid, "key id deny", 201)
	f.request(t, "GET", "/thread-pool/v1/capabilities", developer, nil, 401, nil)

	// A token minted later from the same key is refused too: the deny is on the
	// key id, not on one jti.
	later, e := f.auth.Exchange(context.Background(), raw, nil)
	if e != nil {
		t.Fatal(e)
	}
	f.request(t, "GET", "/thread-pool/v1/capabilities", later.AccessToken, nil, 401, nil)

	// Exchange still succeeds (the key is not revoked), it just cannot be used.
	if _, e = f.auth.Validate(context.Background(), later.AccessToken); e == nil {
		t.Fatal("kid-denied token validated")
	}
}

// The route is admin-only and requires a scope, and an empty token id is refused
// rather than creating a useless row.
func TestMVPWADenyRouteRequiresAdminAndTokenID(t *testing.T) {
	f := setup(t)
	_, developer := f.key(t, "developer")
	_, admin := f.key(t, "admin")

	f.request(t, "POST", "/admin/v1/tokens:revoke", developer, map[string]any{"tokenId": "x"}, 403, nil)
	f.request(t, "POST", "/admin/v1/tokens:revoke", "", map[string]any{"tokenId": "x"}, 401, nil)

	// Empty / blank token id is a 400 with a problem body.
	f.request(t, "POST", "/admin/v1/tokens:revoke", admin, map[string]any{"tokenId": "   "}, 400, nil)
	// An unknown field is refused by Decode rather than silently ignored.
	f.request(t, "POST", "/admin/v1/tokens:revoke", admin, map[string]any{"tokenId": "x", "bogus": 1}, 400, nil)
	// A malformed expiresAt is a 400, never a guessed time.
	f.request(t, "POST", "/admin/v1/tokens:revoke", admin, map[string]any{"tokenId": "x", "expiresAt": "tomorrow"}, 400, nil)

	f.request(t, "GET", "/admin/v1/tokens/revoked", developer, nil, 403, nil)
	f.request(t, "GET", "/admin/v1/tokens/revoked", "", nil, 401, nil)
}

// The admin action is an audited security event on the structured log.
func TestMVPWADenyEmitsSecurityEvent(t *testing.T) {
	db, e := sqlite.Open(filepath.Join(t.TempDir(), "audit.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	buf := &logBuffer{}
	l := slog.New(slog.NewJSONHandler(buf, nil))
	engine, e := pool.New(db, l, "s1", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	a := auth.New(db, config.Default().JWTSecret, "s1")
	s := &api.Server{Engine: engine, Auth: a, Log: l}
	f := &fixture{db, engine, a, s.Handler()}

	raw, _ := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	_, c := f.tokenInfo(t, raw)

	f.denyRevoke(t, admin, c.ID, "audited", 201)

	if !buf.contains("security.token.revoked") {
		t.Fatalf("security event not logged; log=%s", buf.String())
	}
	if !buf.contains(c.ID) {
		t.Fatal("security event does not name the token id")
	}
}

// logBuffer is a mutex-guarded slog sink: the HTTP handler logs from the caller
// goroutine, but the maintenance and webhook paths log from others.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *logBuffer) contains(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), []byte(s))
}
