// Path: internal/store/sqlite/metrics_MVPW.go
// What/why: Slice B SQLite persistence and queries for pool_metrics.
package sqlite

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"strings"
	"time"
)

func (d *DB) applyMetricsMigration() error {
	b, err := migrations.ReadFile("migrations/002_pool_metrics_MVPW.sql")
	if err != nil {
		return err
	}
	_, err = d.db.Exec(string(b))
	return err
}

func (t transaction) putMetricSample(v any) error {
	sample, err := store.DecodeMetricSample(v)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(
		t.ctx,
		`INSERT INTO pool_metrics(ts, scope, job_id, name, value, unit, source) VALUES(?,?,?,?,?,?,?)`,
		sample.TS.UTC().Format(store.MetricTimeLayout),
		sample.Scope,
		sample.JobID,
		sample.Name,
		sample.Value,
		sample.Unit,
		sample.Source,
	)
	return err
}

// QueryMetricSamples queries pool_metrics within an active read/write transaction.
func (t transaction) QueryMetricSamples(filter store.MetricFilter) ([]store.MetricSample, error) {
	var clauses []string
	var args []any
	if candidates := store.ScopeCandidates(filter.Scope); len(candidates) > 0 {
		placeholders := make([]string, len(candidates))
		for i, c := range candidates {
			placeholders[i] = "?"
			args = append(args, c)
		}
		clauses = append(clauses, "LOWER(scope) IN ("+strings.Join(placeholders, ",")+")")
	}
	if strings.TrimSpace(filter.JobID) != "" {
		clauses = append(clauses, "job_id = ?")
		args = append(args, strings.TrimSpace(filter.JobID))
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, "ts >= ?")
		args = append(args, filter.Since.UTC().Format(store.MetricTimeLayout))
	}
	query := "SELECT ts, scope, job_id, name, value, unit, source FROM pool_metrics"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY ts ASC, id ASC"

	rows, err := t.tx.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]store.MetricSample, 0)
	for rows.Next() {
		var s store.MetricSample
		var rawTS string
		if err := rows.Scan(&rawTS, &s.Scope, &s.JobID, &s.Name, &s.Value, &s.Unit, &s.Source); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, rawTS)
		if err == nil {
			s.TS = parsed.UTC()
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// QueryMetricSamples returns matching pool_metrics rows in chronological order.
func (d *DB) QueryMetricSamples(ctx context.Context, filter store.MetricFilter) ([]store.MetricSample, error) {
	return store.QueryMetricSamples(ctx, d, filter)
}
