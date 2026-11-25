// Path: internal/api/subject_revoke_W7_test.go | Author: cmdc for Claude Opus 5.5 | 2026-11-21
// What/why: W7 fix WO finding 2 - anonymous subject-level revoke.
//
// An anonymous contributor token is the only credential of its tab: there is no key
// row (so Service.Revoke cannot reach it) and every renewal mints a fresh jti for the
// same subject (so denying the presented jti is bypassed by the next renewal). The
// admin route POST /admin/v1/subjects:revoke denies the subject itself, which every
// validation checks through the token kid, so the whole renewal chain dies with it.
package api

import (
	"bytes"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w7Anonymous is the in-package fixture: a real handler over SQLite with the
// anonymous join enabled at its defaults.
type w7Anonymous struct {
	db      *sqlite.DB
	engine  *pool.Engine
	auth    *auth.Service
	handler http.Handler
}

func newW7Anonymous(t *testing.T) *w7Anonymous {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "w7_anon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	svc := auth.New(db, config.Default().JWTSecret, "s1")
	cfg := config.Default()
	cfg.AnonymousContributors = config.DefaultAnonymousContributorConfig()
	server := &Server{Engine: engine, Auth: svc, Log: logger, Config: cfg}
	return &w7Anonymous{db, engine, svc, server.Handler()}
}

// w7Call issues one request and asserts the status.
func (f *w7Anonymous) call(t *testing.T, method, path, token string, body any, want int, remote string) []byte {
	t.Helper()
	var raw []byte
	switch v := body.(type) {
	case nil:
	case []byte:
		raw = v
	default:
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if remote != "" {
		r.RemoteAddr = remote
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Body.Bytes()
}

// w7IssueAnonymous joins anonymously.
func (f *w7Anonymous) issue(t *testing.T, remote string) auth.Token {
	t.Helper()
	var tok auth.Token
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", "", nil, http.StatusOK, remote), &tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

// w7Credentials mints a raw key for role and returns the raw key and a bearer.
func (f *w7Anonymous) credentials(t *testing.T, role string) (string, string) {
	t.Helper()
	account := "B"
	if role == "admin" || role == "director" {
		account = "A"
	}
	raw, _, err := f.auth.CreateKey(context.Background(), auth.User{ID: auth.ID(), AccountStore: account, Roles: []string{role}}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.auth.Exchange(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw, tok.AccessToken
}

// w7Claims decodes the claims of a bearer without verifying it.
func w7Claims(t *testing.T, token string) auth.Claims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c auth.Claims
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestW7SubjectRevokeKillsRenewalChain is WO finding 2.
func TestW7SubjectRevokeKillsRenewalChain(t *testing.T) {
	f := newW7Anonymous(t)
	const remote = "198.51.100.110:4242"

	first := f.issue(t, remote)
	sub := w7Claims(t, first.AccessToken).Subject

	// The subject renews while it is not denied: this is the chain to cut.
	var renewed auth.Token
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", first.AccessToken, nil, http.StatusOK, remote), &renewed); err != nil {
		t.Fatal(err)
	}
	if got := w7Claims(t, renewed.AccessToken).Subject; got != sub {
		t.Fatalf("renewal subject = %s, want %s", got, sub)
	}

	_, admin := f.credentials(t, "admin")
	body := f.call(t, http.MethodPost, "/admin/v1/subjects:revoke", admin, map[string]any{
		"subject": sub,
		"reason":  "leaked bearer, subject level",
	}, http.StatusCreated, "")
	if !strings.Contains(string(body), sub) {
		t.Fatalf("subject revoke reply does not name the subject: %s", body)
	}

	// The presented bearer is refused at once on a strict route.
	f.call(t, http.MethodPost, "/thread-pool/v1/workers/register", renewed.AccessToken, map[string]any{"hostId": "revoked", "slots": 1}, http.StatusUnauthorized, remote)
	// And so is a token minted for the subject after the revoke: the deny is on the
	// subject (kid), which IssueAnonymous stamps on every token of the chain.
	later, err := f.auth.IssueAnonymous(sub, auth.ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.auth.Validate(context.Background(), later.AccessToken); err == nil {
		t.Fatal("a token of the revoked subject validated")
	}
	// The renewal route falls back to a fresh identity, which is a new subject.
	next := f.issueWithBearer(t, renewed.AccessToken, remote)
	if got := w7Claims(t, next.AccessToken).Subject; got == sub {
		t.Fatalf("revoked subject renewed itself: %s", got)
	}

	// The denied rows are visible to the operator who has to audit them.
	var listing struct {
		Items []struct {
			TokenID   string `json:"tokenId"`
			RevokedBy string `json:"revokedBy"`
			Reason    string `json:"reason"`
		} `json:"items"`
	}
	if err = json.Unmarshal(f.call(t, http.MethodGet, "/admin/v1/tokens/revoked", admin, nil, http.StatusOK, ""), &listing); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, item := range listing.Items {
		if item.TokenID != sub {
			continue
		}
		seen = true
		if item.Reason != "leaked bearer, subject level" {
			t.Fatalf("reason = %q", item.Reason)
		}
		if item.RevokedBy == "" {
			t.Fatal("the audit row has no actor")
		}
	}
	if !seen {
		t.Fatal("the subject deny entry is missing from the listing")
	}
}

// issueWithBearer presents a (possibly stale or revoked) bearer to the join route.
func (f *w7Anonymous) issueWithBearer(t *testing.T, bearer, remote string) auth.Token {
	t.Helper()
	var tok auth.Token
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", bearer, nil, http.StatusOK, remote), &tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestW7SubjectRevokeRequiresAdminAndAnonymousSubject covers the route guard.
func TestW7SubjectRevokeRequiresAdminAndAnonymousSubject(t *testing.T) {
	f := newW7Anonymous(t)
	anon := f.issue(t, "198.51.100.111:4242")
	sub := w7Claims(t, anon.AccessToken).Subject
	_, developer := f.credentials(t, "developer")
	_, admin := f.credentials(t, "admin")

	// Only a Store A admin with keys:write may revoke a subject.
	f.call(t, http.MethodPost, "/admin/v1/subjects:revoke", anon.AccessToken, map[string]any{"subject": sub}, http.StatusForbidden, "")
	f.call(t, http.MethodPost, "/admin/v1/subjects:revoke", developer, map[string]any{"subject": sub}, http.StatusForbidden, "")
	f.call(t, http.MethodPost, "/admin/v1/subjects:revoke", "", map[string]any{"subject": sub}, http.StatusUnauthorized, "")

	// The subject must be an anonymous one, so this primitive can never become a
	// wider deny than the caller asked for.
	f.call(t, http.MethodPost, "/admin/v1/subjects:revoke", admin, map[string]any{"subject": ""}, http.StatusBadRequest, "")
	f.call(t, http.MethodPost, "/admin/v1/subjects:revoke", admin, map[string]any{"subject": "developer-key"}, http.StatusBadRequest, "")
	f.call(t, http.MethodPost, "/admin/v1/subjects:revoke", admin, map[string]any{"subject": sub, "bogus": 1}, http.StatusBadRequest, "")
	f.call(t, http.MethodPost, "/admin/v1/subjects:revoke", admin, map[string]any{"subject": sub, "expiresAt": "tomorrow"}, http.StatusBadRequest, "")

	// The auth primitive enforces the same rule for any future caller.
	if _, err := f.auth.DenySubjectAndToken(context.Background(), "developer-key", "", "x", "tester", time.Time{}); err == nil {
		t.Fatal("DenySubjectAndToken accepted a stored key id")
	}
}
