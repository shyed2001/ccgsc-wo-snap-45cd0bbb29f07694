package appegress

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testRoundTrip func(*http.Request) (*http.Response, error)

func (f testRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var f10cNow = time.Unix(1_700_000_000, 0)

func f10cReq() ServiceRequest {
	return ServiceRequest{AppID: "App", WorkerID: "worker", LeaseID: "lease", Pool: "thread", IssuedAt: f10cNow.Unix(), ExpiresAt: f10cNow.Unix() + 60, Nonce: "n-1"}
}
func f10cResp(mut func(*ServiceResponse)) string {
	r := ServiceResponse{Token: "opaque-token", ExpiresAt: f10cNow.Unix() + 50, AppID: "App", WorkerID: "worker", LeaseID: "lease", Pool: "thread", Nonce: "n-1"}
	if mut != nil {
		mut(&r)
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// F10b-09/F10b-24: the response must echo every binding of the request.
func TestF10bDeveloperTokenService(t *testing.T) {
	q := f10cReq()
	pad := func(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }
	for _, tc := range []struct {
		name   string
		body   string
		status int
		valid  bool
	}{
		{"valid", f10cResp(nil), 200, true},
		{"expires after request", f10cResp(func(r *ServiceResponse) { r.ExpiresAt = q.ExpiresAt + 1 }), 200, false},
		{"already expired", f10cResp(func(r *ServiceResponse) { r.ExpiresAt = f10cNow.Unix() }), 200, false},
		{"no expiry", f10cResp(func(r *ServiceResponse) { r.ExpiresAt = 0 }), 200, false},
		{"trailing document", f10cResp(nil) + "{}", 200, false},
		{"redirect", f10cResp(nil), 302, false},
		{"server error", f10cResp(nil), 500, false},
		{"not json", strings.Repeat("x", 8193), 200, false},
		{"body exactly 8192", pad(f10cResp(nil), 8192), 200, true},
		{"body 8193", pad(f10cResp(nil), 8193), 200, false},
		{"unknown field", strings.Replace(f10cResp(nil), "{", `{"extra":1,`, 1), 200, false},
		{"legacy unbound response", `{"token":"opaque","expires_at":1700000050}`, 200, false},
		{"wrong app", f10cResp(func(r *ServiceResponse) { r.AppID = "Other" }), 200, false},
		{"wrong worker", f10cResp(func(r *ServiceResponse) { r.WorkerID = "other" }), 200, false},
		{"wrong lease", f10cResp(func(r *ServiceResponse) { r.LeaseID = "other" }), 200, false},
		{"wrong pool", f10cResp(func(r *ServiceResponse) { r.Pool = "process" }), 200, false},
		{"wrong nonce", f10cResp(func(r *ServiceResponse) { r.Nonce = "n-2" }), 200, false},
		{"missing nonce", f10cResp(func(r *ServiceResponse) { r.Nonce = "" }), 200, false},
		{"empty token", f10cResp(func(r *ServiceResponse) { r.Token = "" }), 200, false},
		{"token with space", f10cResp(func(r *ServiceResponse) { r.Token = "a b" }), 200, false},
		{"token with CRLF", f10cResp(func(r *ServiceResponse) { r.Token = "a\r\nX: y" }), 200, false},
		{"token non ascii", f10cResp(func(r *ServiceResponse) { r.Token = "tokén" }), 200, false},
		{"token 4096", f10cResp(func(r *ServiceResponse) { r.Token = strings.Repeat("a", 4096) }), 200, true},
		{"token 4097", f10cResp(func(r *ServiceResponse) { r.Token = strings.Repeat("a", 4097) }), 200, false},
	} {
		client := &http.Client{Transport: testRoundTrip(func(r *http.Request) (*http.Response, error) {
			var got ServiceRequest
			if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer service-token" || json.NewDecoder(r.Body).Decode(&got) != nil || got != q {
				t.Fatalf("%s: bad service request %+v", tc.name, got)
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
		})}
		_, err := fetchToken(context.Background(), client, "https://token.example.com/token", "service-token", q, f10cNow)
		if (err == nil) != tc.valid {
			t.Errorf("%s: valid=%v err=%v", tc.name, tc.valid, err)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "0.0.0.0", "224.0.0.1", "fe80::1", "192.168.1.1", "172.16.0.1", "64:ff9b::7f00:1", "2002:7f00:1::1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Fatalf("private IP admitted: %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(ip)) {
			t.Fatalf("public IP refused: %s", ip)
		}
	}
}

type f10cService struct {
	srv     *httptest.Server
	roots   *x509.CertPool
	calls   atomic.Int32
	handler http.HandlerFunc
}

func f10cStart(t *testing.T) *f10cService {
	t.Helper()
	s := &f10cService{}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.handler(w, r)
	}))
	t.Cleanup(s.srv.Close)
	s.roots = x509.NewCertPool()
	s.roots.AddCert(s.srv.Certificate())
	return s
}

