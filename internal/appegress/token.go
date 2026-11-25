// Package appegress contains common, workload-agnostic app egress primitives.
// The two pool schedulers must not depend on one another.
package appegress

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ccgsc/poolscore/internal/store"
)

var ErrInvalid = errors.New("invalid application egress request")

const keyBucket = "app_egress_keys"
const signingKey = "lease_ed25519_v1"
const MaxLifetime = 10 * time.Minute

// ClockSkew is the only tolerance for a token issued by another instance
// whose clock runs ahead. Expiry never gets a tolerance.
const ClockSkew = 5 * time.Second

// Strict rejects non-zero trailing bits, so one signature has one encoding.
var b64 = base64.RawURLEncoding.Strict()

type Signer struct {
	key ed25519.PrivateKey
	kid string
}
type Claims struct {
	JobID         string `json:"jobId,omitempty"`
	TaskID        string `json:"taskId,omitempty"`
	PolicyVersion uint64 `json:"policyVersion,omitempty"`
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Subject       string `json:"sub"`
	Lease         string `json:"lease"`
	Pool          string `json:"pool"`
	Owner         string `json:"owner"`
	Purpose       string `json:"purpose"`
	NotBefore     int64  `json:"nbf,omitempty"`
	IssuedAt      int64  `json:"iat"`
	ExpiresAt     int64  `json:"exp"`
	ID            string `json:"jti"`
}
type Header struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}
type JWK struct {
	KeyType   string `json:"kty"`
	Curve     string `json:"crv"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	KeyID     string `json:"kid"`
	X         string `json:"x"`
}
type JWKS struct {
	Keys []JWK `json:"keys"`
}

func NewSigner(ctx context.Context, s store.Store) (*Signer, error) {
	var raw []byte
	err := s.Update(ctx, func(tx store.Tx) error {
		err := tx.Get(keyBucket, signingKey, &raw)
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		atomic, ok := tx.(store.PutIfAbsentTx)
		if !ok {
			return store.ErrNotImplemented
		}
		if _, err = atomic.PutIfAbsent(keyBucket, signingKey, []byte(key)); err != nil {
			return err
		}
		return tx.Get(keyBucket, signingKey, &raw)
	})
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, ErrInvalid
	}
	key := ed25519.PrivateKey(append([]byte(nil), raw...))
	// Reject corrupted persisted key halves, rather than sign with an invalid key.
	if !key.Equal(ed25519.NewKeyFromSeed(key.Seed())) {
		return nil, ErrInvalid
	}
	pub := key.Public().(ed25519.PublicKey)
	h := sha256.Sum256(pub)
	return &Signer{key: key, kid: hex.EncodeToString(h[:])}, nil
}
func (s *Signer) Public() JWKS {
	return JWKS{Keys: []JWK{{KeyType: "OKP", Curve: "Ed25519", Algorithm: "EdDSA", Use: "sig", KeyID: s.kid, X: b64.EncodeToString(s.key.Public().(ed25519.PublicKey))}}}
}
func (s *Signer) Issue(owner, app, worker, lease, pool, purpose string, now, deadline time.Time) (string, Claims, error) {
	return s.IssueBound(owner, app, worker, lease, pool, purpose, now, deadline, "", "", 0)
}
func (s *Signer) IssueBound(owner, app, worker, lease, pool, purpose string, now, deadline time.Time, job, task string, version uint64) (string, Claims, error) {
	expiry := now.Add(MaxLifetime)
	if deadline.Before(expiry) {
		expiry = deadline
	}
	if purpose == "service" && now.Add(time.Minute).Before(expiry) {
		expiry = now.Add(time.Minute)
	}
	if owner == "" || app == "" || worker == "" || lease == "" || (pool != "thread" && pool != "process") || (purpose != "worker" && purpose != "service" && purpose != "runner") || expiry.Unix() <= now.Unix() {
		return "", Claims{}, ErrInvalid
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", Claims{}, err
	}
	c := Claims{JobID: job, TaskID: task, PolicyVersion: version, Issuer: "ccgsc", Audience: Audience(purpose, app), Subject: worker, Lease: lease, Pool: pool, Owner: owner, Purpose: purpose, IssuedAt: now.Unix(), ExpiresAt: expiry.Unix(), ID: b64.EncodeToString(nonce[:])}
	h, _ := json.Marshal(Header{Algorithm: "EdDSA", KeyID: s.kid, Type: "JWT"})
	p, _ := json.Marshal(c)
	msg := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	return msg + "." + b64.EncodeToString(ed25519.Sign(s.key, []byte(msg))), c, nil
}

// Audience is the exact `aud` of a purpose: a worker token names the app, every
// other purpose is prefixed so no purpose can stand in for another.
func Audience(purpose, app string) string {
	if purpose == "worker" {
		return app
	}
	return "ccgsc:" + purpose + ":" + app
}

// Verify checks the signature, key id, algorithm, purpose and time window of a
// token issued by this signer. It does NOT bind the token to an app, owner,
// pool, worker or lease: a caller that uses the claims MUST go through
// VerifyScoped or Claims.Check with the identity it expects.
func (s *Signer) Verify(token, purpose string, now time.Time) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 8192 {
		return c, ErrInvalid
	}
	h, err := b64.DecodeString(parts[0])
	if err != nil {
		return c, ErrInvalid
	}
	var hdr Header
	if json.Unmarshal(h, &hdr) != nil || hdr.Algorithm != "EdDSA" || hdr.KeyID == "" || hdr.KeyID != s.kid || hdr.Type != "JWT" {
		return c, ErrInvalid
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(s.key.Public().(ed25519.PublicKey), []byte(parts[0]+"."+parts[1]), sig) {
		return c, ErrInvalid
	}
	p, err := b64.DecodeString(parts[1])
	if err != nil || json.Unmarshal(p, &c) != nil {
		return Claims{}, ErrInvalid
	}
	t := now.Unix()
	skew := int64(ClockSkew / time.Second)
	if c.Issuer != "ccgsc" || c.Purpose != purpose || c.Audience == "" || c.Owner == "" || c.Subject == "" || c.Lease == "" || c.ID == "" || (c.Pool != "thread" && c.Pool != "process") ||
		c.IssuedAt > t+skew || c.NotBefore > t+skew || c.ExpiresAt <= t || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > int64(MaxLifetime/time.Second) {
		return Claims{}, ErrInvalid
	}
	return c, nil
}

// Expect is the identity a verified token must carry. App, Owner, Pool,
// Worker and Lease are mandatory: an empty field is a caller bug and fails
// closed instead of silently skipping the comparison. Job, Task and
// PolicyVersion are compared only when set.
type Expect struct {
	Purpose, App, Owner, Pool, Worker, Lease, Job, Task string
	PolicyVersion                                       uint64
}

// Check compares the claims with the expected identity.
func (c Claims) Check(x Expect) error {
	if x.Purpose == "" || x.App == "" || x.Owner == "" || x.Pool == "" || x.Worker == "" || x.Lease == "" {
		return ErrInvalid
	}
	if c.Purpose != x.Purpose || c.Audience != Audience(x.Purpose, x.App) || c.Owner != x.Owner || c.Pool != x.Pool || c.Subject != x.Worker || c.Lease != x.Lease {
		return ErrInvalid
	}
	if (x.Job != "" && c.JobID != x.Job) || (x.Task != "" && c.TaskID != x.Task) || (x.PolicyVersion != 0 && c.PolicyVersion != x.PolicyVersion) {
		return ErrInvalid
	}
	return nil
}

// VerifyScoped is Verify plus Check: the one entry point for a token whose
// scope the caller knows beforehand.
func (s *Signer) VerifyScoped(token string, x Expect, now time.Time) (Claims, error) {
	c, err := s.Verify(token, x.Purpose, now)
	if err != nil {
		return Claims{}, err
	}
	if err = c.Check(x); err != nil {
		return Claims{}, err
	}
	return c, nil
}
