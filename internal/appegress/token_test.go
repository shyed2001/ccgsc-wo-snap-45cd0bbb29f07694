package appegress

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store/sqlite"
)

func TestF10bEdDSAContract(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewSigner(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSigner(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	token, claims, err := s.Issue("owner", "AppAlpha", "worker", "lease-id", "thread", "worker", now, now.Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claims.ExpiresAt != now.Add(MaxLifetime).Unix() || claims.Audience != "AppAlpha" || claims.Owner != "owner" {
		t.Fatalf("bad claims: %+v", claims)
	}
	got, err := second.Verify(token, "worker", now)
	if err != nil || got != claims {
		t.Fatalf("cross-engine verify: %v", err)
	}
	if _, err = second.Verify(token, "runner", now); err == nil {
		t.Fatal("worker token accepted as runner grant")
	}
	if _, err = second.Verify(token, "worker", now.Add(MaxLifetime)); err == nil {
		t.Fatal("expired token accepted")
	}
	parts := strings.Split(token, ".")
	jwk := s.Public().Keys[0]
	pub, err := b64.DecodeString(jwk.X)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("JWKS did not verify compact JWS")
	}
	sig[0] ^= 1
	if _, err = s.Verify(parts[0]+"."+parts[1]+"."+b64.EncodeToString(sig), "worker", now); err == nil {
		t.Fatal("tampered signature accepted")
	}
	svc, sc, err := s.Issue("owner", "AppAlpha", "worker", "lease-id", "thread", "service", now, now.Add(45*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if sc.Audience != "ccgsc:service:AppAlpha" || sc.ExpiresAt != now.Add(45*time.Second).Unix() {
		t.Fatal("service purpose not separated")
	}
	if _, err = s.Verify(svc, "worker", now); err == nil {
		t.Fatal("service token accepted as worker token")
	}
	if _, err = s.Verify(svc, "service", now); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.Public())
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "\"d\"") {
		t.Fatal("private material exposed")
	}
}
func TestF10bPolicyCSPAndRelease(t *testing.T) {
	p, err := Normalize(Policy{Endpoints: []string{"wss://api.example.com", "https://API.Example.com/", "https://api.example.com"}, TokenServiceURL: "https://token.example.com/ccgsc/token"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ConnectSrc() != "https://api.example.com wss://api.example.com" {
		t.Fatalf("CSP: %s", p.ConnectSrc())
	}
	if (Policy{}).ConnectSrc() != "'none'" {
		t.Fatal("default network policy open")
	}
	for _, url := range []string{"https://*.example.com", "https://user:pass@example.com", "http://api.example.com", "https://api.example.com;", "https://api.example.com/path", "https://127.0.0.1", "https://api.example.com/?a=b", "https://api.example.com#x", "https://a.local", "https://api.example.com:444"} {
		if _, err := Normalize(Policy{Endpoints: []string{url}}); err == nil {
			t.Errorf("unsafe source accepted: %s", url)
		}
	}
	now := time.Unix(1700000000, 0)
	f := TabFence{}
	f.Bind("tp", "owner", "AppAlpha", "slot1")
	f.IdleSince = now
	if f.Permits("tp", "owner", "AppBeta") || f.Permits("pp", "owner", "AppAlpha") || f.Permits("tp", "other", "AppAlpha") {
		t.Fatal("cross app/pool/owner binding")
	}
	d := DefaultReleasePolicy()
	if d.CanRelease(f, false, now.Add(29*time.Second)) || !d.CanRelease(f, false, now.Add(30*time.Second)) || d.CanRelease(f, true, now.Add(time.Hour)) {
		t.Fatal("idle release boundary")
	}
	d.Mode = Immediate
	if !d.CanRelease(f, false, now) || d.CanRelease(f, true, now) {
		t.Fatal("immediate release boundary")
	}
}
