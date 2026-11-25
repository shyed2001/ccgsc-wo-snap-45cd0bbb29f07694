// Path: internal/auth/anonymous_T96.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-20
// What/why: the anonymous contributor join of WO_T96. A volunteer browser tab joins
// the pool without an id and a password: the server mints a short-lived contributor
// token for a synthetic store B identity. No API key is stored and nothing is
// written to the key store: the token carries its own jti and is checked against the
// deny-list on every validation, so an operator revokes it like any other token.
package auth

import (
	"context"
	"errors"
	"time"
)

// AnonymousIDPrefix marks a synthetic anonymous identity ("anon-<id>").
const AnonymousIDPrefix = "anon-"

// ContributorWorkerScopes are the scopes a contributor key carries in this codebase:
// the worker routes of internal/api gate on exactly this scope (role contributor,
// scope worker). The anonymous token gets these scopes and nothing else, so it can
// never reach a developer or an administrative route.
var ContributorWorkerScopes = []string{"worker"}

// ErrAnonymousIdentity is returned when an identity is not an anonymous one.
var ErrAnonymousIdentity = errors.New("anonymous identity must use the anon- prefix")

// IsAnonymousID reports whether the identity belongs to an anonymous contributor.
func IsAnonymousID(id string) bool {
	return len(id) > len(AnonymousIDPrefix) && id[:len(AnonymousIDPrefix)] == AnonymousIDPrefix
}

// AnonymousID builds the synthetic identity of one anonymous contributor session.
// The random part is the identity of the session, never a secret: it is the key id
// of the minted token and the owner of the tabs of that session.
func AnonymousID() string { return AnonymousIDPrefix + ID() }

// AnonymousRenewalGrace is the longest past-expiry a POST {pool}/workers/anonymous
// renewal may accept. It is not applied to leases, heartbeats or results.
// Hard ceiling 120 s: validate also requires iat >= now-1020 and a token lives
// 900 s, so a larger value would not extend the window (W7 review, cmdc).
const AnonymousRenewalGrace = 60 * time.Second

// ValidateAnonymousRenewal checks a bearer presented to the anonymous join route.
//
// The token must verify (signature, issuer, audience), must not be deny-listed,
// and must already be an anonymous subject. A token expired by at most grace is
// accepted here only, so a tab that renews just after expiry keeps its identity.
// grace is capped at AnonymousRenewalGrace. Any other token returns
// ErrUnauthorized and the caller mints a fresh identity.
func (s *Service) ValidateAnonymousRenewal(ctx context.Context, token string, grace time.Duration) (Claims, error) {
	if grace < 0 {
		grace = 0
	}
	if grace > AnonymousRenewalGrace {
		grace = AnonymousRenewalGrace
	}
	c, err := s.validate(ctx, token, grace)
	if err != nil || !c.Anonymous || !IsAnonymousID(c.Subject) {
		return Claims{}, ErrUnauthorized
	}
	return c, nil
}

// AnonymousStaleClaims returns the claims of a signed anonymous bearer that is
// past its renewal window (W7 fix: stale token at the live-tab cap).
//
// The join route may only fall back to a fresh identity under the tab cap. A tab
// that slept past its token therefore finds the cap filled by its own old
// registration and is refused until the engine reaps that record, although the
// bearer proves which registration it is. This helper identifies that registration:
// the signature, issuer, audience and anonymous identity shape must hold, and the
// token must be older than exp+AnonymousRenewalGrace, so a bearer inside the
// renewal window (or a forged, foreign or non-anonymous one) never takes the
// replacement path. The caller still applies its own caps after the replacement.
func (s *Service) AnonymousStaleClaims(token string) (Claims, error) {
	c, err := s.parse(token)
	if err != nil || !c.Anonymous || !IsAnonymousID(c.Subject) || !IsAnonymousID(c.KeyID) ||
		c.KeyID != c.Subject || c.Issuer != s.issuer || c.Audience != "pools-core" {
		return Claims{}, ErrUnauthorized
	}
	if !time.Unix(c.Expires, 0).Add(AnonymousRenewalGrace).Before(s.Now()) {
		return Claims{}, ErrUnauthorized
	}
	return c, nil
}

// IssueAnonymous mints a contributor token for a synthetic store B identity.
//
// The token has the same 15 minute lifetime and the same renewal path as an API key
// token, but no key is created: the jti is the identity of the token and the
// deny-list is read on every validation. The scopes are the contributor worker
// scopes of this codebase and nothing more.
// AnonymousSessionMax is the absolute lifetime of one anonymous session (T110).
// After it, the next renewal mints a fresh identity that passes the live-tab and
// rate limits again.
const AnonymousSessionMax = 24 * time.Hour

// IssueAnonymous mints a token for a new anonymous session.
func (s *Service) IssueAnonymous(id string, scopes []string) (Token, error) {
	return s.IssueAnonymousSession(id, scopes, time.Time{})
}

// IssueAnonymousSession mints a token that ends no later than sessionEnd. A zero
// sessionEnd starts a new session of AnonymousSessionMax.
func (s *Service) IssueAnonymousSession(id string, scopes []string, sessionEnd time.Time) (Token, error) {
	if !IsAnonymousID(id) {
		return Token{}, ErrAnonymousIdentity
	}
	if len(scopes) == 0 {
		scopes = ContributorWorkerScopes
	}
	now := s.Now()
	if sessionEnd.IsZero() {
		sessionEnd = now.Add(AnonymousSessionMax)
	}
	exp := now.Add(tokenLifetime)
	if sessionEnd.Before(exp) {
		exp = sessionEnd
	}
	u := User{ID: id, AccountStore: "B", Roles: []string{"contributor"}}
	c := Claims{
		Subject: id, Issuer: s.issuer, Audience: "pools-core", ID: ID(), KeyID: id,
		IssuedAt: now.Unix(), Expires: exp.Unix(),
		User: u, Scopes: scopes, Anonymous: true, SessionExpires: sessionEnd.Unix(),
	}
	return s.sign(c), nil
}
