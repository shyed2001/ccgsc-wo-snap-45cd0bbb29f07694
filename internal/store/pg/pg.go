// Path: internal/store/pg/pg.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: explicit pgx seam, no network or silent debug fallback; WWPP-R18.2.
// T6.2 (hermes MiniMax-M3, 2026-11-17): the settings store of db/pool_config.sql lives in
// config.go next to this file; OpenPool connects to a PostgreSQL that already carries the
// schema of db/pool_config.sql. The store never creates or migrates tables on its own.
package pg

import (
	"ccgsc/poolscore/internal/redact"
	"ccgsc/poolscore/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

var _ store.Store = (*DB)(nil)

const recordsTableSchema = `
CREATE TABLE IF NOT EXISTS pool_records (
    bucket text NOT NULL,
    id text NOT NULL,
    body jsonb NOT NULL,
    raw_body bytea,
    job_id text,
    task_id text,
    PRIMARY KEY (bucket, id)
);`

const recordsRawBodyMigration = `
ALTER TABLE pool_records ADD COLUMN IF NOT EXISTS raw_body bytea
`

const recordsResultIndex = `
CREATE UNIQUE INDEX IF NOT EXISTS pool_records_results_job_task_uq
    ON pool_records (job_id, task_id)
    WHERE bucket = 'results' AND job_id IS NOT NULL AND task_id IS NOT NULL
`

// Open connects to PostgreSQL and prepares the generic durable store used by the pool engine.
func Open(dsn string) (store.Store, error) {
	return OpenWithMaxConns(dsn, 0)
}

// OpenWithMaxConns connects to PostgreSQL with a bounded connection pool.
// R15 AT22-GM2-3 (TD-17): the owner rule is "never all threads", so the pool of the
// database may not grow without a cap. maxConns of zero keeps the pgx default.
func OpenWithMaxConns(dsn string, maxConns int) (store.Store, error) {
	pool, err := OpenPoolWithMaxConns(context.Background(), dsn, maxConns)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("begin pool store schema: %w", err)
	}
	for _, statement := range []string{recordsTableSchema, recordsRawBodyMigration, recordsResultIndex, poolMetricsTableSchema, poolMetricsTSIndexSchema, poolMetricsScopeIndexSchema, sequenceSchema, sequenceSeedLock, sequenceSeed} {
		if _, err = tx.Exec(ctx, statement); err != nil {
			_ = tx.Rollback(ctx)
			pool.Close()
			return nil, fmt.Errorf("create pool store schema: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		_ = tx.Rollback(ctx)
		pool.Close()
		return nil, fmt.Errorf("commit pool store schema: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// OpenPool connects to a PostgreSQL server and returns the pool. The database must already
// carry the schema of db/pool_config.sql; this function creates nothing.
//
// T101 (cmdc leak sweep, 2026-11-20): pgx parse errors can echo the whole
// connection string, so every error leaving this function is wrapped through
// internal/redact - the password never survives in the message, while host,
// user and database stay visible for diagnosis.
func OpenPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return OpenPoolWithMaxConns(ctx, dsn, 0)
}

// OpenPoolWithMaxConns connects to a PostgreSQL server and returns the pool. The
// database must already carry the schema of db/pool_config.sql; this function creates
// nothing. maxConns of zero keeps the pgx default, a positive value caps the pool.
//
// T101 (cmdc leak sweep, 2026-11-20): pgx parse errors can echo the whole
// connection string, so every error leaving this function is wrapped through
// internal/redact - the password never survives in the message, while host,
// user and database stay visible for diagnosis.
func OpenPoolWithMaxConns(ctx context.Context, dsn string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := ParsePoolConfig(dsn, maxConns)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, redact.RedactDSNError(fmt.Errorf("open PostgreSQL: %w", err), dsn)
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, redact.RedactDSNError(fmt.Errorf("ping PostgreSQL: %w", err), dsn)
	}
	return pool, nil
}

// ParsePoolConfig builds the pgx pool configuration of a DSN and applies the
// connection cap of the owner (R15 AT22-GM2-3, TD-17). A cap of zero keeps the pgx
// default. The DSN never survives in an error message (internal/redact).
func ParsePoolConfig(dsn string, maxConns int) (*pgxpool.Config, error) {
	if dsn == "" {
		return nil, fmt.Errorf("the PostgreSQL DSN is empty")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, redact.RedactDSNError(fmt.Errorf("open PostgreSQL: %w", err), dsn)
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	return cfg, nil
}

// OpenConfigStore connects to a PostgreSQL server and returns the settings store for the
// pool_config_version API. The logger is the one of internal/logx, so every call writes one
// debug line (owner A116 debug mode).
func OpenConfigStore(ctx context.Context, dsn string, logger *slog.Logger) (*ConfigStore, *pgxpool.Pool, error) {
	pool, err := OpenPool(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	return NewConfigStore(pool, logger), pool, nil
}

func (d *DB) Close() error {
	if d == nil || d.Pool == nil {
		return nil
	}
	d.Pool.Close()
	return nil
}

type transaction struct {
	tx   pgx.Tx
	ctx  context.Context
	lock bool
}

func (t transaction) Get(bucket, key string, out any) error {
	if bucket == store.ConfigVersions && strings.HasPrefix(key, "active:") {
		return t.activePolicyConfig(strings.TrimPrefix(key, "active:"), out)
	}
	if bucket == store.Idempotency && t.lock {
		if _, err := t.tx.Exec(t.ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			bucket+":"+key); err != nil {
			return err
		}
	}
	var body []byte
	if err := t.tx.QueryRow(t.ctx, getQuery(t.lock), bucket, key).Scan(&body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	}
	return json.Unmarshal(body, out)
}

// getQuery is the statement of one record read. A write transaction locks the single
// row it reads, so the lease row the engine settles is fenced; a read-only view does not.
func getQuery(lock bool) string {
	query := `SELECT COALESCE(raw_body, convert_to(body::text, 'UTF8')) FROM pool_records WHERE bucket = $1 AND id = $2`
	if lock {
		query += ` FOR UPDATE`
	}
	return query
}

func (t transaction) List(bucket string) ([]store.Record, error) {
	rows, err := t.tx.Query(t.ctx, listQuery(), bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []store.Record
	for rows.Next() {
		var record store.Record
		var body []byte
		if err = rows.Scan(&record.Key, &body); err != nil {
			return nil, err
		}
		record.Value = json.RawMessage(body)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (t transaction) Put(bucket, key string, value any) error {
	if bucket == store.ConfigVersions && strings.HasPrefix(key, "job:") {
		return t.pinPolicyConfig(strings.TrimPrefix(key, "job:"), value)
	}
	if bucket == store.Metrics && strings.HasPrefix(key, store.MetricSampleKeyPrefix) {
		return t.putMetricSample(value)
	}
	var body []byte
	var err error
	if raw, ok := value.(store.StoredJSONMarshaler); ok {
		body, err = raw.MarshalStoredJSON()
	} else {
		body, err = json.Marshal(value)
	}
	if err != nil {
		return err
	}
	var jobID, taskID *string
	if bucket == store.Results {
		var fields struct {
			JobID  string `json:"jobId"`
			TaskID string `json:"taskId"`
		}
		if err = json.Unmarshal(body, &fields); err != nil {
			return err
		}
		if strings.TrimSpace(fields.JobID) != "" && strings.TrimSpace(fields.TaskID) != "" {
			jobID, taskID = &fields.JobID, &fields.TaskID
		}
	}
	if bucket == store.Idempotency {
		_, err = t.tx.Exec(t.ctx, `
			INSERT INTO pool_records (bucket, id, body, raw_body, job_id, task_id)
			VALUES ($1, $2, $3::text::jsonb, $6::bytea, $4, $5)
			ON CONFLICT (bucket, id) DO NOTHING`,
			bucket, key, string(body), jobID, taskID, body)
		return err
	}

	_, err = t.tx.Exec(t.ctx, `
		INSERT INTO pool_records (bucket, id, body, raw_body, job_id, task_id)
		VALUES ($1, $2, $3::text::jsonb, $6::bytea, $4, $5)
		ON CONFLICT (bucket, id) DO UPDATE
		SET body = EXCLUDED.body, raw_body = EXCLUDED.raw_body, job_id = EXCLUDED.job_id, task_id = EXCLUDED.task_id`,
		bucket, key, string(body), jobID, taskID, body)
	return err
}

func (t transaction) PutIfAbsent(bucket, key string, value any) (bool, error) {
	var body []byte
	var err error
	if raw, ok := value.(store.StoredJSONMarshaler); ok {
		body, err = raw.MarshalStoredJSON()
	} else {
		body, err = json.Marshal(value)
	}
	if err != nil {
		return false, err
	}
	command, err := t.tx.Exec(t.ctx, `
		INSERT INTO pool_records (bucket, id, body, raw_body)
		VALUES ($1, $2, $3::text::jsonb, $4::bytea)
		ON CONFLICT (bucket, id) DO NOTHING`,
		bucket, key, string(body), body)
	return command.RowsAffected() == 1, err
}

// Delete removes one record; it satisfies the optional store.DeleteTx contract.
func (t transaction) Delete(bucket, key string) error {
	_, err := t.tx.Exec(t.ctx, `DELETE FROM pool_records WHERE bucket = $1 AND id = $2`, bucket, key)
	return err
}

// NextSequence hands out the next value of a named SEQUENCE and satisfies the optional
// store.SequenceTx contract. R17-A2 (WO R17-SCALE item 3): the durable event ids come
// from a SEQUENCE instead of a read-increment-write of one "event_sequence" row, so two
// coordinators never serialize on a row and a claim/submit transaction never waits for
// the event counter. A sequence value is consumed by nextval even when the transaction
// rolls back: the ids may carry gaps, and every consumer orders by the id and tolerates
// them (the SSE route and the gRPC stream compare cursors, never contiguity).
func (t transaction) NextSequence(name string) (uint64, error) {
	if name == "" {
		return 0, fmt.Errorf("sequence name is empty")
	}
	var next int64
	if err := t.tx.QueryRow(t.ctx, `SELECT nextval($1::regclass)`, identifierForSequence(name)).Scan(&next); err != nil {
		return 0, err
	}
	if next < 1 {
		return 0, fmt.Errorf("sequence %q returned %d", name, next)
	}
	return uint64(next), nil
}

// identifierForSequence quotes a sequence name for the regclass lookup, so a name can
// only ever address a sequence and never another relation of the schema.
func identifierForSequence(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// sequenceSchema creates the durable event sequence. It is idempotent, like every other
// statement of the store schema.
const sequenceSchema = `
CREATE SEQUENCE IF NOT EXISTS pool_event_seq START WITH 1 INCREMENT BY 1 NO MAXVALUE CACHE 1;`

// sequenceSeedLock serializes the seed across concurrent opens of the same
// database. The seed is a one-time migration (the is_called gate below), so a
// second Open that already seeded can never rewind a value another process
// consumed; the lock closes the last window of two simultaneous first opens.
const sequenceSeedLock = `SELECT pg_advisory_xact_lock(hashtextextended('pool_event_seq_seed', 0))`

// sequenceSeed advances the event sequence past every event that is already stored.
//
// WO R17-SCALE item 3: an existing database already holds event ids of the old
// read-increment-write counter. A fresh SEQUENCE starts at 1 again, so new events would
// sort BEFORE the stored ones and the SSE/gRPC replay (which walks the ids in order and
// advances a cursor) would skip every new event. The seed only ever moves the sequence
// forward: the GREATEST keeps the current value, and the is_called gate makes the seed
// run only while the sequence is still fresh, so a later Open never rewinds a sequence
// that is already serving values (a concurrent nextval landing between the last_value
// read and the setval would otherwise be handed out twice).
const sequenceSeed = `
SELECT setval(
  'pool_event_seq',
  GREATEST(
    (SELECT COALESCE(MAX(id::bigint), 0) FROM pool_records WHERE bucket = 'events' AND id ~ '^[0-9]+$'),
    (SELECT last_value FROM pool_event_seq)
  ),
  true
)
FROM pool_event_seq
WHERE is_called = false;`

// listQuery is the statement of a bucket read. R17-A2 (WO R17-SCALE item 3): a List never
// takes a row lock. The engine lists every job row on each Claim (the expiry walk) and on
// each Sweep, so a `FOR UPDATE` over the whole bucket serialized every claim of the pool
// behind one table scan. The rows a caller rewrites are version-guarded by the caller (the
// lease token and the task generation) and the Redis lease fencing keeps a second writer
// out; the single-row Get of the settlement below still locks.
func listQuery() string {
	return `SELECT id, COALESCE(raw_body, convert_to(body::text, 'UTF8')) FROM pool_records WHERE bucket = $1 ORDER BY id`
}

func (d *DB) View(ctx context.Context, fn func(store.Reader) error) error {
	return d.withTx(ctx, true, fn)
}

func (d *DB) Update(ctx context.Context, fn func(store.Tx) error) error {
	return d.withTx(ctx, false, fn)
}

func (d *DB) withTx(ctx context.Context, readOnly bool, fn any) error {
	tx, err := d.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: func() pgx.TxAccessMode {
		if readOnly {
			return pgx.ReadOnly
		}
		return pgx.ReadWrite
	}()})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if readOnly {
		err = fn.(func(store.Reader) error)(transaction{tx: tx, ctx: ctx, lock: false})
	} else {
		err = fn.(func(store.Tx) error)(transaction{tx: tx, ctx: ctx, lock: true})
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
