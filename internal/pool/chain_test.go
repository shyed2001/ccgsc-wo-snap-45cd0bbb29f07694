// Path: internal/pool/chain_test.go | Author: Hermes (MiniMax) for the owner | 2026-11-18
// What/why: tests of the per-job SHA-256 audit chain, of the global Merkle checkpoints
// and of the admin gate of options.tamperEvidence; WWPP-R22.3, R23.3, R26.2, R35.2
// (WO T42 F3).
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// chainHarness is a pool engine on a file backed SQLite store with a fixed clock and
// the admin switches of the audit chain.
type chainHarness struct {
	engine *Engine
	now    time.Time
}

func newChainHarness(t *testing.T) *chainHarness {
	t.Helper()
	db, e := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if e != nil {
		t.Fatalf("sqlite.Open: %v", e)
	}
	t.Cleanup(func() { _ = db.Close() })
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, e := New(db, l, "s1", time.Minute)
	if e != nil {
		t.Fatalf("New: %v", e)
	}
	h := &chainHarness{engine: engine, now: time.Unix(1_700_000_000, 0).UTC()}
	engine.Now = func() time.Time { return h.now }
	engine.Verifier.SetClock(func() time.Time { return h.now })
	return h
}

// chainEnabled turns the per-job chain on for the admin.
func (h *chainHarness) chainEnabled(t *testing.T) {
	t.Helper()
	cfg := h.engine.ChainConfig()
	cfg.ChainEnabled = true
	if e := h.engine.SetChainConfig(cfg); e != nil {
		t.Fatalf("SetChainConfig: %v", e)
	}
}

// merkleEnabled turns the per-job chain and the Merkle checkpoints on for the admin.
func (h *chainHarness) merkleEnabled(t *testing.T) {
	t.Helper()
	cfg := h.engine.ChainConfig()
	cfg.ChainEnabled = true
	cfg.MerkleEnabled = true
	if e := h.engine.SetChainConfig(cfg); e != nil {
		t.Fatalf("SetChainConfig: %v", e)
	}
}

// zeroRerun switches the re-run sampler off, so the first result of a task is the
// accepted one. The default re-run rate is 1 % and the decision hashes the random job
// id, so a test that keeps the default would be flaky in 1 % of its runs.
var zeroRerun = 0.0

// job creates a job with n tasks and the given developer options.
func (h *chainHarness) job(t *testing.T, owner string, n int, options string) Job {
	t.Helper()
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, RerunPercent: &zeroRerun}
	if options != "" {
		q.Options = json.RawMessage(options)
	}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "task-" + string(rune('a'+i)), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	j, e := h.engine.Create(ctx, Thread, owner, "", q)
	if e != nil {
		t.Fatalf("Create: %v", e)
	}
	return j
}

// settle runs one task of the job to the end and returns the accepted result.
func (h *chainHarness) settle(t *testing.T, j Job, workerOwner string) Result {
	t.Helper()
	ctx := context.Background()
	w, e := h.engine.Register(ctx, Thread, workerOwner, Worker{HostID: "host-" + workerOwner, Slots: 4})
	if e != nil {
		t.Fatalf("Register: %v", e)
	}
	a, e := h.engine.Claim(ctx, Thread, workerOwner, w.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	out := json.RawMessage(`{"json":{"answer":42}}`)
	r, e := h.engine.Submit(ctx, Thread, workerOwner, w.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)})
	if e != nil {
		t.Fatalf("Submit: %v", e)
	}
	return r
}

// --- 1. the chain is opt-in and off by default (WWPP-R26.2) ---------------------

func TestChainIsOffByDefault(t *testing.T) {
	h := newChainHarness(t)
	// The admin did not switch the chain on.
	if h.engine.ChainConfig().ChainEnabled {
		t.Fatal("the chain is on by default")
	}
	j := h.job(t, "developer", 1, "")
	if j.ChainMode != ChainModeNone {
		t.Fatalf("chain mode = %q, want %q", j.ChainMode, ChainModeNone)
	}
	h.settle(t, j, "contributor")
	got, e := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if got.ChainHead != "" || got.ChainEntries != 0 {
		t.Fatalf("a job without a chain wrote a chain: %+v", got)
	}
	// The security log stays on: the accepted result is in the job record.
	if len(got.Results) != 1 || got.Results[0].Status != "done" {
		t.Fatalf("results = %+v, want one accepted result", got.Results)
	}
}

