// Path: internal/grpcapi/event_gap_stream_R17_test.go | Author: CommandCode for the owner | 2026-11-23
// What/why: R18B review row 4 - the event stream must not lose a transaction that commits
// out of id order. The durable ids come from a PostgreSQL SEQUENCE, and a transaction
// consumes its id before it commits: another coordinator can commit a higher id first, so
// the snapshot cursor passes an id whose transaction is still uncommitted. The old stream
// skipped every row below the cursor, so that event was lost forever. The stream now
// deduplicates by exact id and delivers a new row even when it sorts below the cursor.
package grpcapi

import (
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
)

// TestR17StreamDeliversAnEventCommittedBelowTheCursor pins review row 4: the snapshot
// cursor advanced past a gap id (the higher id committed first), and the transaction that
// holds the lower id commits later. The stream delivers that event instead of dropping it.
func TestR17StreamDeliversAnEventCommittedBelowTheCursor(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "r17_gap_stream.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.New(db, config.Default().JWTSecret, "s1")
	raw, _, err := svc.CreateKey(t.Context(), auth.User{ID: auth.ID(), AccountStore: "B", Roles: []string{"developer"}}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.Exchange(t.Context(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.Validate(t.Context(), tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	s := New(engine, svc)

	// The committed state before the stream connects: ids 1, 2 and 4 (id 3 is the gap -
	// its transaction is still uncommitted, exactly like a sequence id consumed by a
	// transaction that has not committed yet).
	put := func(id uint64) {
		t.Helper()
		key := fmt.Sprintf("%020d", id)
		if err := s.Engine.Store.Update(context.Background(), func(tx store.Tx) error {
			return tx.Put(store.Events, key, pool.Event{ID: key, Type: "job.queued", JobID: "gap-job", At: time.Now().UTC(), Owner: claims.KeyID, PoolID: pool.Thread})
		}); err != nil {
			t.Fatalf("put event %d: %v", id, err)
		}
	}
	put(1)
	put(2)
	put(4)

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+tok.AccessToken))
	msgs := make(chan *EventMessage, 16)
	errs := make(chan error, 1)
	go func() {
		errs <- s.streamEvents(ctx, &EventStreamRequest{}, pool.Thread, func(msg *EventMessage) error {
			msgs <- msg
			return nil
		})
	}()

	// The snapshot delivers 1, 2 and 4 in id order.
	var want []uint64
	for len(want) < 3 {
		select {
		case msg := <-msgs:
			var id uint64
			if _, err := fmt.Sscanf(msg.GetEventId(), "%020d", &id); err != nil {
				t.Fatalf("event id %q: %v", msg.GetEventId(), err)
			}
			want = append(want, id)
		case err := <-errs:
			t.Fatalf("the stream ended during the snapshot: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatalf("snapshot delivered %v, want 3 events", want)
		}
	}
	if fmt.Sprint(want) != "[1 2 4]" {
		t.Fatalf("snapshot ids = %v, want [1 2 4]", want)
	}

	// The transaction of the gap id commits AFTER the cursor already passed it.
	put(3)

	// The late event must be delivered even though it sorts below the cursor.
	select {
	case msg := <-msgs:
		if msg.GetEventId() != fmt.Sprintf("%020d", 3) {
			t.Fatalf("late event id = %q, want %q (the committed gap event was replaced by something else)", msg.GetEventId(), fmt.Sprintf("%020d", 3))
		}
		if msg.GetType() != "job.queued" {
			t.Fatalf("late event type = %q, want job.queued", msg.GetType())
		}
	case err := <-errs:
		t.Fatalf("the stream ended before the late event: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the event that committed below the cursor was never delivered (the stream skipped it forever)")
	}
}
