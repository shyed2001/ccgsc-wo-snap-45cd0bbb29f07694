// Path: internal/pool/claim_clobber_R17_test.go | Author: CommandCode for the owner | 2026-11-23
// What/why: R18B review row 3 - the settlement fence of the lock-free List read. Claim
// and Sweep read every job row with List, which takes no row lock (a FOR UPDATE over the
// bucket serialized every claim behind one table scan), and then blind-write the whole
// Job. A submit that settles a lease between the List and the write is clobbered: the
// stale snapshot resurrects the lease and the settled result disappears, so the same
// lease can settle twice. The fix re-reads the job row with the write lock (FOR UPDATE)
// before the first mutation and skips the job when it changed concurrently.
//
// The test forces the exact interleaving with PostgreSQL row locks:
//   - a helper transaction holds the job row lock,
//   - the submit parks first on the same row lock (its settlement Get is queued first),
//   - the racing claim reads the stale List snapshot and parks second on its re-check,
//   - the helper releases; the submit settles and commits, then the claim wakes, sees the
//     change and must NOT write its stale snapshot.
package pool

import (
	"ccgsc/poolscore/internal/store/pg"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// clobberSchema creates one isolated schema for the test and returns its name and the
// store DSN scoped to it.
func clobberSchema(t *testing.T) (string, string, string) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		for _, name := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI"} {
			if v := strings.TrimSpace(os.Getenv(name)); v != "" && v != "false" && v != "0" {
				t.Fatal("POOLS_PG_DSN is not set and CI is set")
			}
		}
		t.Skip("POOLS_PG_DSN is not set; skipping PostgreSQL claim-clobber test")
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
	schema := fmt.Sprintf("r17_clobber_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = admin.Exec(c, "DROP SCHEMA "+schema+" CASCADE")
	})
	scoped := dsn
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse DSN: %v", err)
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		q.Set("connect_timeout", "10")
		u.RawQuery = q.Encode()
		scoped = u.String()
	} else {
		scoped += " search_path=" + schema + ",public connect_timeout=10"
	}
	return schema, scoped, dsn
}

