// CLI-1: exercise the real engine against PostgreSQL, including durable restart state.
package pool

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
	"ccgsc/poolscore/internal/verify"

	"github.com/jackc/pgx/v5"
)

type pgEngineHarness struct {
	e   *Engine
	now time.Time
	dsn string
}

func pgEngineOK(t *testing.T, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T): %v", operation, err, err)
	}
}

func newPGEngineHarness(t *testing.T) *pgEngineHarness {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		// Match internal/store/pg's CI convention, including explicit false/0.
		for _, name := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI"} {
			if v := strings.TrimSpace(os.Getenv(name)); v != "" && v != "false" && v != "0" {
				t.Fatal("POOLS_PG_DSN is not set and CI is set")
			}
		}
		t.Skip("POOLS_PG_DSN is not set; skipping PostgreSQL engine integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	pgEngineOK(t, "parse PostgreSQL configuration", err)
	cfg.ConnectTimeout = 10 * time.Second
	admin, err := pgx.ConnectConfig(ctx, cfg)
	pgEngineOK(t, "connect to PostgreSQL", err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pgEngineOK(t, "close schema connection", admin.Close(ctx))
	})
	var nonce [16]byte
	_, err = rand.Read(nonce[:])
	pgEngineOK(t, "generate test namespace", err)
	schema := "cli1_engine_" + hex.EncodeToString(nonce[:])
	identifier := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+identifier)
	pgEngineOK(t, "create isolated test schema (CREATE privilege required)", err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Only this randomly named schema belongs to the test; never touch public.
		_, err := admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		pgEngineOK(t, "remove isolated test schema", err)
	})
	// pg.Open exercises the production migrations. Override search_path on every
	// pooled connection, for both URL and keyword DSNs, without changing the env.
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		pgEngineOK(t, "parse PostgreSQL URL", err)
		q := u.Query()
		q.Set("search_path", schema+",public")
		q.Set("connect_timeout", "10")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema + ",public connect_timeout=10"
	}
	h := &pgEngineHarness{dsn: dsn, now: time.Now().UTC().Truncate(time.Second)}
	h.open(t)
	return h
}

func (h *pgEngineHarness) open(t *testing.T) {
	t.Helper()
	db, err := pg.Open(h.dsn)
	pgEngineOK(t, "pg.Open", err)
	t.Cleanup(func() { pgEngineOK(t, "close PostgreSQL store", db.Close()) })
	h.e, err = New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "cli1", time.Minute)
	pgEngineOK(t, "Engine.New", err)
	h.e.Now = func() time.Time { return h.now }
	h.e.Verifier.SetClock(func() time.Time { return h.now })
}

func (h *pgEngineHarness) restart(t *testing.T) {
	t.Helper()
	pgEngineOK(t, "close before restart", h.e.Store.Close())
	h.open(t) // a new connection pool as well as a new Engine
}

func (h *pgEngineHarness) module(t *testing.T) string {
	t.Helper()
	data := []byte("CLI-1 integration module")
	id := verify.SHA256(data)
	_, err := h.e.Upload(context.Background(), id, "", 0, int64(len(data)), data)
	pgEngineOK(t, "Upload", err)
	return id
}

func (h *pgEngineHarness) job(t *testing.T, n, batch int, rerun float64, options string) Job {
	t.Helper()
	q := CreateRequest{ModuleArtifactIDs: []string{h.module(t)}, RerunPercent: &rerun, BatchSize: batch}
	if options != "" {
		q.Options = json.RawMessage(options)
	}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: fmt.Sprintf("task-%d", i), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	j, err := h.e.Create(context.Background(), Thread, "developer", "", q)
	pgEngineOK(t, "Create", err)
	return j
}

func (h *pgEngineHarness) worker(t *testing.T, pool, owner string, slots int) Worker {
	t.Helper()
	w, err := h.e.Register(context.Background(), pool, owner, Worker{HostID: "host-" + owner, Slots: slots})
	pgEngineOK(t, "Register", err)
	return w
}

func (h *pgEngineHarness) claim(t *testing.T, w Worker) Assignment {
	t.Helper()
	a, err := h.e.Claim(context.Background(), w.PoolID, w.Owner, w.TabID)
	pgEngineOK(t, "Claim", err)
	return a
}

