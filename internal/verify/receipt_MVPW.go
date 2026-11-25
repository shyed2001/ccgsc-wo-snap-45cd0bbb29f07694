// MVPW-F, Codex, 2026-11-19: WWPP-R36.2 domain-separated lease receipt signatures.
// F12 (owner decisions 2026-11-24, WO section 16): per-task run-capped receipts
// join the retired batch receipt under a separate v2 domain, with a hard 30
// minute window ceiling (G-F12-1).
package verify

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"time"
)

// validReceiptHash is a lowercase SHA-256 hex digest. Receipt ids and the
// bound token, envelope and input digests are all that shape.
func validReceiptHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	raw, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(raw) == s
}

const LeaseReceiptDomain = "ccgsc.pools.lease-receipt.v1"

// LeaseReceiptDomainV2 separates the per-task receipt of the F12 owner policy
// (WO section 16, G-F12-1/2) from the retired batch receipt of MVPW-F.
const LeaseReceiptDomainV2 = "ccgsc.pools.lease-receipt.v2"

// MaxReceiptWindowSeconds bounds the legacy v1 batch receipt only. The F12
// per-task receipt is bounded by MaxTaskReceiptWindowSeconds.
const MaxReceiptWindowSeconds int64 = 72 * 60 * 60

// TaskReceiptVersion marks the per-task receipt (F12, G-F12-2): exactly one
// leased task, bound to pool, job, app, developer owner, worker, lease
// generation, lease token hash, envelope digest and input digest.
const TaskReceiptVersion = 2

// MaxTaskReceiptWindowSeconds is the hard owner ceiling of the Dew window
// (G-F12-1: admin range 1-30 minutes). No receipt, whatever the configuration,
// can outlive it.
const MaxTaskReceiptWindowSeconds int64 = 30 * 60

// LeaseReceipt uses Unix seconds for both timestamps. TaskIDs and InputHashes
// are parallel, ordered arrays; neither is sorted independently. Signature is
// standard base64 on the JSON wire (Go []byte encoding), excluded from signing.
type LeaseReceipt struct {
	ReceiptID     string   `json:"receiptId"`
	TaskIDs       []string `json:"taskIds"`
	InputHashes   []string `json:"inputHashes"`
	Deadline      int64    `json:"deadline"`
	IssuedAt      int64    `json:"issuedAt"`
	WindowSeconds int64    `json:"windowSeconds"`
	Algorithm     string   `json:"algorithm"`
	KeyID         string   `json:"keyId"`
	Signature     []byte   `json:"signature"`

	// F12 per-task receipt (Version == TaskReceiptVersion). Every field below
	// is signed. TaskIDs/InputHashes keep their wire shape and hold exactly one
	// entry. The receipt expires at MIN(Deadline, end of the run): the run end
	// is server state (a terminal job), it cannot be signed in advance, so the
	// submit path re-checks it at verification time.
	Version          int    `json:"version,omitempty"`
	PoolID           string `json:"poolId,omitempty"`
	JobID            string `json:"jobId,omitempty"`
	TaskID           string `json:"taskId,omitempty"`
	AppID            string `json:"appId,omitempty"`
	DeveloperOwner   string `json:"developerOwner,omitempty"`
	ContributorOwner string `json:"contributorOwner,omitempty"`
	WorkerID         string `json:"workerId,omitempty"`
	Generation       uint64 `json:"generation,omitempty"`
	// LeaseTokenSHA256 binds the lease token without copying the bearer secret
	// into a second document.
	LeaseTokenSHA256 string `json:"leaseTokenSha256,omitempty"`
	EnvelopeSHA256   string `json:"envelopeSha256,omitempty"`
}

// TaskReceiptID derives the unique id of the receipt of one leased task. The
// id is deterministic, so the server and the contributor agree on it without a
// second round trip, and a re-lease (new generation, new token) gets a new id.
func TaskReceiptID(jobID, taskID string, generation uint64, leaseTokenSHA256 string) string {
	return SHA256([]byte(fmt.Sprintf("ccgsc:receipt:v2\x00%s\x00%s\x00%d\x00%s", jobID, taskID, generation, leaseTokenSHA256)))
}

