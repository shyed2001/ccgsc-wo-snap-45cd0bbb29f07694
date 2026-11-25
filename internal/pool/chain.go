// Path: internal/pool/chain.go | Author: Hermes (MiniMax) for the owner | 2026-11-18
// What/why: the per-job SHA-256 audit chain over the accepted results, the periodic
// global Merkle checkpoint over the tips of the job chains and the admin gate of the
// developer option options.tamperEvidence; WWPP-R22.3, R23.3, R26.2, R35.2 (WO T42 F3).
//
// The chain is opt-in per job (WWPP-R26.2: the chain is off by default, the security log
// stays on) and every hash is computed over the canonical JSON of the entry fields with
// a domain separator, so one byte sequence covers exactly one entry.
//
// The verification is independent of the server: it recomputes the entry hashes, the
// links between the entries, the content hashes of the accepted results and the head of
// the chain. A changed result, a changed entry and a removed entry are all detected.
//
// The Merkle checkpoint is a snapshot over the tips of the job chains. Every checkpoint
// hashes the previous one, so the checkpoints are a chain of their own and a rewritten
// job chain is caught by the checkpoint that covered it (WWPP-R22.3).
package pool

import (
	"bytes"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// The domain separators of the audit chain and of the Merkle tree (WWPP-R39.1: every
// hash covers exactly one byte sequence).
const (
	chainDomain      = "ccgsc.pools.audit-chain.v1"
	merkleLeafDomain = "ccgsc.pools.merkle-leaf.v1"
	merkleNodeDomain = "ccgsc.pools.merkle-node.v1"
	merkleCkptDomain = "ccgsc.pools.merkle-checkpoint.v1"
)

// The keys of the audit bucket. The chain state, the chain entries, the Merkle state and
// the Merkle checkpoints share the bucket with distinct prefixes.
const (
	chainStatePrefix  = "chainstate/"
	chainEntryPrefix  = "chain/"
	merkleStateKey    = "merklestate"
	merkleEntryPrefix = "merkle/"
)

// DefaultMerkleInterval is the period of a global Merkle checkpoint.
const DefaultMerkleInterval = time.Minute

// chainOp is the name of the light metric of the audit chain (WWPP-R37.2).
const chainOp = "chain"

// ChainMode is the effective tamper evidence mode of a job.
type ChainMode string

const (
	// ChainModeNone: no per-job chain. The security log stays on.
	ChainModeNone ChainMode = "none"
	// ChainModeSecurityLog: the security log only, no per-job chain.
	ChainModeSecurityLog ChainMode = "security-log"
	// ChainModePerJobChain: a per-job SHA-256 chain over the accepted results.
	ChainModePerJobChain ChainMode = "per-job-chain"
	// ChainModePerRun: developer opt-in per-run module/input/result evidence
	// (F4). Unlike the result-only legacy mode this audit is FAIL OPEN.
	ChainModePerRun ChainMode = "per-run-chain"
	// ChainModeMerkle: the per-job chain plus the global Merkle checkpoints.
	ChainModeMerkle ChainMode = "merkle"
)

// chainModeActive reports whether a mode keeps a per-job hash chain.
func chainModeActive(m ChainMode) bool {
	return m == ChainModePerJobChain || m == ChainModeMerkle
}

// ChainAuthority is the authority rule of the chain mode (WWPP-R23.3).
const (
	// ChainAuthorityA: the admin decides the global Merkle on or off and the developer
	// may only raise the mode of its job, never lower it.
	ChainAuthorityA = "a"
	// ChainAuthorityB: the developer chooses freely inside the allowance of the admin.
	ChainAuthorityB = "b"
)

// ChainConfig holds the admin switches of the audit chain.
//
// ChainEnabled is the global switch of the per-job hash chain and MerkleEnabled the
// global switch of the periodic Merkle checkpoints. Both are off by default
// (WWPP-R26.2: the chain is opt-in, the security log only). ChainAuthority decides
// whether the developer may only raise the mode (a) or chooses freely inside
// AllowedModes (b). MerkleInterval is the period of a checkpoint (WWPP-R22.3).
type ChainConfig struct {
	ChainEnabled   bool          `json:"chainEnabled"`
	MerkleEnabled  bool          `json:"merkleEnabled"`
	ChainAuthority string        `json:"chainAuthority"`
	AllowedModes   []ChainMode   `json:"allowedModes"`
	MerkleInterval time.Duration `json:"merkleInterval"`
}

// DefaultChainConfig returns the defaults of WWPP-R26.2 and WWPP-R22.3: the chain and
// the Merkle checkpoints are off, the authority rule is (a) and the allowance carries
// every mode.
func DefaultChainConfig() ChainConfig {
	return ChainConfig{
		ChainEnabled:   false,
		MerkleEnabled:  false,
		ChainAuthority: ChainAuthorityA,
		AllowedModes:   []ChainMode{ChainModeNone, ChainModeSecurityLog, ChainModePerJobChain, ChainModePerRun, ChainModeMerkle},
		MerkleInterval: DefaultMerkleInterval,
	}
}

// normalized returns the config with the empty fields filled by the defaults.
func (c ChainConfig) normalized() ChainConfig {
	d := DefaultChainConfig()
	if c.ChainAuthority == "" {
		c.ChainAuthority = d.ChainAuthority
	}
	if c.ChainAuthority != ChainAuthorityA && c.ChainAuthority != ChainAuthorityB {
		c.ChainAuthority = d.ChainAuthority
	}
	if len(c.AllowedModes) == 0 {
		c.AllowedModes = d.AllowedModes
	}
	if c.MerkleInterval <= 0 {
		c.MerkleInterval = d.MerkleInterval
	}
	return c
}

// ChainConfig returns the active admin configuration of the audit chain.
func (e *Engine) ChainConfig() ChainConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c := e.chain
	c.AllowedModes = append([]ChainMode(nil), c.AllowedModes...)
	return c
}

