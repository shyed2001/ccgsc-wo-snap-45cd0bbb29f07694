// F11, 2026-11-25: red->green hub tests over MemStore. The same semantics
// are re-tested against PostgreSQL in pg_test.go on the gate machines
// (P2P_TEST_DSN); there the same-app rule is additionally enforced by the
// INSERT ... SELECT statement itself (PostgreSQL multi-server safety).
// RED on base 063bfac5: this package does not exist.
package p2p

import (
	"context"
	"testing"
	"time"
)

func hubNow(offset func(*time.Time) time.Duration) func() time.Time {
	current := time.Unix(1700000000, 0)
	return func() time.Time {
		current = current.Add(offset(&current))
		return current
	}
}

func leasePeer(peerID, appID string) Peer {
	return Peer{PeerID: peerID, Pool: PoolThread, AppID: appID, OwnerID: "owner-" + appID,
		LeaseID: "lease-" + peerID, WorkerID: "worker-" + peerID, Mode: ModeLan}
}

func offerEnvelope(t *testing.T, callID string) Envelope {
	t.Helper()
	env := Envelope{V: 1, Type: TypeOffer, CallID: callID, SDP: "v=0...", Digest: digest64()}
	if err := ValidateEnvelope(&env); err != nil {
		t.Fatalf("test envelope invalid: %v", err)
	}
	return env
}

func TestRegisterRequiresLeaseBinding(t *testing.T) {
	store := NewMemStore()
	hub := NewHub(store, nil, func() time.Time { return time.Unix(1700000000, 0) })
	ctx := context.Background()
	peer := leasePeer("peer-alpha-01", "app-1")
	if err := hub.Register(ctx, peer); err != nil {
		t.Fatalf("register refused: %v", err)
	}
	if err := hub.Register(ctx, Peer{PeerID: "peer-alpha-01", Pool: PoolThread}); err == nil {
		t.Fatalf("registration without app/lease binding must be refused")
	}
	if err := hub.Register(ctx, Peer{PeerID: "short", Pool: PoolThread, AppID: "a", LeaseID: "l"}); err == nil {
		t.Fatalf("malformed peer id must be refused")
	}
	if err := hub.Register(ctx, Peer{PeerID: "peer-alpha-02", Pool: "zz", AppID: "a", LeaseID: "l"}); err == nil {
		t.Fatalf("unknown pool must be refused")
	}
}

func TestPeerIDConflictAcrossLiveLeases(t *testing.T) {
	store := NewMemStore()
	// Real wall clock: MemStore compares expiry against time.Now().
	current := time.Now()
	hub := NewHub(store, nil, func() time.Time { return current })
	ctx := context.Background()
	first := leasePeer("peer-alpha-01", "app-1")
	first.ExpiresAt = current.Add(peerTTL)
	if err := store.Register(ctx, first); err != nil {
		t.Fatal(err)
	}
	hijack := leasePeer("peer-alpha-01", "app-1")
	hijack.LeaseID = "lease-attacker"
	if err := hub.Register(ctx, hijack); err != ErrConflict {
		t.Fatalf("live peer id must not be stolen by another lease, got %v", err)
	}
	current = current.Add(2 * peerTTL) // stale registration: the id may rotate
	if err := hub.Register(ctx, hijack); err != nil {
		t.Fatalf("stale registration must be replaceable, got %v", err)
	}
}

func TestDiscoverIsSameAppOnlyAndLeakFree(t *testing.T) {
	store := NewMemStore()
	hub := NewHub(store, nil, nil)
	ctx := context.Background()
	if err := hub.Register(ctx, leasePeer("peer-alpha-01", "app-1")); err != nil {
		t.Fatal(err)
	}
	if err := hub.Register(ctx, leasePeer("peer-alpha-02", "app-1")); err != nil {
		t.Fatal(err)
	}
	if err := hub.Register(ctx, leasePeer("peer-beta-01", "app-2")); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Mode: ModeLan}
	answer, err := hub.Discover(ctx, leasePeer("peer-alpha-01", "app-1"), cfg, digest64())
	if err != nil {
		t.Fatalf("discover refused: %v", err)
	}
	if answer.Mode != ModeLan || len(answer.ICEServers) != 0 {
		t.Fatalf("LAN-only discovery must carry no STUN server: %+v", answer)
	}
	if len(answer.Peers) != 1 || answer.Peers[0].PeerID != "peer-alpha-02" {
		t.Fatalf("G-F11-1 violated: discovery crossed the app boundary: %+v", answer.Peers)
	}
	for _, peer := range answer.Peers {
		if peer.AppID != "app-1" {
			t.Fatalf("cross-app peer in discovery: %+v", peer)
		}
	}
	if _, err := hub.Discover(ctx, leasePeer("peer-alpha-01", "app-1"), cfg, "nothex"); err == nil {
		t.Fatalf("non-digest discovery must be refused")
	}
	// The self peer is never offered.
	for _, peer := range answer.Peers {
		if peer.PeerID == "peer-alpha-01" {
			t.Fatalf("discovery must exclude the requester itself")
		}
	}
}

