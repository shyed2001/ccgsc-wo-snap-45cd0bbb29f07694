// Path: internal/p2p/hub.go | Author: GLM (Z.ai) for the owner | 2026-11-25
// What/why: the F11 signaling hub. The hub owns the rules the owner fixed in
// grill section 16 (G-F11-1/2, BINDING):
//
//	G-F11-1 peers only between tabs locked to the SAME app. Every envelope is
//	        relayed only after BOTH registrations (sender and target) resolve
//	        to the same app id, and the app id itself comes from the lease
//	        token (F10), never from a client body. The store enforces the same
//	        rule again at the database level (PostgreSQL multi-server safety).
//	G-F11-2 the mode decides discovery ICE configuration: 'lan' answers an
//	        empty iceServers list (host candidates only), 'stun' answers only
//	        the validated self-hosted entries. No public STUN, no TURN.
//
// Caps mirror rules.js: 10 signals/s per peer and at most 2 concurrent
// handshakes per peer (the in-memory limiter is per instance; the hard
// message-size and queue caps below are enforced everywhere, so a relay can
// never be talked past them).
package p2p

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Hub limits, mirroring rules.js SIGNAL_RATE_PER_SEC and SIGNAL_HANDSHAKES_MAX.
const (
	RatePerSecond = 10
	HandshakesMax = 2
	// peerTTL is rules.js PEER_TTL_MS: a registration dies unless refreshed.
	peerTTL = 90 * time.Second
	// DiscoverLimit bounds one discovery answer.
	DiscoverLimit = 8
)

// Hub is the signaling orchestrator over a PeerStore.
type Hub struct {
	store PeerStore
	cfg   ConfigStore
	now   func() time.Time

	mu        sync.Mutex
	rate      map[string][]time.Time // peerID -> recent send times
	handshake map[string][]time.Time // peerID -> active offer times
	lastSweep time.Time
}

// NewHub builds a hub over store.
func NewHub(store PeerStore, cs ConfigStore, now func() time.Time) *Hub {
	if now == nil {
		now = time.Now
	}
	return &Hub{store: store, cfg: cs, now: now, rate: map[string][]time.Time{}, handshake: map[string][]time.Time{}}
}

// LoadConfig returns the stored admin configuration of a pool; an absent row
// (or a nil config store) means off.
func (h *Hub) LoadConfig(ctx context.Context, pool string) (Config, error) {
	if !ValidPool(pool) {
		return Config{}, ErrInvalid
	}
	if h.cfg == nil {
		return Config{Mode: ModeOff}, nil
	}
	cfg, err := h.cfg.LoadConfig(ctx, pool)
	if err != nil {
		return Config{}, err
	}
	if cfg.Mode == "" {
		cfg = Config{Mode: ModeOff}
	}
	return cfg, nil
}

