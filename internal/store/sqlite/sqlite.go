// Path: internal/store/sqlite/sqlite.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: pure-Go transactional debug persistence with embedded migration; WWPP-R41.1.
package sqlite

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

type DB struct{ db *sql.DB }

var _ store.Store = (*DB)(nil)

func Open(path string) (*DB, error) {
	if path != ":memory:" {
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return nil, e
		}
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	d := &DB{db: db}
	fail := func(e error) (*DB, error) { _ = db.Close(); return nil, e }
	if _, e = db.Exec("PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;"); e != nil {
		return fail(e)
	}
	b, e := migrations.ReadFile("migrations/001_records.sql")
	if e != nil {
		return fail(e)
	}
	if _, e = db.Exec(string(b)); e != nil {
		return fail(e)
	}
	if e = d.applyMetricsMigration(); e != nil {
		return fail(e)
	}
	return d, nil
}
func (d *DB) Close() error { return d.db.Close() }

type transaction struct {
	tx  *sql.Tx
	ctx context.Context
}

func (t transaction) Get(bucket, key string, out any) error {
	var b []byte
	e := t.tx.QueryRowContext(t.ctx, "SELECT body FROM records WHERE bucket=? AND id=?", bucket, key).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func (t transaction) List(bucket string) ([]store.Record, error) {
	rows, e := t.tx.QueryContext(t.ctx, "SELECT id,body FROM records WHERE bucket=? ORDER BY id", bucket)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []store.Record{}
	for rows.Next() {
		var r store.Record
		var body []byte
		if e = rows.Scan(&r.Key, &body); e != nil {
			return nil, e
		}
		r.Value = json.RawMessage(body)
		out = append(out, r)
	}
	return out, rows.Err()
}
func (t transaction) Put(bucket, key string, v any) error {
	if bucket == store.Metrics && strings.HasPrefix(key, store.MetricSampleKeyPrefix) {
		return t.putMetricSample(v)
	}
	var b []byte
	var e error
	if raw, ok := v.(store.StoredJSONMarshaler); ok {
		b, e = raw.MarshalStoredJSON()
	} else {
		b, e = json.Marshal(v)
	}
	if e != nil {
		return e
	}
	_, e = t.tx.ExecContext(t.ctx, "INSERT INTO records(bucket,id,body) VALUES(?,?,?) ON CONFLICT(bucket,id) DO UPDATE SET body=excluded.body", bucket, key, string(b))
	return e
}
func (t transaction) PutIfAbsent(bucket, key string, v any) (bool, error) {
	var b []byte
	var e error
	if raw, ok := v.(store.StoredJSONMarshaler); ok {
		b, e = raw.MarshalStoredJSON()
	} else {
		b, e = json.Marshal(v)
	}
	if e != nil {
		return false, e
	}
	result, e := t.tx.ExecContext(t.ctx, "INSERT OR IGNORE INTO records(bucket,id,body) VALUES(?,?,?)", bucket, key, string(b))
	if e != nil {
		return false, e
	}
	n, e := result.RowsAffected()
	return n == 1, e
}

// Delete removes one record; it satisfies the optional store.DeleteTx contract.
func (t transaction) Delete(bucket, key string) error {
	_, e := t.tx.ExecContext(t.ctx, "DELETE FROM records WHERE bucket=? AND id=?", bucket, key)
	return e
}
func (d *DB) View(ctx context.Context, fn func(store.Reader) error) error {
	tx, e := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(transaction{tx, ctx}); e != nil {
		return e
	}
	return tx.Commit()
}
func (d *DB) Update(ctx context.Context, fn func(store.Tx) error) error {
	tx, e := d.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(transaction{tx, ctx}); e != nil {
		return e
	}
	return tx.Commit()
}