func TestRelayNeverCrossesAppsEvenAcrossInstances(t *testing.T) {
	// Two hubs over ONE store model two server instances of the deployment.
	store := NewMemStore()
	ctx := context.Background()
	current := time.Unix(1700000000, 0)
	hubA := NewHub(store, nil, func() time.Time { return current })
	hubB := NewHub(store, nil, func() time.Time { return current })
	if err := hubA.Register(ctx, leasePeer("peer-alpha-01", "app-1")); err != nil {
		t.Fatal(err)
	}
	if err := hubB.Register(ctx, leasePeer("peer-beta-01", "app-2")); err != nil {
		t.Fatal(err)
	}
	err := hubA.Send(ctx, leasePeer("peer-alpha-01", "app-1"), "peer-beta-01", offerEnvelope(t, "call-app-cross"))
	if err != ErrForbidden {
		t.Fatalf("G-F11-1 violated: cross-app relay got %v; want ErrForbidden", err)
	}
	// A bye carries no handshake accounting; the sender rate window is 10/s.
	bye := Envelope{V: 1, Type: TypeBye, CallID: "call-app-gone"}
	if err := hubA.Send(ctx, leasePeer("peer-alpha-01", "app-1"), "peer-alpha-missing", bye); err != ErrNotFound {
		t.Fatalf("relay to unknown peer got %v; want ErrNotFound", err)
	}
	// Same app relays fine.
	if err := hubA.Register(ctx, leasePeer("peer-alpha-02", "app-1")); err != nil {
		t.Fatal(err)
	}
	if err := hubA.Send(ctx, leasePeer("peer-alpha-01", "app-1"), "peer-alpha-02", offerEnvelope(t, "call-app-same")); err != nil {
		t.Fatalf("same-app relay refused: %v", err)
	}
	messages, _, err := hubB.Poll(ctx, leasePeer("peer-alpha-02", "app-1"), 0, 8)
	if err != nil {
		t.Fatalf("poll refused: %v", err)
	}
	if len(messages) != 1 || messages[0].Envelope.CallID != "call-app-same" {
		t.Fatalf("relay queue wrong: %+v", messages)
	}
	if messages[0].FromApp != "app-1" {
		t.Fatalf("relay must carry the verified app binding")
	}
}

func TestRateAndHandshakeCaps(t *testing.T) {
	store := NewMemStore()
	current := time.Unix(1700000000, 0)
	hub := NewHub(store, nil, func() time.Time { current = current.Add(10 * time.Millisecond); return current })
	ctx := context.Background()
	if err := hub.Register(ctx, leasePeer("peer-alpha-01", "app-1")); err != nil {
		t.Fatal(err)
	}
	if err := hub.Register(ctx, leasePeer("peer-alpha-02", "app-1")); err != nil {
		t.Fatal(err)
	}
	sender := leasePeer("peer-alpha-01", "app-1")
	// Rate cap: 10 byes per rolling second, the 11th is refused. Byes carry no
	// handshake accounting, so only the rate limiter is under test here.
	for i := 0; i < RatePerSecond; i++ {
		bye := Envelope{V: 1, Type: TypeBye, CallID: "call-bye-" + string(rune('a'+i)) + "xx"}
		if err := hub.Send(ctx, sender, "peer-alpha-02", bye); err != nil {
			t.Fatalf("bye %d refused: %v", i, err)
		}
	}
	if err := hub.Send(ctx, sender, "peer-alpha-02", Envelope{V: 1, Type: TypeBye, CallID: "call-bye-overf"}); err != ErrTooMany {
		t.Fatalf("rate cap not enforced, got %v", err)
	}
	// Handshake cap: a THIRD live offer inside the window is refused. A fresh
	// hub keeps the rate window clear; each tick advances one second.
	store2 := NewMemStore()
	current2 := time.Unix(1700000000, 0)
	hub2 := NewHub(store2, nil, func() time.Time { current2 = current2.Add(time.Second); return current2 })
	if err := hub2.Register(ctx, leasePeer("peer-beta-01", "app-1")); err != nil {
		t.Fatal(err)
	}
	if err := hub2.Register(ctx, leasePeer("peer-beta-02", "app-1")); err != nil {
		t.Fatal(err)
	}
	for i, callID := range []string{"call-offer-aaaa", "call-offer-bbbb", "call-offer-cccc"} {
		err := hub2.Send(ctx, leasePeer("peer-beta-01", "app-1"), "peer-beta-02", offerEnvelope(t, callID))
		if i < HandshakesMax && err != nil {
			t.Fatalf("offer %d refused: %v", i, err)
		}
		if i == HandshakesMax && err != ErrTooMany {
			t.Fatalf("handshake cap not enforced, got %v", err)
		}
	}
}

