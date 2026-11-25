// Path: internal/auth/r10_fix_test.go | R10 fix lane (web review of fe646643).
// What/why: the DeepSeek MAJOR/MINOR rows against the anonymous renewal grace and
// its deny-list. Every test here is written to be red on the unfixed tree:
//
//	d10 (MAJOR, auth.go): the grace probe was only exp-1s, so a deny whose
//	    RevokedAt lands in (exp-1s, exp] was invisible to renewal.
//	d11 (MAJOR, denylist_MVPW.go): ConsumeRenewal checked only the jti row, so a
//	    subject revoke between validate and consume still minted a token.
//	d12 (MINOR, denylist_MVPW.go): the consume row was clamped to now+tokenLifetime,
//	    which shortens it below the token exp under a +120 s iat skew.
//	d13 (MINOR, auth.go): sign hardcoded ExpiresIn 900, so a session-capped token
//	    reported a lifetime it does not have.
package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestR10DenyInLastSecondBeforeExpRejectsGraceRenewal is the d10 row.
//
// A deny written 500 ms before exp is in force at exp, but the old single
// exp-1s probe asked only about that instant: IsDenied requires RevokedAt <=
// at, so the entry was invisible and the subject renewed into a fresh token.
func TestR10DenyInLastSecondBeforeExpRejectsGraceRenewal(t *testing.T) {
	a := newW7Auth(t)
	ctx := context.Background()
	tok, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	c := w7Claims(t, tok.AccessToken)
	exp := time.Unix(c.Expires, 0)
	a.at(exp.Add(-500 * time.Millisecond))
	if _, err = a.svc.DenySubjectAndToken(ctx, c.Subject, "", "late", "tester", exp); err != nil {
		t.Fatal(err)
	}
	a.at(exp.Add(30 * time.Second))
	if _, err = a.svc.ValidateAnonymousRenewal(ctx, tok.AccessToken, AnonymousRenewalGrace); err == nil {
		t.Fatal("a subject revoked in the last second before exp renewed")
	}
}

// TestR10SubjectRevokeRaceWithConsume is the d11 row.
//
// The renewal handler validates the bearer, then consumes the jti. A subject
// revoke in between is not seen by a jti-only PutIfAbsent, and the revoked
// subject gets a fresh token.
func TestR10SubjectRevokeRaceWithConsume(t *testing.T) {
	a := newW7Auth(t)
	ctx := context.Background()
	tok, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	c := w7Claims(t, tok.AccessToken)
	exp := time.Unix(c.Expires, 0)
	if _, err = a.svc.ValidateAnonymousRenewal(ctx, tok.AccessToken, AnonymousRenewalGrace); err != nil {
		t.Fatalf("renewal validation: %v", err)
	}
	if _, err = a.svc.DenySubjectAndToken(ctx, c.Subject, "", "race", "tester", exp); err != nil {
		t.Fatal(err)
	}
	if err = a.svc.ConsumeRenewal(ctx, c.ID, c.Subject, exp); err == nil {
		t.Fatal("ConsumeRenewal accepted a renewal for a just-revoked subject")
	}
}

// TestR10ConsumeRowOutlivesTokenUnderSkew is the d12 row.
//
// A minter whose clock runs 120 s ahead mints a token whose exp is still in the
// future when the server consumes it. Clamping the row to now+tokenLifetime
// shortens it below that exp, so a prune between the clamp and the real exp
// forgets the single-use mark and a second renewal succeeds.
func TestR10ConsumeRowOutlivesTokenUnderSkew(t *testing.T) {
	a := newW7Auth(t)
	ctx := context.Background()
	T := time.Now()
	a.at(T.Add(120 * time.Second))
	tok, err := a.svc.IssueAnonymous(AnonymousID(), ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	c := w7Claims(t, tok.AccessToken)
	exp := time.Unix(c.Expires, 0)
	a.at(T)
	if err = a.svc.ConsumeRenewal(ctx, c.ID, c.Subject, exp); err != nil {
		t.Fatal(err)
	}
	a.at(exp.Add(-30 * time.Second))
	if _, err = a.svc.PruneDenylist(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.svc.ConsumeRenewal(ctx, c.ID, c.Subject, exp); !errors.Is(err, ErrRenewalReused) {
		t.Fatalf("second renewal accepted after prune: %v", err)
	}
}

// TestR10SessionCappedExpiresIn is the d13 row.
//
// A token capped by the session end must report the lifetime it really has, or
// the tab schedules its renewal after the token is already dead.
func TestR10SessionCappedExpiresIn(t *testing.T) {
	a := newW7Auth(t)
	now := time.Now()
	a.at(now)
	tok, err := a.svc.IssueAnonymousSession(AnonymousID(), ContributorWorkerScopes, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if tok.ExpiresIn < 295 || tok.ExpiresIn > 300 {
		t.Fatalf("ExpiresIn = %d, want ~300", tok.ExpiresIn)
	}
}
