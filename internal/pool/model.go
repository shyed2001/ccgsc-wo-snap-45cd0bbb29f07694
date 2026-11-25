// Path: internal/pool/model.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: shared lowerCamelCase job/session/worker wire contracts; WWPP-R26.4, R32.2, R32.9.
package pool

import (
	"ccgsc/poolscore/internal/verify"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("state or fencing conflict")
	// ErrRunnerStale refuses leases to a tab whose runner does not trust the
	// live task key (T114); the page must reload.
	ErrRunnerStale    = errors.New("runner does not trust the live task key: reload the page")
	ErrForbidden      = errors.New("owner or pool mismatch")
	ErrEmpty          = errors.New("no eligible task")
	ErrLeaseContended = errors.New("lease contention")
	ErrUnsupported    = errors.New("feature is reserved for a follow-up work order")
)

const (
	Thread  = "tp"
	Process = "pp"
)

type TaskSpec struct {
	TaskID    string          `json:"taskId"`
	Input     json.RawMessage `json:"input,omitempty"`
	TimeoutMS int             `json:"timeoutMs,omitempty"`
}
type SplitSpec struct {
	ArtifactID string `json:"artifactId"`
	Count      int    `json:"count"`
	Format     string `json:"format,omitempty"`
	Chunksize  int    `json:"chunksize,omitempty"`
}
type CreateRequest struct {
	policyPresent     map[string]bool
	AppID             string `json:"appId,omitempty"` // F9: optional approved Process application
	chainMode         ChainMode
	ModuleArtifactIDs []string        `json:"moduleArtifactIds"`
	ModuleSHA256      string          `json:"moduleSha256,omitempty"`
	Tasks             []TaskSpec      `json:"tasks,omitempty"`
	Split             *SplitSpec      `json:"split,omitempty"`
	BatchSize         int             `json:"batchSize,omitempty"`
	Chunksize         map[string]int  `json:"chunksize,omitempty"`
	Needs             string          `json:"needs,omitempty"`
	Policy            string          `json:"policy,omitempty"`
	Threshold         float64         `json:"threshold,omitempty"`
	MaxAttempts       int             `json:"maxAttempts,omitempty"`
	Priority          string          `json:"priority,omitempty"`
	RerunPercent      *float64        `json:"rerunPercent,omitempty"`
	GroupBy           json.RawMessage `json:"groupBy,omitempty"`
	Pin               json.RawMessage `json:"pin,omitempty"`
	Options           json.RawMessage `json:"options,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	Webhook           *WebhookConfig  `json:"webhook,omitempty"`
}
type WebhookConfig struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret"`
	Events []string `json:"events,omitempty"`
}
type Progress struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
}
type Warning struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}
type Job struct {
	AppID         string     `json:"appId,omitempty"` // F9: approved process application, empty retains legacy pool
	JobID         string     `json:"jobId"`
	ServerID      string     `json:"serverId"`
	PoolID        string     `json:"poolId"`
	Owner         string     `json:"owner"`
	State         string     `json:"state"`
	CreatedAt     time.Time  `json:"createdAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Needs         string     `json:"needs"`
	Policy        string     `json:"policy"`
	MaxAttempts   int        `json:"maxAttempts"`
	Priority      string     `json:"priority"`
	Progress      Progress   `json:"progress"`
	Warnings      []Warning  `json:"warnings"`
	SessionID     string     `json:"sessionId,omitempty"`
	ConfigVersion string     `json:"configVersion"`
	// WorkloadKind is the platform ABI of the entry module (D-W10-10), read from
	// the artifact record at job creation because the claim path may not open a
	// second store read. It is signed into every task envelope; empty means the
	// W10c js-module default.
	WorkloadKind string        `json:"workloadKind,omitempty"`
	Request      CreateRequest `json:"request"`
	Tasks        []Task        `json:"tasks"`
	Results      []Result      `json:"results"`
	ChainMode    ChainMode     `json:"chainMode,omitempty"`
	ChainHead    string        `json:"chainHead,omitempty"`
	ChainEntries int           `json:"chainEntries,omitempty"`
	AuditGap     bool          `json:"auditGap,omitempty"` // F4 per-run fail-open audit gap

	ChosenPolicy *PolicySelection `json:"chosenPolicy,omitempty"`
}
type Task struct {
	TaskSpec
	State          string `json:"state"`
	Attempts       int    `json:"attempts"`
	CanaryAttempts int    `json:"canaryAttempts,omitempty"`
	// Refunds counts the stale-runner refunds this task already granted (W9 F1):
	// at most one, so a second stale report on the same task is a failed try.
	Refunds     int    `json:"refunds,omitempty"`
	Generation  uint64 `json:"generation"`
	Lease       *Lease `json:"lease,omitempty"`
	Quarantined bool   `json:"quarantined"`
	// Trust state of the task (WWPP-R35.1, R37.1).
	Pending            *PendingResult `json:"pending,omitempty"`      // first result held for a re-run
	RerunExclude       []string       `json:"rerunExclude,omitempty"` // workers that already ran this task
	RerunExcludeOwners []string       `json:"rerunExcludeOwners,omitempty"`
	FirstSHA256        string         `json:"firstSha256,omitempty"` // hash of the first result
	CanaryID           string         `json:"canaryId,omitempty"`    // canary injected with this task
	CanaryTaskID       string         `json:"canaryTaskId,omitempty"`
	CanaryEvidenceID   string         `json:"canaryEvidenceId,omitempty"`
	CanaryWorkerID     string         `json:"canaryWorkerId,omitempty"`
	CanaryLeaseToken   string         `json:"canaryLeaseToken,omitempty"`
	CanaryGeneration   uint64         `json:"canaryGeneration,omitempty"`
	CanaryState        string         `json:"canaryState,omitempty"`
	// DewReceipt records that the lease of this task carried a signed dew receipt
	// at claim time (R11 Part E). A submission must present that receipt; a lease
	// claimed before dew default ON carries none, and the addendum keeps it
	// accepted until its own TTL instead of rejecting the result.
	DewReceipt bool `json:"dewReceipt,omitempty"`
	// Dew is the server-side state of the per-task receipt of the CURRENT lease
	// (F12, G-F12-2). While it is active (now before Deadline, run not ended) a
	// lease that lost its heartbeat is NOT re-assigned (G-F12-5).
	Dew            *DewLease          `json:"dew,omitempty"`
	EnvelopeDigest string             `json:"envelopeDigest,omitempty"`
	EnvelopeUsed   bool               `json:"envelopeUsed,omitempty"`
	Flagged        bool               `json:"flagged,omitempty"` // tamper evidence: needs review
	FlagReason     string             `json:"flagReason,omitempty"`
	ExpiredLease   *LateLeaseEvidence `json:"expiredLease,omitempty"`
	// Redundant is the 2-of-N replica state while the task waits for quorum
	// (W10C row 1). A task in this state is owned by the verification measure.
	Redundant *RedundantState `json:"redundant,omitempty"`
	// DeferredRecompute holds the submission of a sampled task whose recompute
	// verdict was unavailable at submit time and whose pool defers instead of
	// skipping (G-R17-1, WO R18). The engine retries the recompute in the
	// background and settles the task with the verdict; the bounded retries and
	// the sweep fall back to skip.
	DeferredRecompute *DeferredRecompute `json:"deferredRecompute,omitempty"`
}

// StatePendingVerification is the task state while a held redundant result waits
// for its replica(s) or for the verification deadline (W10C row 1).
const StatePendingVerification = "pending_verification"

// RedundantState is the held-result state of a redundant 2-of-N verification.
type RedundantState struct {
	// Target is the number of results needed: 2 for the quorum, 3 after a
	// mismatch started the tie-break.
	Target int `json:"target"`
	// Phase is "replica" while the second result is awaited and "tie-break"
	// after a mismatch.
	Phase string `json:"phase"`
	// Held contains the results already accepted for comparison. The first
	// entry is the original result.
	Held []HeldResult `json:"held,omitempty"`
	// Deadline ends the wait: the first result is accepted without quorum.
	Deadline time.Time `json:"deadline"`
}

// HeldResult is one result held by the redundant verification, together with the
// identity of the tab that produced it (Sybil guard: TabID, HostID, IP /24).
type HeldResult struct {
	PendingResult
	HostID string `json:"hostId,omitempty"`
	IP24   string `json:"ip24,omitempty"`
	// R11-G8: the GPU metrics of THIS submission. A quorum that accepts a held
	// replica rebuilds the result from these bytes, so the measurements must
	// travel with them; reading them from the last submission lost the accepted
	// replica's evidence.
	GPU *GPUResultMetrics `json:"gpu,omitempty"`
}

// DewLease records the receipt issued with one lease. It holds no bearer
// secret: the lease token is kept only as a SHA-256. The claim transaction
// writes it next to DewReceipt so the re-assignment fence survives restarts.
type DewLease struct {
	ReceiptID        string    `json:"receiptId"`
	WorkerID         string    `json:"workerId"`
	ContributorOwner string    `json:"contributorOwner"`
	Generation       uint64    `json:"generation"`
	LeaseTokenSHA256 string    `json:"leaseTokenSha256"`
	IssuedAt         time.Time `json:"issuedAt"`
	Deadline         time.Time `json:"deadline"`
}

type LateLeaseEvidence struct {
	Lease            Lease     `json:"lease"`
	ContributorKeyID string    `json:"contributorKeyId"`
	EnvelopeDigest   string    `json:"envelopeDigest"`
	ExpiredAt        time.Time `json:"expiredAt"`
	Consumed         bool      `json:"consumed"`
}

// PendingResult is the first result of a task that waits for the re-run verdict
// (WWPP-R35.1: the server compares exact hashes only, it never runs developer code).
type PendingResult struct {
	WorkerID string          `json:"workerId"`
	SHA256   string          `json:"sha256"`
	Output   json.RawMessage `json:"output"`
	// Bytes is the base64 form of the exact submitted bytes. The JSON round trip
	// compacts a raw message, so the comparison of the re-run needs the original
	// bytes, not the stored ones.
	Bytes    string    `json:"bytes,omitempty"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
}