// SetChainConfig applies the admin configuration of the audit chain. An unknown
// authority rule is refused, so a typo never silently changes the policy.
func (e *Engine) SetChainConfig(c ChainConfig) error {
	if c.ChainAuthority != "" && c.ChainAuthority != ChainAuthorityA && c.ChainAuthority != ChainAuthorityB {
		return fmt.Errorf("%w: unknown chain authority %q", ErrInvalid, c.ChainAuthority)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.chain = c.normalized()
	return nil
}

// TamperEvidenceOption is the developer request of the tamper evidence mode
// (WWPP-R35.2). The mode is none, security-log, per-job-chain or merkle.
type TamperEvidenceOption struct {
	Mode        string `json:"mode,omitempty"`
	ChainPerJob bool   `json:"chainPerJob,omitempty"`
}

// jobOptions is the parsed developer options of a job. Only the tamper evidence option
// is handled by this work order; every other option stays unsupported until the feature
// that owns it is built, so no request field is silently ignored (WO T42 common rules).
type jobOptions struct {
	TaskTimeoutMS        int64                 `json:"taskTimeoutMs,omitempty"`
	MaxTaskBytes         int64                 `json:"maxTaskBytes,omitempty"`
	Tags                 []string              `json:"tags,omitempty"`
	ToleranceCompare     bool                  `json:"toleranceCompare,omitempty"`
	ComparatorArtifactID string                `json:"comparatorArtifactId,omitempty"`
	TamperEvidence       *TamperEvidenceOption `json:"tamperEvidence,omitempty"`
	UploadChecks         *json.RawMessage      `json:"uploadChecks,omitempty"`
	P2P                  *json.RawMessage      `json:"p2p,omitempty"`
	Dew                  *json.RawMessage      `json:"dew,omitempty"`
}

// applyOptions parses the developer options of a job and resolves the tamper evidence
// mode against the admin policy (WWPP-R24.1, R35.2, R23.3).
func (e *Engine) applyOptions(q *CreateRequest) error {
	if len(bytes.TrimSpace(q.Options)) == 0 {
		// No options at all: the chain stays opt-in (WWPP-R26.2).
		q.chainMode = ChainModeNone
		return nil
	}
	if !json.Valid(q.Options) {
		return ErrInvalid
	}
	trimmed := bytes.TrimSpace(q.Options)
	if trimmed[0] != '{' {
		return ErrInvalid
	}
	// The names of the option fields are read first, so an unknown field is refused
	// with its name instead of being silently ignored (WO T42 common rules).
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(q.Options, &fields); err != nil {
		return ErrInvalid
	}
	known := map[string]bool{
		"taskTimeoutMs": true, "maxTaskBytes": true, "tags": true,
		"toleranceCompare": true, "comparatorArtifactId": true,
		"tamperEvidence": true, "uploadChecks": true, "p2p": true, "dew": true,
	}
	for name := range fields {
		if !known[name] {
			return fmt.Errorf("%w: unknown option field %q", ErrUnsupported, name)
		}
	}
	var opt jobOptions
	dec := json.NewDecoder(bytes.NewReader(q.Options))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&opt); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if opt.TaskTimeoutMS != 0 || opt.MaxTaskBytes != 0 || len(opt.Tags) > 0 || opt.ToleranceCompare ||
		opt.ComparatorArtifactID != "" || opt.UploadChecks != nil || opt.P2P != nil {
		return ErrUnsupported
	}
	if _, err := parseDewOptions(q.Options); err != nil {
		return err
	}
	mode, err := e.resolveTamperEvidence(opt.TamperEvidence)
	if err != nil {
		return err
	}
	q.chainMode = mode
	return nil
}