// The developer may not switch the chain on while the admin has it off.
func TestChainRequestRefusedWhileAdminOff(t *testing.T) {
	h := newChainHarness(t)
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "t1"}}, Options: json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`)}
	if _, e := h.engine.Create(ctx, Thread, "developer", "", q); !errors.Is(e, ErrForbidden) {
		t.Fatalf("Create with a chain request = %v, want ErrForbidden", e)
	}
}

// --- 2. the per-job chain over the accepted results (WWPP-R26.2) ----------------

func TestPerJobChainLinksTheAcceptedResults(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 2, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	if j.ChainMode != ChainModePerJobChain {
		t.Fatalf("chain mode = %q, want %q", j.ChainMode, ChainModePerJobChain)
	}
	h.settle(t, j, "contributor")
	h.settle(t, j, "contributor")
	report, e := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if len(report.Entries) != 2 {
		t.Fatalf("chain entries = %d, want 2", len(report.Entries))
	}
	if !report.Verification.Valid {
		t.Fatalf("the chain of a clean job does not verify: %+v", report.Verification)
	}
	// The links are real: every entry points at its predecessor.
	if report.Entries[0].Prev != "" {
		t.Errorf("the first entry has a predecessor: %q", report.Entries[0].Prev)
	}
	if report.Entries[1].Prev != report.Entries[0].Hash {
		t.Errorf("the second entry does not link to the first: %q vs %q", report.Entries[1].Prev, report.Entries[0].Hash)
	}
	if report.Entries[0].Seq != 1 || report.Entries[1].Seq != 2 {
		t.Errorf("the sequence numbers are wrong: %d %d", report.Entries[0].Seq, report.Entries[1].Seq)
	}
	// The head of the chain is on the job record.
	got, e := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if got.ChainHead != report.Entries[1].Hash || got.ChainEntries != 2 {
		t.Fatalf("the job record does not carry the head of the chain: %+v", got)
	}
	// The entries are the accepted results of the job.
	if report.Entries[0].ResultIndex != 0 || report.Entries[1].ResultIndex != 1 {
		t.Errorf("the result indexes are wrong: %d %d", report.Entries[0].ResultIndex, report.Entries[1].ResultIndex)
	}
}

// The chain of a job of another developer is not readable.
func TestChainOfAnotherOwnerIsForbidden(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	if _, e := h.engine.JobChain(context.Background(), Thread, "stranger", j.JobID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("JobChain of another owner = %v, want ErrForbidden", e)
	}
}

// --- 3. the tamper test: a changed result breaks the verification --------------

func TestTamperedResultFailsTheChainVerification(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	report, e := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if !report.Verification.Valid {
		t.Fatalf("the chain of a clean job does not verify: %+v", report.Verification)
	}
	// The result of the job is rewritten behind the back of the server.
	if e := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var job Job
		if er := tx.Get(store.Jobs, j.JobID, &job); er != nil {
			return er
		}
		job.Results[0].Output = json.RawMessage(`{"json":{"answer":0}}`)
		return tx.Put(store.Jobs, j.JobID, job)
	}); e != nil {
		t.Fatalf("the tamper write failed: %v", e)
	}
	// The verification fails and names the broken entry.
	report, e = h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if report.Verification.Valid {
		t.Fatalf("a tampered result verifies: %+v", report.Verification)
	}
	if report.Verification.BrokenAt != 1 {
		t.Errorf("the broken entry = %d, want 1", report.Verification.BrokenAt)
	}
	if report.Verification.Reason == "" {
		t.Error("the verification gives no reason")
	}
}

// A rewritten entry breaks the chain as well.
func TestTamperedChainEntryFailsTheVerification(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	// The worker id of the entry is rewritten: the hash of the entry no longer matches.
	if e := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var entry ChainEntry
		if er := tx.Get(store.Audit, chainEntryKey(j.JobID, 1), &entry); er != nil {
			return er
		}
		entry.WorkerID = "somebody-else"
		return tx.Put(store.Audit, chainEntryKey(j.JobID, 1), entry)
	}); e != nil {
		t.Fatalf("the tamper write failed: %v", e)
	}
	report, e := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if report.Verification.Valid {
		t.Fatalf("a rewritten entry verifies: %+v", report.Verification)
	}
}

// A removed entry breaks the chain as well.
func TestRemovedChainEntryFailsTheVerification(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 2, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	h.settle(t, j, "contributor")
	// The first entry is removed: the link of the second entry is broken.
	if e := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var entry ChainEntry
		if er := tx.Get(store.Audit, chainEntryKey(j.JobID, 1), &entry); er != nil {
			return er
		}
		entry.Prev = "0000000000000000000000000000000000000000000000000000000000000000"
		return tx.Put(store.Audit, chainEntryKey(j.JobID, 1), entry)
	}); e != nil {
		t.Fatalf("the tamper write failed: %v", e)
	}
	report, e := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if report.Verification.Valid {
		t.Fatalf("a broken link verifies: %+v", report.Verification)
	}
}

// --- 4. the global Merkle checkpoint (WWPP-R22.3) -----------------------------

func TestMerkleCheckpointCoversTheJobChainTips(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	cp, e := h.engine.ForceCheckpoint(context.Background())
	if e != nil {
		t.Fatalf("ForceCheckpoint: %v", e)
	}
	if cp.JobCount != 1 || cp.Root == "" || cp.Hash == "" {
		t.Fatalf("the checkpoint covers nothing: %+v", cp)
	}
	if cp.Leaves[0].JobID != j.JobID {
		t.Errorf("the leaf is not the job: %+v", cp.Leaves[0])
	}
	// The tip of the leaf is the head of the chain of the job.
	report, e := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if cp.Leaves[0].Tip != report.Head {
		t.Errorf("the leaf tip = %q, want the head of the chain %q", cp.Leaves[0].Tip, report.Head)
	}
	// The checkpoint verifies.
	v, e := h.engine.VerifyMerkleCheckpoint(context.Background(), cp.Seq)
	if e != nil {
		t.Fatalf("VerifyMerkleCheckpoint: %v", e)
	}
	if !v.Valid {
		t.Fatalf("a clean checkpoint does not verify: %+v", v)
	}
}

// A rewritten job chain breaks the checkpoint that covered it.
func TestMerkleCheckpointDetectsARewrittenChain(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	cp, e := h.engine.ForceCheckpoint(context.Background())
	if e != nil {
		t.Fatalf("ForceCheckpoint: %v", e)
	}
	// The chain of the job is rewritten behind the back of the server.
	if e := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var job Job
		if er := tx.Get(store.Jobs, j.JobID, &job); er != nil {
			return er
		}
		job.ChainHead = "1111111111111111111111111111111111111111111111111111111111111111"
		return tx.Put(store.Jobs, j.JobID, job)
	}); e != nil {
		t.Fatalf("the tamper write failed: %v", e)
	}
	v, e := h.engine.VerifyMerkleCheckpoint(context.Background(), cp.Seq)
	if e != nil {
		t.Fatalf("VerifyMerkleCheckpoint: %v", e)
	}
	if v.Valid {
		t.Fatalf("a rewritten chain verifies against the checkpoint: %+v", v)
	}
}

func TestMerkleCheckpointRemainsValidAfterAppend(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	j := h.job(t, "developer", 2, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	cp, e := h.engine.ForceCheckpoint(context.Background())
	if e != nil {
		t.Fatalf("ForceCheckpoint: %v", e)
	}

	h.settle(t, j, "contributor")
	v, e := h.engine.VerifyMerkleCheckpoint(context.Background(), cp.Seq)
	if e != nil {
		t.Fatalf("VerifyMerkleCheckpoint after append: %v", e)
	}
	if !v.Valid {
		t.Fatalf("a valid historical checkpoint was invalidated by an append: %+v", v)
	}
}

func TestMerkleCheckpointRejectsMissingPreviousCheckpoint(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	cp := MerkleCheckpoint{Seq: 2, Root: merkleRoot(nil), PrevRoot: "missing", PrevHash: "missing", At: h.now}
	var err error
	cp.Hash, err = checkpointHash(cp)
	if err != nil {
		t.Fatalf("checkpointHash: %v", err)
	}
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		return tx.Put(store.Audit, merkleEntryKey(cp.Seq), cp)
	}); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	v, err := h.engine.VerifyMerkleCheckpoint(context.Background(), cp.Seq)
	if err != nil {
		t.Fatalf("VerifyMerkleCheckpoint: %v", err)
	}
	if v.Valid || v.Reason != "the previous checkpoint is missing" {
		t.Fatalf("verification = %+v, want missing previous checkpoint failure", v)
	}
}

// The checkpoints form a chain of their own.
func TestMerkleCheckpointsLinkToEachOther(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	first, e := h.engine.ForceCheckpoint(context.Background())
	if e != nil {
		t.Fatalf("ForceCheckpoint: %v", e)
	}
	h.now = h.now.Add(2 * h.engine.ChainConfig().MerkleInterval)
	second, e := h.engine.ForceCheckpoint(context.Background())
	if e != nil {
		t.Fatalf("ForceCheckpoint: %v", e)
	}
	if second.Seq != first.Seq+1 {
		t.Fatalf("the second checkpoint has the sequence %d, want %d", second.Seq, first.Seq+1)
	}
	if second.PrevHash != first.Hash || second.PrevRoot != first.Root {
		t.Fatalf("the second checkpoint does not link to the first: %+v", second)
	}
	// The sweep writes the periodic checkpoint on its own.
	h.now = h.now.Add(2 * h.engine.ChainConfig().MerkleInterval)
	if e := h.engine.Sweep(context.Background()); e != nil {
		t.Fatalf("Sweep: %v", e)
	}
	report, e := h.engine.MerkleCheckpoints(context.Background(), 0)
	if e != nil {
		t.Fatalf("MerkleCheckpoints: %v", e)
	}
	if len(report.Checkpoints) != 3 {
		t.Fatalf("checkpoints = %d, want 3", len(report.Checkpoints))
	}
	for _, v := range report.Verifications {
		if !v.Valid {
			t.Fatalf("a clean checkpoint does not verify: %+v", v)
		}
	}
}

// The Merkle checkpoints are off while the admin has them off.
func TestMerkleIsOffWhileTheAdminKeepsItOff(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	if e := h.engine.Sweep(context.Background()); e != nil {
		t.Fatalf("Sweep: %v", e)
	}
	report, e := h.engine.MerkleCheckpoints(context.Background(), 0)
	if e != nil {
		t.Fatalf("MerkleCheckpoints: %v", e)
	}
	if len(report.Checkpoints) != 0 {
		t.Fatalf("the admin disabled the Merkle checkpoints but %d were written", len(report.Checkpoints))
	}
}

// --- 5. the admin gate of options.tamperEvidence (WWPP-R23.3, R35.2) ----------

// Authority (a): the admin decides the global Merkle and the developer may only raise
// the mode of its job.
func TestChainAuthorityARaisesTheMode(t *testing.T) {
	h := newChainHarness(t)
	cfg := h.engine.ChainConfig()
	cfg.ChainEnabled = true
	cfg.MerkleEnabled = true
	cfg.ChainAuthority = ChainAuthorityA
	if e := h.engine.SetChainConfig(cfg); e != nil {
		t.Fatalf("SetChainConfig: %v", e)
	}
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	if j.ChainMode != ChainModeMerkle {
		t.Fatalf("chain mode = %q, want %q", j.ChainMode, ChainModeMerkle)
	}
}

// Authority (a): a developer that asks for nothing keeps no chain.
func TestChainAuthorityAKeepsTheChainOffWithoutARequest(t *testing.T) {
	h := newChainHarness(t)
	cfg := h.engine.ChainConfig()
	cfg.ChainEnabled = true
	cfg.MerkleEnabled = true
	if e := h.engine.SetChainConfig(cfg); e != nil {
		t.Fatalf("SetChainConfig: %v", e)
	}
	j := h.job(t, "developer", 1, "")
	if j.ChainMode != ChainModeNone {
		t.Fatalf("chain mode = %q, want %q", j.ChainMode, ChainModeNone)
	}
}

// Authority (b): the developer chooses freely inside the allowance of the admin.
func TestChainAuthorityBAllowsEveryModeInsideTheAllowance(t *testing.T) {
	h := newChainHarness(t)
	cfg := h.engine.ChainConfig()
	cfg.ChainEnabled = true
	cfg.ChainAuthority = ChainAuthorityB
	cfg.AllowedModes = []ChainMode{ChainModeNone, ChainModeSecurityLog, ChainModePerJobChain}
	if e := h.engine.SetChainConfig(cfg); e != nil {
		t.Fatalf("SetChainConfig: %v", e)
	}
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	if j.ChainMode != ChainModePerJobChain {
		t.Fatalf("chain mode = %q, want %q", j.ChainMode, ChainModePerJobChain)
	}
	// A mode outside the allowance is refused.
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "t1"}}, Options: json.RawMessage(`{"tamperEvidence":{"mode":"merkle"}}`)}
	if _, e := h.engine.Create(ctx, Thread, "developer", "", q); !errors.Is(e, ErrForbidden) {
		t.Fatalf("Create outside the allowance = %v, want ErrForbidden", e)
	}
}

// The option tamperEvidence.chainPerJob is the shorthand of the mode per-job-chain.
func TestTamperEvidenceChainPerJobShorthand(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"chainPerJob":true}}`)
	if j.ChainMode != ChainModePerJobChain {
		t.Fatalf("chain mode = %q, want %q", j.ChainMode, ChainModePerJobChain)
	}
}

