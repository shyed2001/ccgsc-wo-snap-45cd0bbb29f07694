package pool

// F4: developer-owned, opt-in, fail-OPEN per-run chain of MODULE, INPUT and
// accepted RESULT digests. Distinct from the historical fail-closed per-job
// result chain and the server-wide Merkle checkpoint. An audit storage error
// NEVER retroactively refuses or rolls back a committed worker submission.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

const runAuditDomain = "ccgsc.pools.run-audit.v1"
const runAuditStatePrefix = "run-state/"
const runAuditEntryPrefix = "run-evidence/"
const runAuditGapWarning = "run-audit-gap"

type RunAuditEntry struct {
	Seq      uint64    `json:"seq"`
	Kind     string    `json:"kind"` // module, input, result, gap
	JobID    string    `json:"jobId"`
	TaskID   string    `json:"taskId,omitempty"`
	RefIndex int       `json:"refIndex"`
	Digest   string    `json:"sha256,omitempty"`
	WorkerID string    `json:"workerId,omitempty"`
	At       time.Time `json:"at"`
	Prev     string    `json:"prev"`
	Hash     string    `json:"hash"`
}
type runAuditState struct {
	Head     string `json:"head"`
	Seq      uint64 `json:"seq"`
	Examined int    `json:"examined"`
	Seeded   bool   `json:"seeded"`
	GapNoted bool   `json:"gapNoted"`
}

func runAuditEntryKey(jobID string, seq uint64) string {
	return fmt.Sprintf("%s%s/%020d", runAuditEntryPrefix, jobID, seq)
}
func runAuditHash(e RunAuditEntry) (string, error) {
	fields := struct {
		Seq      uint64    `json:"seq"`
		Kind     string    `json:"kind"`
		JobID    string    `json:"jobId"`
		TaskID   string    `json:"taskId,omitempty"`
		RefIndex int       `json:"refIndex"`
		Digest   string    `json:"sha256,omitempty"`
		WorkerID string    `json:"workerId,omitempty"`
		At       time.Time `json:"at"`
		Prev     string    `json:"prev"`
	}{e.Seq, e.Kind, e.JobID, e.TaskID, e.RefIndex, e.Digest, e.WorkerID, e.At.UTC(), e.Prev}
	raw, err := verify.CanonicalJSON(fields)
	if err != nil {
		return "", err
	}
	return verify.SHA256(append([]byte(runAuditDomain+"\x00"), raw...)), nil
}
func (e *Engine) appendRunAuditEntry(tx store.Tx, st *runAuditState, j Job, kind, task string, index int, digest, worker string) error {
	entry := RunAuditEntry{Seq: st.Seq + 1, Kind: kind, JobID: j.JobID, TaskID: task, RefIndex: index, Digest: digest, WorkerID: worker, At: e.Now().UTC(), Prev: st.Head}
	h, err := runAuditHash(entry)
	if err != nil {
		return err
	}
	entry.Hash = h
	if err := tx.Put(store.Audit, runAuditEntryKey(j.JobID, entry.Seq), entry); err != nil {
		return err
	}
	st.Seq = entry.Seq
	st.Head = entry.Hash
	return nil
}

// writeRunAudit is an INDEPENDENT transaction after primary job creation or
// settlement has COMMITTED. It re-reads the durable Job under a PG row lock,
// fences the run-audit state and advances the published chain head together.
func (e *Engine) writeRunAudit(ctx context.Context, jobID string) (Job, error) {
	var updated Job
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		var j Job
		if err := tx.Get(store.Jobs, jobID, &j); err != nil {
			return err
		}
		updated = j
		if j.ChainMode != ChainModePerRun {
			return nil
		}
		var st runAuditState
		er := tx.Get(store.Audit, runAuditStatePrefix+jobID, &st)
		if er != nil && !errors.Is(er, store.ErrNotFound) {
			return er
		}
		if st.Head != j.ChainHead || int(st.Seq) != j.ChainEntries {
			return ErrConflict
		}
		if st.Seeded && st.Examined == len(j.Results) && (!j.AuditGap || st.GapNoted) {
			return nil
		}
		if !st.Seeded {
			for i, artifact := range j.Request.ModuleArtifactIDs {
				if err := e.appendRunAuditEntry(tx, &st, j, "module", "", i, artifact, ""); err != nil {
					return err
				}
			}
			for i, task := range j.Tasks {
				if err := e.appendRunAuditEntry(tx, &st, j, "input", task.TaskID, i, verify.SHA256(task.Input), ""); err != nil {
					return err
				}
			}
			st.Seeded = true
		}
		if j.AuditGap && !st.GapNoted {
			if err := e.appendRunAuditEntry(tx, &st, j, "gap", "", -1, "", ""); err != nil {
				return err
			}
			st.GapNoted = true
		}
		if st.Examined > len(j.Results) {
			return ErrConflict
		} // results cannot shrink
		for i := st.Examined; i < len(j.Results); i++ {
			result := j.Results[i]
			if result.Status != "done" {
				continue
			}
			digest := verify.SHA256(result.Output)
			if err := e.appendRunAuditEntry(tx, &st, j, "result", result.TaskID, i, digest, result.WorkerID); err != nil {
				return err
			}
		}
		st.Examined = len(j.Results)
		if err := tx.Put(store.Audit, runAuditStatePrefix+jobID, st); err != nil {
			return err
		}
		j.ChainHead = st.Head
		j.ChainEntries = int(st.Seq)
		if err := tx.Put(store.Jobs, jobID, j); err != nil {
			return err
		}
		updated = j
		return nil
	})
	if err != nil {
		return Job{}, err
	}
	return updated, nil
}
func hasRunAuditGapWarning(warnings []Warning) bool {
	for _, w := range warnings {
		if w.Code == runAuditGapWarning {
			return true
		}
	}
	return false
}