// resolveTamperEvidence applies the admin authority rule to the developer request
// (WWPP-R23.3, R35.2).
//
// Authority (a): the admin decides the global Merkle on or off and the developer may
// only raise the mode of its job, so a request for a chain is raised to merkle while the
// admin keeps the Merkle on. A request for a chain while the admin has the chain off is
// refused: admin off means unavailable.
//
// Authority (b): the developer chooses freely inside the allowance of the admin. A mode
// outside the allowance is refused. The global switches of the admin still bound the
// availability: no chain means no per-job chain.
func (e *Engine) resolveTamperEvidence(req *TamperEvidenceOption) (ChainMode, error) {
	cfg := e.ChainConfig()
	if req == nil {
		// The developer asked for no chain: the chain stays opt-in (WWPP-R26.2).
		return ChainModeNone, nil
	}
	mode := ChainMode(req.Mode)
	if mode == "" {
		if req.ChainPerJob {
			mode = ChainModePerJobChain
		} else {
			mode = ChainModeSecurityLog
		}
	}
	switch mode {
	case ChainModeNone, ChainModeSecurityLog, ChainModePerJobChain, ChainModePerRun, ChainModeMerkle:
	default:
		return "", fmt.Errorf("%w: unknown tamper evidence mode %q", ErrInvalid, req.Mode)
	}
	// F4 developer-owned per-run evidence is independent of the global server
	// Merkle/legacy result chain. The developer opts in per run; no per-pool
	// chain or platform checkpoint is created by this mode.
	if mode == ChainModePerRun {
		return mode, nil
	}
	if chainModeActive(mode) && !cfg.ChainEnabled {
		return "", fmt.Errorf("%w: the admin disabled the per-job hash chain", ErrForbidden)
	}
	if mode == ChainModeMerkle && !cfg.MerkleEnabled {
		return "", fmt.Errorf("%w: the admin disabled the global Merkle checkpoints", ErrForbidden)
	}
	if cfg.ChainAuthority == ChainAuthorityB {
		if !slices.Contains(cfg.AllowedModes, mode) {
			return "", fmt.Errorf("%w: the tamper evidence mode %q is outside the admin allowance", ErrForbidden, mode)
		}
		return mode, nil
	}
	// Authority (a): the developer may only raise the mode.
	if cfg.MerkleEnabled && chainModeActive(mode) {
		return ChainModeMerkle, nil
	}
	return mode, nil
}

// ChainEntry is one link of the per-job audit chain: the evidence of one accepted
// result. The hash of the entry covers the fields below, so a change of any field
// breaks the chain.
type ChainEntry struct {
	Seq         uint64    `json:"seq"`
	JobID       string    `json:"jobId"`
	TaskID      string    `json:"taskId"`
	ResultIndex int       `json:"resultIndex"`
	ResultSHA   string    `json:"resultSha256"`
	WorkerID    string    `json:"workerId,omitempty"`
	At          time.Time `json:"at"`
	Prev        string    `json:"prev"`
	Hash        string    `json:"hash"`
}

// ChainState is the head of the chain of one job: how many results of the job were
// examined, how many entries the chain has and the hash of the last entry.
type ChainState struct {
	Examined int    `json:"examined"`
	Entries  int    `json:"entries"`
	Head     string `json:"head"`
}

