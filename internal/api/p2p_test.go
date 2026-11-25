// F11, 2026-11-25: red->green tests of the pools-core P2P HTTP surface.
// RED on base 063bfac5: internal/api/p2p.go does not exist. The tests wire
// stub authenticators through the SAME SetP2PAuthenticator seam the server
// assembly will use; the production lease-token check itself is F10 code and
// is covered there.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ccgsc/poolscore/internal/p2p"
)

func p2pTestHub(t *testing.T) (*p2p.Hub, func()) {
	t.Helper()
	config := p2p.NewMemConfigStore()
	if err := config.SaveConfig(context.Background(), p2p.PoolThread, p2p.Config{Mode: p2p.ModeLan}); err != nil {
		t.Fatal(err)
	}
	hub := p2p.NewHub(p2p.NewMemStore(), config, func() time.Time { return time.Now() })
	SetP2PAuthenticator(
		func(_ context.Context, r *http.Request, pool string) (p2p.Peer, error) {
			app := r.Header.Get("X-Test-App")
			if app == "" {
				return p2p.Peer{}, context.Canceled
			}
			if !p2p.ValidPool(pool) {
				return p2p.Peer{}, context.Canceled
			}
			return p2p.Peer{PeerID: r.Header.Get("X-Test-Peer"), Pool: pool, AppID: app,
				OwnerID: "owner-" + app, LeaseID: "lease-" + app, WorkerID: "w-" + app}, nil
		},
		func(_ context.Context, r *http.Request) error {
			if r.Header.Get("X-Test-Admin") == "" {
				return context.Canceled
			}
			return nil
		})
	return hub, func() { SetP2PAuthenticator(nil, nil) }
}

func p2pDo(t *testing.T, handler http.Handler, method, path string, headers map[string]string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		blob, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(blob)
	}
	request := httptest.NewRequest(method, path, reader)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	out := map[string]any{}
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
			t.Fatalf("non-JSON response %q: %v", recorder.Body.String(), err)
		}
	}
	return recorder, out
}

func TestP2PRoutesFailClosedWithoutAuthWiring(t *testing.T) {
	SetP2PAuthenticator(nil, nil)
	handler := P2PMux(nil)
	recorder, _ := p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/register",
		nil, map[string]string{"peerId": "peer-alpha-01"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired peer auth must 503, got %d", recorder.Code)
	}
	recorder, _ = p2pDo(t, handler, http.MethodGet, "/thread-pool/v1/p2p/config", nil, nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired admin auth must 503, got %d", recorder.Code)
	}
}

func TestP2PRegisterLeaveAndAuth(t *testing.T) {
	hub, done := p2pTestHub(t)
	defer done()
	handler := P2PMux(hub)
	recorder, body := p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/register",
		map[string]string{"X-Test-App": "app-1", "X-Test-Peer": "peer-alpha-01"},
		map[string]string{"peerId": "peer-alpha-01"})
	if recorder.Code != http.StatusOK || body["ttlMs"].(float64) <= 0 {
		t.Fatalf("register failed: %d %v", recorder.Code, body)
	}

	recorder, _ = p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/register", nil,
		map[string]string{"peerId": "peer-beta-01"})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing lease token must 401, got %d", recorder.Code)
	}

	recorder, _ = p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/leave",
		map[string]string{"X-Test-App": "app-1", "X-Test-Peer": "peer-alpha-01"},
		map[string]string{"peerId": "peer-alpha-01"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("leave failed: %d", recorder.Code)
	}
}

