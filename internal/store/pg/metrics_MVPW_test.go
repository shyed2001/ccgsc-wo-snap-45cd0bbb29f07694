// Path: internal/store/pg/metrics_MVPW_test.go
// What/why: Slice B PostgreSQL pool_metrics migration and round-trip store test.
package pg

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPoolMetricsPGMigrationAndRoundTrip(t *testing.T) {
	rawSQL, err := os.ReadFile("../../../db/pool_metrics_MVPW.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(rawSQL)
	for _, needle := range []string{
		"CREATE TABLE IF NOT EXISTS pool_metrics",
		"ts ",
		"scope ",
		"job_id ",
		"name ",
		"value ",
		"unit ",
		"source ",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("db/pool_metrics_MVPW.sql missing %q", needle)
		}
	}

	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		if isCI() {
			t.Fatal("POOLS_PG_DSN is not set and CI is set")
		}
		t.Skip("POOLS_PG_DSN is not set; skipping PostgreSQL metrics integration test")
	}

	rawStore, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawStore.Close() })
	pgDB := rawStore.(*DB)

	ctx := context.Background()
	jobID := fmt.Sprintf("pg-metrics-%d", time.Now().UnixNano())
	ts := time.Now().UTC().Truncate(time.Microsecond)
	if err := pgDB.Update(ctx, func(tx store.Tx) error {
		return store.RecordMetricSample(tx, store.MetricSample{
			TS:     ts,
			Scope:  "tp",
			JobID:  jobID,
			Name:   "lease_issued",
			Value:  2,
			Unit:   "count",
			Source: "pool",
		})
	}); err != nil {
		t.Fatal(err)
	}

	samples, err := pgDB.QueryMetricSamples(ctx, store.MetricFilter{
		Scope: "thread-pool",
		JobID: jobID,
		Since: ts.Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].Name != "lease_issued" || samples[0].Value != 2 {
		t.Fatalf("PG queried samples = %+v", samples)
	}
}