func (h *pgEngineHarness) submit(t *testing.T, w Worker, a Assignment, output json.RawMessage) Result {
	t.Helper()
	r, err := h.e.Submit(context.Background(), w.PoolID, w.Owner, w.TabID,
		Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: output, SHA256: verify.SHA256(output)})
	pgEngineOK(t, "Submit", err)
	return r
}

func (h *pgEngineHarness) get(t *testing.T, j Job) Job {
	t.Helper()
	got, err := h.e.Get(context.Background(), j.PoolID, j.Owner, j.JobID)
	pgEngineOK(t, "Get", err)
	return got
}

func TestPGEngineSubmitLeaseSettle(t *testing.T) {
	h := newPGEngineHarness(t)
	j := h.job(t, 1, 0, 0, "")
	w := h.worker(t, Thread, "worker", 1)
	a := h.claim(t, w)
	if a.Lease.JobID != j.JobID || a.Lease.TaskID != j.Tasks[0].TaskID || a.Lease.Token == "" {
		t.Fatal("claim did not lease the created task")
	}
	pgEngineOK(t, "verify task envelope", h.e.VerifyTaskEnvelope(a.TaskEnvelope))
	out := json.RawMessage(`{ "answer" : 42 }`)
	r := h.submit(t, w, a, out)
	if r.Status != "done" || !r.HashOK || r.SHA256 != verify.SHA256(out) {
		t.Fatalf("unexpected settlement: status=%s hashOK=%v", r.Status, r.HashOK)
	}
	h.restart(t)
	got := h.get(t, j)
	if got.State != "completed" || got.Progress.Completed != 1 || len(got.Results) != 1 {
		t.Fatal("completed job/result did not persist across restart")
	}
	if !bytes.Equal(got.Results[0].Output, out) {
		t.Fatal("PostgreSQL changed the exact submitted output bytes")
	}
}

func TestPGEngineBatchLeaseRenew(t *testing.T) {
	h := newPGEngineHarness(t)
	j := h.job(t, 3, 3, 0, "")
	w := h.worker(t, Thread, "worker", 3)
	a := h.claim(t, w)
	if len(a.Batch) != 3 {
		t.Fatalf("batch size=%d, want 3", len(a.Batch))
	}
	seen := map[string]bool{}
	for _, member := range a.Batch {
		if seen[member.Lease.TaskID] || member.Lease.Token != a.Lease.Token {
			t.Fatal("batch repeated a task or lost its shared lease token")
		}
		seen[member.Lease.TaskID] = true
	}
	h.now = h.now.Add(30 * time.Second)
	forged := a.Lease
	forged.Generation++
	_, err := h.e.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &forged)
	if !errors.Is(err, ErrConflict) {
		t.Fatal("forged batch renewal was not rejected")
	}
	_, err = h.e.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &a.Lease)
	pgEngineOK(t, "batch Heartbeat", err)
	for _, task := range h.get(t, j).Tasks {
		if !task.Lease.ExpiresAt.Equal(h.now.Add(h.e.TTL)) {
			t.Fatal("renewal did not persist for every batch member")
		}
	}
	h.now = h.now.Add(40 * time.Second) // past original expiry, inside renewed TTL
	for _, member := range a.Batch {
		r := h.submit(t, w, member, json.RawMessage(`{"ok":true}`))
		if r.Status != "done" {
			t.Fatal("renewed batch member did not settle")
		}
	}
	got := h.get(t, j)
	if got.State != "completed" || got.Progress.Completed != 3 || len(got.Results) != 3 {
		t.Fatal("batch did not settle exactly once per task")
	}
}

