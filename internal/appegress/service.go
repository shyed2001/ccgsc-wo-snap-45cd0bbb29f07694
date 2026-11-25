package appegress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

const maxTokenResponse = 8192

var forbiddenRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range forbiddenRanges {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// resolver is the DNS and dial surface of the token-service client. Production
// uses the system resolver and a plain TCP dialer; tests substitute both.
type resolver struct {
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

func systemResolver() resolver {
	d := net.Dialer{Timeout: 3 * time.Second}
	return resolver{
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dial: d.DialContext,
	}
}

// DialContext resolves the approved host ONCE, refuses the whole answer set when
// ANY address is non-public (a mixed public/private answer is a rebinding
// primitive), and then dials only the vetted literals. The transport never
// resolves again, and TLS still verifies the original host name because the
// request URL, not the dialed literal, supplies the server name.
func (r resolver) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || host == "" {
		return nil, ErrInvalid
	}
	if _, err = netip.ParseAddr(host); err == nil {
		return nil, errors.New("developer token service address must be a host name")
	}
	ips, err := r.lookup(ctx, host)
	if err != nil || len(ips) == 0 || len(ips) > 16 {
		return nil, errors.New("developer token service DNS unavailable")
	}
	vetted := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if ip.Zone() != "" || !publicIP(ip) {
			return nil, errors.New("developer token service resolves to forbidden address")
		}
		vetted = append(vetted, ip.Unmap())
	}
	for _, ip := range vetted {
		conn, err := r.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("developer token service connection unavailable")
}

// ServiceRequest is the body sent to the developer token service. Nonce is the
// id of the signed service credential and must be echoed back.
type ServiceRequest struct {
	AppID     string `json:"app_id"`
	WorkerID  string `json:"worker_id"`
	LeaseID   string `json:"lease_id"`
	Pool      string `json:"pool"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	Nonce     string `json:"nonce"`
}

// ServiceResponse echoes every binding of the request next to the opaque token.
// CCGSC cannot inspect the developer token itself, so the response is accepted
// only when it proves it was produced for exactly this request.
type ServiceResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	AppID     string `json:"app_id"`
	WorkerID  string `json:"worker_id"`
	LeaseID   string `json:"lease_id"`
	Pool      string `json:"pool"`
	Nonce     string `json:"nonce"`
}

// FetchDeveloperToken asks the developer's token service for one token. The
// service credential must be a CCGSC-signed purpose=service token bound to the
// same app, owner, pool, worker and lease as the request; it is verified here
// before any byte leaves the server. Everything fails closed.
func FetchDeveloperToken(ctx context.Context, signer *Signer, endpointURL, serviceToken string, want Expect, q ServiceRequest, now time.Time) (ServiceResponse, error) {
	return fetchDeveloperToken(ctx, systemResolver(), nil, signer, endpointURL, serviceToken, want, q, now)
}
func fetchDeveloperToken(ctx context.Context, r resolver, roots *x509.CertPool, signer *Signer, endpointURL, serviceToken string, want Expect, q ServiceRequest, now time.Time) (ServiceResponse, error) {
	if signer == nil {
		return ServiceResponse{}, ErrInvalid
	}
	endpointURL, err := endpoint(endpointURL, true)
	if err != nil {
		return ServiceResponse{}, err
	}
	want.Purpose = "service"
	claims, err := signer.VerifyScoped(serviceToken, want, now)
	if err != nil || q.AppID != want.App || q.WorkerID != want.Worker || q.LeaseID != want.Lease || q.Pool != want.Pool {
		return ServiceResponse{}, ErrInvalid
	}
	q.Nonce = claims.ID
	u, err := url.Parse(endpointURL)
	if err != nil {
		return ServiceResponse{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tr := &http.Transport{
		DialContext:            r.DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname(), RootCAs: roots},
		TLSHandshakeTimeout:    3 * time.Second,
		ResponseHeaderTimeout:  3 * time.Second,
		MaxResponseHeaderBytes: 8192,
		DisableKeepAlives:      true,
		DisableCompression:     true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return fetchToken(ctx, client, endpointURL, serviceToken, q, now)
}
func fetchToken(ctx context.Context, client *http.Client, endpointURL, serviceToken string, q ServiceRequest, now time.Time) (ServiceResponse, error) {
	if serviceToken == "" || q.AppID == "" || q.WorkerID == "" || q.LeaseID == "" || q.Nonce == "" || q.ExpiresAt <= now.Unix() {
		return ServiceResponse{}, ErrInvalid
	}
	data, err := json.Marshal(q)
	if err != nil {
		return ServiceResponse{}, ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(data))
	if err != nil {
		return ServiceResponse{}, ErrInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	res, err := client.Do(req)
	if err != nil {
		return ServiceResponse{}, errors.New("developer token service unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ServiceResponse{}, errors.New("developer token service refused request")
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxTokenResponse+1))
	if err != nil || len(b) > maxTokenResponse {
		return ServiceResponse{}, ErrInvalid
	}
	var out ServiceResponse
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || dec.Decode(new(any)) != io.EOF || !printableToken(out.Token) ||
		out.ExpiresAt <= now.Unix() || out.ExpiresAt > q.ExpiresAt ||
		out.AppID != q.AppID || out.WorkerID != q.WorkerID || out.LeaseID != q.LeaseID || out.Pool != q.Pool || out.Nonce != q.Nonce {
		return ServiceResponse{}, ErrInvalid
	}
	return out, nil
}

// printableToken accepts only visible ASCII: the value is placed in an
// Authorization header and a WebSocket message by the browser.
func printableToken(t string) bool {
	if t == "" || len(t) > 4096 {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] < 0x21 || t[i] > 0x7e {
			return false
		}
	}
	return true
}
