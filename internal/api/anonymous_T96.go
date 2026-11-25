// Path: internal/api/anonymous_T96.go | Author: agy Gemini 3.8 Flash high for Claude Opus 5.5 | 2026-11-20
// What/why: anonymous contributor join endpoint POST {pool}/workers/anonymous; WO_T96.
// WO_T96B: a bearer that still validates as anonymous is re-issued for the same
// subject. That renewal keeps the per-IP issue rate and does not spend the
// live-tab cap. A token expired by at most 60s is accepted for this route only.
package api

import (
	"errors"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"net/http"
	"strings"
	"time"
)

func (s *Server) anonymousRateLimiter() *auth.RateLimiter {
	s.anonOnce.Do(func() {
		limit := s.Config.AnonymousContributors.IssueRatePerMinute
		if limit <= 0 {
			limit = 10
		}
		s.anonLimit = auth.NewRateLimiter(limit, time.Minute)
	})
	return s.anonLimit
}

func (s *Server) anonymousWorkerRoute(w http.ResponseWriter, r *http.Request, p string) {
	if !s.Config.AnonymousContributors.Enabled {
		httpx.Problem(w, r, http.StatusForbidden, "anonymous-disabled", "Anonymous contributor join is disabled.")
		return
	}
	ip := s.clientIP(r)
	rl := s.anonymousRateLimiter()
	if !rl.AllowAndRecord(ip) {
		w.Header().Set("Retry-After", "60")
		httpx.Problem(w, r, http.StatusTooManyRequests, "rate-limit-exceeded", "Too many anonymous token requests. Please retry later.")
		return
	}

	id, old, renewed := s.anonymousRenewalID(r)
	var sessionEnd time.Time
	if renewed {
		// T110: the absolute session deadline travels with the chain. A token
		// minted before T110 (no sxp) starts its deadline now.
		if old.SessionExpires > 0 {
			sessionEnd = time.Unix(old.SessionExpires, 0)
		}
		if !sessionEnd.IsZero() && !time.Now().Before(sessionEnd) {
			renewed = false
		}
	}
	if renewed {
		// W7 review (qwen): consume the presented jti BEFORE minting, atomically.
		// A reused bearer (concurrent fork, copied token, lost-response replay)
		// does not renew; it falls through to the fresh-identity path and its
		// limits. A store error fails closed: no token is issued.
		switch err := s.Auth.ConsumeRenewal(r.Context(), old.ID, id, time.Unix(old.Expires, 0)); {
		case err == nil:
		case errors.Is(err, auth.ErrRenewalReused):
			s.Log.Warn("anonymous.renewal.reused", "subject", id)
			renewed = false
		default:
			s.Log.Error("anonymous.renewal.consume", "error", err)
			w.Header().Set("Retry-After", "5")
			httpx.Problem(w, r, http.StatusServiceUnavailable, "renewal-unavailable", "Token renewal is temporarily unavailable.")
			return
		}
	}
	if !renewed {
		// A fresh identity would register another live tab. A renewal of a tab
		// that is already at the cap must still receive a token (WO_T96B).
		//
		// W7 fix (stale token at the cap): a tab that slept past its token is no
		// longer renewable, and its own old registration can be the record that
		// fills the cap. The stale bearer proves which registration that is, so
		// only those dead records of that subject are released before the caps are
		// checked; a live tab is never touched and every cap still applies.
		if subject, err := s.staleAnonymousSubject(r); err == nil {
			if n, err := s.Engine.EvictStaleAnonymousTabs(r.Context(), subject); err == nil && n > 0 {
				s.Log.Info("anonymous.evicted_stale_tabs", "subject", subject, "tabs", n)
			}
		}
		if err := s.Engine.CheckAnonymousLimits(r.Context(), ip); err != nil {
			w.Header().Set("Retry-After", "60")
			httpx.Problem(w, r, http.StatusTooManyRequests, "anonymous-quota-exceeded", err.Error())
			return
		}
		id = auth.AnonymousID()
		sessionEnd = time.Time{}
	}

	token, err := s.Auth.IssueAnonymousSession(id, auth.ContributorWorkerScopes, sessionEnd)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	token.PoolID = p
	// W7 fix (cmdc): the renewed bearer is a credential. A proxy, bfcache or
	// service worker must never retain the reply.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, token)
}

// staleAnonymousSubject reports the anonymous subject of a bearer that is past
// its renewal window (W7 fix: stale token at the live-tab cap). It is the bearer
// of a tab that slept too long; the caller may release the dead registrations of
// that subject before it falls back to a fresh identity. An error means "no such
// bearer", and the caller then keeps the ordinary path.
func (s *Server) staleAnonymousSubject(r *http.Request) (string, error) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) || s.Auth == nil {
		return "", auth.ErrUnauthorized
	}
	raw := strings.TrimSpace(h[len(prefix):])
	if raw == "" {
		return "", auth.ErrUnauthorized
	}
	c, err := s.Auth.AnonymousStaleClaims(raw)
	if err != nil {
		return "", err
	}
	return c.Subject, nil
}

// anonymousRenewalID reports the subject to re-issue when the request carries a
// bearer that is safe to renew. ok is false for a missing, invalid, expired,
// deny-listed or non-anonymous token, and the caller mints a fresh identity.
func (s *Server) anonymousRenewalID(r *http.Request) (string, auth.Claims, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.HasPrefix(h, prefix) {
		return "", auth.Claims{}, false
	}
	raw := strings.TrimSpace(h[len(prefix):])
	if raw == "" || s.Auth == nil {
		return "", auth.Claims{}, false
	}
	c, err := s.Auth.ValidateAnonymousRenewal(r.Context(), raw, auth.AnonymousRenewalGrace)
	if err != nil || !auth.IsAnonymousID(c.Subject) {
		return "", auth.Claims{}, false
	}
	return c.Subject, c, true
}