// An unknown tamper evidence mode is refused.
func TestUnknownTamperEvidenceModeIsRefused(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "t1"}}, Options: json.RawMessage(`{"tamperEvidence":{"mode":"magic"}}`)}
	if _, e := h.engine.Create(ctx, Thread, "developer", "", q); !errors.Is(e, ErrInvalid) {
		t.Fatalf("Create with an unknown mode = %v, want ErrInvalid", e)
	}
}

// The options that belong to another feature stay unsupported: no request field is
// silently ignored (WO T42 common rules).
func TestOtherOptionsStayUnsupported(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	for _, options := range []string{
		`{"toleranceCompare":true}`,
		`{"comparatorArtifactId":"` + verify.SHA256([]byte("cmp")) + `"}`,
		`{"tags":["a"]}`,
		`{"taskTimeoutMs":1000}`,
		`{"maxTaskBytes":10}`,
		`{"p2p":{"enabled":true}}`,
		`{"uploadChecks":{"mode":"all"}}`,
		`{"somethingElse":1}`,
	} {
		q := CreateRequest{ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "t1"}}, Options: json.RawMessage(options)}
		if _, e := h.engine.Create(ctx, Thread, "developer", "", q); !errors.Is(e, ErrUnsupported) {
			t.Errorf("Create with the options %s = %v, want ErrUnsupported", options, e)
		}
	}
}

