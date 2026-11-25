// Path: internal/auth/auth.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: hashed keys, short HS256 tokens, role/store separation and revocation; WWPP-R32.4, R38.3.
package auth

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

var ErrUnauthorized = errors.New("invalid or revoked credentials")

type User struct {
	ID           string   `json:"id"`
	AccountStore string   `json:"accountStore"`
	Roles        []string `json:"roles"`
}
type Key struct {
	ID      string   `json:"id"`
	Hash    string   `json:"hash"`
	User    User     `json:"user"`
	Scopes  []string `json:"scopes"`
	Revoked bool     `json:"revoked"`
}
type Claims struct {
	Subject  string   `json:"sub"`
	Issuer   string   `json:"iss"`
	Audience string   `json:"aud"`
	ID       string   `json:"jti"`
	KeyID    string   `json:"kid"`
	IssuedAt int64    `json:"iat"`
	Expires  int64    `json:"exp"`
	User     User     `json:"user"`
	Scopes   []string `json:"scopes"`
	// Anonymous marks a token of an anonymous contributor session (WO_T96). Such a
	// token has no stored key: it is validated by its signature and its own jti
	// against the deny-list, and the server treats the identity as low trust.
	Anonymous bool `json:"anon,omitempty"`
	// SessionExpires is the absolute end of an anonymous session (T110). Renewal
	// copies it unchanged, so a renewal chain cannot outlive it.
	SessionExpires int64 `json:"sxp,omitempty"`
}

func (c Claims) HasRole(role string) bool { return slices.Contains(c.User.Roles, role) }
func (c Claims) Allows(scope string) bool {
	return slices.Contains(c.Scopes, scope) || slices.Contains(c.Scopes, "*")
}