// f10cDNS answers `answer`, counts lookups and dials, and always dials the local
// test server instead of the (fake public) address.
type f10cDNS struct {
	answer  []netip.Addr
	err     error
	lookups atomic.Int32
	dials   []string
	target  string
}

func (d *f10cDNS) resolver() resolver {
	return resolver{
		lookup: func(context.Context, string) ([]netip.Addr, error) { d.lookups.Add(1); return d.answer, d.err },
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d.dials = append(d.dials, address)
			var nd net.Dialer
			return nd.DialContext(ctx, network, d.target)
		},
	}
}

func f10cCredential(t *testing.T, s *Signer, purpose string) (string, Expect) {
	t.Helper()
	tok, _, err := s.IssueBound("dev", "App", "worker", "lease", "thread", purpose, f10cNow, f10cNow.Add(5*time.Minute), "job", "task", 3)
	if err != nil {
		t.Fatal(err)
	}
	return tok, Expect{App: "App", Owner: "dev", Pool: "thread", Worker: "worker", Lease: "lease", Job: "job", Task: "task", PolicyVersion: 3}
}

// F10b-08/F10b-10/F10b-24: real TLS, injected resolver. Resolve once, vet every
// address, dial the literal, verify the certificate against the original host.
func TestF10cDeveloperTokenServiceTransport(t *testing.T) {
	signer := f10cSigner(t)
	svc := f10cStart(t)
	cred, want := f10cCredential(t, signer, "service")
	var seen struct {
		auth, host string
		req        ServiceRequest
	}
	svc.handler = func(w http.ResponseWriter, r *http.Request) {
		seen.auth, seen.host = r.Header.Get("Authorization"), r.Host
		_ = json.NewDecoder(r.Body).Decode(&seen.req)
		_, _ = io.WriteString(w, f10cResp(func(x *ServiceResponse) { x.Nonce = seen.req.Nonce }))
	}
	public := []netip.Addr{netip.MustParseAddr("8.8.8.8")}
	q := f10cReq()
	// The nonce of the request is the id of the signed credential, not a value the
	// caller chooses.
	q.Nonce = "caller-chosen"
	dns := &f10cDNS{answer: public, target: svc.srv.Listener.Addr().String()}
	out, err := fetchDeveloperToken(context.Background(), dns.resolver(), svc.roots, signer, "https://example.com/ccgsc/token", cred, want, q, f10cNow)
	if err != nil {
		t.Fatalf("control request: %v", err)
	}
	claims, _ := signer.Verify(cred, "service", f10cNow)
	if out.Token != "opaque-token" || seen.req.Nonce != claims.ID || seen.req.Nonce == "caller-chosen" || seen.auth != "Bearer "+cred || seen.host != "example.com" {
		t.Fatalf("request/response binding: %+v %q", seen, out.Token)
	}
	if dns.lookups.Load() != 1 || len(dns.dials) != 1 || dns.dials[0] != "8.8.8.8:443" {
		t.Fatalf("resolve once and dial the vetted literal: lookups=%d dials=%v", dns.lookups.Load(), dns.dials)
	}

	t.Run("certificate must match the approved host", func(t *testing.T) {
		before := svc.calls.Load()
		d := &f10cDNS{answer: public, target: svc.srv.Listener.Addr().String()}
		if _, err := fetchDeveloperToken(context.Background(), d.resolver(), svc.roots, signer, "https://other.example.org/ccgsc/token", cred, want, q, f10cNow); err == nil {
			t.Fatal("certificate for a different host name accepted")
		}
		if svc.calls.Load() != before {
			t.Fatal("request reached the server despite the TLS failure")
		}
	})
	t.Run("untrusted certificate", func(t *testing.T) {
		d := &f10cDNS{answer: public, target: svc.srv.Listener.Addr().String()}
		if _, err := fetchDeveloperToken(context.Background(), d.resolver(), nil, signer, "https://example.com/ccgsc/token", cred, want, q, f10cNow); err == nil {
			t.Fatal("self-signed certificate accepted by the system trust store")
		}
	})
	t.Run("forbidden address sets never dial", func(t *testing.T) {
		for name, answer := range map[string][]string{
			"loopback":      {"127.0.0.1"},
			"private":       {"10.1.2.3"},
			"metadata":      {"169.254.169.254"},
			"mixed":         {"8.8.8.8", "10.0.0.1"},
			"mixed reorder": {"192.168.0.9", "8.8.8.8"},
			"v6 loopback":   {"::1"},
			"v4 mapped":     {"::ffff:127.0.0.1"},
			"v6 mixed":      {"2606:4700:4700::1111", "fc00::1"},
			"zoned":         {"fe80::1%eth0"},
		} {
			var addrs []netip.Addr
			for _, a := range answer {
				addrs = append(addrs, netip.MustParseAddr(a))
			}
			d := &f10cDNS{answer: addrs, target: svc.srv.Listener.Addr().String()}
			before := svc.calls.Load()
			if _, err := fetchDeveloperToken(context.Background(), d.resolver(), svc.roots, signer, "https://example.com/ccgsc/token", cred, want, q, f10cNow); err == nil {
				t.Errorf("%s: forbidden answer accepted", name)
			}
			if len(d.dials) != 0 || svc.calls.Load() != before {
				t.Errorf("%s: dialed %v", name, d.dials)
			}
		}
		for name, d := range map[string]*f10cDNS{
			"empty answer": {target: svc.srv.Listener.Addr().String()},
			"lookup error": {err: errors.New("nxdomain"), target: svc.srv.Listener.Addr().String()},
		} {
			if _, err := fetchDeveloperToken(context.Background(), d.resolver(), svc.roots, signer, "https://example.com/ccgsc/token", cred, want, q, f10cNow); err == nil || len(d.dials) != 0 {
				t.Errorf("%s: %v %v", name, err, d.dials)
			}
		}
	})
	t.Run("redirect is not followed", func(t *testing.T) {
		svc.handler = func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
		}
		d := &f10cDNS{answer: public, target: svc.srv.Listener.Addr().String()}
		before := svc.calls.Load()
		if _, err := fetchDeveloperToken(context.Background(), d.resolver(), svc.roots, signer, "https://example.com/ccgsc/token", cred, want, q, f10cNow); err == nil {
			t.Fatal("redirect accepted")
		}
		if svc.calls.Load() != before+1 || len(d.dials) != 1 {
			t.Fatalf("redirect followed: calls=%d dials=%v", svc.calls.Load()-before, d.dials)
		}
	})
	t.Run("timeout is bounded", func(t *testing.T) {
		release := make(chan struct{})
		svc.handler = func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
		defer close(release)
		d := &f10cDNS{answer: public, target: svc.srv.Listener.Addr().String()}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, err := fetchDeveloperToken(ctx, d.resolver(), svc.roots, signer, "https://example.com/ccgsc/token", cred, want, q, f10cNow); err == nil {
			t.Fatal("stalled service accepted")
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("not bounded: %v", time.Since(start))
		}
	})
}

