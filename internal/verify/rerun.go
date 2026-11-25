// Path: internal/verify/rerun.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: deterministic re-run sampling and hash-only result comparison;
// WWPP-R35.1 (optional, developer-led re-run %, exact hashes only), R37.1.
package verify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// RerunSampler draws the re-run decision for a task from a stable hash of its task id.
// The same task id always yields the same decision (no server state, no RNG), so any
// pool node reaches the same conclusion for the same task.
type RerunSampler struct {
	metrics *Metrics
	now     func() time.Time
}

// NewRerunSampler returns a sampler. A nil metrics argument uses the process-wide registry.
func NewRerunSampler(m *Metrics) *RerunSampler {
	return &RerunSampler{metrics: m, now: time.Now}
}

// ShouldRerun reports whether the job/task sampling key is inside the re-run percentage.
// percent is clamped to [0,100]; 0 never re-runs, 100 always re-runs.
func (s *RerunSampler) ShouldRerun(taskID string, percent float64) bool {
	start := startOn(s.metrics)
	decision := ShouldRerun(taskID, percent)
	recordOn(s.metrics, OpRerun, start, true)
	return decision
}

// ShouldRerunWithKey reports whether the keyed sampling decision is inside the
// re-run percentage. The secret keeps workers from predicting the selection.
func (s *RerunSampler) ShouldRerunWithKey(key []byte, taskID string, percent float64) bool {
	start := startOn(s.metrics)
	decision := ShouldRerunWithKey(key, taskID, percent)
	recordOn(s.metrics, OpRerun, start, true)
	return decision
}

// ShouldRerun is the package level sampler: it hashes the task id and compares the first
// 16 bits (as a fraction of 65536) against percent. Deterministic and stateless.
func ShouldRerun(taskID string, percent float64) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	h := sha256.Sum256([]byte(taskID))
	v := binary.BigEndian.Uint16(h[0:2])
	return float64(v)/65536.0*100.0 < percent
}

// ShouldRerunWithKey is deterministic across nodes sharing the same secret while
// keeping the sampling decision unpredictable from the public task identifiers.
func ShouldRerunWithKey(key []byte, taskID string, percent float64) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	return RerunBucketWithKey(key, taskID) < percent
}

// RerunBucketWithKey returns the keyed deterministic bucket of a task id in [0,100).
func RerunBucketWithKey(key []byte, taskID string) float64 {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(taskID))
	sum := mac.Sum(nil)
	v := binary.BigEndian.Uint16(sum[:2])
	return float64(v) / 65536.0 * 100.0
}

// RerunBucket returns the deterministic bucket of a task id in [0,100).
func RerunBucket(taskID string) float64 {
	h := sha256.Sum256([]byte(taskID))
	v := binary.BigEndian.Uint16(h[0:2])
	return float64(v) / 65536.0 * 100.0
}

// CompareResults compares two results by exact SHA-256 hash only. The server never runs
// developer code, so it cannot compute a tolerant comparison (WWPP-R35.1).
func CompareResults(a, b []byte) bool {
	return SHA256(a) == SHA256(b)
}

// CompareResultHashes compares two hex digests (case-insensitive) without rehashing.
func CompareResultHashes(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	da, err := hex.DecodeString(a)
	if err != nil {
		return false
	}
	db, err := hex.DecodeString(b)
	if err != nil {
		return false
	}
	return bytes.Equal(da, db)
}