// MerkleLeaf is one tip of a job chain inside a Merkle checkpoint.
type MerkleLeaf struct {
	JobID string `json:"jobId"`
	Tip   string `json:"tip"`
}

// MerkleCheckpoint is one periodic global checkpoint over the tips of the job chains.
// The checkpoint hashes the previous checkpoint, so the checkpoints are a chain of their
// own (WWPP-R22.3).
type MerkleCheckpoint struct {
	Seq      uint64       `json:"seq"`
	At       time.Time    `json:"at"`
	Root     string       `json:"root"`
	PrevRoot string       `json:"prevRoot,omitempty"`
	PrevHash string       `json:"prevHash,omitempty"`
	Hash     string       `json:"hash"`
	JobCount int          `json:"jobCount"`
	Leaves   []MerkleLeaf `json:"leaves"`
}

// MerkleState is the head of the checkpoint chain.
type MerkleState struct {
	Count uint64    `json:"count"`
	At    time.Time `json:"at"`
	Root  string    `json:"root"`
	Hash  string    `json:"hash"`
}

// ChainVerification is the verdict of the verification of one job chain.
type ChainVerification struct {
	JobID    string    `json:"jobId"`
	Valid    bool      `json:"valid"`
	Mode     ChainMode `json:"mode"`
	Entries  int       `json:"entries"`
	Head     string    `json:"head"`
	Reason   string    `json:"reason,omitempty"`
	BrokenAt int       `json:"brokenAt,omitempty"`
}

// MerkleVerification is the verdict of the verification of one Merkle checkpoint.
type MerkleVerification struct {
	Seq        uint64 `json:"seq"`
	Valid      bool   `json:"valid"`
	Root       string `json:"root"`
	Recomputed string `json:"recomputedRoot"`
	JobCount   int    `json:"jobCount"`
	Reason     string `json:"reason,omitempty"`
}

// ChainReport is the answer of the chain route of a job.
type ChainReport struct {
	JobID        string            `json:"jobId"`
	Mode         ChainMode         `json:"mode"`
	Head         string            `json:"head"`
	Entries      []ChainEntry      `json:"entries"`
	RunEntries   []RunAuditEntry   `json:"runEntries,omitempty"` // F4 independent per-run evidence
	AuditGap     bool              `json:"auditGap,omitempty"`
	Verification ChainVerification `json:"verification"`
}

// MerkleReport is the answer of the Merkle route.
type MerkleReport struct {
	Checkpoints   []MerkleCheckpoint   `json:"checkpoints"`
	Verifications []MerkleVerification `json:"verifications"`
	LatestRoot    string               `json:"latestRoot,omitempty"`
}

// outputHash returns the hash of the output bytes as the store keeps them. The store
// compacts a raw message on the way in, so the hash is taken over the compacted bytes:
// the chain and the verification then compare the same bytes (WWPP-R37.1).
func outputHash(out json.RawMessage) (string, error) {
	if len(out) == 0 {
		return verify.SHA256(nil), nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return verify.SHA256(b), nil
}

// chainEntryHash computes the hash of one chain entry over the canonical JSON of its
// fields with a domain separator.
func chainEntryHash(e ChainEntry) (string, error) {
	payload := struct {
		Seq         uint64    `json:"seq"`
		JobID       string    `json:"jobId"`
		TaskID      string    `json:"taskId"`
		ResultIndex int       `json:"resultIndex"`
		ResultSHA   string    `json:"resultSha256"`
		WorkerID    string    `json:"workerId"`
		At          time.Time `json:"at"`
		Prev        string    `json:"prev"`
	}{e.Seq, e.JobID, e.TaskID, e.ResultIndex, e.ResultSHA, e.WorkerID, e.At.UTC(), e.Prev}
	b, err := verify.CanonicalJSON(payload)
	if err != nil {
		return "", err
	}
	return verify.SHA256(append([]byte(chainDomain+"\x00"), b...)), nil
}

// checkpointHash computes the hash of one Merkle checkpoint. It covers the previous
// checkpoint, so the checkpoints form a chain.
func checkpointHash(c MerkleCheckpoint) (string, error) {
	payload := struct {
		Seq      uint64    `json:"seq"`
		Root     string    `json:"root"`
		PrevRoot string    `json:"prevRoot"`
		PrevHash string    `json:"prevHash"`
		JobCount int       `json:"jobCount"`
		At       time.Time `json:"at"`
	}{c.Seq, c.Root, c.PrevRoot, c.PrevHash, c.JobCount, c.At.UTC()}
	b, err := verify.CanonicalJSON(payload)
	if err != nil {
		return "", err
	}
	return verify.SHA256(append([]byte(merkleCkptDomain+"\x00"), b...)), nil
}

// merkleRoot computes the Merkle root over the leaves. The leaves are sorted by job id
// by the caller, so the root depends on the set of jobs only and not on the order of
// the listing. An odd node is promoted to the next level unchanged.
func merkleRoot(leaves []MerkleLeaf) string {
	if len(leaves) == 0 {
		return verify.SHA256([]byte(merkleLeafDomain))
	}
	level := make([][]byte, 0, len(leaves))
	for _, l := range leaves {
		h := verify.SHA256Bytes([]byte(merkleLeafDomain + "\x00" + l.Tip))
		level = append(level, h[:])
	}
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 < len(level) {
				pair := append(append([]byte(nil), level[i]...), level[i+1]...)
				h := verify.SHA256Bytes(append([]byte(merkleNodeDomain+"\x00"), pair...))
				next = append(next, h[:])
				continue
			}
			next = append(next, level[i])
		}
		level = next
	}
	return hex.EncodeToString(level[0])
}

