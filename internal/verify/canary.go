// Path: internal/verify/canary.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: scheduler-owned known-answer canary tasks and their server-side check;
// WWPP-R37.1 (canary known-answer tasks), R39.1 (canaries/reputation review fix).
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrUnknownCanary is returned when a canary id is not registered.
var ErrUnknownCanary = errors.New("verify: unknown canary id")

// Canary is a known-answer task owned by the scheduler. The expected answer is only ever
// held server side: the task body carries no canary marker and no expected hash.
type Canary struct {
	ID             string    `json:"id"`
	ArtifactSHA256 string    `json:"artifactSha256,omitempty"` // required for signed injection; digest of the actual module
	TaskID         string    `json:"taskId"`
	Input          []byte    `json:"input"`
	ExpectedSHA    string    `json:"expectedSha256"`
	CreatedAt      time.Time `json:"createdAt"`
	Rotations      int       `json:"rotations"`
}

// CanaryResult is the outcome of a canary check.
type CanaryResult struct {
	CanaryID string    `json:"canaryId"`
	Pass     bool      `json:"pass"`
	Expected string    `json:"expectedSha256,omitempty"`
	Actual   string    `json:"actualSha256,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at"`
}

// CanaryRegistry stores canary fixtures and compares returned answers by hash only.
type CanaryRegistry struct {
	mu       sync.RWMutex
	canaries map[string]Canary
	metrics  *Metrics
	now      func() time.Time
}

// NewCanaryRegistry returns an empty registry. A nil metrics argument uses the
// process-wide registry.
func NewCanaryRegistry(m *Metrics) *CanaryRegistry {
	return &CanaryRegistry{canaries: make(map[string]Canary), metrics: m, now: time.Now}
}

// Register adds a canary fixture with a known expected answer hash.
func (r *CanaryRegistry) Register(c Canary) error {
	if c.ID == "" {
		return fmt.Errorf("%w: canary id is required", ErrInvalidEnv)
	}
	if c.ExpectedSHA == "" {
		return fmt.Errorf("%w: canary %s has no expected hash", ErrInvalidEnv, c.ID)
	}
	if _, err := hex.DecodeString(c.ExpectedSHA); err != nil || len(c.ExpectedSHA) != 64 {
		return fmt.Errorf("%w: canary %s expected hash is not a sha256 hex digest", ErrInvalidEnv, c.ID)
	}
	if c.ArtifactSHA256 != "" {
		if _, err := hex.DecodeString(c.ArtifactSHA256); err != nil || len(c.ArtifactSHA256) != 64 {
			return fmt.Errorf("%w: canary artifact hash is not a sha256 hex digest", ErrInvalidEnv)
		}
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = r.now().UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.canaries[c.ID] = c
	return nil
}

// RegisterWithAnswer registers a canary and derives the expected hash from the answer.
func (r *CanaryRegistry) RegisterWithAnswer(id, taskID string, input, answer []byte) (Canary, error) {
	c := Canary{ID: id, TaskID: taskID, Input: append([]byte(nil), input...), ExpectedSHA: SHA256(answer)}
	if err := r.Register(c); err != nil {
		return Canary{}, err
	}
	return c, nil
}

// Rotate replaces the answer of a canary and keeps the rotation counter (R39.1: rotating
// fixtures, no predictable schedule).
func (r *CanaryRegistry) Rotate(id string, answer []byte) (Canary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.canaries[id]
	if !ok {
		return Canary{}, ErrUnknownCanary
	}
	c.ExpectedSHA = SHA256(answer)
	c.Rotations++
	c.CreatedAt = r.now().UTC()
	r.canaries[id] = c
	return c, nil
}

// Get returns a canary fixture.
func (r *CanaryRegistry) Get(id string) (Canary, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.canaries[id]
	if !ok {
		return Canary{}, ErrUnknownCanary
	}
	return c, nil
}

// Len returns the number of registered canaries.
func (r *CanaryRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.canaries)
}

// CheckCanary compares the returned answer against the known answer of a canary.
// The comparison is a hash compare only; the server never runs developer code.
func (r *CanaryRegistry) CheckCanary(id string, answer []byte) CanaryResult {
	start := startOn(r.metrics)
	res := r.check(id, answer)
	recordOn(r.metrics, OpCanary, start, res.Pass)
	return res
}

// CheckCanaryHash compares a returned answer hash against the known answer of a canary.
func (r *CanaryRegistry) CheckCanaryHash(id, answerSHA string) CanaryResult {
	start := startOn(r.metrics)
	res := r.checkHash(id, answerSHA)
	recordOn(r.metrics, OpCanary, start, res.Pass)
	return res
}

func (r *CanaryRegistry) check(id string, answer []byte) CanaryResult {
	return r.checkHash(id, SHA256(answer))
}

func (r *CanaryRegistry) checkHash(id, answerSHA string) CanaryResult {
	r.mu.RLock()
	c, ok := r.canaries[id]
	r.mu.RUnlock()
	if !ok {
		return CanaryResult{CanaryID: id, Pass: false, Reason: "unknown canary", At: r.now().UTC()}
	}
	pass := CompareResultHashes(c.ExpectedSHA, answerSHA)
	res := CanaryResult{
		CanaryID: id,
		Pass:     pass,
		Expected: c.ExpectedSHA,
		Actual:   answerSHA,
		At:       r.now().UTC(),
	}
	if !pass {
		res.Reason = "canary answer mismatch"
	}
	return res
}

// CanaryTask returns the wire form of a canary task: the input only, without the canary
// marker and without the expected hash (R39.1).
func (r *CanaryRegistry) CanaryTask(id string) (json.RawMessage, error) {
	c, err := r.Get(id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"taskId": c.TaskID,
		"input":  json.RawMessage(c.Input),
	})
}

// CanaryDigest returns the sha256 of the canary input, used to bind a canary answer to
// the exact bytes that were sent.
func CanaryDigest(input []byte) string {
	h := sha256.Sum256(input)
	return hex.EncodeToString(h[:])
}
