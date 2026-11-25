// Path: internal/store/pg/event_sequence_A2_R17_test.go | Author: CommandCode for the owner | 2026-11-22
// What/why: WO R17-SCALE item 3 (A2) - the durable event sequence id. The engine used
// to read-increment-write one "event_sequence" row inside the settlement transaction;
// PostgreSQL now answers from a SEQUENCE, so two event writers never serialize on one
// row. A sequence value is consumed even when the transaction rolls back, so the test
// proves the consumers tolerate a gap.
package pg

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// openIsolatedSchema opens the store against a fresh schema of the test, so the
// sequence state of one run never leaks into the next.
func openIsolatedSchema(t *testing.T) (store.Store, string) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		t.Fatal("POOLS_PG_DSN is not set and this test is part of the A2 gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL configuration: %v", err)
	}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = admin.Close(c)
	})
	schema := fmt.Sprintf("r17_seq_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = admin.Exec(c, "DROP SCHEMA "+schema+" CASCADE")
	})
	scoped, err := scopedDSN(dsn, schema)
	if err != nil {
		t.Fatalf("scope the DSN: %v", err)
	}
	db, err := Open(scoped)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, schema
}

// TestEventSequenceIsATableSequence pins that the store really hands out SEQUENCE
// values: two calls give strictly increasing numbers and a rolled back transaction still
// consumed its value (the gap the replay has to tolerate).
func TestEventSequenceIsATableSequence(t *testing.T) {
	db, _ := openIsolatedSchema(t)
	ctx := context.Background()
	var first, second, rolled uint64
	if err := db.Update(ctx, func(tx store.Tx) error {
		var err error
		first, err = transactionSequence(tx, "pool_event_seq")
		return err
	}); err != nil {
		t.Fatalf("first sequence: %v", err)
	}
	rollback := errors.New("force rollback")
	if err := db.Update(ctx, func(tx store.Tx) error {
		var err error
		if rolled, err = transactionSequence(tx, "pool_event_seq"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("forced rollback = %v", err)
	}
	if err := db.Update(ctx, func(tx store.Tx) error {
		var err error
		second, err = transactionSequence(tx, "pool_event_seq")
		return err
	}); err != nil {
		t.Fatalf("second sequence: %v", err)
	}
	if !(first < rolled && rolled < second) {
		t.Fatalf("sequence values = %d, %d, %d, want strictly increasing including the rolled back one", first, rolled, second)
	}
	if second != rolled+1 {
		t.Fatalf("the rollback did not consume its sequence value: rolled %d, next %d", rolled, second)
	}
}

// scopedDSN points every pooled connection at the isolated schema, for both URL and
// keyword DSN forms, without touching the environment.
func scopedDSN(dsn, schema string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		q.Set("connect_timeout", "10")
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return dsn + " search_path=" + schema + ",public connect_timeout=10", nil
}

// transactionSequence reads the next value of a named sequence through the optional
// store.SequenceTx contract.
func transactionSequence(tx store.Tx, name string) (uint64, error) {
	sequence, ok := tx.(store.SequenceTx)
	if !ok {
		return 0, fmt.Errorf("the store does not implement store.SequenceTx")
	}
	return sequence.NextSequence(name)
}

// TestEventSequenceFirstConcurrentWriters pins the original defect: two first-ever event
// writers of one sequence must never receive the same id.
func TestEventSequenceFirstConcurrentWriters(t *testing.T) {
	db, _ := openIsolatedSchema(t)
	ctx := context.Background()
	const writers = 16
	ids := make([]uint64, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = db.Update(ctx, func(tx store.Tx) error {
				var err error
				ids[i], err = transactionSequence(tx, "pool_event_seq")
				return err
			})
		}(i)
	}
	wg.Wait()
	seen := map[uint64]int{}
	for i, id := range ids {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		seen[id]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("sequence value %d was handed out %d times", id, n)
		}
	}
}