// SaveConfig stores the admin configuration after fail-closed validation.
func (h *Hub) SaveConfig(ctx context.Context, pool string, cfg Config) error {
	if !ValidPool(pool) {
		return ErrInvalid
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if h.cfg == nil {
		return ErrInvalid
	}
	return h.cfg.SaveConfig(ctx, pool, cfg)
}

// Register binds a random session peer id to the lease-authenticated app
// binding. The caller (API layer) passes the binding from the lease token.
func (h *Hub) Register(ctx context.Context, peer Peer) error {
	if !ValidPool(peer.Pool) || peer.AppID == "" || peer.OwnerID == "" || peer.LeaseID == "" {
		return ErrInvalid
	}
	if err := ValidatePeerID(peer.PeerID); err != nil {
		return ErrInvalid
	}
	now := h.now()
	peer.SeenAt = now
	peer.ExpiresAt = now.Add(peerTTL)
	h.sweepIfDue(ctx, now)
	return h.store.Register(ctx, peer)
}

// Leave removes a registration (best effort; the lease must match).
func (h *Hub) Leave(ctx context.Context, peerID, leaseID string) error {
	return h.store.Unregister(ctx, peerID, leaseID)
}

// DiscoveryAnswer is the payload of the discovery endpoint: the admin mode
// with its (already sanitized) ICE configuration and the same-app peers.
type DiscoveryAnswer struct {
	Mode       Mode            `json:"mode"`
	ICEServers []ICEServer     `json:"iceServers"`
	Peers      []DiscoveryPeer `json:"peers"`
}

// DiscoveryPeer is one same-app candidate. Deliberately narrow: no owner,
// lease or worker identifiers ever leave the server (G-F11-1, G-F10-2).
type DiscoveryPeer struct {
	PeerID string `json:"peerId"`
	AppID  string `json:"appId"`
	Mode   Mode   `json:"mode"`
}

// Discover lists same-app live peers plus the pool's ICE configuration.
// digest pins the request to a SHA-256 address (the digest the transfer will
// verify) but does not change who may appear: G-F11-1 is app-scoped only.
func (h *Hub) Discover(ctx context.Context, requester Peer, cfg Config, digest string) (DiscoveryAnswer, error) {
	if !ValidPool(requester.Pool) || requester.AppID == "" || requester.LeaseID == "" {
		return DiscoveryAnswer{}, ErrInvalid
	}
	if err := ValidateDigest(digest); err != nil {
		return DiscoveryAnswer{}, ErrInvalid
	}
	peers, err := h.store.SameAppPeers(ctx, requester.Pool, requester.AppID, requester.PeerID, DiscoverLimit)
	if err != nil {
		return DiscoveryAnswer{}, err
	}
	answer := DiscoveryAnswer{Mode: cfg.Mode, ICEServers: cfg.ICEConfig(), Peers: []DiscoveryPeer{}}
	for _, peer := range peers {
		answer.Peers = append(answer.Peers, DiscoveryPeer{PeerID: peer.PeerID, AppID: peer.AppID, Mode: peer.Mode})
	}
	return answer, nil
}

// Send relays one envelope from the sender's registration to a same-app peer.
// Errors: ErrInvalid (shape), ErrNotFound (target or sender not registered),
// ErrForbidden (cross-app), ErrTooMany (rate/queue/handshake caps).
func (h *Hub) Send(ctx context.Context, from Peer, toPeer string, env Envelope) error {
	if !ValidPool(from.Pool) || from.AppID == "" || from.LeaseID == "" {
		return ErrInvalid
	}
	if err := ValidatePeerID(toPeer); err != nil {
		return ErrInvalid
	}
	if err := ValidateEnvelope(&env); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	now := h.now()
	if err := h.limits(from.PeerID, env.Type, now); err != nil {
		return err
	}
	h.sweepIfDue(ctx, now)
	return h.store.Push(ctx, from, toPeer, env, now)
}

// Poll drains the peer's relay queue. The lease binding is verified against
// the registration before any message is returned.
func (h *Hub) Poll(ctx context.Context, peer Peer, after int64, limit int) ([]StoredMessage, int64, error) {
	if !ValidPool(peer.Pool) || peer.AppID == "" || peer.LeaseID == "" || after < 0 {
		return nil, 0, ErrInvalid
	}
	if limit <= 0 || limit > MaxQueuedPerPeer {
		limit = MaxQueuedPerPeer
	}
	registration, err := h.store.Get(ctx, peer.PeerID)
	if err != nil {
		return nil, 0, err
	}
	if registration.LeaseID != peer.LeaseID || registration.AppID != peer.AppID {
		return nil, 0, ErrForbidden
	}
	return h.store.Poll(ctx, peer.PeerID, after, limit, h.now())
}

// limits enforces the per-instance rate and handshake caps.
func (h *Hub) limits(peerID, envType string, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	times := h.rate[peerID]
	fresh := times[:0]
	for _, t := range times {
		if now.Sub(t) < time.Second {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= RatePerSecond {
		return ErrTooMany
	}
	h.rate[peerID] = append(fresh, now)

	if envType == TypeOffer {
		offers := h.handshake[peerID][:0]
		for _, t := range h.handshake[peerID] {
			if now.Sub(t) < 30*time.Second {
				offers = append(offers, t)
			}
		}
		if len(offers) >= HandshakesMax {
			h.handshake[peerID] = offers
			return ErrTooMany
		}
		h.handshake[peerID] = append(offers, now)
	}
	return nil
}

// sweepIfDue piggybacks the store sweep on hub activity (no goroutines).
func (h *Hub) sweepIfDue(ctx context.Context, now time.Time) {
	h.mu.Lock()
	due := h.lastSweep.IsZero() || now.Sub(h.lastSweep) >= 30*time.Second
	if due {
		h.lastSweep = now
	}
	h.mu.Unlock()
	if due {
		_, _ = h.store.Sweep(ctx, now) // best effort; the next window retries
	}
}
