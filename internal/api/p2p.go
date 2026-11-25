// Path: internal/api/p2p.go | Author: GLM (Z.ai) for the owner | 2026-11-25
// What/why: the F11 production P2P HTTP surface of pools-core, per owner
// grill section 16 (G-F11-1/2, BINDING): signaling + same-app discovery on
// pools-core, admin mode 'stun' (self-hosted STUN-only) or 'lan' (host
// candidates) per pool, no public STUN, no TURN, code default off.
//
// WIRING SEAMS (the only two lines the server assembly needs):
//
//	SetP2PAuthenticator(peerAuth, adminAuth)  — lease-token / admin checks
//	mount P2PMux(hub) under both pool prefixes
//
// Both fail closed (401/503) until wired, so a build without F10 tokens can
// never expose an open relay. The peer binding (app/owner/lease/worker) is
// derived by the authenticator from the lease token (F10), never from the
// request body.
//
// Paths (both prefixes, matching transport.js scopeOf pools):
//
//	POST {prefix}/p2p/register   {peerId}            -> {ttlMs}
//	POST {prefix}/p2p/leave      {peerId}            -> {left:true}
//	GET  {prefix}/p2p/peers                          -> {mode, iceServers, peers:[{peerId,appId,mode}]}
//	POST {prefix}/p2p/signal     {from,to,envelope}  -> {sent:true}
//	GET  {prefix}/p2p/signal?after=N&peer=ID     -> {cursor, messages:[{id,fromPeer,fromApp,envelope}]}
//	GET  {prefix}/p2p/config   (admin)             -> {mode, stunUrls}
//	PUT  {prefix}/p2p/config   (admin) {mode,stunUrls} -> {mode, stunUrls}
//
// PostgreSQL multi-server safety: all state lives in internal/p2p PGStore;
// this file holds no per-peer state. No secrets are returned to the browser:
// STUN needs no credential and the ICE payload cannot express one.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ccgsc/poolscore/internal/p2p"
)

// PeerAuthenticator resolves the lease token of a P2P request into the app
// binding the relay trusts (F10 signed lease token).
type PeerAuthenticator func(ctx context.Context, r *http.Request, pool string) (p2p.Peer, error)

// AdminAuthenticator checks an admin session for config access.
type AdminAuthenticator func(ctx context.Context, r *http.Request) error

var (
	p2pAuthMu   sync.RWMutex
	p2pPeerAuth PeerAuthenticator
	p2pAdmin    AdminAuthenticator
)

// SetP2PAuthenticator installs the two authenticators (server assembly only).
func SetP2PAuthenticator(peer PeerAuthenticator, admin AdminAuthenticator) {
	p2pAuthMu.Lock()
	defer p2pAuthMu.Unlock()
	p2pPeerAuth, p2pAdmin = peer, admin
}

// P2PMux returns the handler for the /p2p/ routes of both pool prefixes.
// hub must be built over p2p.NewPGStore(db) in the server assembly; a nil
// hub answers 503 and nothing else.
func P2PMux(hub *p2p.Hub) http.Handler {
	mux := http.NewServeMux()
	for _, prefix := range []string{"/thread-pool/v1/p2p/", "/process-pool/v1/p2p/"} {
		pool := p2p.PoolThread
		if strings.HasPrefix(prefix, "/process-pool") {
			pool = p2p.PoolProcess
		}
		p := prefix
		mux.HandleFunc(p+"register", func(w http.ResponseWriter, r *http.Request) { handleP2PRegister(hub, pool, w, r) })
		mux.HandleFunc(p+"leave", func(w http.ResponseWriter, r *http.Request) { handleP2PLeave(hub, pool, w, r) })
		mux.HandleFunc(p+"peers", func(w http.ResponseWriter, r *http.Request) { handleP2PPeers(hub, pool, w, r) })
		mux.HandleFunc(p+"signal", func(w http.ResponseWriter, r *http.Request) { handleP2PSignal(hub, pool, w, r) })
		mux.HandleFunc(p+"config", func(w http.ResponseWriter, r *http.Request) { handleP2PConfig(hub, pool, w, r) })
	}
	return mux
}

func writeP2PJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeP2PError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, p2p.ErrInvalid):
		writeP2PJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid"})
	case errors.Is(err, p2p.ErrForbidden):
		writeP2PJSON(w, http.StatusForbidden, map[string]string{"error": "cross-app"})
	case errors.Is(err, p2p.ErrNotFound):
		writeP2PJSON(w, http.StatusNotFound, map[string]string{"error": "no-peer"})
	case errors.Is(err, p2p.ErrConflict):
		writeP2PJSON(w, http.StatusConflict, map[string]string{"error": "peer-id-held"})
	case errors.Is(err, p2p.ErrTooMany):
		writeP2PJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate"})
	default:
		writeP2PJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
	}
}

// p2pAuth verifies the hook wiring and the lease token. Fail closed.
func p2pAuth(w http.ResponseWriter, r *http.Request, pool string) (p2p.Peer, bool) {
	p2pAuthMu.RLock()
	hook := p2pPeerAuth
	p2pAuthMu.RUnlock()
	if hook == nil {
		writeP2PJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "p2p-auth-not-wired"})
		return p2p.Peer{}, false
	}
	peer, err := hook(r.Context(), r, pool)
	if err != nil {
		writeP2PJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return p2p.Peer{}, false
	}
	return peer, true
}

type p2pRegisterBody struct {
	PeerID string `json:"peerId"`
}

