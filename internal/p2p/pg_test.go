// F11, 2026-11-25: PostgreSQL integration tests of the F11 relay store.
// These run ONLY against a real PostgreSQL (P2P_TEST_DSN set); the repo gate
// machines provide PG+Redis, so on the gate they run for real (0 skips on a
// green gate). They pin the multi-server safety properties: the same-app
// rule enforced by SQL, cursor polling and the config store.
//
// The blank import registers the database/sql driver already present in the
// module's dependency set.
package p2p

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func pgStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("P2P_TEST_DSN")
	if dsn == "" {
		t.Skip("P2P_TEST_DSN not set; PG store tests run on the gate")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := NewPGStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(),
			`DELETE FROM p2p_messages; DELETE FROM p2p_peers; DELETE FROM p2p_config;`)
		_ = db.Close()
	})
	return store
}

func TestPGRegisterPollSameApp(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	alice := leasePeer("peer-pg-alice1", "app-1")
	bob := leasePeer("peer-pg-bob001", "app-1")
	mallory := leasePeer("peer-pg-mallor", "app-2")
	for _, peer := range []Peer{alice, bob, mallory} {
		peer.SeenAt, peer.ExpiresAt = now, now.Add(peerTTL)
		if err := store.Register(ctx, peer); err != nil {
			t.Fatalf("register %s: %v", peer.PeerID, err)
		}
	}
	// Same-app push works.
	if err := store.Push(ctx, alice, bob.PeerID, offerEnvelope(t, "call-pg-same1"), now); err != nil {
		t.Fatalf("same-app push refused: %v", err)
	}
	// Cross-app push is refused BY THE STATEMENT.
	if err := store.Push(ctx, alice, mallory.PeerID, offerEnvelope(t, "call-pg-cross"), now); err != ErrForbidden {
		t.Fatalf("cross-app push got %v; want ErrForbidden", err)
	}
	// Cursor polling consumes exactly once.
	messages, cursor, err := store.Poll(ctx, bob.PeerID, 0, 8, now)
	if err != nil || len(messages) != 1 {
		t.Fatalf("poll: %v, %d", err, len(messages))
	}
	if messages[0].Envelope.CallID != "call-pg-same1" {
		t.Fatalf("wrong envelope delivered")
	}
	if messages2, _, _ := store.Poll(ctx, bob.PeerID, cursor, 8, now); len(messages2) != 0 {
		t.Fatalf("consumed envelope redelivered")
	}
	// Poll refreshes presence.
	fresh, err := store.Get(ctx, bob.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.ExpiresAt.After(now.Add(peerTTL - time.Second)) {
		t.Fatalf("poll must refresh presence, got %v", fresh.ExpiresAt)
	}
}

func TestPGConfigRoundTrip(t *testing.T) {
	store := pgStore(t)
	cs := NewPGConfigStore(store.db)
	ctx := context.Background()
	cfg, err := cs.LoadConfig(ctx, PoolThread)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeOff {
		t.Fatalf("absent row must default to off, got %v", cfg.Mode)
	}
	want := Config{Mode: ModeStun, StunURLs: []string{"stun:stun.corp.internal:3478", "stuns://stun2.corp.internal"}}
	if err := cs.SaveConfig(ctx, PoolThread, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := cs.LoadConfig(ctx, PoolThread)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != want.Mode || len(got.StunURLs) != len(want.StunURLs) {
		t.Fatalf("config round trip mismatch: %+v vs %+v", got, want)
	}
	if len(got.ICEConfig()) != 1 {
		t.Fatalf("stun config must surface exactly one ICE entry")
	}
	if err := cs.SaveConfig(ctx, PoolThread, Config{Mode: ModeLan, StunURLs: []string{"stun:x"}}); err == nil {
		t.Fatalf("lan config with stun urls must be refused")
	}
}

func TestPGSweep(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := leasePeer("peer-pg-stale1", "app-1")
	stale.SeenAt = now.Add(-2 * peerTTL)
	stale.ExpiresAt = now.Add(-peerTTL)
	if err := store.Register(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sweep(ctx, now); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := store.Get(ctx, stale.PeerID); err != ErrNotFound {
		t.Fatalf("expired registration must be swept, got %v", err)
	}
}
