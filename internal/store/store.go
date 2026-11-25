// Path: internal/store/store.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: atomic storage boundary shared by SQLite and future pgx; WWPP-R18.2, R38.6, R40.2.
package store

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrNotFound = errors.New("record not found")
var ErrNotImplemented = errors.New("PostgreSQL adapter is a skeleton; implementation required")

// Buckets are stable module contracts. Jobs own their task/lease/result transaction;
// Tasks/Leases/Results are reserved for normalized production repositories.
const (
	Artifacts = "artifacts"
	// ArtifactOwners holds the per-owner reference rows of the shared
	// content-addressed artifact blobs (T100, owner decision 2026-11-21
	// option A): one record per (artifact id, owner subject), so a second
	// developer uploading identical bytes joins the same blob instead of
	// being refused, and the blob is deleted only when no reference remains.
	ArtifactOwners    = "artifact_owners"
	Jobs              = "jobs"
	Tasks             = "tasks"
	Leases            = "leases"
	Results           = "results"
	Sessions          = "sessions"
	ConfigVersions    = "config_versions"
	Audit             = "audit"
	Metrics           = "metrics"
	Users             = "users"
	Keys              = "keys"
	Workers           = "workers"
	Events            = "events"
	DeadLetters       = "dead_letters"
	WebhookDeliveries = "webhook_deliveries"
	Idempotency       = "idempotency"
	// TokenDenylist holds denied bearer-token ids (jti or key id) with an expiry.
	TokenDenylist = "token_denylist"
	// DewResults is the durable ledger of every receipted result submission
	// (F12, G-F12-4): one row per unique worker/lease output, kept for the
	// developer. The platform never compares these rows (G-F3).
	DewResults = "dew_results"
	// DewCredits holds the idempotent compute-time credit rows of the Dew
	// policy: at most one credit per unique worker/lease output (F12, G-F12-4).
	DewCredits = "dew_credits"
	// DewPolicy holds the admin Dew window per pool and the per-app developer
	// overrides (F12, G-F12-1).
	DewPolicy = "dew_policy"
)

type Record struct {
	Key   string
	Value json.RawMessage
}

// StoredJSONMarshaler allows records with byte-sensitive JSON fields to retain
// those bytes without changing their public API representation.
type StoredJSONMarshaler interface {
	MarshalStoredJSON() ([]byte, error)
}
type Reader interface {
	Get(bucket, key string, out any) error
	List(bucket string) ([]Record, error)
}
type Tx interface {
	Reader
	Put(bucket, key string, value any) error
}

// PutIfAbsentTx inserts a record only when the key is not already present.
// It is optional so Store wrappers can retain the basic Tx contract.
type PutIfAbsentTx interface {
	PutIfAbsent(bucket, key string, value any) (bool, error)
}

// DeleteTx removes a record. It is optional for the same reason as PutIfAbsentTx:
// a wrapper that does not forward it simply cannot prune. Callers must check the
// assertion and degrade, never assume every Tx supports removal.
type DeleteTx interface {
	Delete(bucket, key string) error
}

// SequenceTx hands out the next durable event sequence number. R17-A2 (WO R17-SCALE
// item 3): the engine used to read-increment-write one "event_sequence" row inside the
// settlement transaction, which serialized every event writer on that single row.
// PostgreSQL answers from a SEQUENCE instead, so the counter needs no row lock. Gaps
// are allowed and expected: a number is consumed even when the transaction that asked
// for it rolls back. Consumers must therefore order by the id and tolerate a gap, which
// the SSE and the gRPC replay already do: they walk the ids in order and compare
// cursors, never contiguity.
//
// It is optional, so a wrapper or the SQLite store can keep its own counter.
type SequenceTx interface {
	NextSequence(name string) (uint64, error)
}

// Update must commit all writes together or roll back on callback failure. View is a
// consistent snapshot. Callbacks must never re-enter Store (single connection in debug).
type Store interface {
	View(context.Context, func(Reader) error) error
	Update(context.Context, func(Tx) error) error
	Close() error
}

// Unsupported is embedded by the PostgreSQL stub: no accidental SQLite fallback.
type Unsupported struct{}

func (Unsupported) View(context.Context, func(Reader) error) error { return ErrNotImplemented }
func (Unsupported) Update(context.Context, func(Tx) error) error   { return ErrNotImplemented }
func (Unsupported) Close() error                                   { return nil }