func TestConfigStoreDefaultsOffAndValidates(t *testing.T) {
	cs := NewMemConfigStore()
	hub := NewHub(NewMemStore(), cs, nil)
	ctx := context.Background()
	cfg, err := hub.LoadConfig(ctx, PoolThread)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeOff {
		t.Fatalf("absent admin row must default to off, got %v", cfg.Mode)
	}
	if err := hub.SaveConfig(ctx, PoolThread, Config{Mode: ModeLan}); err != nil {
		t.Fatalf("lan config refused: %v", err)
	}
	if err := hub.SaveConfig(ctx, PoolThread, Config{Mode: ModeStun, StunURLs: []string{"stun:stun.l.google.com:19302"}}); err == nil {
		t.Fatalf("public STUN must be refused")
	}
	if err := hub.SaveConfig(ctx, PoolThread, Config{Mode: ModeStun, StunURLs: []string{"stun:stun.corp.internal:3478"}}); err != nil {
		t.Fatalf("self-hosted stun config refused: %v", err)
	}
	saved, err := hub.LoadConfig(ctx, PoolThread)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ICEConfig()) != 1 {
		t.Fatalf("stun config must surface exactly one ICE entry")
	}
	if err := hub.SaveConfig(ctx, "zz", Config{Mode: ModeOff}); err == nil {
		t.Fatalf("unknown pool must be refused")
	}
}

func TestPollCursorAndExpiredMessages(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	current := time.Unix(1700000000, 0)
	hub := NewHub(store, nil, func() time.Time { return current })
	if err := hub.Register(ctx, leasePeer("peer-alpha-01", "app-1")); err != nil {
		t.Fatal(err)
	}
	if err := hub.Register(ctx, leasePeer("peer-alpha-02", "app-1")); err != nil {
		t.Fatal(err)
	}
	if err := hub.Send(ctx, leasePeer("peer-alpha-01", "app-1"), "peer-alpha-02", offerEnvelope(t, "call-app-one")); err != nil {
		t.Fatal(err)
	}
	messages, cursor, err := hub.Poll(ctx, leasePeer("peer-alpha-02", "app-1"), 0, 8)
	if err != nil || len(messages) != 1 {
		t.Fatalf("first poll wrong: %v %d", err, len(messages))
	}
	// Consumed: a second poll sees nothing.
	if messages2, _, _ := hub.Poll(ctx, leasePeer("peer-alpha-02", "app-1"), cursor, 8); len(messages2) != 0 {
		t.Fatalf("consumed envelopes must not redeliver")
	}
	// A message left to expire is never delivered.
	if err := hub.Send(ctx, leasePeer("peer-alpha-01", "app-1"), "peer-alpha-02", offerEnvelope(t, "call-app-two")); err != nil {
		t.Fatal(err)
	}
	current = current.Add(MessageTTLSeconds*time.Second + time.Minute)
	if messages3, _, _ := hub.Poll(ctx, leasePeer("peer-alpha-02", "app-1"), 0, 8); len(messages3) != 0 {
		t.Fatalf("expired envelope must not deliver")
	}
	// Polling refreshes the presence window.
	peer, err := store.Get(ctx, "peer-alpha-02")
	if err != nil {
		t.Fatal(err)
	}
	if !peer.ExpiresAt.After(current) {
		t.Fatalf("poll must refresh the presence window")
	}
}