func TestPGEngineCanaryAndRerun(t *testing.T) {
	t.Run("canary", func(t *testing.T) {
		h := newPGEngineHarness(t)
		cfg := h.e.TrustConfig()
		cfg.CanaryRate = 100
		cfg.RerunEnabled = false
		h.e.SetTrustConfig(cfg)
		answer := json.RawMessage(`"known answer"`)
		_, err := h.e.RegisterCanaryWithAnswer("canary", "canary-task", []byte(`{"canary":1}`), answer)
		pgEngineOK(t, "register canary", err)
		j := h.job(t, 1, 0, 0, "")
		w := h.worker(t, Thread, "worker", 1)
		a := h.claim(t, w)
		if string(a.Task.Input) != `{"canary":1}` {
			t.Fatal("100 percent canary was not assigned")
		}
		r := h.submit(t, w, a, answer)
		if r.Status != "canary" || !r.CanaryOK || r.Verified {
			t.Fatal("canary must pass without settling the developer task")
		}
		got := h.get(t, j)
		if len(got.Results) != 0 || got.Tasks[0].State != "queued" {
			t.Fatal("canary did not requeue developer task")
		}
		next := h.claim(t, w)
		if next.Task.TaskID == a.Task.TaskID || string(next.Task.Input) != `{"json":{"n":1}}` {
			t.Fatal("developer task was not restored after canary")
		}
		r = h.submit(t, w, next, json.RawMessage(`{"answer":42}`))
		if r.Status != "done" || !r.CanaryRan || !r.CanaryOK || !r.Verified || !r.TrustComplete {
			t.Fatal("settlement lost canary evidence")
		}
		h.restart(t)
		got = h.get(t, j)
		if len(got.Results) != 1 || !got.Results[0].CanaryOK || got.State != "completed" {
			t.Fatal("canary evidence did not persist")
		}
	})
	t.Run("rerun", func(t *testing.T) {
		h := newPGEngineHarness(t)
		j := h.job(t, 1, 0, 100, "")
		w1 := h.worker(t, Thread, "worker-one", 1)
		w2 := h.worker(t, Thread, "worker-two", 1)
		out := json.RawMessage(`{"answer":"same"}`)
		h.submit(t, w1, h.claim(t, w1), out)
		if got := h.get(t, j); got.State == "completed" || len(got.Results) != 0 {
			t.Fatal("100 percent rerun settled before independent confirmation")
		}
		r := h.submit(t, w2, h.claim(t, w2), out)
		if !r.Rerun || !r.RerunOK || r.Status != "done" {
			t.Fatal("matching independent rerun did not settle")
		}
		h.restart(t)
		got := h.get(t, j)
		if got.State != "completed" || len(got.Results) != 1 || !got.Results[0].RerunOK {
			t.Fatal("rerun evidence did not persist")
		}
	})
}

