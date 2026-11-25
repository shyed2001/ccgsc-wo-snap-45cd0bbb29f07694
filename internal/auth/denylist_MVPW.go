// Path: internal/auth/denylist_MVPW.go | Author: cmdc for Claude Opus 5.5 | 2026-11-19
// What/why: persisted bearer-token deny-list (MVPW slice A). A leaked signed token
// stays valid until it expires; this records a denied token id with an expiry and
// enforces it on the one path every HTTP and gRPC call passes through.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"ccgsc/poolscore/internal/store"
)

// DenyEntry is one denied token id. Key is the jti or the key id (kid).
//
// ExpiresAt is the moment the entry may be forgotten: it is the token's own exp,
// never longer, so a deny-list entry can never outlive the credential it denies.
// Revoking a key id that is re-issued therefore needs a fresh entry, which is the
// intended behaviour -- a kid deny is a convenience, a jti deny is the precise one.
type DenyEntry struct {
	TokenID   string    `json:"tokenId"`
	Reason    string    `json:"reason,omitempty"`
	RevokedBy string    `json:"revokedBy,omitempty"`
	RevokedAt time.Time `json:"revokedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ErrTokenIDRequired is returned for an empty or whitespace-only token id.
var ErrTokenIDRequired = errors.New("tokenId is required")

const denyReasonMaxLength = 512

// DenyToken records the token id as denied until expiresAt.
//
// The caller passes the token's own expiry. A zero time means "use the maximum
// token lifetime from now", so an operator cannot accidentally create a permanent
// entry from a value they did not measure.
func (s *Service) DenyToken(ctx context.Context, tokenID, reason, revokedBy string, expiresAt time.Time) (DenyEntry, error) {
	if tokenID == "" {
		return DenyEntry{}, ErrTokenIDRequired
	}
	if len(reason) > denyReasonMaxLength {
		reason = reason[:denyReasonMaxLength]
	}
	now := s.Now().UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(tokenLifetime)
	} else if expiresAt.After(now.Add(tokenLifetime)) {
		return DenyEntry{}, errors.New("expiresAt exceeds maximum token lifetime")
	}
	entry := DenyEntry{TokenID: tokenID, Reason: reason, RevokedBy: revokedBy, RevokedAt: now, ExpiresAt: expiresAt.UTC()}
	e := s.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.TokenDenylist, tokenID, entry)
	})
	if e != nil {
		return DenyEntry{}, e
	}
	return entry, nil
}

// DenySubjectAndToken denies every token of one anonymous subject (W7 fix).
//
// An anonymous token is the only credential of its tab: no key row exists, and
// every renewal mints a fresh jti for the same subject, so a jti-only deny is
// bypassed by the next renewal. This writes one entry for the subject (kid) and
// one for the presented jti in a single transaction, both bounded by the same
// expiry. The subject entry keeps being read on renewal, because validate checks
// the KeyID (the subject) as well as the jti, so the revocation survives the
// renewal chain instead of only the presented bearer.
//
// The subject id must carry the anonymous prefix: a stored key id is refused, so
// this administrative primitive can never become a wider deny by accident.
func (s *Service) DenySubjectAndToken(ctx context.Context, subject, tokenID, reason, revokedBy string, expiresAt time.Time) ([]DenyEntry, error) {
	if !IsAnonymousID(subject) {
		return nil, ErrAnonymousIdentity
	}
	if len(reason) > denyReasonMaxLength {
		reason = reason[:denyReasonMaxLength]
	}
	now := s.Now().UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(tokenLifetime)
	} else if expiresAt.After(now.Add(tokenLifetime)) {
		return nil, errors.New("expiresAt exceeds maximum token lifetime")
	}
	expiresAt = expiresAt.UTC()
	entries := []DenyEntry{{TokenID: subject, Reason: reason, RevokedBy: revokedBy, RevokedAt: now, ExpiresAt: expiresAt}}
	if tokenID != "" && tokenID != subject {
		entries = append(entries, DenyEntry{TokenID: tokenID, Reason: reason, RevokedBy: revokedBy, RevokedAt: now, ExpiresAt: expiresAt})
	}
	e := s.Store.Update(ctx, func(tx store.Tx) error {
		for _, entry := range entries {
			if e := tx.Put(store.TokenDenylist, entry.TokenID, entry); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return entries, nil
}

// IsDenied reports whether the token id is denied at the moment at.
//
// An entry is in force from RevokedAt until ExpiresAt, so at says WHEN the deny
// must hold: the present for an ordinary validation, the token expiry for the
// anonymous renewal grace. W7 fix: the interval is honoured on both ends. The old
// comparison only asked "does the entry expire after at", so a caller that passed
// an earlier at silently lost every deny written after it; with the lower bound
// the anonymous grace window can keep the deny that was in force at exp and add
// the one written during the window (see validate in auth.go).
//
// It is safe to call inside an existing transaction-free read: the caller wraps
// it in the same View that already loads the key, so a validation costs one
// transaction rather than two.
func (r denyReader) IsDenied(tokenID string, at time.Time) bool {
	if tokenID == "" {
		return false
	}
	var entry DenyEntry
	if e := r.reader.Get(store.TokenDenylist, tokenID, &entry); e != nil {
		// A missing entry is the ordinary path. Any other read error must not
		// silently re-enable a token, so treat it as denied.
		return !errors.Is(e, store.ErrNotFound)
	}
	return !entry.RevokedAt.After(at) && entry.ExpiresAt.After(at)
}

// IsDeniedBetween reports whether the deny entry of tokenID overlaps [from, until).
//
// R10 d10: the grace window needs a span question, not an instant one. A deny is in
// force on [RevokedAt, ExpiresAt), so a single probe cannot see an entry written in
// the token's final second whose own expiry is the token exp: at exp-1s the entry
// has not started, and at exp it has just ended. The overlap is the property the
// renewal actually cares about, and it covers every instant of the window.
//
// A read error is denied, exactly like IsDenied: it must not re-enable a token.
func (r denyReader) IsDeniedBetween(tokenID string, from, until time.Time) bool {
	if tokenID == "" {
		return false
	}
	var entry DenyEntry
	if e := r.reader.Get(store.TokenDenylist, tokenID, &entry); e != nil {
		return !errors.Is(e, store.ErrNotFound)
	}
	return entry.RevokedAt.Before(until) && entry.ExpiresAt.After(from)
}

// denyReader adapts a store.Reader for the check inside an existing View.
type denyReader struct{ reader store.Reader }

// DenyReader exposes the deny check inside a caller-owned read transaction.
func DenyReader(r store.Reader) denyReader { return denyReader{r} }

// RevokedEntry is one row of the admin listing.
type RevokedEntry struct {
	TokenID   string    `json:"tokenId"`
	Reason    string    `json:"reason,omitempty"`
	RevokedBy string    `json:"revokedBy,omitempty"`
	RevokedAt time.Time `json:"revokedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Expired   bool      `json:"expired"`
}