func handleP2PRegister(hub *p2p.Hub, pool string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeP2PJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	peer, ok := p2pAuth(w, r, pool)
	if !ok {
		return
	}
	var body p2pRegisterBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeP2PJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid"})
		return
	}
	registration := peer
	registration.PeerID = body.PeerID
	if hub == nil {
		writeP2PJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "p2p-disabled"})
		return
	}
	cfg, err := hub.LoadConfig(r.Context(), pool)
	if err != nil {
		writeP2PError(w, err)
		return
	}
	registration.Mode = cfg.Mode
	if err := hub.Register(r.Context(), registration); err != nil {
		writeP2PError(w, err)
		return
	}
	writeP2PJSON(w, http.StatusOK, map[string]any{"ttlMs": int64(90 * time.Second / time.Millisecond)})
}

func handleP2PLeave(hub *p2p.Hub, pool string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeP2PJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	peer, ok := p2pAuth(w, r, pool)
	if !ok {
		return
	}
	var body p2pRegisterBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeP2PJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid"})
		return
	}
	if err := hub.Leave(r.Context(), body.PeerID, peer.LeaseID); err != nil {
		writeP2PError(w, err)
		return
	}
	writeP2PJSON(w, http.StatusOK, map[string]any{"left": true})
}

func handleP2PPeers(hub *p2p.Hub, pool string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeP2PJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	peer, ok := p2pAuth(w, r, pool)
	if !ok {
		return
	}
	if hub == nil {
		writeP2PJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "p2p-disabled"})
		return
	}
	// Workers read the stored admin configuration: the mode decides whether
	// they may dial and which ICE configuration applies (nil store -> off).
	cfg, err := hub.LoadConfig(r.Context(), pool)
	if err != nil {
		writeP2PError(w, err)
		return
	}
	digest := strings.TrimSpace(r.URL.Query().Get("digest"))
	answer, discErr := hub.Discover(r.Context(), peer, cfg, digest)
	if discErr != nil {
		writeP2PError(w, discErr)
		return
	}
	writeP2PJSON(w, http.StatusOK, answer)
}

type p2pSignalBody struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Envelope p2p.Envelope `json:"envelope"`
}

func handleP2PSignal(hub *p2p.Hub, pool string, w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		peer, ok := p2pAuth(w, r, pool)
		if !ok {
			return
		}
		if hub == nil {
			writeP2PJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "p2p-disabled"})
			return
		}
		cfg, err := hub.LoadConfig(r.Context(), pool)
		if err != nil {
			writeP2PError(w, err)
			return
		}
		if cfg.Mode == p2p.ModeOff {
			writeP2PJSON(w, http.StatusForbidden, map[string]string{"error": "p2p-off"})
			return
		}
		var body p2pSignalBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&body); err != nil {
			writeP2PJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid"})
			return
		}
		// The sender identity comes from the LEASE, not the body: the client
		// 'from' field must match the authenticated peer id when present.
		sender := peer
		sender.PeerID = body.From
		if body.From != "" && body.From != peer.PeerID {
			writeP2PJSON(w, http.StatusForbidden, map[string]string{"error": "unknown-peer"})
			return
		}
		if err := hub.Send(r.Context(), sender, body.To, body.Envelope); err != nil {
			writeP2PError(w, err)
			return
		}
		writeP2PJSON(w, http.StatusOK, map[string]any{"sent": true})
	case http.MethodGet:
		peer, ok := p2pAuth(w, r, pool)
		if !ok {
			return
		}
		if hub == nil {
			writeP2PJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "p2p-disabled"})
			return
		}
		var after int64
		if raw := r.URL.Query().Get("after"); raw != "" {
			parsed, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr != nil || parsed < 0 {
				writeP2PJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid"})
				return
			}
			after = parsed
		}
		queryPeer := r.URL.Query().Get("peer")
		if queryPeer != "" && peer.PeerID != "" && queryPeer != peer.PeerID {
			writeP2PJSON(w, http.StatusForbidden, map[string]string{"error": "unknown-peer"})
			return
		}
		peer.PeerID = queryPeer
		if peer.PeerID == "" {
			writeP2PJSON(w, http.StatusBadRequest, map[string]string{"error": "peer"})
			return
		}
		messages, cursor, err := hub.Poll(r.Context(), peer, after, p2p.MaxQueuedPerPeer)
		if err != nil {
			writeP2PError(w, err)
			return
		}
		writeP2PJSON(w, http.StatusOK, map[string]any{"cursor": cursor, "messages": messages})
	default:
		writeP2PJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
	}
}

type p2pConfigBody struct {
	Mode     string   `json:"mode"`
	StunURLs []string `json:"stunUrls"`
}

func handleP2PConfig(hub *p2p.Hub, pool string, w http.ResponseWriter, r *http.Request) {
	p2pAuthMu.RLock()
	adminHook := p2pAdmin
	p2pAuthMu.RUnlock()
	if adminHook == nil {
		writeP2PJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "p2p-admin-not-wired"})
		return
	}
	if err := adminHook(r.Context(), r); err != nil {
		writeP2PJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if hub == nil {
		writeP2PJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "p2p-disabled"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, err := hub.LoadConfig(r.Context(), pool)
		if err != nil {
			writeP2PError(w, err)
			return
		}
		writeP2PJSON(w, http.StatusOK, map[string]any{"mode": string(cfg.Mode), "stunUrls": cfg.StunURLs})
	case http.MethodPut:
		var body p2pConfigBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body); err != nil {
			writeP2PJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid"})
			return
		}
		mode, err := p2p.ParseMode(body.Mode)
		if err != nil {
			writeP2PJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid"})
			return
		}
		cfg := p2p.Config{Mode: mode, StunURLs: body.StunURLs}
		if err := hub.SaveConfig(r.Context(), pool, cfg); err != nil {
			writeP2PError(w, err)
			return
		}
		writeP2PJSON(w, http.StatusOK, map[string]any{"mode": string(cfg.Mode), "stunUrls": cfg.StunURLs})
	default:
		writeP2PJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
	}
}