func TestP2PModeOffRefusesSignal(t *testing.T) {
	hub := p2p.NewHub(p2p.NewMemStore(), p2p.NewMemConfigStore(), time.Now)
	defer SetP2PAuthenticator(nil, nil)
	SetP2PAuthenticator(
		func(_ context.Context, r *http.Request, pool string) (p2p.Peer, error) {
			return p2p.Peer{PeerID: r.Header.Get("X-Test-Peer"), Pool: pool,
				AppID: "app-1", OwnerID: "owner-1", LeaseID: "lease-1", WorkerID: "worker-1"}, nil
		},
		func(context.Context, *http.Request) error { return nil },
	)
	handler := P2PMux(hub)
	offer := map[string]any{"v": 1, "type": "offer", "callId": "call-off-01",
		"sdp": "v=0", "digest": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	recorder, _ := p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/signal",
		map[string]string{"X-Test-Peer": "peer-off-01"},
		map[string]any{"from": "peer-off-01", "to": "peer-off-02", "envelope": offer})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("off mode must refuse signal, got %d", recorder.Code)
	}
}

func TestP2PSignalRelayIsSameAppOnly(t *testing.T) {
	hub, done := p2pTestHub(t)
	defer done()
	handler := P2PMux(hub)
	for _, registration := range []struct{ app, peer string }{
		{"app-1", "peer-alpha-01"}, {"app-1", "peer-alpha-02"}, {"app-2", "peer-beta-01"},
	} {
		recorder, _ := p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/register",
			map[string]string{"X-Test-App": registration.app, "X-Test-Peer": registration.peer},
			map[string]string{"peerId": registration.peer})
		if recorder.Code != http.StatusOK {
			t.Fatalf("register %s: %d", registration.peer, recorder.Code)
		}
	}
	offer := map[string]any{"v": 1, "type": "offer", "callId": "call-app-same1",
		"sdp": "v=0...", "digest": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	headers := map[string]string{"X-Test-App": "app-1", "X-Test-Peer": "peer-alpha-01"}
	recorder, _ := p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/signal", headers,
		map[string]any{"from": "peer-alpha-01", "to": "peer-alpha-02", "envelope": offer})
	if recorder.Code != http.StatusOK {
		t.Fatalf("same-app send refused: %d %s", recorder.Code, recorder.Body.String())
	}
	// Cross-app: refused by the relay (G-F11-1).
	recorder, _ = p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/signal", headers,
		map[string]any{"from": "peer-alpha-01", "to": "peer-beta-01", "envelope": offer})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-app relay must 403, got %d", recorder.Code)
	}
	// Spoofed sender: the body 'from' must match the lease-authenticated peer.
	recorder, _ = p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/signal", headers,
		map[string]any{"from": "peer-alpha-02", "to": "peer-alpha-02", "envelope": offer})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("spoofed sender must 403, got %d", recorder.Code)
	}
	// Oversized SDP: refused by the caps (rules.js mirror).
	big := map[string]any{"v": 1, "type": "offer", "callId": "call-app-big1",
		"sdp": make([]byte, 17*1024), "digest": offer["digest"].(string)}
	recorder, _ = p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/signal", headers,
		map[string]any{"from": "peer-alpha-01", "to": "peer-alpha-02", "envelope": big})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized sdp must 400, got %d", recorder.Code)
	}
	// The target drains the envelope with its cursor.
	recorder, out := p2pDo(t, handler, http.MethodGet,
		"/thread-pool/v1/p2p/signal?after=0&peer=peer-alpha-02",
		map[string]string{"X-Test-App": "app-1", "X-Test-Peer": "peer-alpha-02"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("poll refused: %d", recorder.Code)
	}
	messages := out["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("poll must deliver exactly the same-app envelope: %v", out)
	}
	if _, has := messages[0].(map[string]any)["fromApp"]; !has {
		t.Fatalf("poll must carry the verified fromApp binding")
	}
	// Polling with a foreign peer id is refused.
	recorder, _ = p2pDo(t, handler, http.MethodGet,
		"/thread-pool/v1/p2p/signal?after=0&peer=peer-alpha-01",
		map[string]string{"X-Test-App": "app-1", "X-Test-Peer": "peer-alpha-02"}, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("foreign peer poll must 403, got %d", recorder.Code)
	}
}

