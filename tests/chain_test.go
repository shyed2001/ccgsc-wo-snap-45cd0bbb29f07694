// Path: tests/chain_test.go | Author: Hermes (MiniMax) for the owner | 2026-11-18
// What/why: offline HTTP acceptance of the audit chain and of the Merkle checkpoints:
// the capabilities report, the chain route, the tamper evidence gate and the tamper
// test over the real handler; WWPP-R22.3, R23.3, R26.2, R35.2 (WO T42 F3).
package tests

import (
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// chainZeroRerun switches the re-run sampler off, so the first result of a task is the
// accepted one. The default re-run rate is 1 % and the decision hashes the random job
// id, so a test that keeps the default would be flaky in 1 % of its runs.
var chainZeroRerun = 0.0

// chainFixture is a fixture whose engine has the admin switches of the audit chain on.
func chainFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	cfg := f.engine.ChainConfig()
	cfg.ChainEnabled = true
	cfg.MerkleEnabled = true
	if e := f.engine.SetChainConfig(cfg); e != nil {
		t.Fatal(e)
	}
	return f
}

// chainOnlyFixture is a fixture whose engine has the per-job chain on but the global
// Merkle checkpoints off, so the effective mode of a job is the mode the developer
// asked for (the authority rule (a) raises the mode only when the admin has the
// Merkle on).
func chainOnlyFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	cfg := f.engine.ChainConfig()
	cfg.ChainEnabled = true
	if e := f.engine.SetChainConfig(cfg); e != nil {
		t.Fatal(e)
	}
	return f
}

// The capabilities report the truth about the audit chain (WO T42 common rules).
func TestCapabilitiesReportTheAuditChain(t *testing.T) {
	f := setup(t)
	_, token := f.key(t, "developer")
	body := f.request(t, "GET", "/thread-pool/v1/capabilities", token, nil, 200, nil)
	caps := decode[struct {
		TamperEvidence struct {
			SecurityLogEnabled   bool `json:"securityLogEnabled"`
			PerJobChainAvailable bool `json:"perJobChainAvailable"`
			MerkleAvailable      bool `json:"merkleAvailable"`
		} `json:"tamperEvidence"`
	}](t, body)
	if !caps.TamperEvidence.SecurityLogEnabled {
		t.Error("the security log is reported as off")
	}
	// The chain is off by default (WWPP-R26.2).
	if caps.TamperEvidence.PerJobChainAvailable || caps.TamperEvidence.MerkleAvailable {
		t.Errorf("the chain is reported as available by default: %+v", caps.TamperEvidence)
	}
	// With the admin switches on, the capabilities report the chain.
	f2 := chainFixture(t)
	_, token2 := f2.key(t, "developer")
	body = f2.request(t, "GET", "/thread-pool/v1/capabilities", token2, nil, 200, nil)
	caps = decode[struct {
		TamperEvidence struct {
			SecurityLogEnabled   bool `json:"securityLogEnabled"`
			PerJobChainAvailable bool `json:"perJobChainAvailable"`
			MerkleAvailable      bool `json:"merkleAvailable"`
		} `json:"tamperEvidence"`
	}](t, body)
	if !caps.TamperEvidence.PerJobChainAvailable || !caps.TamperEvidence.MerkleAvailable {
		t.Errorf("the capabilities do not report the chain: %+v", caps.TamperEvidence)
	}
}

// The developer option options.tamperEvidence is accepted and the chain route returns
// the chain of the job.
func TestJobChainRouteReturnsTheChain(t *testing.T) {
	f := chainOnlyFixture(t)
	_, token := f.key(t, "developer")
	_, workerToken := f.key(t, "contributor")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		RerunPercent:      &chainZeroRerun,
		Options:           json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`),
	}
	j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", token, q, 201, nil))
	if j.ChainMode != pool.ChainModePerJobChain {
		t.Fatalf("chain mode = %q, want %q", j.ChainMode, pool.ChainModePerJobChain)
	}
	w := decode[pool.Worker](t, f.request(t, "POST", base+"/workers/register", workerToken, map[string]any{"hostId": "fake-host", "slots": 1}, 201, nil))
	a := decode[pool.Assignment](t, f.request(t, "POST", base+"/workers/"+w.TabID+"/leases", workerToken, nil, 200, nil))
	out := json.RawMessage(`{"json":{"answer":42}}`)
	f.request(t, "POST", base+"/workers/"+w.TabID+"/results", workerToken, pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}, 200, nil)

	report := decode[pool.ChainReport](t, f.request(t, "GET", base+"/jobs/"+j.JobID+"/chain", token, nil, 200, nil))
	if len(report.Entries) != 1 {
		t.Fatalf("chain entries = %d, want 1", len(report.Entries))
	}
	if !report.Verification.Valid {
		t.Fatalf("the chain of a clean job does not verify: %+v", report.Verification)
	}
	if report.Entries[0].TaskID != "one" {
		t.Errorf("the entry names the task %q, want one", report.Entries[0].TaskID)
	}
}

// The chain of a job of another developer is not readable.
func TestJobChainRouteRefusesAnotherOwner(t *testing.T) {
	f := chainOnlyFixture(t)
	_, token := f.key(t, "developer")
	_, stranger := f.key(t, "developer")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		RerunPercent:      &chainZeroRerun,
		Options:           json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`),
	}
	j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", token, q, 201, nil))
	f.request(t, "GET", base+"/jobs/"+j.JobID+"/chain", stranger, nil, 403, nil)
}