func TestPGEngineChainMerkleCheckpoint(t *testing.T) {
	h := newPGEngineHarness(t)
	cfg := h.e.ChainConfig()
	cfg.ChainEnabled, cfg.MerkleEnabled = true, true
	pgEngineOK(t, "enable chain and Merkle", h.e.SetChainConfig(cfg))
	j := h.job(t, 2, 0, 0, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	w := h.worker(t, Thread, "worker", 1)
	for i := 0; i < 2; i++ {
		h.submit(t, w, h.claim(t, w), json.RawMessage(`{ "answer": 42 }`))
	}
	ctx := context.Background()
	report, err := h.e.JobChain(ctx, Thread, j.Owner, j.JobID)
	pgEngineOK(t, "JobChain", err)
	if !report.Verification.Valid || len(report.Entries) != 2 || report.Head == "" {
		t.Fatal("settled result chain is invalid or incomplete")
	}
	if report.Entries[1].Prev != report.Entries[0].Hash {
		t.Fatal("chain entries are not linked")
	}
	first, err := h.e.ForceCheckpoint(ctx)
	pgEngineOK(t, "first checkpoint", err)
	h.now = h.now.Add(2 * cfg.MerkleInterval)
	second, err := h.e.ForceCheckpoint(ctx)
	pgEngineOK(t, "second checkpoint", err)
	if first.JobCount != 1 || first.Root == "" || second.Seq != first.Seq+1 ||
		second.PrevHash != first.Hash || second.PrevRoot != first.Root {
		t.Fatal("Merkle checkpoints lost job coverage or ancestry")
	}
	h.restart(t)
	report, err = h.e.JobChain(ctx, Thread, j.Owner, j.JobID)
	pgEngineOK(t, "reloaded JobChain", err)
	if !report.Verification.Valid || len(report.Entries) != 2 {
		t.Fatal("chain failed verification after PostgreSQL reload")
	}
	v, err := h.e.VerifyMerkleCheckpoint(ctx, second.Seq)
	pgEngineOK(t, "verify reloaded checkpoint", err)
	if !v.Valid {
		t.Fatal("persisted Merkle checkpoint did not verify")
	}
	results := h.get(t, j).Results
	results[0].Output = json.RawMessage(`{"tampered":true}`)
	if VerifyChain(report.Entries, results, report.Head).Valid {
		t.Fatal("exported chain accepted a modified result")
	}
}

func TestPGEngineSigningKeyringRestart(t *testing.T) {
	h := newPGEngineHarness(t)
	h.job(t, 1, 0, 0, "")
	w := h.worker(t, Thread, "worker", 1)
	a := h.claim(t, w)
	oldID := h.e.TaskKeyID
	key := append([]byte(nil), h.e.TaskKey...)
	samplingKey := append([]byte(nil), h.e.rerunSecret...)
	h.restart(t)
	if h.e.TaskKeyID != oldID || !bytes.Equal(key, h.e.TaskKey) || !bytes.Equal(samplingKey, h.e.rerunSecret) {
		t.Fatal("signing or sampling key changed across Engine.New")
	}
	pgEngineOK(t, "verify pre-restart envelope", h.e.VerifyTaskEnvelope(a.TaskEnvelope))
	pgEngineOK(t, "rotate signing key", h.e.RotateTaskKey(context.Background(), time.Minute))
	newID := h.e.TaskKeyID
	newKey := append([]byte(nil), h.e.TaskKey...)
	if newID == oldID {
		t.Fatal("rotation did not publish a successor")
	}
	h.restart(t)
	if h.e.TaskKeyID != newID || !bytes.Equal(newKey, h.e.TaskKey) {
		t.Fatal("rotated signing key did not persist")
	}
	pgEngineOK(t, "restored retired key during overlap", h.e.VerifyTaskEnvelope(a.TaskEnvelope))
	h.now = h.now.Add(time.Minute)
	if !errors.Is(h.e.VerifyTaskEnvelope(a.TaskEnvelope), verify.ErrUnknownKey) {
		t.Fatal("retired key remained valid beyond its overlap")
	}
}

func TestPGEngineSessionHeartbeatRestartIndex(t *testing.T) {
	h := newPGEngineHarness(t)
	id := h.module(t)
	w := h.worker(t, Process, "worker", 1)
	ctx := context.Background()
	s, err := h.e.OpenSession(ctx, "developer", "", SessionRequest{TabCount: 1, ModuleArtifactIDs: []string{id}})
	pgEngineOK(t, "OpenSession", err)
	if s.State != "open" || len(s.Tabs) != 1 || s.Tabs[0].TabID != w.TabID {
		t.Fatal("session did not reserve the process worker")
	}
	zero := 0.0
	j, err := h.e.SessionTasks(ctx, "developer", s.SessionID, "", CreateRequest{
		Tasks: []TaskSpec{{TaskID: "session-task", Input: json.RawMessage(`{"n":1}`)}}, RerunPercent: &zero,
	})
	pgEngineOK(t, "SessionTasks", err)
	h.now = h.now.Add(3 * h.e.TTL)
	_, err = h.e.Heartbeat(ctx, Process, w.Owner, w.TabID, nil)
	pgEngineOK(t, "session worker Heartbeat", err)
	got, err := h.e.GetSession(ctx, "developer", s.SessionID)
	pgEngineOK(t, "GetSession after heartbeat", err)
	if got.State != "open" || !got.Tabs[0].Heartbeat.Equal(h.now) {
		t.Fatal("heartbeat did not refresh the reserved session tab")
	}
	h.restart(t)
	if !slices.Contains(indexedSessions(h.e), s.SessionID) || !slices.Contains(indexedJobs(h.e, s.SessionID), j.JobID) {
		t.Fatal("Engine.New did not rebuild session and session-job indexes")
	}
	got, err = h.e.GetSession(ctx, "developer", s.SessionID)
	pgEngineOK(t, "GetSession after restart", err)
	if got.State != "open" || !got.Tabs[0].Heartbeat.Equal(h.now) {
		t.Fatal("session heartbeat did not survive reload")
	}
	h.now = h.now.Add(3 * h.e.TTL)
	pgEngineOK(t, "Sweep after restart", h.e.Sweep(ctx))
	got, err = h.e.GetSession(ctx, "developer", s.SessionID)
	pgEngineOK(t, "GetSession after stale sweep", err)
	if got.State != "draining" && got.State != "closed" {
		t.Fatal("rebuilt index did not drive stale-session expiry")
	}
}

// T100 (owner decision 2026-11-21, option A): shared content-addressed artifact
// blobs with per-owner reference rows on the PostgreSQL store, including the
// first-uploader migration and the delete rules.
func TestPGEngineArtifactOwnerReferences(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx := context.Background()
	data := []byte("T100 shared pg module bytes")
	id := verify.SHA256(data)

	// The first upload creates the blob and the first reference.
	if _, err := h.e.UploadOwned(ctx, id, "application/octet-stream", 0, int64(len(data)), data, "dev-a", false); err != nil {
		t.Fatalf("UploadOwned(dev-a): %v", err)
	}
	// The second upload of identical bytes joins the shared blob.
	a, err := h.e.UploadOwned(ctx, id, "application/octet-stream", 0, int64(len(data)), data, "dev-b", false)
	if err != nil {
		t.Fatalf("UploadOwned(dev-b) of identical bytes: %v", err)
	}
	if a.ArtifactID != id || !a.Complete || a.Owner != "dev-a" {
		t.Fatalf("join upload = %+v, want the shared blob with the first uploader kept", a)
	}
	// Both holders may use the artifact; a third subject may not.
	for _, subject := range []string{"dev-a", "dev-b"} {
		if err := h.e.CanUseArtifacts(ctx, subject, false, []string{id}); err != nil {
			t.Fatalf("CanUseArtifacts(%s): %v", subject, err)
		}
	}
	if err := h.e.CanUseArtifacts(ctx, "dev-c", false, []string{id}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CanUseArtifacts(dev-c) = %v, want ErrForbidden", err)
	}
	// The delete of one reference keeps the blob for the other holder.
	out, err := h.e.DeleteArtifactReference(ctx, id, "dev-a")
	if err != nil {
		t.Fatalf("DeleteArtifactReference(dev-a): %v", err)
	}
	if !out.Removed || out.BlobDeleted || out.RemainingRefs != 1 {
		t.Fatalf("delete by dev-a = %+v, want one remaining reference", out)
	}
	if _, err := h.e.Artifact(ctx, id); err != nil {
		t.Fatalf("blob gone although dev-b holds a reference: %v", err)
	}
	// The last delete removes the blob on PostgreSQL too.
	out, err = h.e.DeleteArtifactReference(ctx, id, "dev-b")
	if err != nil {
		t.Fatalf("DeleteArtifactReference(dev-b): %v", err)
	}
	if !out.BlobDeleted {
		t.Fatalf("delete by dev-b = %+v, want the blob deleted", out)
	}
	if _, err := h.e.Artifact(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Artifact after the last delete = %v, want ErrNotFound", err)
	}
}

// T100 migration: an artifact row written by a pre-T100 engine becomes its
// first uploader's reference on the next engine start, and the migration stays
// idempotent across restarts.
func TestPGEngineArtifactOwnerReferenceMigration(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx := context.Background()
	data := []byte("T100 legacy pg module")
	id := verify.SHA256(data)
	legacy := Artifact{
		ArtifactID: id, SHA256: id, Owner: "legacy-key", SizeBytes: int64(len(data)),
		Complete: true, ContentType: "application/octet-stream",
		CreatedAt: h.now.UTC(), Data: data,
	}
	if err := h.e.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Artifacts, id, legacy)
	}); err != nil {
		t.Fatalf("seed legacy artifact: %v", err)
	}
	// A restart of the engine runs the migration over the legacy row.
	h.restart(t)
	if err := h.e.CanUseArtifacts(ctx, "legacy-key", false, []string{id}); err != nil {
		t.Fatalf("CanUseArtifacts(legacy-key) after the migration = %v, want nil", err)
	}
	if err := h.e.CanUseArtifacts(ctx, "someone-else", false, []string{id}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CanUseArtifacts(someone-else) = %v, want ErrForbidden", err)
	}
	// One more restart keeps exactly one reference row.
	h.restart(t)
	if err := h.e.CanUseArtifacts(ctx, "legacy-key", false, []string{id}); err != nil {
		t.Fatalf("CanUseArtifacts(legacy-key) after the second restart = %v, want nil", err)
	}
	var refs []store.Record
	if err := h.e.Store.View(ctx, func(r store.Reader) error {
		var e error
		refs, e = r.List(store.ArtifactOwners)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("references after two migrations = %d, want exactly one", len(refs))
	}
}