// chainStateKey returns the key of the chain state of a job.
func chainStateKey(jobID string) string { return chainStatePrefix + jobID }

// chainEntryKey returns the key of one chain entry of a job.
func chainEntryKey(jobID string, seq uint64) string {
	return fmt.Sprintf("%s%s/%010d", chainEntryPrefix, jobID, seq)
}

// merkleEntryKey returns the key of one Merkle checkpoint.
func merkleEntryKey(seq uint64) string {
	return fmt.Sprintf("%s%010d", merkleEntryPrefix, seq)
}

// chainState reads the chain state of a job. A job without a chain has no state.
func (e *Engine) chainState(tx store.Reader, jobID string) (ChainState, error) {
	var st ChainState
	err := tx.Get(store.Audit, chainStateKey(jobID), &st)
	if errors.Is(err, store.ErrNotFound) {
		return ChainState{}, nil
	}
	if err != nil {
		return st, err
	}
	return st, nil
}

// chainEntries reads the chain entries of a job in the order of the chain.
func (e *Engine) chainEntries(r store.Reader, jobID string) ([]ChainEntry, error) {
	rows, err := r.List(store.Audit)
	if err != nil {
		return nil, err
	}
	prefix := chainEntryPrefix + jobID + "/"
	out := []ChainEntry{}
	for _, row := range rows {
		if !strings.HasPrefix(row.Key, prefix) {
			continue
		}
		var entry ChainEntry
		if err := json.Unmarshal(row.Value, &entry); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Seq < out[k].Seq })
	return out, nil
}

// chainSync appends the accepted results of a job that are not in its chain yet. The
// chain is written in the same transaction as the job, so the head of the chain and the
// job record never disagree.
func (e *Engine) chainSync(tx store.Tx, j *Job) error {
	if !chainModeActive(j.ChainMode) {
		return nil
	}
	start := e.Now()
	st, err := e.chainState(tx, j.JobID)
	if err != nil {
		return err
	}
	if st.Examined > len(j.Results) {
		// The result list of the job shrank: the chain does not follow it any more, so
		// the chain is closed and the job is flagged by the verification.
		st.Examined = len(j.Results)
	}
	newEntries := []ChainEntry{}
	for i := st.Examined; i < len(j.Results); i++ {
		r := j.Results[i]
		if r.Status != "done" {
			// Only an accepted result is evidence.
			continue
		}
		content, err := outputHash(r.Output)
		if err != nil {
			return err
		}
		entry := ChainEntry{
			Seq:         uint64(st.Entries + 1),
			JobID:       j.JobID,
			TaskID:      r.TaskID,
			ResultIndex: i,
			ResultSHA:   content,
			WorkerID:    r.WorkerID,
			At:          r.At.UTC(),
			Prev:        st.Head,
		}
		h, err := chainEntryHash(entry)
		if err != nil {
			return err
		}
		entry.Hash = h
		newEntries = append(newEntries, entry)
		st.Entries++
		st.Head = h
	}
	st.Examined = len(j.Results)
	if len(newEntries) == 0 && st.Head == j.ChainHead {
		verify.DefaultMetrics().RecordSince(chainOp, start, true)
		return nil
	}
	for _, entry := range newEntries {
		if err := tx.Put(store.Audit, chainEntryKey(j.JobID, entry.Seq), entry); err != nil {
			return err
		}
	}
	if err := tx.Put(store.Audit, chainStateKey(j.JobID), st); err != nil {
		return err
	}
	j.ChainHead = st.Head
	j.ChainEntries = st.Entries
	verify.DefaultMetrics().RecordSince(chainOp, start, true)
	return nil
}