// The tamper test over the HTTP route: a result that was rewritten behind the back
// of the server fails the verification.
func TestJobChainRouteFailsAfterATamper(t *testing.T) {
	f := chainOnlyFixture(t)
	_, token := f.key(t, "developer")
	_, workerToken := f.key(t, "contributor")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		RerunPercent:      &chainZeroRerun,
		Options:           json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`),
	}
	j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", token, q, 201, nil))
	w := decode[pool.Worker](t, f.request(t, "POST", base+"/workers/register", workerToken, map[string]any{"hostId": "fake-host", "slots": 1}, 201, nil))
	a := decode[pool.Assignment](t, f.request(t, "POST", base+"/workers/"+w.TabID+"/leases", workerToken, nil, 200, nil))
	out := json.RawMessage(`{"json":{"answer":42}}`)
	f.request(t, "POST", base+"/workers/"+w.TabID+"/results", workerToken, pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}, 200, nil)

	// The result of the job is rewritten behind the back of the server.
	if e := f.db.Update(context.Background(), func(tx store.Tx) error {
		var job pool.Job
		if er := tx.Get(store.Jobs, j.JobID, &job); er != nil {
			return er
		}
		job.Results[0].Output = json.RawMessage(`{"json":{"answer":0}}`)
		return tx.Put(store.Jobs, j.JobID, job)
	}); e != nil {
		t.Fatal(e)
	}
	report := decode[pool.ChainReport](t, f.request(t, "GET", base+"/jobs/"+j.JobID+"/chain", token, nil, 200, nil))
	if report.Verification.Valid {
		t.Fatalf("a tampered result verifies: %+v", report.Verification)
	}
}

// The Merkle checkpoints are served over the HTTP route and verify.
func TestMerkleCheckpointsRoute(t *testing.T) {
	f := chainFixture(t)
	_, token := f.key(t, "developer")
	_, workerToken := f.key(t, "contributor")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		RerunPercent:      &chainZeroRerun,
		Options:           json.RawMessage(`{"tamperEvidence":{"mode":"merkle"}}`),
	}
	j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", token, q, 201, nil))
	w := decode[pool.Worker](t, f.request(t, "POST", base+"/workers/register", workerToken, map[string]any{"hostId": "fake-host", "slots": 1}, 201, nil))
	a := decode[pool.Assignment](t, f.request(t, "POST", base+"/workers/"+w.TabID+"/leases", workerToken, nil, 200, nil))
	out := json.RawMessage(`{"json":{"answer":42}}`)
	f.request(t, "POST", base+"/workers/"+w.TabID+"/results", workerToken, pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}, 200, nil)
	if _, e := f.engine.ForceCheckpoint(context.Background()); e != nil {
		t.Fatal(e)
	}
	report := decode[pool.MerkleReport](t, f.request(t, "GET", base+"/merkle:checkpoints", token, nil, 200, nil))
	if len(report.Checkpoints) != 1 {
		t.Fatalf("checkpoints = %d, want 1", len(report.Checkpoints))
	}
	if len(report.Verifications) != 1 || !report.Verifications[0].Valid {
		t.Fatalf("the checkpoint does not verify: %+v", report.Verifications)
	}
	if report.Checkpoints[0].JobCount != 1 || report.Checkpoints[0].Leaves[0].JobID != j.JobID {
		t.Fatalf("the checkpoint does not cover the job: %+v", report.Checkpoints[0])
	}
}

// The developer may not ask for a chain while the admin has it off.
func TestTamperEvidenceRequestRefusedWhileAdminOff(t *testing.T) {
	f := setup(t)
	_, token := f.key(t, "developer")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		RerunPercent:      &chainZeroRerun,
		Options:           json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`),
	}
	// 403: the admin policy forbids the mode.
	f.request(t, "POST", base+"/jobs", token, q, 403, nil)
}

// The options of another feature stay unsupported over HTTP (501).
func TestOtherOptionsAreUnsupportedOverHTTP(t *testing.T) {
	f := chainFixture(t)
	_, token := f.key(t, "developer")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		Options:           json.RawMessage(`{"toleranceCompare":true}`),
	}
	f.request(t, "POST", base+"/jobs", token, q, 501, nil)
}