// DeferredRecompute is the held submission of a task whose sampled recompute
// could not run at submit time (G-R17-1, WO R18). The engine retries the
// recompute in the background against these exact bytes and settles the task
// with the verdict; the bounded retries and the sweep fall back to skip.
type DeferredRecompute struct {
	HeldResult
	// Retries counts the background recompute attempts already spent.
	Retries int `json:"retries,omitempty"`
	// At is when the result was held; the sweep accepts it after the re-run
	// window.
	At time.Time `json:"at,omitempty"`
}

// exactBytes returns the exact submitted bytes of a pending result.
func (p *PendingResult) exactBytes() []byte {
	if p == nil {
		return nil
	}
	if p.Bytes != "" {
		if b, e := base64.StdEncoding.DecodeString(p.Bytes); e == nil {
			return b
		}
		return nil
	}
	return []byte(p.Output)
}

func (p *PendingResult) exactBytesValid() bool {
	return p == nil || p.Bytes == "" || func() bool {
		_, err := base64.StdEncoding.DecodeString(p.Bytes)
		return err == nil
	}()
}

type Lease struct {
	WorkerSlotID string    `json:"workerSlotId,omitempty"` // F10: one individual Web Worker within the contributor tab
	JobID        string    `json:"jobId"`
	TaskID       string    `json:"taskId"`
	WorkerID     string    `json:"workerId"`
	SessionID    string    `json:"sessionId,omitempty"`
	Token        string    `json:"token"`
	Generation   uint64    `json:"generation"`
	Attempt      int       `json:"attempt"`
	StartedAt    time.Time `json:"startedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	GPU          bool      `json:"gpu,omitempty"`
}
type Assignment struct {
	Egress       *AppEgressGrant      `json:"egress,omitempty"`
	InputJSON    string               `json:"inputJSON,omitempty"`
	LeaseReceipt *verify.LeaseReceipt `json:"leaseReceipt,omitempty"`
	// Batch contains all members (including the first) when batchSize > 1.
	// Top-level fields mirror the first member; submit each member separately.
	Batch             []Assignment    `json:"batch,omitempty"`
	Lease             Lease           `json:"lease"`
	Task              TaskSpec        `json:"task"`
	ModuleArtifactIDs []string        `json:"moduleArtifactIds"`
	Envelope          verify.Envelope `json:"envelope"`
	// TaskEnvelope is the signed task envelope of WWPP-R37.1: Ed25519 over the
	// canonical JSON of {jobId,taskId,artifactSha256,inputSha256,issuedAt,expiresAt,
	// keyId} with a domain separator, verifiable against the key ring.
	TaskEnvelope verify.SignedTaskEnvelope `json:"taskEnvelope"`
}
type Result struct {
	GPU      *GPUResultMetrics `json:"gpu,omitempty"`
	JobID    string            `json:"jobId"`
	TaskID   string            `json:"taskId"`
	Status   string            `json:"status"`
	Attempts int               `json:"attempts"`
	Output   json.RawMessage   `json:"output,omitempty"`
	SHA256   string            `json:"sha256,omitempty"`
	At       time.Time         `json:"at"`
	Error    string            `json:"error,omitempty"`
	// Trust evidence of the result (WWPP-R37.1).
	WorkerID       string     `json:"workerId,omitempty"`
	Verified       bool       `json:"verified,omitempty"`
	HashOK         bool       `json:"hashOk,omitempty"`
	TrustComplete  bool       `json:"trustComplete,omitempty"`
	CanaryID       string     `json:"canaryId,omitempty"`
	CanaryOK       bool       `json:"canaryOk,omitempty"`
	CanaryState    string     `json:"canaryState,omitempty"`
	CanaryEnabled  bool       `json:"canaryEnabled,omitempty"`
	CanaryRan      bool       `json:"canaryRan,omitempty"`
	Rerun          bool       `json:"rerun,omitempty"`
	RerunOK        bool       `json:"rerunOk,omitempty"`
	Flagged        bool       `json:"flagged,omitempty"`
	FlagReason     string     `json:"flagReason,omitempty"`
	Late           bool       `json:"late,omitempty"`
	LateAcceptedAt *time.Time `json:"lateAcceptedAt,omitempty"`
	// Redundant marks a result settled by the 2-of-N quorum and counts the
	// results that were compared (W10C row 1).
	Redundant         bool `json:"redundant,omitempty"`
	RedundantReplicas int  `json:"redundantReplicas,omitempty"`
	RecomputeVerified bool `json:"recomputeVerified,omitempty"`
	// RecomputeUnverified marks a recompute that ran and disagreed with the
	// worker on an unpinned reference size: the mismatch is never fraud
	// (G-R17-3, WO R18).
	RecomputeUnverified bool `json:"recomputeUnverified,omitempty"`
	// RecomputeDeferred marks a result that was held for the deferred recompute
	// (G-R17-1, WO R18): the submission, the background verdict or the skip
	// fallback.
	RecomputeDeferred bool `json:"recomputeDeferred,omitempty"`
}

type resultJSON Result

func marshalStoredResult(r Result) ([]byte, error) {
	value := resultJSON(r)
	raw := value.Output
	value.Output = nil
	body, err := json.Marshal(value)
	if err != nil || len(raw) == 0 {
		return body, err
	}
	return appendJSONField(body, "output", raw), nil
}

// jobJSON is the plain durable view of a job. Results is omitted here and
// appended afterwards, so byte-sensitive output survives the store.
type jobJSON struct {
	AppID         string           `json:"appId,omitempty"`
	JobID         string           `json:"jobId"`
	ServerID      string           `json:"serverId"`
	PoolID        string           `json:"poolId"`
	Owner         string           `json:"owner"`
	State         string           `json:"state"`
	CreatedAt     time.Time        `json:"createdAt"`
	FinishedAt    *time.Time       `json:"finishedAt,omitempty"`
	Needs         string           `json:"needs"`
	Policy        string           `json:"policy"`
	MaxAttempts   int              `json:"maxAttempts"`
	Priority      string           `json:"priority"`
	Progress      Progress         `json:"progress"`
	Warnings      []Warning        `json:"warnings"`
	SessionID     string           `json:"sessionId,omitempty"`
	ConfigVersion string           `json:"configVersion"`
	Request       CreateRequest    `json:"request"`
	Tasks         []Task           `json:"tasks"`
	ChainMode     ChainMode        `json:"chainMode,omitempty"`
	ChainHead     string           `json:"chainHead,omitempty"`
	ChainEntries  int              `json:"chainEntries,omitempty"`
	AuditGap      bool             `json:"auditGap,omitempty"`
	ChosenPolicy  *PolicySelection `json:"chosenPolicy,omitempty"`
}

func appendJSONField(object []byte, name string, value []byte) []byte {
	field, _ := json.Marshal(name)
	if len(object) == 2 {
		out := append(object[:1], field...)
		out = append(out, ':')
		out = append(out, value...)
		return append(out, '}')
	}
	out := append(object[:len(object)-1], ',')
	out = append(out, field...)
	out = append(out, ':')
	out = append(out, value...)
	return append(out, '}')
}

// MarshalStoredJSON preserves result output bytes in durable records. It is
// used only by the stores; normal json.Marshal keeps the public API unchanged.
//
// R10 glm: the results member is left out by the alias struct, never removed
// from the finished document. A byte replace over the whole document rewrote a
// developer-supplied request metadata field that contained the same literal.
func (j Job) MarshalStoredJSON() ([]byte, error) {
	value := jobJSON{
		AppID: j.AppID, JobID: j.JobID, ServerID: j.ServerID, PoolID: j.PoolID, Owner: j.Owner,
		State: j.State, CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt,
		Needs: j.Needs, Policy: j.Policy, MaxAttempts: j.MaxAttempts, Priority: j.Priority,
		Progress: j.Progress, Warnings: j.Warnings, SessionID: j.SessionID,
		ConfigVersion: j.ConfigVersion, Request: j.Request, Tasks: j.Tasks,
		ChainMode: j.ChainMode, ChainHead: j.ChainHead, ChainEntries: j.ChainEntries, AuditGap: j.AuditGap,
		ChosenPolicy: j.ChosenPolicy,
	}
	body, err := json.Marshal(value)
	if err != nil {
		return body, err
	}
	if len(j.Results) == 0 {
		return appendJSONField(body, "results", []byte("null")), nil
	}
	rawResults := make([]byte, 0)
	for i, result := range j.Results {
		raw, err := marshalStoredResult(result)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			rawResults = append(rawResults, ',')
		}
		rawResults = append(rawResults, raw...)
	}
	rawResults = append(append([]byte{'['}, rawResults...), ']')
	return appendJSONField(body, "results", rawResults), nil
}

type Submission struct {
	GPU          *GPUResultMetrics    `json:"gpu,omitempty"`
	LeaseReceipt *verify.LeaseReceipt `json:"leaseReceipt,omitempty"`
	Offline      bool                 `json:"offline,omitempty"`
	// ComputeMS is the optional compute time the contributor claims for a late
	// duplicate result (F12, G-F12-4). It only ever lowers the credit: the
	// server bounds it by the receipt span it issued.
	ComputeMS    int64                     `json:"computeMs,omitempty"`
	PeerRelayed  bool                      `json:"peerRelayed,omitempty"`
	Lease        Lease                     `json:"lease"`
	TaskEnvelope verify.SignedTaskEnvelope `json:"taskEnvelope,omitempty"`
	Output       json.RawMessage           `json:"output,omitempty"`
	SHA256       string                    `json:"sha256,omitempty"`
	Error        string                    `json:"error,omitempty"`
	// CanaryID names the canary the output answers, when the task carried one
	// (WWPP-R37.1). The server checks it against its own known answer.
	CanaryID string `json:"canaryId,omitempty"`
	// CanaryAnswer is the answer of the canary task. The expected answer stays
	// server side; the comparison is a hash compare only.
	CanaryAnswer json.RawMessage `json:"canaryAnswer,omitempty"`
}
type Artifact struct {
	ArtifactID    string    `json:"artifactId"`
	SHA256        string    `json:"sha256"`
	Owner         string    `json:"owner,omitempty"`
	SizeBytes     int64     `json:"sizeBytes"`
	ReceivedBytes int64     `json:"receivedBytes"`
	NextOffset    int64     `json:"nextOffset"`
	Complete      bool      `json:"complete"`
	ContentType   string    `json:"contentType"`
	CreatedAt     time.Time `json:"createdAt"`
	Data          []byte    `json:"data,omitempty"`
	// F8 upload scan and admin review state (WWPP-R36.1).
	Scan         *ScanReport `json:"scan,omitempty"`
	ReviewState  string      `json:"reviewState,omitempty"`
	ReviewReason string      `json:"reviewReason,omitempty"`
	Reviewers    []string    `json:"reviewers,omitempty"`
	ReviewQuorum int         `json:"reviewQuorum,omitempty"`
	ReviewedAt   *time.Time  `json:"reviewedAt,omitempty"`
}

// ArtifactOwner is one per-owner reference to a shared content-addressed
// artifact blob (T100, owner decision 2026-11-21 option A). The blob itself is
// stored once per sha256 and the record keeps the first uploader as its
// Owner; every owner subject that uploaded the same bytes (or that inherited
// the reference from the migration) holds one ArtifactOwner row, so reads and
// job use are allowed for every holder and the blob is deleted only when no
// reference remains.
type ArtifactOwner struct {
	ArtifactID string    `json:"artifactId"`
	Owner      string    `json:"owner"`
	CreatedAt  time.Time `json:"createdAt"`
}
type Worker struct {
	EgressConsentIDs []string  `json:"egressConsentIds,omitempty"`
	EgressConsent    bool      `json:"egressConsent,omitempty"`
	AppID            string    `json:"appId,omitempty"`
	AppOwner         string    `json:"appOwner,omitempty"`
	AppExpiresAt     time.Time `json:"appExpiresAt,omitempty"` // F9: tab affinity lease
	TabID            string    `json:"tabId"`
	HostID           string    `json:"hostId"`
	Owner            string    `json:"owner"`
	PoolID           string    `json:"poolId"`
	Slots            int       `json:"slots"`
	GPU              bool      `json:"gpu"`
	GPUSlots         int       `json:"gpuSlots,omitempty"`
	GPUs             []GPUInfo `json:"gpus,omitempty"`
	DeviceClass      string    `json:"deviceClass,omitempty"`
	BatteryPercent   int       `json:"batteryPercent,omitempty"`
	Charging         bool      `json:"charging,omitempty"`
	MemoryBytes      int64     `json:"memoryBytes,omitempty"`
	Heartbeat        time.Time `json:"heartbeat"`
	SessionID        string    `json:"sessionId,omitempty"`
	Anonymous        bool      `json:"anonymous,omitempty"`
	ClientIP         string    `json:"clientIp,omitempty"`
	// BrowserTabID is the identity of the browser tab that registered (R10:
	// sessionStorage, so two tabs of one browser differ). It keys the reload
	// reuse: without it two tabs of one owner share localStorage's HostID and
	// the second register overwrote the first tab's record.
	BrowserTabID string `json:"browserTabId,omitempty"`
	// RunnerStale marks a tab whose runner cannot verify the live task key
	// (T114). It gets no lease until the page reloads and registers again.
	RunnerStale bool `json:"runnerStale,omitempty"`
	// RunnerPin is the pin the tab last reported: the sha256 of its runner.html
	// and the id of the task key that page trusts, as "<sha256>:<keyId>"
	// (W9 F1). The key half is what the refund rule verifies; the sha half is
	// kept as evidence of the page the tab runs.
	RunnerPin string `json:"runnerPin,omitempty"`
}

type GPUInfo struct {
	Vendor       string `json:"vendor"`
	Architecture string `json:"architecture"`
}
type SessionRequest struct {
	TabCount                  int             `json:"tabCount"`
	HostCount                 int             `json:"hostCount,omitempty"`
	TTLSeconds                int             `json:"ttlSeconds,omitempty"`
	Needs                     string          `json:"needs,omitempty"`
	ModuleArtifactIDs         []string        `json:"moduleArtifactIds,omitempty"`
	WarmInitializerArtifactID string          `json:"warmInitializerArtifactId,omitempty"`
	Pin                       json.RawMessage `json:"pin,omitempty"`
	Options                   json.RawMessage `json:"options,omitempty"`
	Metadata                  json.RawMessage `json:"metadata,omitempty"`
}
type Session struct {
	SessionID                 string          `json:"sessionId"`
	Owner                     string          `json:"owner"`
	State                     string          `json:"state"`
	TabCount                  int             `json:"tabCount"`
	HostCount                 int             `json:"hostCount"`
	CreatedAt                 time.Time       `json:"createdAt"`
	ExpiresAt                 time.Time       `json:"expiresAt"`
	DrainUntil                *time.Time      `json:"drainUntil,omitempty"`
	Tabs                      []Worker        `json:"tabs"`
	Progress                  Progress        `json:"progress"`
	WarmState                 string          `json:"warmState"`
	Pin                       json.RawMessage `json:"pin,omitempty"`
	WarmInitializerArtifactID string          `json:"warmInitializerArtifactId,omitempty"`
	Request                   SessionRequest  `json:"request"`
	Warnings                  []Warning       `json:"warnings"`
}
type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	JobID     string    `json:"jobId"`
	TaskID    string    `json:"taskId,omitempty"`
	At        time.Time `json:"at"`
	Owner     string    `json:"owner"`
	PoolID    string    `json:"poolId"`
	SessionID string    `json:"sessionId,omitempty"`
	Result    *Result   `json:"result,omitempty"`
}

type SessionLimits struct {
	MaxSessionsPerOwner      int
	MaxTabsPerOwner          int
	MaxWorkersPerOwner       int
	MaxSlotsPerWorker        int
	MaxArtifactBytesPerOwner int64
	MaxArtifactBytesTotal    int64
}

func DefaultSessionLimits() SessionLimits {
	return SessionLimits{
		MaxSessionsPerOwner:      8,
		MaxTabsPerOwner:          64,
		MaxWorkersPerOwner:       64,
		MaxSlotsPerWorker:        32,
		MaxArtifactBytesPerOwner: 128 * 1024 * 1024,
		MaxArtifactBytesTotal:    512 * 1024 * 1024,
	}
}

var engineLimits sync.Map

func (e *Engine) SetSessionLimits(l SessionLimits) {
	d := DefaultSessionLimits()
	if l.MaxSessionsPerOwner > 0 {
		d.MaxSessionsPerOwner = l.MaxSessionsPerOwner
	}
	if l.MaxTabsPerOwner > 0 {
		d.MaxTabsPerOwner = l.MaxTabsPerOwner
	}
	if l.MaxWorkersPerOwner > 0 {
		d.MaxWorkersPerOwner = l.MaxWorkersPerOwner
	}
	if l.MaxSlotsPerWorker > 0 {
		d.MaxSlotsPerWorker = l.MaxSlotsPerWorker
	}
	if l.MaxArtifactBytesPerOwner > 0 {
		d.MaxArtifactBytesPerOwner = l.MaxArtifactBytesPerOwner
	}
	if l.MaxArtifactBytesTotal > 0 {
		d.MaxArtifactBytesTotal = l.MaxArtifactBytesTotal
	}
	engineLimits.Store(e, d)
}

func (e *Engine) sessionLimits() SessionLimits {
	if v, ok := engineLimits.Load(e); ok {
		return v.(SessionLimits)
	}
	d := DefaultSessionLimits()
	engineLimits.Store(e, d)
	return d
}
