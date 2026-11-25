package p2p

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPGXTwoHubsRelayAcrossSharedPostgres(t *testing.T) {
	dsn := os.Getenv("P2P_TEST_DSN")
	if dsn == "" {
		t.Skip("P2P_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	peers1, cfg1 := NewPGXStores(db)
	peers2, cfg2 := NewPGXStores(db)
	if err := peers1.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(ctx, `DELETE FROM p2p_messages; DELETE FROM p2p_peers; DELETE FROM p2p_config;`)
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM p2p_messages; DELETE FROM p2p_peers; DELETE FROM p2p_config;`)
	})
	if err := cfg1.SaveConfig(ctx, PoolThread, Config{Mode: ModeLan}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	hub1 := NewHub(peers1, cfg1, func() time.Time { return now })
	hub2 := NewHub(peers2, cfg2, func() time.Time { return now })
	alice := leasePeer("peer-pgx-a01", "app-1")
	bob := leasePeer("peer-pgx-b01", "app-1")
	foreign := leasePeer("peer-pgx-x01", "app-2")
	for _, peer := range []Peer{alice, bob, foreign} {
		if err := hub1.Register(ctx, peer); err != nil {
			t.Fatal(err)
		}
	}
	if err := hub1.Send(ctx, alice, bob.PeerID, offerEnvelope(t, "call-pgx-01")); err != nil {
		t.Fatalf("shared PostgreSQL relay: %v", err)
	}
	messages, _, err := hub2.Poll(ctx, bob, 0, MaxQueuedPerPeer)
	if err != nil || len(messages) != 1 || messages[0].Envelope.CallID != "call-pgx-01" {
		t.Fatalf("cross-hub poll: %v %#v", err, messages)
	}
	if err := hub2.Send(ctx, alice, foreign.PeerID, offerEnvelope(t, "call-pgx-cross")); err != ErrForbidden {
		t.Fatalf("cross-app relay got %v; want ErrForbidden", err)
	}
}