func TestP2PDiscoveryAndConfigAdminGate(t *testing.T) {
	hub, done := p2pTestHub(t)
	defer done()
	handler := P2PMux(hub)
	recorder, _ := p2pDo(t, handler, http.MethodPost, "/thread-pool/v1/p2p/register",
		map[string]string{"X-Test-App": "app-1", "X-Test-Peer": "peer-alpha-01"},
		map[string]string{"peerId": "peer-alpha-01"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("register failed: %d", recorder.Code)
	}
	// Config PUT without admin auth is refused.
	recorder, _ = p2pDo(t, handler, http.MethodPut, "/thread-pool/v1/p2p/config",
		map[string]string{}, map[string]any{"mode": "lan"})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("admin config without admin auth must 401, got %d", recorder.Code)
	}
	// LAN-only config: discovery must answer an EMPTY iceServers list.
	recorder, _ = p2pDo(t, handler, http.MethodPut, "/thread-pool/v1/p2p/config",
		map[string]string{"X-Test-Admin": "yes"}, map[string]any{"mode": "lan"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin lan config refused: %d", recorder.Code)
	}
	recorder, out := p2pDo(t, handler, http.MethodGet, "/thread-pool/v1/p2p/peers?digest=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		map[string]string{"X-Test-App": "app-1", "X-Test-Peer": "peer-alpha-01"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("discovery refused: %d", recorder.Code)
	}
	if out["mode"] != "lan" || out["iceServers"] != nil {
		t.Fatalf("LAN-only mode must have no STUN: %v", out)
	}
	// STUN config with a public provider is refused; self-hosted passes.
	recorder, _ = p2pDo(t, handler, http.MethodPut, "/thread-pool/v1/p2p/config",
		map[string]string{"X-Test-Admin": "yes"},
		map[string]any{"mode": "stun", "stunUrls": []string{"stun:stun.l.google.com:19302"}})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("public STUN must be refused, got %d", recorder.Code)
	}
	recorder, _ = p2pDo(t, handler, http.MethodPut, "/thread-pool/v1/p2p/config",
		map[string]string{"X-Test-Admin": "yes"},
		map[string]any{"mode": "stun", "stunUrls": []string{"stun:stun.corp.internal:3478", "turn:evil.internal"}})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("config with a turn url must be refused, got %d", recorder.Code)
	}
	recorder, _ = p2pDo(t, handler, http.MethodPut, "/thread-pool/v1/p2p/config",
		map[string]string{"X-Test-Admin": "yes"},
		map[string]any{"mode": "stun", "stunUrls": []string{"stun:stun.corp.internal:3478"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("self-hosted stun config refused: %d", recorder.Code)
	}
	recorder, out = p2pDo(t, handler, http.MethodGet, "/thread-pool/v1/p2p/peers?digest=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		map[string]string{"X-Test-App": "app-1", "X-Test-Peer": "peer-alpha-01"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("discovery refused: %d", recorder.Code)
	}
	iceServers := out["iceServers"].([]any)
	if out["mode"] != "stun" || len(iceServers) != 1 {
		t.Fatalf("stun discovery wrong: %v", out)
	}
	entry := iceServers[0].(map[string]any)
	urls := entry["urls"].([]any)
	if len(urls) != 1 || urls[0] != "stun:stun.corp.internal:3478" {
		t.Fatalf("stun urls wrong: %v", entry)
	}
	for _, key := range []string{"username", "credential", "credentialType"} {
		if _, has := entry[key]; has {
			t.Fatalf("ICE payload must never carry %s (no secrets in the browser)", key)
		}
	}
	// The registration payload never carries lease or worker ids to peers.
	peers := out["peers"].([]any)
	if len(peers) != 0 {
		t.Fatalf("a single registered tab must see no other peers: %v", peers)
	}
	// Process pool is an independent admin surface.
	recorder, _ = p2pDo(t, handler, http.MethodPut, "/process-pool/v1/p2p/config",
		map[string]string{"X-Test-Admin": "yes"}, map[string]any{"mode": "lan"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("process pool config refused: %d", recorder.Code)
	}
}
