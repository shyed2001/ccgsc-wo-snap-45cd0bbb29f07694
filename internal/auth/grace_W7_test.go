// Path: internal/auth/grace_W7_test.go | Author: cmdc for Claude Opus 5.5 | 2026-11-21
// What/why: W7 fixes of the anonymous renewal grace window and its deny-list interaction.
//
// Every property here was a review row against 56db3f75:
//   - WO finding 1 / agy HIGH: a deny written DURING the grace window was invisible,
//     because validate replaced the lookup time with exp-1s instead of taking
//     max(now, exp-1s).
//   - agy LOW: `c.Expires < now-grace` accepted a token at exactly exp+grace, so the
//     documented 60 s window was 61 s (60.999 s with the second-floored clock, cmdc LOW).
//   - cmdc LOW: the grace branch lived in the generic validate() gated only by the
//     argument, so a stored-key token could take it.
//   - agy LOW test gap: no test covered "revoked during grace, then pruned".
//
// The tests call the private validate() and the public deny/prune primitives of the
// package, with the service clock pinned to each interesting instant.
package auth

import (
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w7Auth is the minimal in-package harness: one SQLite store and one service.
type w7Auth struct {
	svc *Service
	db  *sqlite.DB
}

func newW7Auth(t *testing.T) *w7Auth {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "auth_w7.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &w7Auth{svc: New(db, config.Default().JWTSecret, "s1"), db: db}
}

// at pins the service clock.
func (a *w7Auth) at(t time.Time) { a.svc.Now = func() time.Time { return t } }

// w7Claims decodes the claims of a signed bearer without verifying it.
func w7Claims(t *testing.T, token string) Claims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c Claims
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// token issues a stored-key developer token, which is the shape the grace must refuse.
func (a *w7Auth) keyToken(t *testing.T) Token {
	t.Helper()
	raw, _, err := a.svc.CreateKey(context.Background(), User{ID: "dev", AccountStore: "B", Roles: []string{"developer"}}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.svc.Exchange(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestW7GraceIsAnonymousOnly is the cmdc LOW row: the window belongs to anonymous
// claims, so a stored-key token expired by 30 s is refused even when the caller
// passes a full grace.
func TestW7GraceIsAnonymousOnly(t *testing.T) {
	a := newW7Auth(t)
	keyTok := a.keyToken(t)
	anonTok, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}

	keyExp := w7Claims(t, keyTok.AccessToken).Expires
	anonExp := w7Claims(t, anonTok.AccessToken).Expires
	if keyExp != anonExp {
		t.Fatalf("fixture expiry mismatch: key %d anon %d", keyExp, anonExp)
	}
	a.at(time.Unix(keyExp, 0).Add(30 * time.Second))

	if _, err = a.svc.validate(context.Background(), keyTok.AccessToken, AnonymousRenewalGrace); err == nil {
		t.Fatal("a stored-key token was accepted through the anonymous grace window")
	}
	if _, err = a.svc.validate(context.Background(), anonTok.AccessToken, AnonymousRenewalGrace); err != nil {
		t.Fatalf("an anonymous claim was refused inside its grace window: %v", err)
	}
	// Without a grace argument the strict rule applies to the anonymous claim too.
	if _, err = a.svc.validate(context.Background(), anonTok.AccessToken, 0); err == nil {
		t.Fatal("an expired anonymous claim was accepted without a grace argument")
	}
}

// TestW7GraceWindowBoundary is the agy LOW off-by-one row and the cmdc LOW clock row.
//
// The window is [exp, exp+60s) measured on a time.Time: 59.999 s inside is accepted,
// exactly exp+60s and 1 s later are refused. `c.Expires < now-grace` accepted the
// boundary, and the second-floored clock stretched the window to 60.999 s.
func TestW7GraceWindowBoundary(t *testing.T) {
	a := newW7Auth(t)
	tok, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Unix(w7Claims(t, tok.AccessToken).Expires, 0)

	for _, tc := range []struct {
		name  string
		at    time.Time
		allow bool
	}{
		{"1 s past exp, inside the window", exp.Add(time.Second), true},
		{"59.999 s, the last moment of the window", exp.Add(AnonymousRenewalGrace - time.Millisecond), true},
		{"exactly exp+grace", exp.Add(AnonymousRenewalGrace), false},
		{"1 s past the window", exp.Add(AnonymousRenewalGrace + time.Second), false},
	} {
		a.at(tc.at)
		_, err := a.svc.validate(context.Background(), tok.AccessToken, AnonymousRenewalGrace)
		if tc.allow && err != nil {
			t.Fatalf("%s: refused: %v", tc.name, err)
		}
		if !tc.allow && err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
	}
}

// TestW7DenyDuringGraceRejectsToken is WO finding 1 and the agy HIGH row, in both
// directions the review names:
//
//  1. "max(now, exp-1s)": a deny written DURING the window must be seen, so the
//     reference is the present and not the token expiry alone.
//  2. "exp-1s": a deny in force at the token expiry must keep holding through the
//     window even though its own ExpiresAt is already past.
func TestW7DenyDuringGraceRejectsToken(t *testing.T) {
	a := newW7Auth(t)
	ctx := context.Background()

	// Half 1: the deny is written 30 s after exp, with the default lifetime the admin
	// route uses when the operator omits expiresAt.
	during, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	duringClaims := w7Claims(t, during.AccessToken)
	a.at(time.Unix(duringClaims.Expires, 0).Add(30 * time.Second))
	if _, err = a.svc.DenyToken(ctx, duringClaims.ID, "revoked during grace", "tester", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.svc.validate(ctx, during.AccessToken, AnonymousRenewalGrace); err == nil {
		t.Fatal("a deny written during grace did not reject the token")
	}
	// Control: another subject at the same clock still validates, so the refusal is
	// the deny and not the clock.
	control, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.svc.validate(ctx, control.AccessToken, AnonymousRenewalGrace); err != nil {
		t.Fatalf("an unrelated anonymous token was refused at the same clock: %v", err)
	}

	// Half 2: the deny is written before exp and expires at exp, so only the
	// exp-1s reference still sees it inside the window.
	before, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	beforeClaims := w7Claims(t, before.AccessToken)
	beforeExp := time.Unix(beforeClaims.Expires, 0)
	a.at(beforeExp.Add(-30 * time.Second))
	if _, err = a.svc.DenyToken(ctx, beforeClaims.ID, "revoked before exp", "tester", beforeExp); err != nil {
		t.Fatal(err)
	}
	a.at(beforeExp.Add(30 * time.Second))
	if _, err = a.svc.validate(ctx, before.AccessToken, AnonymousRenewalGrace); err == nil {
		t.Fatal("a deny in force at exp stopped being honoured inside the grace window")
	}
}

// TestW7DenyEntrySurvivesPruneThroughGrace is the agy test-gap row: prune must keep
// a deny through the grace window, or a prune right after exp would let a revoked
// token renew.
func TestW7DenyEntrySurvivesPruneThroughGrace(t *testing.T) {
	a := newW7Auth(t)
	ctx := context.Background()
	tok, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	c := w7Claims(t, tok.AccessToken)
	exp := time.Unix(c.Expires, 0)
	if _, err = a.svc.DenyToken(ctx, c.ID, "grace prune", "tester", exp); err != nil {
		t.Fatal(err)
	}

	a.at(exp.Add(30 * time.Second))
	pruned, err := a.svc.PruneDenylist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 0 {
		t.Fatalf("prune removed %d entries while the grace window was open, want 0", pruned)
	}
	var entry DenyEntry
	if err = a.db.View(ctx, func(r store.Reader) error { return r.Get(store.TokenDenylist, c.ID, &entry) }); err != nil {
		t.Fatalf("the deny row is gone inside the grace window: %v", err)
	}

	a.at(exp.Add(AnonymousRenewalGrace + time.Second))
	if pruned, err = a.svc.PruneDenylist(ctx); err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("prune removed %d entries past the grace window, want 1", pruned)
	}
	if err = a.db.View(ctx, func(r store.Reader) error { return r.Get(store.TokenDenylist, c.ID, &entry) }); err == nil || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the deny row survived a prune past the grace window: %v", err)
	}
}