// The chain of a job without a chain request stays empty, also when the admin has the
// chain on.
func TestChainRequestIsTheOnlyWayToGetAChain(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, "")
	h.settle(t, j, "contributor")
	report, e := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if len(report.Entries) != 0 || report.Head != "" {
		t.Fatalf("a job without a request got a chain: %+v", report)
	}
	if !report.Verification.Valid {
		t.Fatalf("the empty chain of a job without a request does not verify: %+v", report.Verification)
	}
}

// The verification of a chain works on the exported entries alone, so a developer can
// verify the chain outside the server.
func TestChainVerificationWorksOnTheExportedEntries(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	report, e := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	got, e := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	// The exported entries verify against the exported results and the exported head.
	if v := VerifyChain(report.Entries, got.Results, report.Head); !v.Valid {
		t.Fatalf("the exported chain does not verify: %+v", v)
	}
	// A changed result in the export breaks the verification.
	results := append([]Result(nil), got.Results...)
	results[0].Output = json.RawMessage(`{"json":{"answer":1}}`)
	if v := VerifyChain(report.Entries, results, report.Head); v.Valid {
		t.Fatalf("a changed result verifies: %+v", v)
	}
}

// The hash of an entry covers the result bytes in their stored form, so a result that
// arrives with insignificant whitespace still hashes to the same value as the stored
// result: the chain does not break on the JSON round trip.
func TestChainHashSurvivesTheJSONRoundTrip(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}}, RerunPercent: &zeroRerun, Options: json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`)}
	j, e := h.engine.Create(ctx, Thread, "developer", "", q)
	if e != nil {
		t.Fatalf("Create: %v", e)
	}
	// The output arrives with insignificant whitespace around the value.
	w, e := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host", Slots: 1})
	if e != nil {
		t.Fatalf("Register: %v", e)
	}
	a, e := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	out := json.RawMessage(`{ "json" : { "answer" : 42 } }`)
	if _, e = h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}); e != nil {
		t.Fatalf("Submit: %v", e)
	}
	// The chain of the job still verifies after the round trip through the store.
	report, e := h.engine.JobChain(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("chain entries = %d, want 1", len(report.Entries))
	}
	if !report.Verification.Valid {
		t.Fatalf("the chain does not survive the JSON round trip: %+v", report.Verification)
	}
}

// The chain of a job stays consistent when many workers settle tasks of the same job
// at the same time: the sequence numbers are unique and the links are unbroken.
func TestChainUnderConcurrentSubmissions(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	const workers = 6
	const tasks = 12
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, RerunPercent: &zeroRerun, Options: json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`)}
	for i := 0; i < tasks; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "task-" + string(rune('a'+i)), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	j, e := h.engine.Create(ctx, Thread, "developer", "", q)
	if e != nil {
		t.Fatalf("Create: %v", e)
	}
	tabs := make([]Worker, workers)
	for i := range tabs {
		w, e := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-" + string(rune('a'+i)), Slots: 4})
		if e != nil {
			t.Fatalf("Register: %v", e)
		}
		tabs[i] = w
	}
	var wg sync.WaitGroup
	errs := make(chan error, tasks)
	for i := range tabs {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			out := json.RawMessage(`{"json":{"answer":42}}`)
			for {
				a, e := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
				if e != nil {
					if errors.Is(e, ErrEmpty) {
						return
					}
					errs <- e
					return
				}
				if _, e := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}); e != nil {
					errs <- e
					return
				}
			}

		}(tabs[i])
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("a concurrent submission failed: %v", e)
	}
	report, e := h.engine.JobChain(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("JobChain: %v", e)
	}
	if len(report.Entries) != tasks {
		t.Fatalf("chain entries = %d, want %d", len(report.Entries), tasks)
	}
	if !report.Verification.Valid {
		t.Fatalf("the chain of a concurrent job does not verify: %+v", report.Verification)
	}
	// The sequence numbers are unique and the links are unbroken.
	seen := map[uint64]bool{}
	for i, entry := range report.Entries {
		if seen[entry.Seq] {
			t.Fatalf("the sequence number %d is used twice", entry.Seq)
		}
		seen[entry.Seq] = true
		if i > 0 && entry.Prev != report.Entries[i-1].Hash {
			t.Fatalf("the link of the entry %d is broken", entry.Seq)
		}
	}
}

