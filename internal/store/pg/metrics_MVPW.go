// Path: internal/store/pg/metrics_MVPW.go
// What/why: Slice B PostgreSQL schema hook and queries for pool_metrics.
package pg

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"fmt"
	"strings"
)

const poolMetricsTableSchema = `
CREATE TABLE IF NOT EXISTS pool_metrics (
    id     bigserial        PRIMARY KEY,
    ts     timestamptz      NOT NULL DEFAULT now(),
    scope  text             NOT NULL,
    job_id text             NOT NULL DEFAULT '',
    name   text             NOT NULL,
    value  double precision NOT NULL,
    unit   text             NOT NULL,
    source text             NOT NULL,
    CONSTRAINT pool_metrics_scope_ck  CHECK (btrim(scope) <> ''),
    CONSTRAINT pool_metrics_name_ck   CHECK (btrim(name) <> ''),
    CONSTRAINT pool_metrics_unit_ck   CHECK (btrim(unit) <> ''),
    CONSTRAINT pool_metrics_source_ck CHECK (btrim(source) <> '')
);`

const poolMetricsTSIndexSchema = `
CREATE INDEX IF NOT EXISTS pool_metrics_ts_ix ON pool_metrics (ts)`

const poolMetricsScopeIndexSchema = `
CREATE INDEX IF NOT EXISTS pool_metrics_scope_job_name_ix ON pool_metrics (scope, job_id, name, ts)`

func (t transaction) putMetricSample(value any) error {
	sample, err := store.DecodeMetricSample(value)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(
		t.ctx,
		`INSERT INTO pool_metrics (ts, scope, job_id, name, value, unit, source) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sample.TS.UTC(),
		sample.Scope,
		sample.JobID,
		sample.Name,
		sample.Value,
		sample.Unit,
		sample.Source,
	)
	return err
}

// QueryMetricSamples queries pool_metrics within an active transaction.
func (t transaction) QueryMetricSamples(filter store.MetricFilter) ([]store.MetricSample, error) {
	var clauses []string
	var args []any
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if candidates := store.ScopeCandidates(filter.Scope); len(candidates) > 0 {
		clauses = append(clauses, "LOWER(scope) = ANY("+addArg(candidates)+")")
	}
	if strings.TrimSpace(filter.JobID) != "" {
		clauses = append(clauses, "job_id = "+addArg(strings.TrimSpace(filter.JobID)))
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, "ts >= "+addArg(filter.Since.UTC()))
	}
	query := `SELECT ts, scope, job_id, name, value, unit, source FROM pool_metrics`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY ts ASC, id ASC"

	rows, err := t.tx.Query(t.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]store.MetricSample, 0)
	for rows.Next() {
		var s store.MetricSample
		if err := rows.Scan(&s.TS, &s.Scope, &s.JobID, &s.Name, &s.Value, &s.Unit, &s.Source); err != nil {
			return nil, err
		}
		s.TS = s.TS.UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// QueryMetricSamples returns matching pool_metrics rows in chronological order.
func (d *DB) QueryMetricSamples(ctx context.Context, filter store.MetricFilter) ([]store.MetricSample, error) {
	return store.QueryMetricSamples(ctx, d, filter)
}