// clobberEngine opens an engine on the scoped DSN of the shared test schema.
func clobberEngine(t *testing.T, scoped, id string) *Engine {
	t.Helper()
	db, err := pg.Open(scoped)
	if err != nil {
		t.Fatalf("pg.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e, err := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "r18b-"+id, time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// waitForLockWaiters polls pg_stat_activity until at least n sessions wait for a lock, so
// the test knows each goroutine is parked at its lock point.
func waitForLockWaiters(t *testing.T, conn *pgx.Conn, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := conn.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'`).Scan(&count); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if count >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	var detail []string
	rows, err := conn.Query(context.Background(),
		`SELECT pid, wait_event_type, wait_event, state FROM pg_stat_activity ORDER BY pid`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pid int
			var wt, we, state string
			if err := rows.Scan(&pid, &wt, &we, &state); err == nil {
				detail = append(detail, fmt.Sprintf("pid=%d wait=%s/%s state=%s", pid, wt, we, state))
			}
		}
	}
	t.Fatalf("waited for %d lock waiters, only saw: %v", n, detail)
}

// TestR17ClaimCannotClobberAConcurrentSubmit is the 2-goroutine proof of review row 3.
// A claim that read a job through the lock-free List while a submit settles a lease of
// the same job must skip the job instead of writing its stale snapshot back.
func TestR17ClaimCannotClobberAConcurrentSubmit(t *testing.T) {
	schema, scoped, dsn := clobberSchema(t)
	claimEngine := clobberEngine(t, scoped, "claim")
	submitEngine := clobberEngine(t, scoped, "submit")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Both engines must judge every verdict the same way: no re-run sampling, no
	// canary injection.
	for _, e := range []*Engine{claimEngine, submitEngine} {
		cfg := e.TrustConfig()
		cfg.RerunEnabled = false
		cfg.CanaryRate = 0
		e.SetTrustConfig(cfg)
	}

	// One job with two tasks: task-a is leased to worker-1 (and settled by the
	// concurrent submit), task-b stays queued for the racing claim.
	data := []byte("r18b clobber module")
	artifact := verify.SHA256(data)
	if _, err := claimEngine.Upload(ctx, artifact, "", 0, int64(len(data)), data); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	job, err := claimEngine.Create(ctx, Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{artifact},
		Tasks: []TaskSpec{
			{TaskID: "task-a", Input: json.RawMessage(`{"json":{"n":1}}`)},
			{TaskID: "task-b", Input: json.RawMessage(`{"json":{"n":2}}`)},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	w1, err := claimEngine.Register(ctx, Thread, "contributor", Worker{HostID: "host-1", Slots: 2})
	if err != nil {
		t.Fatalf("Register(worker-1): %v", err)
	}
	a, err := claimEngine.Claim(ctx, Thread, w1.Owner, w1.TabID)
	if err != nil {
		t.Fatalf("Claim(task-a): %v", err)
	}
	if a.Lease.TaskID != "task-a" {
		t.Fatalf("the first claim leased %q, want task-a", a.Lease.TaskID)
	}
	w2, err := claimEngine.Register(ctx, Thread, "contributor-2", Worker{HostID: "host-2", Slots: 2})
	if err != nil {
		t.Fatalf("Register(worker-2): %v", err)
	}

	// The helper transaction holds the job row lock: the submit and then the claim park
	// on it in queue order, which is the exact interleaving the fix has to survive.
	hcfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	helper, err := pgx.ConnectConfig(ctx, hcfg)
	if err != nil {
		t.Fatalf("connect helper: %v", err)
	}
	t.Cleanup(func() { _ = helper.Close(context.Background()) })
	if _, err = helper.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatalf("scope helper: %v", err)
	}
	htx, err := helper.Begin(ctx)
	if err != nil {
		t.Fatalf("begin helper: %v", err)
	}
	t.Cleanup(func() { _ = htx.Rollback(context.Background()) })
	if _, err = htx.Exec(ctx, `SELECT id FROM pool_records WHERE bucket = 'jobs' AND id = $1 FOR UPDATE`, job.JobID); err != nil {
		t.Fatalf("helper lock: %v", err)
	}

	// Goroutine 2: the submit that settles task-a. Its settlement Get is queued FIRST
	// on the job row lock.
	out := json.RawMessage(`{"answer":42}`)
	q := Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}
	type submitResult struct {
		res Result
		err error
	}
	submitted := make(chan submitResult, 1)
	go func() {
		res, err := submitEngine.Submit(ctx, Thread, w1.Owner, w1.TabID, q)
		submitted <- submitResult{res, err}
	}()
	waitForLockWaiters(t, helper, 1)

	// Goroutine 1: the racing claim reads the stale List snapshot (task-a leased,
	// task-b queued) and parks on its re-check of the same row.
	type claimResult struct {
		a   Assignment
		err error
	}
	claimed := make(chan claimResult, 1)
	go func() {
		ra, err := claimEngine.Claim(ctx, Thread, w2.Owner, w2.TabID)
		claimed <- claimResult{ra, err}
	}()
	waitForLockWaiters(t, helper, 2)

	// Release the queue: the submit wins first, settles and commits; the claim wakes
	// second and must skip the job its stale snapshot no longer describes.
	if err = htx.Commit(ctx); err != nil {
		t.Fatalf("commit helper: %v", err)
	}

	sub := <-submitted
	if sub.err != nil {
		t.Fatalf("the concurrent submit was refused: %v", sub.err)
	}
	if sub.res.Status != "done" {
		t.Fatalf("the concurrent submit settled %q, want done", sub.res.Status)
	}
	cl := <-claimed
	if !errors.Is(cl.err, ErrEmpty) {
		t.Fatalf("the racing claim returned %v, want ErrEmpty (the changed job must be skipped, never written)", cl.err)
	}
	if cl.a.Lease.TaskID != "" {
		t.Fatalf("the racing claim leased %q from a stale snapshot", cl.a.Lease.TaskID)
	}

	// The settlement survived the claim: task-a is done with exactly one result, and
	// task-b was never leased by the stale snapshot.
	got, err := claimEngine.Get(ctx, Thread, "developer", job.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Results) != 1 || got.Results[0].TaskID != "task-a" {
		t.Fatalf("results = %+v, want exactly the settled task-a (the stale write clobbered the settlement)", got.Results)
	}
	for _, task := range got.Tasks {
		switch task.TaskID {
		case "task-a":
			if task.State != "done" || task.Lease != nil {
				t.Fatalf("task-a = %q lease %+v, want done without a lease (the settled lease was resurrected)", task.State, task.Lease)
			}
		case "task-b":
			if task.State != "queued" || task.Lease != nil {
				t.Fatalf("task-b = %q lease %+v, want queued without a lease (a stale snapshot leased it)", task.State, task.Lease)
			}
		}
	}
}
