// Path: internal/pool/sse_pg_R17_test.go | Author: CommandCode for the owner | 2026-11-22
// What/why: the smoke of the final gate failed on the SSE stream (no task.done) when
// POOLS_PG_DSN was in the environment. The engine writes the event through the event bus
// wrapper; this test pins that a PostgreSQL store really holds the durable events and that
// their ids come from the sequence, so a stream can replay them.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store/pg"

	"github.com/jackc/pgx/v5"
)

func TestR17PGEngineWritesDurableEvents(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		t.Skip("POOLS_PG_DSN is not set; PostgreSQL integration coverage requires a configured test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse configuration: %v", err)
	}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = admin.Close(c)
	})
	schema := fmt.Sprintf("r17_sse_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
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
	db, err := pg.Open(scoped)
	if err != nil {
		t.Fatalf("pg.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e, err := New(db, quietLogger(), "r17", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := []byte("r17 sse module")
	artifact := verify.SHA256(data)
	if _, err := e.Upload(ctx, artifact, "", 0, int64(len(data)), data); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	j, err := e.Create(ctx, Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{artifact},
		Tasks:             []TaskSpec{{TaskID: "task-1", Input: json.RawMessage(`{"json":{"n":1}}`)}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var rows []store.Record
	if err := e.Store.View(ctx, func(r store.Reader) error {
		var er error
		rows, er = r.List(store.Events)
		return er
	}); err != nil {
		t.Fatalf("List events: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("the PostgreSQL store holds no event after a create: the SSE replay would be empty")
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].Key < rows[b].Key })
	var ev Event
	if err := json.Unmarshal(rows[0].Value, &ev); err != nil {
		t.Fatalf("event did not parse: %v", err)
	}
	if ev.Type != "job.queued" || ev.JobID != j.JobID {
		t.Fatalf("event = %+v, want job.queued for %s", ev, j.JobID)
	}
	if ev.ID != rows[0].Key {
		t.Fatalf("event id %q != stored key %q", ev.ID, rows[0].Key)
	}
}
