// Path: internal/store/pg/no_full_table_lock_A2_R17_test.go | Author: CommandCode for the owner | 2026-11-22
// What/why: WO R17-SCALE item 3 (A2) - a Claim must not take FOR UPDATE on every job row.
// The engine lists every job row on each claim and the store appended `FOR UPDATE` to
// every List of a write transaction, so one claim locked the whole jobs bucket and every
// other claim of the pool waited on it. The test pins the statements the store really
// sends: a bucket List carries no row lock, while the single row a settlement reads is
// still locked.
package pg

import (
	"ccgsc/poolscore/internal/store"
	"strings"
	"testing"
)

// TestListQueryTakesNoRowLock pins that the statement of a List has no `FOR UPDATE`, so a
// Claim never locks every job row of the pool.
func TestListQueryTakesNoRowLock(t *testing.T) {
	query := listQuery()
	if !strings.Contains(query, "FROM pool_records") || !strings.Contains(query, "ORDER BY id") {
		t.Fatalf("listQuery = %q, want the bucket read", query)
	}
	if strings.Contains(strings.ToUpper(query), "FOR UPDATE") {
		t.Fatalf("listQuery still takes a row lock over the whole bucket: %s", query)
	}
}

// TestGetQueryLocksTheSettlementRow pins that the single row the engine settles is still
// locked, so the lease fencing of the settlement read is unchanged.
func TestGetQueryLocksTheSettlementRow(t *testing.T) {
	locked := getQuery(true)
	if !strings.Contains(locked, "bucket = $1 AND id = $2") {
		t.Fatalf("getQuery(true) = %q, want one record", locked)
	}
	if !strings.Contains(strings.ToUpper(locked), "FOR UPDATE") {
		t.Fatalf("getQuery(true) lost the row lock: %s", locked)
	}
	readOnly := getQuery(false)
	if strings.Contains(strings.ToUpper(readOnly), "FOR UPDATE") {
		t.Fatalf("getQuery(false) takes a row lock on a read-only view: %s", readOnly)
	}
}

// TestTheSequenceAndTheEventsLiveInTheSameSchema pins that the A2 sequence is part of the
// store schema, so no migration is needed outside pg.Open.
func TestTheSequenceAndTheEventsLiveInTheSameSchema(t *testing.T) {
	if !strings.Contains(sequenceSchema, "CREATE SEQUENCE IF NOT EXISTS pool_event_seq") {
		t.Fatalf("sequenceSchema = %q, want the event sequence", sequenceSchema)
	}
	if !strings.Contains(recordsTableSchema, "CREATE TABLE IF NOT EXISTS pool_records") {
		t.Fatalf("recordsTableSchema = %q, want the record table", recordsTableSchema)
	}
	_ = store.Events
}