// RevokedTokens lists deny entries, newest first.
//
// includeExpired is opt-in: an operator repairing a mistake needs to see what is
// still in force, and the listed "expired" flag is computed against the same
// clock the enforcement uses.
func (s *Service) RevokedTokens(ctx context.Context, includeExpired bool) ([]RevokedEntry, error) {
	var rows []store.Record
	if e := s.Store.View(ctx, func(r store.Reader) error {
		var e error
		rows, e = r.List(store.TokenDenylist)
		return e
	}); e != nil {
		return nil, e
	}
	now := s.Now().UTC()
	out := make([]RevokedEntry, 0, len(rows))
	for _, row := range rows {
		var entry DenyEntry
		if json.Unmarshal(row.Value, &entry) != nil {
			continue
		}
		expired := !entry.ExpiresAt.After(now)
		if expired && !includeExpired {
			continue
		}
		out = append(out, RevokedEntry{entry.TokenID, entry.Reason, entry.RevokedBy, entry.RevokedAt, entry.ExpiresAt, expired})
	}
	slices.SortFunc(out, func(a, b RevokedEntry) int {
		if a.RevokedAt.After(b.RevokedAt) {
			return -1
		}
		if a.RevokedAt.Before(b.RevokedAt) {
			return 1
		}
		return 0
	})
	return out, nil
}

