// Path: internal/p2p/store.go | Author: GLM (Z.ai) for the owner | 2026-11-25
// What/why: the peer registration + relay queue contract of F11 signaling and
// an in-memory implementation for tests and single-instance development.
//
// PostgreSQL multi-server safety: PRODUCTION USES PGStore (pg.go). The
// same-app rule of G-F11-1 is enforced by the database itself in pg.go
// (INSERT ... SELECT ... WHERE EXISTS), so two server instances can never
// race a cross-app relay past the check. MemStore implements the identical
// semantics for unit tests; it is NOT sufficient for multi-instance deploys.
package p2p

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Sentinel errors of the store contract.
var (
	ErrNotFound  = errors.New("p2p: peer not found")
	ErrForbidden = errors.New("p2p: cross-app relay refused")
	ErrConflict  = errors.New("p2p: peer id held by another live lease")
	ErrTooMany   = errors.New("p2p: rate or queue limit exceeded")
	ErrInvalid   = errors.New("p2p: invalid input")
)

// Pool identifiers, matching transport.js scopeOf and the API prefixes.
const (
	PoolThread  = "tp"
	PoolProcess = "pp"
)

// ValidPool reports whether pool is one of the two pools.
func ValidPool(pool string) bool {
	return pool == PoolThread || pool == PoolProcess
}

// Peer is one registered tab. The app binding comes from the lease token
// (F10), never from the client body.
type Peer struct {
	PeerID    string    `json:"peerId"`
	Pool      string    `json:"pool"`
	AppID     string    `json:"appId"`
	OwnerID   string    `json:"ownerId"`
	LeaseID   string    `json:"leaseId"`
	WorkerID  string    `json:"workerId"`
	Mode      Mode      `json:"mode"`
	SeenAt    time.Time `json:"seenAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// StoredMessage is one relayed envelope with its relay-verified bindings.
type StoredMessage struct {
	ID       int64     `json:"id"`
	FromPeer string    `json:"fromPeer"`
	FromApp  string    `json:"fromApp"`
	Envelope Envelope  `json:"envelope"`
	ExpireAt time.Time `json:"-"`
}

// PeerStore is the persistence contract of the hub.
type PeerStore interface {
	// Register upserts a peer registration bound to a lease. It fails with
	// ErrConflict when a DIFFERENT live lease holds the peer id.
	Register(ctx context.Context, p Peer) error
	// Unregister removes a registration when the lease matches.
	Unregister(ctx context.Context, peerID, leaseID string) error
	// Get returns one registration.
	Get(ctx context.Context, peerID string) (Peer, error)
	// SameAppPeers lists live registrations of one pool+app except excludePeer.
	SameAppPeers(ctx context.Context, pool, appID, excludePeer string, limit int) ([]Peer, error)
	// Push stores one envelope for toPeer. It enforces the same-app rule
	// between the SENDER registration and the TARGET registration at the
	// database level and fails with ErrForbidden when they differ.
	Push(ctx context.Context, from Peer, toPeer string, env Envelope, now time.Time) error
	// Poll drains up to limit envelopes for toPeer after the cursor and
	// consumes them; it also refreshes the peer's presence window.
	Poll(ctx context.Context, peerID string, after int64, limit int, now time.Time) ([]StoredMessage, int64, error)
	// Sweep deletes expired registrations and envelopes.
	Sweep(ctx context.Context, now time.Time) (int64, error)
}

// MemStore is the in-memory PeerStore. All methods take the hub's mutex.
type MemStore struct {
	mu     sync.Mutex
	peers  map[string]Peer
	queues map[string][]StoredMessage
	cursor int64
}

// NewMemStore builds an empty store.
func NewMemStore() *MemStore {
	return &MemStore{peers: map[string]Peer{}, queues: map[string][]StoredMessage{}}
}

// Register implements PeerStore.
func (m *MemStore) Register(_ context.Context, p Peer) error {
	if !ValidPool(p.Pool) || p.PeerID == "" || p.AppID == "" || p.LeaseID == "" {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.peers[p.PeerID]; ok &&
		old.LeaseID != p.LeaseID && time.Now().Before(old.ExpiresAt) {
		return ErrConflict
	}
	m.peers[p.PeerID] = p
	return nil
}

// Unregister implements PeerStore.
func (m *MemStore) Unregister(_ context.Context, peerID, leaseID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.peers[peerID]; !ok || old.LeaseID != leaseID {
		return nil
	}
	delete(m.peers, peerID)
	return nil
}

// Get implements PeerStore.
func (m *MemStore) Get(_ context.Context, peerID string) (Peer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.peers[peerID]
	if !ok {
		return Peer{}, ErrNotFound
	}
	return p, nil
}

// SameAppPeers implements PeerStore.
func (m *MemStore) SameAppPeers(_ context.Context, pool, appID, excludePeer string, limit int) ([]Peer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := []Peer{}
	for _, p := range m.peers {
		if p.Pool == pool && p.AppID == appID && p.PeerID != excludePeer && now.Before(p.ExpiresAt) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].PeerID < out[k].PeerID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Push implements PeerStore with the same-app rule the SQL store enforces.
func (m *MemStore) Push(_ context.Context, from Peer, toPeer string, env Envelope, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	target, ok := m.peers[toPeer]
	if !ok || now.After(target.ExpiresAt) {
		return ErrNotFound
	}
	sender, ok := m.peers[from.PeerID]
	if !ok || now.After(sender.ExpiresAt) {
		return ErrNotFound
	}
	if target.AppID != sender.AppID || target.Pool != sender.Pool {
		return ErrForbidden
	}
	queue := m.queues[toPeer]
	if len(queue) >= MaxQueuedPerPeer {
		return ErrTooMany
	}
	m.cursor++
	m.queues[toPeer] = append(queue, StoredMessage{
		ID: m.cursor, FromPeer: from.PeerID, FromApp: sender.AppID,
		Envelope: env, ExpireAt: now.Add(MessageTTLSeconds * time.Second),
	})
	return nil
}

// Poll implements PeerStore.
func (m *MemStore) Poll(_ context.Context, peerID string, after int64, limit int, now time.Time) ([]StoredMessage, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.peers[peerID]; ok {
		p.SeenAt = now
		p.ExpiresAt = now.Add(peerTTL)
		m.peers[peerID] = p
	}
	queue := m.queues[peerID]
	kept := queue[:0]
	out := []StoredMessage{}
	maxID := after
	for _, message := range queue {
		if message.ID <= after {
			kept = append(kept, message)
			continue
		}
		if now.After(message.ExpireAt) {
			continue // expired: dropped, never delivered
		}
		if len(out) < limit {
			out = append(out, message)
			if message.ID > maxID {
				maxID = message.ID
			}
			continue
		}
		kept = append(kept, message)
	}
	m.queues[peerID] = kept
	return out, maxID, nil
}

// Sweep implements PeerStore.
func (m *MemStore) Sweep(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var removed int64
	for id, p := range m.peers {
		if now.After(p.ExpiresAt) {
			delete(m.peers, id)
			delete(m.queues, id)
			removed++
		}
	}
	for id := range m.queues {
		if _, ok := m.peers[id]; !ok {
			queue := m.queues[id]
			kept := queue[:0]
			for _, message := range queue {
				if now.Before(message.ExpireAt) {
					kept = append(kept, message)
				}
			}
			m.queues[id] = kept
			if _, ok := m.peers[id]; !ok && len(kept) == 0 {
				delete(m.queues, id)
			}
		}
	}
	return removed, nil
}