func TestForceCheckpointRequiresMerkleAdminSwitch(t *testing.T) {
	h := newChainHarness(t)
	if _, e := h.engine.ForceCheckpoint(context.Background()); !errors.Is(e, ErrForbidden) {
		t.Fatalf("ForceCheckpoint with Merkle disabled = %v, want ErrForbidden", e)
	}
}

func TestMerkleReportUsesNewestCheckpointRoot(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	first, e := h.engine.ForceCheckpoint(context.Background())
	if e != nil {
		t.Fatalf("first ForceCheckpoint: %v", e)
	}

	j2 := h.job(t, "developer-two", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j2, "contributor-two")
	h.now = h.now.Add(2 * h.engine.ChainConfig().MerkleInterval)
	second, e := h.engine.ForceCheckpoint(context.Background())
	if e != nil {
		t.Fatalf("second ForceCheckpoint: %v", e)
	}
	report, e := h.engine.MerkleCheckpoints(context.Background(), 0)
	if e != nil {
		t.Fatalf("MerkleCheckpoints: %v", e)
	}
	if len(report.Checkpoints) != 2 || len(report.Verifications) != 2 {
		t.Fatalf("checkpoint report lengths = %d/%d, want 2/2", len(report.Checkpoints), len(report.Verifications))
	}
	if report.Checkpoints[0].Seq != second.Seq || report.LatestRoot != second.Root {
		t.Fatalf("latest root = %q, newest checkpoint = %+v, first = %+v", report.LatestRoot, second, first)
	}
}

func TestVerifyMerkleCheckpointRequiresCompleteAncestry(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	cp2 := MerkleCheckpoint{Seq: 2, At: h.now, Root: merkleRoot(nil), JobCount: 0}
	var err error
	cp2.Hash, err = checkpointHash(cp2)
	if err != nil {
		t.Fatal(err)
	}
	cp3 := MerkleCheckpoint{Seq: 3, At: h.now, Root: merkleRoot(nil), PrevRoot: cp2.Root, PrevHash: cp2.Hash, JobCount: 0}
	cp3.Hash, err = checkpointHash(cp3)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		if err := tx.Put(store.Audit, merkleEntryKey(cp2.Seq), cp2); err != nil {
			return err
		}
		return tx.Put(store.Audit, merkleEntryKey(cp3.Seq), cp3)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := h.engine.VerifyMerkleCheckpoint(context.Background(), cp3.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Fatalf("checkpoint with missing anchor was valid: %+v", got)
	}
	if got.Reason != "a checkpoint ancestor is missing" {
		t.Fatalf("reason = %q, want missing ancestor", got.Reason)
	}
}