type Token struct {
	AccessToken string    `json:"accessToken"`
	TokenType   string    `json:"tokenType"`
	ExpiresIn   int       `json:"expiresIn"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Scopes      []string  `json:"scopes"`
	ServerID    string    `json:"serverId"`
	PoolID      string    `json:"poolId,omitempty"`
}
type Service struct {
	Store  store.Store
	secret []byte
	issuer string
	Now    func() time.Time
}

func New(s store.Store, secret, issuer string) *Service {
	return &Service{s, []byte(secret), issuer, time.Now}
}
func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func validUser(u User) bool {
	if u.ID == "" || len(u.Roles) == 0 {
		return false
	}
	for _, r := range u.Roles {
		if u.AccountStore == "A" && (r == "admin" || r == "director") {
			continue
		}
		if u.AccountStore == "B" && (r == "developer" || r == "contributor") {
			continue
		}
		return false
	}
	return true
}

// CreateKey is a trusted application boundary. HTTP issuance additionally requires Store A admin.
func (s *Service) CreateKey(ctx context.Context, u User, scopes []string) (string, Key, error) {
	raw := "pc_" + ID() + ID()
	k, e := s.saveKey(ctx, raw, u, scopes)
	return raw, k, e
}
func (s *Service) saveKey(ctx context.Context, raw string, u User, scopes []string) (Key, error) {
	if !validUser(u) || len(raw) < 24 {
		return Key{}, errors.New("invalid user/store roles or short key")
	}
	k := Key{ID: hash(raw), Hash: hash(raw), User: u, Scopes: scopes}
	e := s.Store.Update(ctx, func(tx store.Tx) error {
		var old Key
		if e := tx.Get(store.Keys, k.ID, &old); e == nil {
			k = old
			return nil
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if e := tx.Put(store.Users, u.AccountStore+":"+u.ID, u); e != nil {
			return e
		}
		return tx.Put(store.Keys, k.ID, k)
	})
	return k, e
}

// Bootstrap only consumes a configured secret, never prints it or creates a default API key.
func (s *Service) Bootstrap(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	_, e := s.saveKey(ctx, raw, User{"bootstrap", "A", []string{"admin"}}, []string{"*"})
	return e
}
func (s *Service) Revoke(ctx context.Context, id string) error {
	return s.Store.Update(ctx, func(tx store.Tx) error {
		var k Key
		if e := tx.Get(store.Keys, id, &k); e != nil {
			return e
		}
		k.Revoked = true
		return tx.Put(store.Keys, id, k)
	})
}
func (s *Service) Exchange(ctx context.Context, raw string, requested []string) (Token, error) {
	var k Key
	e := s.Store.View(ctx, func(r store.Reader) error { return r.Get(store.Keys, hash(raw), &k) })
	if e != nil || k.Revoked {
		return Token{}, ErrUnauthorized
	}
	scopes := k.Scopes
	if len(requested) > 0 {
		scopes = []string{}
		for _, v := range requested {
			if slices.Contains(k.Scopes, v) || slices.Contains(k.Scopes, "*") {
				scopes = append(scopes, v)
			}
		}
	}
	now := s.Now()
	c := Claims{k.User.ID, s.issuer, "pools-core", ID(), k.ID, now.Unix(), now.Add(15 * time.Minute).Unix(), k.User, scopes, false, 0}
	return s.sign(c), nil
}

// sign renders the signed form of a claim set: the HS256 JWT and the token view.
//
// ExpiresIn is the lifetime the bearer really has, computed from its own bounds
// and not a hardcoded 900 (R10 d13): a token capped by its session end reports
// the capped lifetime, so the tab renews before the credential dies.
func (s *Service) sign(c Claims) Token {
	b, _ := json.Marshal(c)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := header + "." + base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	jwt := body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	in := int(c.Expires - c.IssuedAt)
	if in < 0 {
		in = 0
	}
	return Token{jwt, "Bearer", in, time.Unix(c.Expires, 0).UTC(), c.Scopes, s.issuer, ""}
}
func (s *Service) Validate(ctx context.Context, token string) (Claims, error) {
	return s.validate(ctx, token, 0)
}

// validate checks a signed bearer. expiryGrace is zero for every route except
// anonymous renewal, which may accept a token that expired at most grace ago.
// A zero grace keeps the historical rule that exp == now is already expired.
// The grace window applies to an anonymous claim only (W7 fix), so a stored-key
// token is always checked against the strict rule.

// parse verifies the signature and returns the claims of a signed bearer. It
// deliberately checks no time bound: the callers apply their own (validate for
// the routes, AnonymousSubjectOf to identify a subject whose token went stale).
func (s *Service) parse(token string) (Claims, error) {
	var c Claims
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return c, ErrUnauthorized
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil {
		return c, ErrUnauthorized
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(b, &h) != nil || h.Alg != "HS256" {
		return c, ErrUnauthorized
	}
	sig, e := base64.RawURLEncoding.DecodeString(p[2])
	if e != nil {
		return c, ErrUnauthorized
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(p[0] + "." + p[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, ErrUnauthorized
	}
	b, e = base64.RawURLEncoding.DecodeString(p[1])
	if e != nil || json.Unmarshal(b, &c) != nil {
		return c, ErrUnauthorized
	}
	return c, nil
}

func (s *Service) validate(ctx context.Context, token string, expiryGrace time.Duration) (Claims, error) {
	c, e := s.parse(token)
	if e != nil {
		return c, e
	}
	now := s.Now()
	nowUnix := now.Unix()
	// GLM R7-1: absolute bounds only. The old Expires-IssuedAt > 900 test wrapped on
	// iat = MinInt64 and let a signed token live for days.
	//
	// W7 fix (agy boundary, cmdc Anonymous gate + sub-second clock): the strict rule
	// is c.Expires <= now. A non-zero grace is a renewal-only window that ends
	// exactly at exp+grace - a token at exp+grace is refused, not accepted - and it
	// is gated on c.Anonymous inside validate, so a stored-key token can never take
	// the grace path even if a future caller passes a non-zero grace. The clock is
	// compared as a time.Time, so the window is not 60.999 s long.
	expired := c.Expires <= nowUnix
	if expiryGrace > 0 && c.Anonymous {
		expired = !time.Unix(c.Expires, 0).After(now.Add(-expiryGrace))
	}
	if expired || c.Expires > nowUnix+1020 || c.IssuedAt > nowUnix+120 || c.IssuedAt < nowUnix-1020 || c.Expires < c.IssuedAt || c.Expires-c.IssuedAt > 900 || c.Issuer != s.issuer || c.Audience != "pools-core" || c.Subject != c.User.ID {
		return c, ErrUnauthorized
	}
	var k Key
	denied := false
	// A deny entry ends at the token exp. W7 fix: the deny reference is the present
	// (max(now, exp-1s) whenever the token is inside its grace window), never a
	// substitute for it, and the expiry boundary is checked as well, so a deny
	// written during the grace window and a deny that was in force at the token exp
	// both reject the token. Neither drop can re-enable a revoked bearer.
	//
	// R10 d10: the boundary probe was exp-1s only, so a deny whose RevokedAt lands
	// in (exp-1s, exp] was invisible to a renewal, although it was in force at exp
	// and expires with it. Inside the grace window the question is therefore asked
	// as a span: does any entry overlap [exp, now]? The ordinary path keeps the
	// instant probe at the present.
	denyAt := now.UTC()
	var graceFrom time.Time
	if expiryGrace > 0 && c.Anonymous && c.Expires <= nowUnix {
		// The window the grace must be judged over is the pre-exp second (the W7
		// boundary a deny may end at) through the present.
		graceFrom = time.Unix(c.Expires, 0).UTC().Add(-time.Second)
	}
	e = s.Store.View(ctx, func(r store.Reader) error {
		// The deny-list is read in the same transaction as the key: one round trip,
		// and no window where a key is loaded but its deny entry is not.
		check := DenyReader(r)
		denied = check.IsDenied(c.ID, denyAt) || check.IsDenied(c.KeyID, denyAt)
		if !denied && !graceFrom.IsZero() {
			denied = check.IsDeniedBetween(c.ID, graceFrom, now) || check.IsDeniedBetween(c.KeyID, graceFrom, now)
		}
		if c.Anonymous {
			// An anonymous contributor token (WO_T96) has no stored key on purpose:
			// the server stores no API key for a volunteer tab. Its own jti is
			// deny-listed, so an operator revokes it like any other token.
			return nil
		}
		return r.Get(store.Keys, c.KeyID, &k)
	})
	if c.Anonymous {
		// The anonymous branch is accepted only for a synthetic store B contributor
		// identity whose key id is its own identity: a token of a stored key can
		// never take this path, so it does not weaken the key check.
		// W7 review: scopes and a jti are pinned here too, not only in the minter.
		if e != nil || denied || c.ID == "" || !slices.Equal(c.Scopes, ContributorWorkerScopes) ||
			!IsAnonymousID(c.User.ID) || c.KeyID != c.Subject ||
			c.User.AccountStore != "B" || !c.HasRole("contributor") || len(c.User.Roles) != 1 {
			return c, ErrUnauthorized
		}
		return c, nil
	}
	if e != nil || k.Revoked || k.User.ID != c.Subject || denied {
		return c, ErrUnauthorized
	}
	return c, nil
}