// F10b-08: the credential is verified BEFORE any request is made.
func TestF10cServiceCredentialVerifiedBeforeForwarding(t *testing.T) {
	signer := f10cSigner(t)
	other := f10cSigner(t)
	svc := f10cStart(t)
	svc.handler = func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, f10cResp(nil)) }
	good, want := f10cCredential(t, signer, "service")
	worker, _ := f10cCredential(t, signer, "worker")
	runner, _ := f10cCredential(t, signer, "runner")
	foreign, _ := f10cCredential(t, other, "service")
	issue := func(owner, app, w, lease, pool, job string, at time.Time) string {
		tok, _, err := signer.IssueBound(owner, app, w, lease, pool, "service", at, at.Add(5*time.Minute), job, "task", 3)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	for name, cred := range map[string]string{
		"opaque string": "service-token", "empty": "", "worker purpose": worker, "runner purpose": runner, "foreign key": foreign,
		"expired":   issue("dev", "App", "worker", "lease", "thread", "job", f10cNow.Add(-time.Hour)),
		"wrong app": issue("dev", "Other", "worker", "lease", "thread", "job", f10cNow), "wrong owner": issue("evil", "App", "worker", "lease", "thread", "job", f10cNow),
		"wrong worker": issue("dev", "App", "other", "lease", "thread", "job", f10cNow), "wrong lease": issue("dev", "App", "worker", "other", "thread", "job", f10cNow),
		"wrong pool": issue("dev", "App", "worker", "lease", "process", "job", f10cNow), "wrong job": issue("dev", "App", "worker", "lease", "thread", "other", f10cNow),
	} {
		d := &f10cDNS{answer: []netip.Addr{netip.MustParseAddr("8.8.8.8")}, target: svc.srv.Listener.Addr().String()}
		if _, err := fetchDeveloperToken(context.Background(), d.resolver(), svc.roots, signer, "https://example.com/ccgsc/token", cred, want, f10cReq(), f10cNow); err == nil {
			t.Errorf("%s: credential accepted", name)
		}
		if d.lookups.Load() != 0 || len(d.dials) != 0 {
			t.Errorf("%s: network used before verification", name)
		}
	}
	// A request that disagrees with the verified credential is refused too.
	for name, mut := range map[string]func(*ServiceRequest){
		"app":    func(q *ServiceRequest) { q.AppID = "Other" },
		"worker": func(q *ServiceRequest) { q.WorkerID = "other" },
		"lease":  func(q *ServiceRequest) { q.LeaseID = "other" },
		"pool":   func(q *ServiceRequest) { q.Pool = "process" },
	} {
		q := f10cReq()
		mut(&q)
		d := &f10cDNS{answer: []netip.Addr{netip.MustParseAddr("8.8.8.8")}, target: svc.srv.Listener.Addr().String()}
		if _, err := fetchDeveloperToken(context.Background(), d.resolver(), svc.roots, signer, "https://example.com/ccgsc/token", good, want, q, f10cNow); err == nil || len(d.dials) != 0 {
			t.Errorf("request %s differs from credential: %v", name, err)
		}
	}
	// Endpoint shape is still enforced (no IP, no http, no private suffix).
	for _, u := range []string{"http://example.com/t", "https://127.0.0.1/t", "https://example.com", "https://a.internal/t", "https://example.com:8443/t", "https://u:p@example.com/t", "https://example.com/../t"} {
		d := &f10cDNS{answer: []netip.Addr{netip.MustParseAddr("8.8.8.8")}, target: svc.srv.Listener.Addr().String()}
		if _, err := fetchDeveloperToken(context.Background(), d.resolver(), svc.roots, signer, u, good, want, f10cReq(), f10cNow); err == nil || d.lookups.Load() != 0 {
			t.Errorf("endpoint %s accepted", u)
		}
	}
}