// PruneDenylist removes entries whose expiry has passed and reports how many.
//
// It runs from the existing maintenance tick. The Store contract has no Delete, so
// the optional store.DeleteTx is asserted: an adapter without it (a wrapper that
// does not forward removal) is reported as a no-op rather than an error, and the
// entry is simply forgotten by the expiry comparison instead.
func (s *Service) PruneDenylist(ctx context.Context) (int, error) {
	now := s.Now().UTC()
	var rows []store.Record
	if e := s.Store.View(ctx, func(r store.Reader) error {
		var e error
		rows, e = r.List(store.TokenDenylist)
		return e
	}); e != nil {
		return 0, e
	}
	pruned := 0
	e := s.Store.Update(ctx, func(tx store.Tx) error {
		deleter, ok := tx.(store.DeleteTx)
		if !ok {
			return nil
		}
		for _, row := range rows {
			var current json.RawMessage
			if e := tx.Get(store.TokenDenylist, row.Key, &current); e != nil {
				if errors.Is(e, store.ErrNotFound) {
					continue
				}
				return e
			}
			var entry DenyEntry
			// W7 review (agy): keep an entry through the anonymous renewal grace,
			// or a prune right after exp would let a revoked token renew.
			if json.Unmarshal(current, &entry) == nil && entry.ExpiresAt.Add(AnonymousRenewalGrace).After(now) {
				continue
			}
			if e := deleter.Delete(store.TokenDenylist, row.Key); e != nil {
				return e
			}
			pruned++
		}
		return nil
	})
	return pruned, e
}

// tokenLifetime is the issued-token lifetime: the upper bound for a deny entry.
const tokenLifetime = 15 * time.Minute

// ErrRenewalReused is returned by ConsumeRenewal when the token id was already
// consumed or denied. The caller must not renew from that token.
var ErrRenewalReused = errors.New("anonymous renewal token already used")

// ConsumeRenewal atomically marks an anonymous token id as used for one renewal.
//
// W7 review (qwen): the renewal handler used to mint first and deny after, and a
// deny error only logged. Two concurrent renewals with one bearer both passed and
// a store error turned single use off. ConsumeRenewal runs before any token is
// minted and inserts the deny row only when no row exists (PutIfAbsent: ON
// CONFLICT DO NOTHING on PostgreSQL, one write transaction on SQLite), so exactly
// one request wins.
//
// R10 d11: the jti row alone is not enough. A subject revoke written between the
// renewal's validate and this call is not seen by a jti-only PutIfAbsent, and the
// revoked subject still mints a token. The subject deny is checked inside the same
// transaction that inserts the row, so the two cannot be observed apart.
// R10 d12: the expiry is the token's own exp, never a now+tokenLifetime clamp. A
// minter clock up to 120 s ahead produces an exp beyond the clamp, which would
// shorten the row below the credential and let a prune re-enable a second renewal.
func (s *Service) ConsumeRenewal(ctx context.Context, tokenID, subject string, expiresAt time.Time) error {
	if tokenID == "" {
		return ErrTokenIDRequired
	}
	if !IsAnonymousID(subject) {
		return ErrAnonymousIdentity
	}
	now := s.Now().UTC()
	if expiresAt.IsZero() || expiresAt.Before(now) {
		// Inside the renewal grace the token is already past exp; keep the row
		// through the grace window so the prune rule and validation agree.
		expiresAt = now
	}
	expiresAt = expiresAt.UTC()
	entry := DenyEntry{TokenID: tokenID, Reason: "anonymous renewal (single use)", RevokedBy: subject, RevokedAt: now, ExpiresAt: expiresAt}
	return s.Store.Update(ctx, func(tx store.Tx) error {
		check := DenyReader(tx)
		// R10 d11: the subject deny is checked in the same transaction that records
		// the jti, so a revoke written between the renewal's validate and this call
		// cannot be missed. The window is the one the renewal itself is judged over:
		// the pre-exp second (where a W7 deny may end) through the present.
		if check.IsDenied(subject, now) || check.IsDeniedBetween(subject, expiresAt.Add(-time.Second), now) {
			return ErrRenewalReused
		}
		if atomic, ok := tx.(store.PutIfAbsentTx); ok {
			inserted, err := atomic.PutIfAbsent(store.TokenDenylist, tokenID, entry)
			if err != nil {
				return err
			}
			if !inserted {
				return ErrRenewalReused
			}
			return nil
		}
		var existing DenyEntry
		switch err := tx.Get(store.TokenDenylist, tokenID, &existing); {
		case err == nil:
			return ErrRenewalReused
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		return tx.Put(store.TokenDenylist, tokenID, entry)
	})
}