// VerifyChain recomputes the chain of a job and returns the verdict. The verification
// uses the stored entries, the stored results of the job and the head of the job
// record, so a changed result, a changed entry and a removed entry are detected.
func VerifyChain(entries []ChainEntry, results []Result, head string) ChainVerification {
	v := ChainVerification{Valid: true, Entries: len(entries)}
	if len(entries) > 0 {
		v.Head = entries[len(entries)-1].Hash
	}
	for i, entry := range entries {
		if entry.Seq != uint64(i+1) {
			v.Valid = false
			v.Reason = "the sequence numbers of the chain are not contiguous"
			v.BrokenAt = i + 1
			return v
		}
		h, err := chainEntryHash(entry)
		if err != nil {
			v.Valid = false
			v.Reason = "the hash of an entry could not be computed"
			v.BrokenAt = i + 1
			return v
		}
		if h != entry.Hash {
			v.Valid = false
			v.Reason = "the hash of the entry does not match its fields"
			v.BrokenAt = i + 1
			return v
		}
		if i == 0 {
			if entry.Prev != "" {
				v.Valid = false
				v.Reason = "the first entry of the chain has a predecessor"
				v.BrokenAt = 1
				return v
			}
		} else if entry.Prev != entries[i-1].Hash {
			v.Valid = false
			v.Reason = "the link to the previous entry is broken"
			v.BrokenAt = i + 1
			return v
		}
		if entry.ResultIndex < 0 || entry.ResultIndex >= len(results) {
			v.Valid = false
			v.Reason = "the result of the entry is missing"
			v.BrokenAt = i + 1
			return v
		}
		r := results[entry.ResultIndex]
		if r.Status != "done" {
			v.Valid = false
			v.Reason = "the result of the entry is not an accepted result"
			v.BrokenAt = i + 1
			return v
		}
		if entry.JobID != r.JobID || entry.TaskID != r.TaskID || entry.WorkerID != r.WorkerID {
			v.Valid = false
			v.Reason = "the entry identity does not match the accepted result"
			v.BrokenAt = i + 1
			return v
		}
		content, err := outputHash(r.Output)
		if err != nil {
			v.Valid = false
			v.Reason = "the hash of the result could not be computed"
			v.BrokenAt = i + 1
			return v
		}
		if content != entry.ResultSHA {
			v.Valid = false
			v.Reason = "the accepted result was changed after it was chained"
			v.BrokenAt = i + 1
			return v
		}
	}
	if head != "" && v.Head != head {
		v.Valid = false
		v.Reason = "the head of the chain does not match the job record"
		return v
	}
	if len(entries) == 0 && head != "" {
		v.Valid = false
		v.Reason = "the chain of the job is empty but the job record has a head"
		return v
	}
	return v
}

// JobChain returns the chain of a job with the verdict of the verification. The job
// must belong to the owner and to the pool.
func (e *Engine) JobChain(ctx context.Context, p, owner, jobID string) (ChainReport, error) {
	var report ChainReport
	j, err := e.Get(ctx, p, owner, jobID)
	if err != nil {
		return report, err
	}
	report.JobID = j.JobID
	report.Mode = j.ChainMode
	report.Head = j.ChainHead
	if j.ChainMode == ChainModePerRun {
		return e.runAuditReport(ctx, j)
	}
	entries := []ChainEntry{}
	err = e.Store.View(ctx, func(r store.Reader) error {
		var er error
		entries, er = e.chainEntries(r, j.JobID)
		return er
	})
	if err != nil {
		return report, err
	}
	report.Entries = entries
	report.Verification = VerifyChain(entries, j.Results, j.ChainHead)
	report.Verification.JobID = j.JobID
	report.Verification.Mode = j.ChainMode
	if !report.Verification.Valid {
		e.Log.Warn("chain.verify", "jobId", j.JobID, "reason", report.Verification.Reason)
	}
	return report, nil
}