// The chain of a job of another pool is not readable.
func TestJobChainRouteRefusesAnotherPool(t *testing.T) {
	f := chainOnlyFixture(t)
	_, token := f.key(t, "developer")
	module := f.artifact(t, "/thread-pool/v1", token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		RerunPercent:      &chainZeroRerun,
		Options:           json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`),
	}
	j := decode[pool.Job](t, f.request(t, "POST", "/thread-pool/v1/jobs", token, q, 201, nil))
	f.request(t, "GET", "/process-pool/v1/jobs/"+j.JobID+"/chain", token, nil, 403, nil)
}

// The verification of the chain needs no server state: the exported entries and the
// exported results verify on their own.
func TestChainVerificationIsIndependentOfTheServer(t *testing.T) {
	f := chainOnlyFixture(t)
	_, token := f.key(t, "developer")
	_, workerToken := f.key(t, "contributor")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		RerunPercent:      &chainZeroRerun,
		Options:           json.RawMessage(`{"tamperEvidence":{"mode":"per-job-chain"}}`),
	}
	j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", token, q, 201, nil))
	w := decode[pool.Worker](t, f.request(t, "POST", base+"/workers/register", workerToken, map[string]any{"hostId": "fake-host", "slots": 1}, 201, nil))
	a := decode[pool.Assignment](t, f.request(t, "POST", base+"/workers/"+w.TabID+"/leases", workerToken, nil, 200, nil))
	out := json.RawMessage(`{"json":{"answer":42}}`)
	f.request(t, "POST", base+"/workers/"+w.TabID+"/results", workerToken, pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}, 200, nil)

	report := decode[pool.ChainReport](t, f.request(t, "GET", base+"/jobs/"+j.JobID+"/chain", token, nil, 200, nil))
	claims, err := f.auth.Validate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	rawJob, err := f.engine.Get(context.Background(), pool.Thread, claims.KeyID, j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if v := pool.VerifyChain(report.Entries, rawJob.Results, report.Head); !v.Valid {
		t.Fatalf("the exported chain does not verify: %+v", v)
	}
	// A changed result breaks the verification.
	rawJob.Results[0].Output = json.RawMessage(`{"json":{"answer":1}}`)
	if v := pool.VerifyChain(report.Entries, rawJob.Results, report.Head); v.Valid {
		t.Fatalf("a changed result verifies: %+v", v)
	}
}

// The chain route needs the jobs:read scope.
func TestJobChainRouteNeedsTheScope(t *testing.T) {
	f := chainOnlyFixture(t)
	raw, _ := f.key(t, "developer")
	ctx := context.Background()
	small, e := f.auth.Exchange(ctx, raw, []string{"jobs:write"})
	if e != nil {
		t.Fatal(e)
	}
	f.request(t, "GET", "/thread-pool/v1/jobs/x/chain", small.AccessToken, nil, 403, nil)
}

// The chain route answers 404 for a job that does not exist.
func TestJobChainRouteAnswersNotFound(t *testing.T) {
	f := chainOnlyFixture(t)
	_, token := f.key(t, "developer")
	f.request(t, "GET", "/thread-pool/v1/jobs/s1.tp.nosuchjob/chain", token, nil, 404, nil)
}

// The chain mode of a job that was created without a request stays empty.
func TestJobWithoutARequestHasNoChain(t *testing.T) {
	f := chainOnlyFixture(t)
	_, token := f.key(t, "developer")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
	}
	j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", token, q, 201, nil))
	if j.ChainMode != pool.ChainModeNone {
		t.Fatalf("chain mode = %q, want %q", j.ChainMode, pool.ChainModeNone)
	}
	report := decode[pool.ChainReport](t, f.request(t, "GET", base+"/jobs/"+j.JobID+"/chain", token, nil, 200, nil))
	if len(report.Entries) != 0 {
		t.Fatalf("a job without a request got %d chain entries", len(report.Entries))
	}
}

// The errors of the chain keep their sentinel: an unsupported option is not an
// invalid request.
func TestUnsupportedOptionKeepsItsSentinel(t *testing.T) {
	f := chainFixture(t)
	_, token := f.key(t, "developer")
	base := "/thread-pool/v1"
	module := f.artifact(t, base, token)
	q := pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		Options:           json.RawMessage(`{"unknownOption":true}`),
	}
	body := f.request(t, "POST", base+"/jobs", token, q, 501, nil)
	problem := decode[struct {
		Code string `json:"code"`
	}](t, body)
	if problem.Code != "not-implemented" {
		t.Fatalf("problem code = %q, want not-implemented", problem.Code)
	}
	if errors.Is(pool.ErrUnsupported, pool.ErrInvalid) {
		t.Fatal("the sentinels are not distinct")
	}
}
