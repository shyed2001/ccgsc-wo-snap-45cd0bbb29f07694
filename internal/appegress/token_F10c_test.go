package appegress

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
)

func f10cSigner(t *testing.T) *Signer {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewSigner(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// forge signs arbitrary header and claim documents with the real key, so each
// case below fails for exactly one reason and not because of a bad signature.
func (s *Signer) forge(t *testing.T, h map[string]any, c Claims) string {
	t.Helper()
	hb, _ := json.Marshal(h)
	cb, _ := json.Marshal(c)
	msg := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
	return msg + "." + b64.EncodeToString(ed25519.Sign(s.key, []byte(msg)))
}

func f10cClaims(now time.Time) Claims {
	return Claims{Issuer: "ccgsc", Audience: "AppAlpha", Subject: "worker-1", Lease: "lease-1", Pool: "thread", Owner: "dev", Purpose: "worker",
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), ID: "jti-1"}
}

// F10b-21: every way a compact JWS can be wrong is refused.
func TestF10cVerifyNegativeMatrix(t *testing.T) {
	s := f10cSigner(t)
	other := f10cSigner(t)
	now := time.Unix(1_700_000_000, 0)
	good := map[string]any{"alg": "EdDSA", "kid": s.kid, "typ": "JWT"}
	hdr := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range good {
			m[a] = b
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	if _, err := s.Verify(s.forge(t, good, f10cClaims(now)), "worker", now); err != nil {
		t.Fatalf("control token refused: %v", err)
	}
	headers := map[string]map[string]any{
		"alg none":        hdr("alg", "none"),
		"alg HS256":       hdr("alg", "HS256"),
		"alg lowercase":   hdr("alg", "eddsa"),
		"alg missing":     hdr("alg", nil),
		"kid missing":     hdr("kid", nil),
		"kid empty":       hdr("kid", ""),
		"kid unknown":     hdr("kid", other.kid),
		"typ missing":     hdr("typ", nil),
		"typ other":       hdr("typ", "at+jwt"),
		"kid unknown hex": hdr("kid", strings.Repeat("0", 64)),
	}
	for name, h := range headers {
		if _, err := s.Verify(s.forge(t, h, f10cClaims(now)), "worker", now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	mut := func(f func(*Claims)) string { c := f10cClaims(now); f(&c); return s.forge(t, good, c) }
	claims := map[string]string{
		"wrong issuer":      mut(func(c *Claims) { c.Issuer = "evil" }),
		"empty issuer":      mut(func(c *Claims) { c.Issuer = "" }),
		"empty audience":    mut(func(c *Claims) { c.Audience = "" }),
		"empty owner":       mut(func(c *Claims) { c.Owner = "" }),
		"empty subject":     mut(func(c *Claims) { c.Subject = "" }),
		"empty lease":       mut(func(c *Claims) { c.Lease = "" }),
		"empty jti":         mut(func(c *Claims) { c.ID = "" }),
		"unknown pool":      mut(func(c *Claims) { c.Pool = "legacy" }),
		"empty pool":        mut(func(c *Claims) { c.Pool = "" }),
		"wrong purpose":     mut(func(c *Claims) { c.Purpose = "service" }),
		"expired":           mut(func(c *Claims) { c.ExpiresAt = now.Add(-time.Second).Unix(); c.IssuedAt = now.Add(-time.Minute).Unix() }),
		"expires exactly":   mut(func(c *Claims) { c.ExpiresAt = now.Unix(); c.IssuedAt = now.Add(-time.Minute).Unix() }),
		"exp before iat":    mut(func(c *Claims) { c.ExpiresAt = c.IssuedAt - 1 }),
		"exp equals iat":    mut(func(c *Claims) { c.ExpiresAt = c.IssuedAt }),
		"too long lived":    mut(func(c *Claims) { c.ExpiresAt = c.IssuedAt + int64(MaxLifetime/time.Second) + 1 }),
		"future iat":        mut(func(c *Claims) { c.IssuedAt = now.Add(time.Minute).Unix(); c.ExpiresAt = c.IssuedAt + 60 }),
		"iat beyond skew":   mut(func(c *Claims) { c.IssuedAt = now.Add(ClockSkew + time.Second).Unix(); c.ExpiresAt = c.IssuedAt + 60 }),
		"future nbf":        mut(func(c *Claims) { c.NotBefore = now.Add(time.Minute).Unix() }),
		"nbf beyond skew":   mut(func(c *Claims) { c.NotBefore = now.Add(ClockSkew + time.Second).Unix() }),
		"zero expiry":       mut(func(c *Claims) { c.ExpiresAt = 0 }),
		"negative expiry":   mut(func(c *Claims) { c.ExpiresAt = -1 }),
		"max int expiry":    mut(func(c *Claims) { c.ExpiresAt = 1<<62 - 1 }),
		"missing all":       s.forge(t, good, Claims{}),
		"empty subject set": mut(func(c *Claims) { c.Subject = ""; c.Lease = "" }),
	}
	for name, tok := range claims {
		if _, err := s.Verify(tok, "worker", now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Inside the tolerance and at the boundary the token still verifies.
	for name, tok := range map[string]string{
		"iat within skew": mut(func(c *Claims) { c.IssuedAt = now.Add(ClockSkew).Unix(); c.ExpiresAt = c.IssuedAt + 60 }),
		"nbf in the past": mut(func(c *Claims) { c.NotBefore = now.Add(-time.Hour).Unix() }),
		"last second":     mut(func(c *Claims) { c.ExpiresAt = now.Unix() + 1; c.IssuedAt = now.Unix() - 1 }),
	} {
		if _, err := s.Verify(tok, "worker", now); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
}

func TestF10cVerifyMalformedCompactJWS(t *testing.T) {
	s := f10cSigner(t)
	now := time.Unix(1_700_000_000, 0)
	tok := s.forge(t, map[string]any{"alg": "EdDSA", "kid": s.kid, "typ": "JWT"}, f10cClaims(now))
	parts := strings.Split(tok, ".")
	none := b64.EncodeToString([]byte(`{"alg":"none","kid":"` + s.kid + `","typ":"JWT"}`))
	// A signature whose last character differs only in the unused trailing bits
	// is the same bytes under a lenient decoder; the strict decoder refuses it.
	last := parts[2][len(parts[2])-1]
	alt := string("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"[(strings.IndexByte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", last)+1)%64])
	for name, v := range map[string]string{
		"empty":             "",
		"one part":          parts[0],
		"two parts":         parts[0] + "." + parts[1],
		"four parts":        tok + ".x",
		"empty signature":   parts[0] + "." + parts[1] + ".",
		"alg none unsigned": none + "." + parts[1] + ".",
		"alg none signed":   none + "." + parts[1] + "." + parts[2],
		"bad base64 head":   "!!!." + parts[1] + "." + parts[2],
		"bad base64 body":   parts[0] + ".!!!." + parts[2],
		"bad base64 sig":    parts[0] + "." + parts[1] + ".!!!",
		"padded":            parts[0] + "=." + parts[1] + "." + parts[2],
		"non canonical":     parts[0] + "." + parts[1] + "." + parts[2][:len(parts[2])-1] + alt,
		"short signature":   parts[0] + "." + parts[1] + "." + parts[2][:20],
		"not json header":   b64.EncodeToString([]byte("nope")) + "." + parts[1] + "." + parts[2],
		"oversized":         tok + strings.Repeat("A", 9000),
	} {
		if _, err := s.Verify(v, "worker", now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A different issuer key never verifies, even with a copied kid.
	other := f10cSigner(t)
	if _, err := s.Verify(other.forge(t, map[string]any{"alg": "EdDSA", "kid": s.kid, "typ": "JWT"}, f10cClaims(now)), "worker", now); err == nil {
		t.Error("token signed by another key accepted under this kid")
	}
	// Tampered payload keeps the signature but must not verify.
	c := f10cClaims(now)
	c.Owner = "attacker"
	body, _ := json.Marshal(c)
	if _, err := s.Verify(parts[0]+"."+b64.EncodeToString(body)+"."+parts[2], "worker", now); err == nil {
		t.Error("tampered payload accepted")
	}
}

// F10b-07: scope is checked, not just the signature. Each case changes one
// field of an otherwise valid token or one expectation.
func TestF10cVerifyScopedBindsEveryIdentityField(t *testing.T) {
	s := f10cSigner(t)
	now := time.Unix(1_700_000_000, 0)
	deadline := now.Add(5 * time.Minute)
	for _, purpose := range []string{"worker", "service", "runner"} {
		tok, c, err := s.IssueBound("dev", "AppAlpha", "worker-1", "lease-1", "thread", purpose, now, deadline, "job-1", "task-1", 7)
		if err != nil {
			t.Fatal(err)
		}
		want := Expect{Purpose: purpose, App: "AppAlpha", Owner: "dev", Pool: "thread", Worker: "worker-1", Lease: "lease-1", Job: "job-1", Task: "task-1", PolicyVersion: 7}
		if got, err := s.VerifyScoped(tok, want, now); err != nil || got != c {
			t.Fatalf("%s control: %v", purpose, err)
		}
		bad := map[string]func(*Expect){
			"wrong app":     func(x *Expect) { x.App = "AppBeta" },
			"app prefix":    func(x *Expect) { x.App = "App" },
			"wrong owner":   func(x *Expect) { x.Owner = "other" },
			"wrong pool":    func(x *Expect) { x.Pool = "process" },
			"wrong worker":  func(x *Expect) { x.Worker = "worker-2" },
			"wrong lease":   func(x *Expect) { x.Lease = "lease-2" },
			"wrong job":     func(x *Expect) { x.Job = "job-2" },
			"wrong task":    func(x *Expect) { x.Task = "task-2" },
			"wrong version": func(x *Expect) { x.PolicyVersion = 8 },
			"no app":        func(x *Expect) { x.App = "" },
			"no owner":      func(x *Expect) { x.Owner = "" },
			"no pool":       func(x *Expect) { x.Pool = "" },
			"no worker":     func(x *Expect) { x.Worker = "" },
			"no lease":      func(x *Expect) { x.Lease = "" },
			"no purpose":    func(x *Expect) { x.Purpose = "" },
		}
		for name, mutate := range bad {
			x := want
			mutate(&x)
			if _, err := s.VerifyScoped(tok, x, now); err == nil {
				t.Errorf("%s/%s accepted", purpose, name)
			}
		}
		for _, other := range []string{"worker", "service", "runner"} {
			if other == purpose {
				continue
			}
			x := want
			x.Purpose = other
			if _, err := s.VerifyScoped(tok, x, now); err == nil {
				t.Errorf("purpose confusion: %s token accepted as %s", purpose, other)
			}
		}
		if _, err := s.VerifyScoped(tok, want, now.Add(MaxLifetime+time.Second)); err == nil {
			t.Errorf("%s expired token accepted", purpose)
		}
	}
	// The audience of a worker token is the bare app id, so it can never equal
	// the prefixed audience of a service or runner token for another app.
	if Audience("worker", "AppAlpha") != "AppAlpha" || Audience("runner", "AppAlpha") != "ccgsc:runner:AppAlpha" {
		t.Fatal("audience contract changed")
	}
}

// F10b-06: the signing key survives a new Signer over the same store (restart,
// second instance), the JWKS advertises exactly that key, and a key the store
// does not hold is refused.
func TestF10cSignerKeyStabilityAndJWKS(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := NewSigner(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSigner(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if first.kid != second.kid || first.Public().Keys[0] != second.Public().Keys[0] {
		t.Fatal("second instance generated a different key")
	}
	k := first.Public().Keys[0]
	if len(first.Public().Keys) != 1 || k.KeyType != "OKP" || k.Curve != "Ed25519" || k.Algorithm != "EdDSA" || k.Use != "sig" || len(k.KeyID) != 64 {
		t.Fatalf("JWKS shape: %+v", k)
	}
	pub, err := b64.DecodeString(k.X)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatal("JWKS public key")
	}
	now := time.Unix(1_700_000_000, 0)
	tok, _, err := first.Issue("dev", "AppAlpha", "w", "l", "thread", "worker", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.Verify(tok, "worker", now); err != nil {
		t.Fatalf("token from the first instance refused after restart: %v", err)
	}
	// A corrupted stored key must stop the signer, not sign with garbage.
	bad, err := sqlite.Open(filepath.Join(t.TempDir(), "bad.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	for name, raw := range map[string][]byte{"short": []byte("short"), "mismatched halves": append(make([]byte, 32), make([]byte, 32)...)} {
		if err = bad.Update(context.Background(), func(tx store.Tx) error { return tx.Put(keyBucket, signingKey, raw) }); err != nil {
			t.Fatal(err)
		}
		if _, err = NewSigner(context.Background(), bad); err == nil {
			t.Errorf("%s stored key accepted", name)
		}
	}
}