// markRunAuditGap is a separate best-effort durable note. If the database is
// wholly unavailable even this write can fail, but the original result is
// already committed, and the structured warning is emitted regardless.
func (e *Engine) markRunAuditGap(ctx context.Context, jobID string) error {
	return e.Store.Update(ctx, func(tx store.Tx) error {
		var j Job
		if err := tx.Get(store.Jobs, jobID, &j); err != nil {
			return err
		}
		if j.ChainMode != ChainModePerRun || j.AuditGap {
			return nil
		}
		j.AuditGap = true
		if !hasRunAuditGapWarning(j.Warnings) {
			j.Warnings = append(j.Warnings, Warning{Code: runAuditGapWarning, Message: "Per-run audit append failed; result retained, audit evidence has a gap.", Severity: "warning"})
		}
		return tx.Put(store.Jobs, jobID, j)
	})
}

// bestEffortRunAudit never returns a failure to a developer or worker API;
// retrying a Get/chain verification reconciles any missing committed results.
func (e *Engine) bestEffortRunAudit(ctx context.Context, jobID string) (Job, error) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	j, err := e.writeRunAudit(auditCtx, jobID)
	cancel()
	if err == nil {
		return j, nil
	}
	if e.Log != nil {
		e.Log.Warn("runAudit.appendFailed", "jobId", jobID, "error", err)
	}
	gapCtx, gapCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer gapCancel()
	if gapErr := e.markRunAuditGap(gapCtx, jobID); gapErr != nil && e.Log != nil {
		e.Log.Warn("runAudit.gapMarkFailed", "jobId", jobID, "error", gapErr)
	}
	return Job{}, err
}