// CanonicalBytes is UTF-8 domain + NUL + CanonicalJSON of exactly the protocol
// fields, lexically sorted keys, no whitespace. Timestamps are integer Unix
// seconds, not RFC3339 or protobuf's textual Timestamp representation.
func (r LeaseReceipt) CanonicalBytes() ([]byte, error) {
	if r.Version == TaskReceiptVersion {
		p, err := CanonicalJSON(map[string]any{
			"version": r.Version, "receiptId": r.ReceiptID, "poolId": r.PoolID, "jobId": r.JobID,
			"taskId": r.TaskID, "taskIds": r.TaskIDs, "inputHashes": r.InputHashes,
			"appId": r.AppID, "developerOwner": r.DeveloperOwner, "contributorOwner": r.ContributorOwner,
			"workerId": r.WorkerID, "generation": r.Generation, "leaseTokenSha256": r.LeaseTokenSHA256,
			"envelopeSha256": r.EnvelopeSHA256, "deadline": r.Deadline, "issuedAt": r.IssuedAt,
			"windowSeconds": r.WindowSeconds, "algorithm": r.Algorithm, "keyId": r.KeyID,
		})
		if err != nil {
			return nil, err
		}
		return append([]byte(LeaseReceiptDomainV2+"\x00"), p...), nil
	}
	p, err := CanonicalJSON(map[string]any{
		"receiptId": r.ReceiptID, "taskIds": r.TaskIDs, "inputHashes": r.InputHashes,
		"deadline": r.Deadline, "issuedAt": r.IssuedAt, "windowSeconds": r.WindowSeconds,
		"algorithm": r.Algorithm, "keyId": r.KeyID,
	})
	if err != nil {
		return nil, err
	}
	return append([]byte(LeaseReceiptDomain+"\x00"), p...), nil
}

func (r LeaseReceipt) Valid() error {
	if r.Version != 0 && r.Version != TaskReceiptVersion {
		return fmt.Errorf("%w: unknown receipt version", ErrInvalidEnv)
	}
	if r.Version == TaskReceiptVersion {
		// ContributorOwner is allowed to stay empty: the submit path
		// authenticates the contributor, and the worker record it is enriched
		// from may legitimately be anonymous. DeveloperOwner is required.
		if r.WindowSeconds > MaxTaskReceiptWindowSeconds || r.PoolID == "" || r.JobID == "" ||
			r.TaskID == "" || r.DeveloperOwner == "" || r.WorkerID == "" ||
			r.Generation == 0 || !validReceiptHash(r.ReceiptID) ||
			!validReceiptHash(r.LeaseTokenSHA256) || !validReceiptHash(r.EnvelopeSHA256) ||
			len(r.TaskIDs) != 1 || r.TaskIDs[0] != r.TaskID {
			return fmt.Errorf("%w: invalid per-task lease receipt", ErrInvalidEnv)
		}
	}
	if r.ReceiptID == "" || r.KeyID == "" || r.Algorithm != EnvelopeSignature ||
		len(r.TaskIDs) == 0 || len(r.TaskIDs) != len(r.InputHashes) ||
		r.IssuedAt < 0 || r.WindowSeconds < 1 || r.WindowSeconds > MaxReceiptWindowSeconds ||
		r.Deadline <= r.IssuedAt || r.Deadline-r.IssuedAt != r.WindowSeconds {
		return fmt.Errorf("%w: invalid lease receipt", ErrInvalidEnv)
	}
	seen := map[string]bool{}
	for i, id := range r.TaskIDs {
		if id == "" || seen[id] || !validReceiptHash(r.InputHashes[i]) {
			return fmt.Errorf("%w: invalid receipt task/hash pair", ErrInvalidEnv)
		}
		seen[id] = true
	}
	return nil
}

func SignLeaseReceipt(r LeaseReceipt, key ed25519.PrivateKey, keyID string) (LeaseReceipt, error) {
	if len(key) != ed25519.PrivateKeySize {
		return LeaseReceipt{}, ErrInvalidEnv
	}
	r.KeyID, r.Algorithm = keyID, EnvelopeSignature
	if err := r.Valid(); err != nil {
		return LeaseReceipt{}, err
	}
	b, err := r.CanonicalBytes()
	if err != nil {
		return LeaseReceipt{}, err
	}
	r.TaskIDs = append([]string(nil), r.TaskIDs...)
	r.InputHashes = append([]string(nil), r.InputHashes...)
	r.Signature = ed25519.Sign(key, b)
	return r, nil
}

func VerifyLeaseReceipt(r LeaseReceipt, ring *Keyring, now time.Time) error {
	if err := r.Valid(); err != nil {
		return err
	}
	if ring == nil {
		return ErrUnknownKey
	}
	pub, err := ring.PublicKeyAt(r.KeyID, now)
	if err != nil {
		return err
	}
	b, err := r.CanonicalBytes()
	if err != nil {
		return err
	}
	if len(r.Signature) != ed25519.SignatureSize || !ed25519.Verify(pub, b, r.Signature) {
		return ErrBadSignature
	}
	if now.Before(time.Unix(r.IssuedAt, 0).Add(-maxEnvelopeSkew)) {
		return ErrNotYetValid
	}
	if !now.Before(time.Unix(r.Deadline, 0)) {
		return ErrExpired
	}
	return nil
}