// TestEventSequenceRejectsAnEmptyName pins that a missing name is an error, never an
// accidental query of another relation.
func TestEventSequenceRejectsAnEmptyName(t *testing.T) {
	db, _ := openIsolatedSchema(t)
	err := db.Update(context.Background(), func(tx store.Tx) error {
		_, err := transactionSequence(tx, "")
		return err
	})
	if err == nil {
		t.Fatal("an empty sequence name was accepted")
	}
}

// TestEventSequenceIsSeededPastTheStoredEvents is the migration test of A2: a database
// that already holds events of the old row counter must not hand out ids that sort before
// them, or the cursor replay would skip every new event. The final gate smoke caught
// exactly that shape: two servers of one database, the second one replaying nothing.
//
// R18B review row 2: the seed is a one-time migration now. The second half pins the
// is_called gate: once the sequence serves values, a later Open must never rewind it
// (a stale last_value read would otherwise hand out an id a concurrent writer already
// consumed).
func TestEventSequenceIsSeededPastTheStoredEvents(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		t.Fatal("POOLS_PG_DSN is not set and this test is part of the A2 gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse configuration: %v", err)
	}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = admin.Close(c)
	})
	schema := fmt.Sprintf("r17_seed_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = admin.Exec(c, "DROP SCHEMA "+schema+" CASCADE")
	})
	// The pre-upgrade state: the old counter left events with ids up to 4242 and the
	// sequence of the new store is still fresh, exactly the state of an upgraded server
	// on its first start with the new code.
	const storedMax = 4242
	for _, statement := range []string{
		`SET search_path TO ` + schema,
		`CREATE TABLE pool_records (
			bucket text NOT NULL,
			id text NOT NULL,
			body jsonb NOT NULL,
			raw_body bytea,
			job_id text,
			task_id text,
			PRIMARY KEY (bucket, id))`,
		`CREATE SEQUENCE pool_event_seq START WITH 1 INCREMENT BY 1 NO MAXVALUE CACHE 1`,
		`INSERT INTO pool_records (bucket, id, body) VALUES
			('events', '00000000000000004100', '{}'),
			('events', '` + fmt.Sprintf("%020d", storedMax) + `', '{}')`,
	} {
		if _, err = admin.Exec(ctx, statement); err != nil {
			t.Fatalf("prepare the pre-upgrade state: %v", err)
		}
	}
	scoped, err := scopedDSN(dsn, schema)
	if err != nil {
		t.Fatalf("scope the DSN: %v", err)
	}
	db, err := Open(scoped)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	next, err := sequenceValue(db)
	if err != nil {
		t.Fatalf("read the sequence: %v", err)
	}
	if next <= storedMax {
		t.Fatalf("the sequence hands out %d although the database holds events up to %d: a replay would skip every new event", next, storedMax)
	}
	_ = db.Close()
	// The seed-once gate: rewind the sequence by hand (simulating an id already handed
	// out to another writer) and reopen. The reopen must NOT re-seed: the sequence keeps
	// serving 6, it must not jump back to the stored maximum.
	if _, err = admin.Exec(ctx, `SELECT setval('`+schema+`.pool_event_seq', 5, true)`); err != nil {
		t.Fatalf("rewind the sequence: %v", err)
	}
	db, err = Open(scoped)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	next, err = sequenceValue(db)
	if err != nil {
		t.Fatalf("read the sequence after the reopen: %v", err)
	}
	if next != 6 {
		t.Fatalf("the reopen re-seeded the sequence: next value %d, want 6 (the seed must run only while the sequence is fresh)", next)
	}
}

// sequenceValue reads the next value of the event sequence; the transaction commits and
// consumes it, which is exactly what the seed has to survive.
func sequenceValue(db store.Store) (uint64, error) {
	var next uint64
	err := db.Update(context.Background(), func(tx store.Tx) error {
		sequence, ok := tx.(store.SequenceTx)
		if !ok {
			return fmt.Errorf("the store does not implement store.SequenceTx")
		}
		var err error
		next, err = sequence.NextSequence("pool_event_seq")
		return err
	})
	return next, err
}