// refreshRunAudit reconciles committed evidence only when the durable job
// has more records than the published head covers. A read failure in the
// independent audit transaction must not hide a committed job or result.
func (e *Engine) refreshRunAudit(ctx context.Context, j Job) Job {
	if j.ChainMode != ChainModePerRun {
		return j
	}
	expected := len(j.Request.ModuleArtifactIDs) + len(j.Tasks)
	for _, r := range j.Results {
		if r.Status == "done" {
			expected++
		}
	}
	if j.AuditGap {
		expected++
	}
	if expected == j.ChainEntries {
		return j
	}
	updated, err := e.bestEffortRunAudit(ctx, j.JobID)
	if err == nil {
		return updated
	}
	j.AuditGap = true
	if !hasRunAuditGapWarning(j.Warnings) {
		j.Warnings = append(j.Warnings, Warning{Code: runAuditGapWarning, Message: "Per-run audit append failed; result retained, audit evidence has a gap.", Severity: "warning"})
	}
	return j
}
func (e *Engine) runAuditEntries(r store.Reader, jobID string) ([]RunAuditEntry, error) {
	rows, err := r.List(store.Audit)
	if err != nil {
		return nil, err
	}
	prefix := runAuditEntryPrefix + jobID + "/"
	out := make([]RunAuditEntry, 0)
	for _, row := range rows {
		if !strings.HasPrefix(row.Key, prefix) {
			continue
		}
		var entry RunAuditEntry
		if err := json.Unmarshal(row.Value, &entry); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// VerifyRunAudit recomputes every hash, predecessor and artifact/input/result
// binding against the trusted Job record. A chain carrying a gap is NOT
// considered complete even if every present link hashes correctly.
func VerifyRunAudit(entries []RunAuditEntry, j Job) ChainVerification {
	v := ChainVerification{JobID: j.JobID, Mode: ChainModePerRun, Valid: true, Entries: len(entries)}
	if len(entries) > 0 {
		v.Head = entries[len(entries)-1].Hash
	}
	seenModules := make(map[int]bool)
	seenInputs := make(map[int]bool)
	seenResults := make(map[int]bool)
	gapFound := false
	for i, item := range entries {
		fail := func(reason string) ChainVerification {
			v.Valid = false
			v.Reason = reason
			v.BrokenAt = i + 1
			return v
		}
		if item.JobID != j.JobID || item.Seq != uint64(i+1) {
			return fail("incorrect run or non-contiguous sequence")
		}
		prev := ""
		if i > 0 {
			prev = entries[i-1].Hash
		}
		if item.Prev != prev {
			return fail("broken predecessor link")
		}
		h, err := runAuditHash(item)
		if err != nil || item.Hash != h {
			return fail("run audit hash mismatch")
		}
		if item.At.IsZero() {
			return fail("missing audit event time")
		}
		switch item.Kind {
		case "module":
			if item.RefIndex < 0 || item.RefIndex >= len(j.Request.ModuleArtifactIDs) || seenModules[item.RefIndex] || item.Digest != j.Request.ModuleArtifactIDs[item.RefIndex] {
				return fail("module digest changed or duplicated")
			}
			seenModules[item.RefIndex] = true
		case "input":
			if item.RefIndex < 0 || item.RefIndex >= len(j.Tasks) || seenInputs[item.RefIndex] {
				return fail("input index changed or duplicated")
			}
			task := j.Tasks[item.RefIndex]
			if item.TaskID != task.TaskID || item.Digest != verify.SHA256(task.Input) {
				return fail("input digest changed")
			}
			seenInputs[item.RefIndex] = true
		case "result":
			if item.RefIndex < 0 || item.RefIndex >= len(j.Results) || seenResults[item.RefIndex] {
				return fail("result index changed or duplicated")
			}
			res := j.Results[item.RefIndex]
			h := verify.SHA256(res.Output)
			if res.Status != "done" || item.Digest != h || item.TaskID != res.TaskID || item.WorkerID != res.WorkerID {
				return fail("accepted result changed or missing")
			}
			seenResults[item.RefIndex] = true
		case "gap":
			gapFound = true
			if item.Digest != "" || item.TaskID != "" {
				return fail("malformed audit gap marker")
			}
		default:
			return fail("unknown run audit record kind")
		}
	}
	if v.Head != j.ChainHead || len(entries) != j.ChainEntries {
		v.Valid = false
		v.Reason = "run audit published head/sequence mismatch"
		return v
	}
	if len(seenModules) != len(j.Request.ModuleArtifactIDs) || len(seenInputs) != len(j.Tasks) {
		v.Valid = false
		v.Reason = "missing code/input evidence"
		return v
	}
	done := 0
	for i, res := range j.Results {
		if res.Status == "done" {
			done++
			if !seenResults[i] {
				v.Valid = false
				v.Reason = "missing accepted result evidence"
				return v
			}
		}
	}
	if len(seenResults) != done {
		v.Valid = false
		v.Reason = "unexpected result evidence"
		return v
	}
	if gapFound || j.AuditGap {
		v.Valid = false
		v.Reason = "per-run audit contains a fail-open gap"
		return v
	}
	return v
}
func (e *Engine) runAuditReport(ctx context.Context, j Job) (ChainReport, error) {
	report := ChainReport{JobID: j.JobID, Mode: ChainModePerRun, Head: j.ChainHead, Entries: []ChainEntry{}, AuditGap: j.AuditGap}
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		var latest Job
		if err := tx.Get(store.Jobs, j.JobID, &latest); err != nil {
			return err
		}
		// Retain the gap warning if its durable marker could not be written.
		latest.AuditGap = latest.AuditGap || j.AuditGap
		j = latest
		report.Head = j.ChainHead
		report.AuditGap = j.AuditGap
		var er error
		report.RunEntries, er = e.runAuditEntries(tx, j.JobID)
		return er
	})
	if err != nil {
		return report, err
	}
	report.Verification = VerifyRunAudit(report.RunEntries, j)
	if !report.Verification.Valid && e.Log != nil {
		e.Log.Warn("runAudit.verify", "jobId", j.JobID, "reason", report.Verification.Reason)
	}
	return report, nil
}