// maybeCheckpoint writes a Merkle checkpoint when the period is over. It runs inside the
// transaction of the sweep, so the checkpoint and the job records are consistent.
func (e *Engine) maybeCheckpoint(tx store.Tx) error {
	cfg := e.ChainConfig()
	if !cfg.MerkleEnabled {
		return nil
	}
	var st MerkleState
	if err := tx.Get(store.Audit, merkleStateKey, &st); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if st.Count > 0 && e.Now().Sub(st.At) < cfg.MerkleInterval {
		return nil
	}
	_, err := e.writeCheckpoint(tx, st)
	return err
}

// writeCheckpoint builds and writes one Merkle checkpoint over the tips of the job
// chains that are stored in the same transaction.
func (e *Engine) writeCheckpoint(tx store.Tx, st MerkleState) (MerkleCheckpoint, error) {
	rows, err := tx.List(store.Jobs)
	if err != nil {
		return MerkleCheckpoint{}, err
	}
	leaves := []MerkleLeaf{}
	for _, row := range rows {
		var j Job
		if err := json.Unmarshal(row.Value, &j); err != nil {
			return MerkleCheckpoint{}, err
		}
		if j.ChainHead == "" || !chainModeActive(j.ChainMode) {
			continue
		}
		leaves = append(leaves, MerkleLeaf{JobID: j.JobID, Tip: j.ChainHead})
	}
	sort.Slice(leaves, func(i, k int) bool { return leaves[i].JobID < leaves[k].JobID })
	cp := MerkleCheckpoint{
		Seq:      st.Count + 1,
		At:       e.Now().UTC(),
		PrevRoot: st.Root,
		PrevHash: st.Hash,
		JobCount: len(leaves),
		Leaves:   leaves,
	}
	cp.Root = merkleRoot(leaves)
	h, err := checkpointHash(cp)
	if err != nil {
		return cp, err
	}
	cp.Hash = h
	if err := tx.Put(store.Audit, merkleEntryKey(cp.Seq), cp); err != nil {
		return cp, err
	}
	st.Count = cp.Seq
	st.At = cp.At
	st.Root = cp.Root
	st.Hash = cp.Hash
	if err := tx.Put(store.Audit, merkleStateKey, st); err != nil {
		return cp, err
	}
	e.Log.Info("merkle.checkpoint", "seq", cp.Seq, "root", cp.Root, "jobs", cp.JobCount)
	return cp, nil
}

// ForceCheckpoint writes a Merkle checkpoint right now, outside the period.
func (e *Engine) ForceCheckpoint(ctx context.Context) (MerkleCheckpoint, error) {
	var cp MerkleCheckpoint
	if !e.ChainConfig().MerkleEnabled {
		return cp, ErrForbidden
	}
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		var st MerkleState
		if er := tx.Get(store.Audit, merkleStateKey, &st); er != nil && !errors.Is(er, store.ErrNotFound) {
			return er
		}
		var er error
		cp, er = e.writeCheckpoint(tx, st)
		return er
	})
	return cp, err
}

