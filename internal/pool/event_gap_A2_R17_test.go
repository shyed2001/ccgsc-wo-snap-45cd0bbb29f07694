// Path: internal/pool/event_gap_A2_R17_test.go | Author: CommandCode for the owner | 2026-11-22
// What/why: WO R17-SCALE item 3 (A2) - the event replay must tolerate a gap. The durable
// ids now come from a PostgreSQL SEQUENCE, and a sequence value is consumed even when the
// transaction that asked for it rolls back, so the ids of an event stream may be sparse.
// The SSE route and the gRPC stream both walk List(store.Events) in id order and advance
// a cursor, which is the property this test pins on a real PostgreSQL store.
package pool

import (
	"ccgsc/poolscore/internal/store"
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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// pgEventGapEngine opens an engine on an isolated PostgreSQL schema.
func pgEventGapEngine(t *testing.T) *Engine {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		t.Skip("POOLS_PG_DSN is not set; PostgreSQL integration coverage requires a configured test database")
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
	schema := fmt.Sprintf("r17_gap_%d", time.Now().UnixNano())
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
	db, err := pg.Open(scoped)
	if err != nil {
		t.Fatalf("pg.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine, err := New(db, quietLogger(), "r17", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return engine
}

// quietLogger is the discard logger of the tests of this file.
func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// TestR17EventReplayToleratesAGap pins the A2 replay contract on PostgreSQL: a gap in
// the durable ids (a sequence value consumed by a rolled-back transaction) does not stop
// the replay, the order of the ids follows the order of the events, and a cursor always
// resumes strictly after its last id.
func TestR17EventReplayToleratesAGap(t *testing.T) {
	e := pgEventGapEngine(t)
	ctx := context.Background()
	data := []byte("r17 gap module")
	artifact := verify.SHA256(data)
	if _, err := e.Upload(ctx, artifact, "", 0, int64(len(data)), data); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{artifact}}
	for i := 0; i < 3; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: fmt.Sprintf("task-%d", i), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	job, err := e.Create(ctx, Thread, "developer", "", q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Consume two sequence values and roll them back: the next committed event id is
	// two past the previous one.
	rollback := errors.New("consume a sequence value")
	for i := 0; i < 2; i++ {
		if err := e.Store.Update(ctx, func(tx store.Tx) error {
			if _, err := nextEventSequence(tx, eventSequenceName); err != nil {
				return err
			}
			return rollback
		}); !errors.Is(err, rollback) {
			t.Fatalf("forced rollback = %v", err)
		}
	}
	w := registerForGap(t, e)
	if _, err := e.Claim(ctx, Thread, "contributor", w.TabID); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	events := readGapEvents(t, e)
	if len(events) < 2 {
		t.Fatalf("events = %+v, want the queued and the leased event", events)
	}
	// The ids are strictly increasing but not contiguous.
	gapped := false
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ID)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("event ids out of order: %v", ids)
		}
		var prev, cur int
		if _, err := fmt.Sscanf(ids[i-1], "%020d", &prev); err != nil {
			t.Fatalf("event id %q is not the padded sequence form: %v", ids[i-1], err)
		}
		if _, err := fmt.Sscanf(ids[i], "%020d", &cur); err != nil {
			t.Fatalf("event id %q is not the padded sequence form: %v", ids[i], err)
		}
		if cur != prev+1 {
			gapped = true
		}
	}
	if !gapped {
		t.Fatalf("no gap in %v although two sequence values were consumed by rolled back transactions", ids)
	}
	// The replay of a cursor returns exactly the events after it, in order. A cursor that
	// sits inside the gap (a consumed but never committed sequence value) returns the
	// next committed event without dropping it.
	cursor := ids[0]
	if got := replayAfter(events, cursor); len(got) != len(events)-1 {
		t.Fatalf("replay after %s returned %d events, want %d", cursor, len(got), len(events)-1)
	}
	inGap := betweenIDs(ids[0], ids[1])
	if got := replayAfter(events, inGap); len(got) != len(events)-1 {
		t.Fatalf("a cursor inside the gap (%s) returned %d events, want %d", inGap, len(got), len(events)-1)
	}
	if events[0].JobID != job.JobID {
		t.Fatalf("first event job = %q, want %q", events[0].JobID, job.JobID)
	}
}

// betweenIDs returns the padded sequence id that sits between two ids: the cursor that
// points into a gap of the durable sequence.
func betweenIDs(low, high string) string {
	var a, b int
	if _, err := fmt.Sscanf(low, "%020d", &a); err != nil {
		panic(err)
	}
	if _, err := fmt.Sscanf(high, "%020d", &b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%020d", (a+b)/2)
}

// replayAfter is the SSE/gRPC replay rule: keep the events whose id is strictly greater
// than the cursor, in ascending id order.
func replayAfter(events []Event, cursor string) []Event {
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if ev.ID > cursor {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// readGapEvents reads the durable events in id order.
func readGapEvents(t *testing.T, e *Engine) []Event {
	var rows []store.Record
	if err := e.Store.View(context.Background(), func(r store.Reader) error {
		var er error
		rows, er = r.List(store.Events)
		return er
	}); err != nil {
		t.Fatalf("List events: %v", err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		var ev Event
		if err := json.Unmarshal(row.Value, &ev); err != nil {
			t.Fatalf("event %s did not parse: %v", row.Key, err)
		}
		out = append(out, ev)
	}
	return out
}

// registerForGap registers one contributor tab on the engine of the test.
func registerForGap(t *testing.T, e *Engine) Worker {
	w, err := e.Register(context.Background(), Thread, "contributor", Worker{HostID: "host", Slots: 1})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return w
}
