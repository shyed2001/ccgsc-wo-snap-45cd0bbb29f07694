// File: pools-core/internal/pool/recompute_W10C.go (go)
// W10C-SERVER row 2: server sample recompute of matmul results with a native Go
// kernel that reproduces the bytes of web/workloads/matmul/matmul_kernel.js.
//
// WO R17-RECOMPUTE (G-R16-4) row 1: the platform no longer knows the matmul
// kernel. It looks the reference up in the generic registry by the module
// SHA-256 of the job, and the reference itself (internal/matmulref) is
// registered from outside internal/pool. The platform never reads a workload
// file and never imports workload code (owner hard rule CR-53).
package pool

import (
	"encoding/hex"
	"encoding/json"

	"ccgsc/poolscore/internal/recompute"
	"ccgsc/poolscore/internal/verify"
)

// recomputeSampled reports whether the server-side sample recompute applies to
// this task. It reuses the existing deterministic RerunSampler decision, so every
// pool node reaches the same conclusion for the same task.
//
// WO R17-RECOMPUTE row 1 and row 2: the decision needs three things:
//   - the pool runs the registry recompute mode (the per-pool mode is off by
//     default, so a fresh install never recomputes),
//   - the pool is in a sampling verify mode with a sample rate above 0,
//   - the module of the job has a registered reference.
func (e *Engine) recomputeSampled(j *Job, t *Task) bool {
	s := e.PoolSettings(j.PoolID)
	if e.RecomputeMode(j.PoolID) != RecomputeModeRegistry {
		return false
	}
	if s.VerifyMode != VerifyModeSample && s.VerifyMode != VerifyModeBoth {
		return false
	}
	if s.SampleRate <= 0 {
		return false
	}
	if _, ok := e.recomputeReference(j); !ok {
		return false
	}
	return e.Verifier.Sampler().ShouldRerunWithKey(e.rerunSecret, j.JobID+":"+t.TaskID+":recompute", s.SampleRate*100)
}

// recomputeReference returns the registered reference of a job, if the job's
// module artifact has one. A job with several module artifacts, or with a
// module artifact nobody registered a reference for, is never recomputed.
func (e *Engine) recomputeReference(j *Job) (recompute.Reference, bool) {
	reg := e.RecomputeRegistry()
	if reg == nil {
		return recompute.Reference{}, false
	}
	if len(j.Request.ModuleArtifactIDs) != 1 {
		return recompute.Reference{}, false
	}
	ref, err := reg.Lookup(j.Request.ModuleArtifactIDs[0])
	if err != nil {
		return recompute.Reference{}, false
	}
	return ref, true
}

// outputDigest reads the {n, sha256} result the runner reports for a workload
// output; sha256 is the digest of the raw output bytes.
func outputDigest(output json.RawMessage) (int, string, bool) {
	var out struct {
		N      int    `json:"n"`
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(output, &out) != nil || out.SHA256 == "" {
		return 0, "", false
	}
	if _, err := hex.DecodeString(out.SHA256); err != nil || len(out.SHA256) != 64 {
		return 0, "", false
	}
	return out.N, out.SHA256, true
}

// recomputeVerdictOutcome is the full verdict of one sampled recompute
// (WO R18, G-R17-2/G-R17-3):
//
//   - ran is false when the comparison is unavailable: the mode is off, the
//     module has no reference, the verifier slot is busy, the reference cannot
//     answer the input or the reference failed its own self-check. An
//     unavailable comparison never accuses a worker.
//   - invalid marks a malformed claimed digest (a distinct verdict the admin
//     malformed policy turns into a rejection or a fraud mark).
//   - pinned reports whether the reference published a hash for the computed
//     size: only a pinned (self-checked) reference may accuse a mismatch.
type recomputeVerdictOutcome struct {
	match   bool
	ran     bool
	invalid bool
	pinned  bool
}

// recomputeVerdictFull compares a submission using a nonblocking verifier
// slot. The deferred retry path already owns a slot and calls the shared
// routine with slotHeld=true to avoid reacquiring its own permit.
func (e *Engine) recomputeVerdictFull(j *Job, t *Task, output json.RawMessage) recomputeVerdictOutcome {
	return e.recomputeVerdictWithSlot(j, t, output, false)
}

// recomputeVerdictWithSlot requires the caller to hold a permit when slotHeld
// is true; only the bounded deferred worker uses that path.
func (e *Engine) recomputeVerdictWithSlot(j *Job, t *Task, output json.RawMessage, slotHeld bool) recomputeVerdictOutcome {
	// Enforce the off switch even when called outside the normal sampling path.
	if e.RecomputeMode(j.PoolID) != RecomputeModeRegistry {
		return recomputeVerdictOutcome{}
	}
	ref, ok := e.recomputeReference(j)
	if !ok {
		return recomputeVerdictOutcome{}
	}
	// G-R17-2: the malformed claim is recognized without a verifier slot. The
	// parse is cheap and deterministic, so a busy pool can never defer a
	// malformed digest past the point where the admin policy applies.
	outN, claimed, ok := outputDigest(output)
	if !ok {
		return recomputeVerdictOutcome{ran: true, invalid: true}
	}
	// Acquire before invoking the native verifier: the verifier is the CPU-heavy
	// work that must be bounded, not merely its SHA-256 hashing afterwards.
	if !slotHeld {
		sem := e.workerPool()
		if !sem.TryAcquire() {
			return recomputeVerdictOutcome{}
		}
		defer sem.Release()
	}
	// The reference answers the task input. A reference that cannot answer it
	// is an unavailable comparison, never a worker failure.
	answer, err := ref.Verifier(t.Input)
	if err != nil || len(answer.Output) == 0 {
		return recomputeVerdictOutcome{}
	}
	// A published pin must be checked BEFORE using *any* mismatch to accuse,
	// including a worker-reported size mismatch. Otherwise a faulty native
	// reference can falsely flag honest workers (G-R17-3).
	hash := verify.SHA256(answer.Output)
	want, pinned := ref.ReferenceHash(answer.Size)
	if pinned && hash != want {
		e.Log.Error("recompute.selfCheckFailed", "taskId", t.TaskID, "size", answer.Size, "hash", hash, "want", want)
		return recomputeVerdictOutcome{}
	}
	// The reference owns the size; an incorrect claimed size is actionable
	// only if this reference self-check passed for its published pin.
	if outN != answer.Size {
		return recomputeVerdictOutcome{ran: true, pinned: pinned}
	}
	return recomputeVerdictOutcome{ran: true, match: verify.CompareResultHashes(claimed, hash), pinned: pinned}
}

// recomputeVerdict is the two-value view of recomputeVerdictFull kept for the
// pre-R18 callers: a malformed claim still reads as a mismatch there.
func (e *Engine) recomputeVerdict(j *Job, t *Task, output json.RawMessage) (match, ran bool) {
	v := e.recomputeVerdictFull(j, t, output)
	return v.match, v.ran
}