// MerkleCheckpoints returns the newest checkpoints with the verdict of every one of
// them. A limit of 0 returns every checkpoint.
func (e *Engine) MerkleCheckpoints(ctx context.Context, limit int) (MerkleReport, error) {
	var report MerkleReport
	report.Checkpoints = []MerkleCheckpoint{}
	report.Verifications = []MerkleVerification{}
	err := e.Store.View(ctx, func(r store.Reader) error {
		rows, er := r.List(store.Audit)
		if er != nil {
			return er
		}
		for _, row := range rows {
			if !strings.HasPrefix(row.Key, merkleEntryPrefix) {
				continue
			}
			var cp MerkleCheckpoint
			if er := json.Unmarshal(row.Value, &cp); er != nil {
				return er
			}
			report.Checkpoints = append(report.Checkpoints, cp)
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	sort.Slice(report.Checkpoints, func(i, k int) bool { return report.Checkpoints[i].Seq > report.Checkpoints[k].Seq })
	if limit > 0 && len(report.Checkpoints) > limit {
		report.Checkpoints = report.Checkpoints[:limit]
	}
	for i, cp := range report.Checkpoints {
		v, er := e.VerifyMerkleCheckpoint(ctx, cp.Seq)
		if er != nil {
			return report, er
		}
		report.Verifications = append(report.Verifications, v)
		if i == 0 && cp.Root != "" {
			report.LatestRoot = cp.Root
		}
	}
	return report, nil
}

// chainTipsByJob returns the hashes of the chain entries of every job, read in one
// pass over the audit bucket. The verification of a checkpoint needs the tip of every
// job of the checkpoint, so one listing serves all of them.
func (e *Engine) chainTipsByJob(r store.Reader) (map[string]map[string]bool, error) {
	rows, err := r.List(store.Audit)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, row := range rows {
		if !strings.HasPrefix(row.Key, chainEntryPrefix) {
			continue
		}
		var entry ChainEntry
		if err := json.Unmarshal(row.Value, &entry); err != nil {
			return nil, err
		}
		if out[entry.JobID] == nil {
			out[entry.JobID] = map[string]bool{}
		}
		out[entry.JobID][entry.Hash] = true
	}
	return out, nil
}

// VerifyMerkleCheckpoint recomputes one checkpoint and returns the verdict. The root is
// recomputed from the leaves, the hash of the checkpoint is recomputed and the link to
// the previous checkpoint is checked. Each historical leaf must remain a prefix tip of
// the current append-only chain; later legitimate entries do not invalidate it.
func (e *Engine) VerifyMerkleCheckpoint(ctx context.Context, seq uint64) (MerkleVerification, error) {
	var v MerkleVerification
	err := e.Store.View(ctx, func(r store.Reader) error {
		var cp MerkleCheckpoint
		if er := r.Get(store.Audit, merkleEntryKey(seq), &cp); er != nil {
			return er
		}
		v.Seq = cp.Seq
		v.Root = cp.Root
		v.JobCount = cp.JobCount
		v.Recomputed = merkleRoot(cp.Leaves)
		if v.Recomputed != cp.Root {
			v.Reason = "the root does not match the leaves of the checkpoint"
			return nil
		}
		h, er := checkpointHash(cp)
		if er != nil {
			return er
		}
		if h != cp.Hash {
			v.Reason = "the hash of the checkpoint does not match its fields"
			return nil
		}
		for currentSeq := cp.Seq; currentSeq > 1; currentSeq-- {
			var prev MerkleCheckpoint
			if er := r.Get(store.Audit, merkleEntryKey(currentSeq-1), &prev); er == nil {
				if prev.Seq != currentSeq-1 {
					v.Reason = "a checkpoint has an unexpected sequence number"
					return nil
				}
				if currentSeq == cp.Seq && (cp.PrevHash != prev.Hash || cp.PrevRoot != prev.Root) {
					v.Reason = "the link to the previous checkpoint is broken"
					return nil
				}
				var current MerkleCheckpoint
				if er := r.Get(store.Audit, merkleEntryKey(currentSeq), &current); er != nil {
					return er
				}
				if current.PrevHash != prev.Hash || current.PrevRoot != prev.Root {
					v.Reason = "the checkpoint ancestry is broken"
					return nil
				}
			} else if errors.Is(er, store.ErrNotFound) {
				if currentSeq == cp.Seq {
					v.Reason = "the previous checkpoint is missing"
				} else {
					v.Reason = "a checkpoint ancestor is missing"
				}
				return nil
			} else {
				return er
			}
		}
		for _, leaf := range cp.Leaves {
			var job Job
			if er := r.Get(store.Jobs, leaf.JobID, &job); er != nil {
				if errors.Is(er, store.ErrNotFound) {
					v.Reason = "a job of the checkpoint is gone"
					return nil
				}
				return er
			}
			entries, er := e.chainEntries(r, leaf.JobID)
			if er != nil {
				return er
			}
			current := VerifyChain(entries, job.Results, job.ChainHead)
			if !current.Valid {
				v.Reason = "the chain of a job of the checkpoint is invalid: " + current.Reason
				return nil
			}
			found := false
			for _, entry := range entries {
				if entry.Hash == leaf.Tip {
					found = true
					break
				}
			}
			if !found {
				v.Reason = "the chain of a job of the checkpoint does not contain its historical tip"
				return nil
			}
		}
		v.Valid = true
		return nil
	})
	return v, err
}
