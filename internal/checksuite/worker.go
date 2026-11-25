// Path: internal/checksuite/worker.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: the builtin CPU worker of the T108 check suite plus the matmul
// reference output, the anonymous contributor path of WO_T96, the result hash
// check and the T99 revocation of the suite's own keys. The worker starts only
// after the watches are established and holds every lease for the configured
// settle delay, so the queued -> leased -> done phases stay observable.
package checksuite

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/verify"
)

// sleepStop waits for d or until stop closes; it reports whether the full
// wait completed.
func sleepStop(stop <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

// workerState carries the observable facts of the builtin worker loop.
type workerState struct {
	mu      sync.Mutex
	started time.Time
	err     error
}

func (s *workerState) setStarted(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = at
}

func (s *workerState) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *workerState) startedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *workerState) error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// runBuiltinWorker registers one contributor tab and serves every lease until
// stop is closed. Leases are held for the settle delay before the result is
// submitted, so a watcher has a deterministic window to observe the leased
// phase and to resume with Last-Event-ID before the done event. HTTP calls run
// on reqCtx, not on the stop signal: stopping must not abort an in-flight
// result submission whose job the orchestrator may already see as completed.
func (t *transport) runBuiltinWorker(reqCtx context.Context, stop <-chan struct{}, prefix, token string, o Options, st *workerState) {
	st.setStarted(time.Now())
	ctx := reqCtx
	status, raw, err := t.do(ctx, http.MethodPost, prefix+"/workers/register", token,
		[]byte(`{"hostId":"checksuite-builtin","slots":2}`), nil)
	if err != nil || status != 201 {
		st.setErr(fmt.Errorf("worker register: %s", httpDetail(http.MethodPost, prefix+"/workers/register", status, err)))
		return
	}
	var w pool.Worker
	if json.Unmarshal(raw, &w) != nil || w.TabID == "" {
		st.setErr(fmt.Errorf("worker register: invalid worker response"))
		return
	}
	if status, _, hbErr := t.do(ctx, http.MethodPost, prefix+"/workers/"+url.PathEscape(w.TabID)+"/heartbeat", token, []byte(`{}`), nil); hbErr != nil || status != 200 {
		st.setErr(fmt.Errorf("worker heartbeat: HTTP %d (%v)", status, err))
		return
	}
	for {
		select {
		case <-stop:
			return
		default:
		}
		status, raw, err := t.do(ctx, http.MethodPost, prefix+"/workers/"+url.PathEscape(w.TabID)+"/leases", token, nil, nil)
		if err != nil {
			st.setErr(fmt.Errorf("lease pull failed: %v", err))
			return
		}
		if status == http.StatusNoContent {
			if !sleepStop(stop, 5*time.Millisecond) {
				return
			}
			continue
		}
		if status != http.StatusOK {
			st.setErr(httpError(prefix+"/workers/{tabId}/leases", status, raw))
			return
		}
		var a pool.Assignment
		if json.Unmarshal(raw, &a) != nil {
			st.setErr(fmt.Errorf("lease pull: invalid assignment response"))
			return
		}
		members := a.Batch
		if len(members) == 0 {
			members = []pool.Assignment{a}
		}
		for _, m := range members {
			out := matmulOutput(assignedSize(m, o.Size))
			// Settle delay: the lease is held while the watcher resumes.
			if !sleepStop(stop, o.SettleDelay) {
				return
			}
			sub := pool.Submission{Lease: m.Lease, TaskEnvelope: m.TaskEnvelope, LeaseReceipt: m.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}
			status, _, err := t.do(ctx, http.MethodPost, prefix+"/workers/"+url.PathEscape(w.TabID)+"/results", token, mustJSON(sub), nil)
			if err != nil || status != http.StatusOK {
				st.setErr(fmt.Errorf("result submit: %s", httpDetail(http.MethodPost, prefix+"/workers/{tabId}/results", status, err)))
				return
			}
		}
	}
}

// assignedSize reads the matmul size from the leased task input envelope
// {"json":{"kind":"matmul-f32-gpu","n":160}}; anything else falls back.
func assignedSize(a pool.Assignment, fallback int) int {
	var env struct {
		JSON struct {
			N float64 `json:"n"`
		} `json:"json"`
	}
	if json.Unmarshal(a.Task.Input, &env) == nil && env.JSON.N > 0 {
		return int(env.JSON.N)
	}
	return fallback
}

// matmulResult is the output envelope of the existing test module.
type matmulResult struct {
	N      int    `json:"n"`
	SHA256 string `json:"sha256"`
}

// matmulOutput computes the reference matmul of the existing test module: the
// matrix bytes are little endian float32 and the returned JSON carries their
// sha256. It reproduces the algorithm of cmd/pools-e2e byte for byte, so the
// settled result hash must match Matmul160ReferenceSHA256 at size 160.
func matmulOutput(n int) []byte {
	values := make([]float32, n*n)
	for row := 0; row < n; row++ {
		for col := 0; col < n; col++ {
			var sum float32
			for k := 0; k < n; k++ {
				a := float32((row*n+k)%7 - 3)
				b := float32((k*n+col)%5 - 2)
				sum = float32(sum + float32(a*b))
			}
			values[row*n+col] = sum
		}
	}
	raw := make([]byte, len(values)*4)
	for i, v := range values {
		u := math.Float32bits(v)
		raw[i*4], raw[i*4+1], raw[i*4+2], raw[i*4+3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
	}
	sum := sha256.Sum256(raw)
	return mustJSON(matmulResult{N: n, SHA256: hex.EncodeToString(sum[:])})
}

// resultHashCheck verifies, for both jobs, that the settled result matches the
// reference hash of the matmul module, that the stored output hash still
// covers the exact bytes and that the server hash check passed.
func (t *transport) resultHashCheck(ctx context.Context, prefix, token, name, jobA, jobB string, size int) []Check {
	var checks []Check
	start := time.Now()
	local := matmulOutput(size)
	var reference matmulResult
	if json.Unmarshal(local, &reference) != nil {
		return []Check{fail(name, checkResultHash, "the local matmul output did not parse")}
	}
	if size == 160 && reference.SHA256 != Matmul160ReferenceSHA256 {
		return []Check{fail(name, checkResultHash,
			"local 160^3 matmul hash %s does not match the published reference %s",
			reference.SHA256, Matmul160ReferenceSHA256)}
	}
	for _, job := range []string{jobA, jobB} {
		items, err := t.getResults(ctx, prefix, token, job)
		if err != nil {
			checks = append(checks, fail(name, checkResultHash, "results fetch for job %s failed: %v", job, err))
			continue
		}
		var done *pool.Result
		for i := range items {
			if items[i].Status == "done" {
				done = &items[i]
				break
			}
		}
		if done == nil {
			checks = append(checks, fail(name, checkResultHash, "job %s settled without a done result", job))
			continue
		}
		// The REST result view wraps a plain JSON object output into the
		// {"json":{...}} envelope of the TaskOutput contract; a non-object
		// output arrives inline. Accept both shapes.
		var out matmulResult
		var envelope struct {
			JSON *matmulResult `json:"json"`
		}
		if json.Unmarshal(done.Output, &envelope) == nil && envelope.JSON != nil {
			out = *envelope.JSON
		} else if json.Unmarshal(done.Output, &out) != nil {
			checks = append(checks, fail(name, checkResultHash, "job %s result output did not parse", job))
			continue
		}
		switch {
		case out.SHA256 != reference.SHA256:
			checks = append(checks, fail(name, checkResultHash,
				"job %s result hash %s != reference %s (n=%d)", job, out.SHA256, reference.SHA256, out.N))
		case !done.HashOK:
			// hashOk is the server verdict over the exact submitted bytes (T53):
			// it proves the stored result still hashes to what the worker
			// declared, which the re-marshaled REST view cannot show directly.
			checks = append(checks, fail(name, checkResultHash, "job %s server hash check did not pass", job))
		default:
			check := okCheck(name, checkResultHash,
				fmt.Sprintf("matmul %d^3 result hash matches the reference", size), start)
			check.JobID = job
			checks = append(checks, check)
		}
	}
	return checks
}

// jwtClaims carries the two fields of the anonymous token the suite asserts on.
type jwtClaims struct {
	Subject   string `json:"sub"`
	Anonymous bool   `json:"anon"`
}

// decodeJWTClaims reads the unverified payload of an anonymous join token. The
// token arrived from the server over this connection; only the subject-keeps
// assertion of the renewal reads it, like tests/anonymous_T96_test.go does.
func decodeJWTClaims(token string) (jwtClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, false
	}
	var c jwtClaims
	if json.Unmarshal(raw, &c) != nil {
		return jwtClaims{}, false
	}
	return c, true
}

// anonymousCheck walks the anonymous contributor path of WO_T96: join without
// a login, register a tab, lease a task, settle the result, then renew the
// token and keep the same subject (WO_T96B). The renewed token must still
// authorize the same tab.
func (t *transport) anonymousCheck(ctx context.Context, o Options, prefix, devToken, artifactID, name string) []Check {
	var checks []Check
	start := time.Now()
	step := func(detail string) { checks = append(checks, okCheck(name, checkAnonymous, detail, start)) }

	// 1. join: no bearer, no body.
	status, body, err := t.do(ctx, http.MethodPost, prefix+"/workers/anonymous", "", nil, nil)
	if err != nil || status != http.StatusOK {
		return []Check{fail(name, checkAnonymous, "join failed: %s",
			httpDetail(http.MethodPost, prefix+"/workers/anonymous", status, err))}
	}
	var tok auth.Token
	if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		return []Check{fail(name, checkAnonymous, "join response carried no token")}
	}
	claims, ok := decodeJWTClaims(tok.AccessToken)
	if !ok || !claims.Anonymous || claims.Subject == "" {
		return []Check{fail(name, checkAnonymous, "join token is not an anonymous JWT with a subject")}
	}

	// 2. register the anonymous tab.
	status, body, err = t.do(ctx, http.MethodPost, prefix+"/workers/register", tok.AccessToken,
		[]byte(`{"hostId":"checksuite-anon","slots":1}`), nil)
	if err != nil || status != http.StatusCreated {
		return []Check{fail(name, checkAnonymous, "anonymous register failed: %s",
			httpDetail(http.MethodPost, prefix+"/workers/register", status, err))}
	}
	var w pool.Worker
	if json.Unmarshal(body, &w) != nil || w.TabID == "" || !w.Anonymous || w.Owner != claims.Subject {
		return []Check{fail(name, checkAnonymous, "anonymous register returned a wrong worker view")}
	}

	// 3. a small job for the anonymous tab.
	jobID, status, err := t.restSubmitJob(ctx, prefix, devToken, artifactID, 1, "anon-task", "anon-"+name)
	if err != nil || status != http.StatusCreated {
		return []Check{fail(name, checkAnonymous, "anonymous job submit failed: %s",
			httpDetail(http.MethodPost, prefix+"/jobs", status, err))}
	}

	// 4. lease the task.
	status, body, err = t.do(ctx, http.MethodPost, prefix+"/workers/"+url.PathEscape(w.TabID)+"/leases", tok.AccessToken, nil, nil)
	if err != nil || status != http.StatusOK {
		return []Check{fail(name, checkAnonymous, "anonymous lease failed: %s",
			httpDetail(http.MethodPost, prefix+"/workers/{tabId}/leases", status, err))}
	}
	var a pool.Assignment
	if json.Unmarshal(body, &a) != nil || a.Lease.Token == "" {
		return []Check{fail(name, checkAnonymous, "anonymous lease returned no assignment")}
	}

	// 5. settle the result.
	out := matmulOutput(assignedSize(a, 1))
	sub := pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}
	status, body, err = t.do(ctx, http.MethodPost, prefix+"/workers/"+url.PathEscape(w.TabID)+"/results", tok.AccessToken, mustJSON(sub), nil)
	if err != nil || status != http.StatusOK {
		return []Check{fail(name, checkAnonymous, "anonymous result submit failed: %s",
			httpDetail(http.MethodPost, prefix+"/workers/{tabId}/results", status, err))}
	}
	var result pool.Result
	if json.Unmarshal(body, &result) != nil || result.Status != "done" {
		return []Check{fail(name, checkAnonymous, "anonymous result was not accepted (status %q)", result.Status)}
	}

	// 6. renewal keeps the same subject (WO_T96B).
	status, body, err = t.do(ctx, http.MethodPost, prefix+"/workers/anonymous", tok.AccessToken, nil, nil)
	if err != nil || status != http.StatusOK {
		return []Check{fail(name, checkAnonymous, "renewal failed: %s",
			httpDetail(http.MethodPost, prefix+"/workers/anonymous", status, err))}
	}
	var renewed auth.Token
	if json.Unmarshal(body, &renewed) != nil || renewed.AccessToken == "" {
		return []Check{fail(name, checkAnonymous, "renewal response carried no token")}
	}
	renewedClaims, ok := decodeJWTClaims(renewed.AccessToken)
	if !ok || !renewedClaims.Anonymous || renewedClaims.Subject != claims.Subject {
		return []Check{fail(name, checkAnonymous, "renewal did not keep the same anonymous subject")}
	}

	// 7. the renewed token still authorizes the same tab: no work left -> 204.
	status, _, err = t.do(ctx, http.MethodPost, prefix+"/workers/"+url.PathEscape(w.TabID)+"/leases", renewed.AccessToken, nil, nil)
	if err != nil || status != http.StatusNoContent {
		return []Check{fail(name, checkAnonymous, "renewed token lost the tab (lease pull HTTP %d, %v)", status, err)}
	}
	_ = jobID
	step("join ok, register ok, lease ok, result done, renewal kept the subject, tab still authorized")
	return checks
}

// revokeOwnKeys revokes the developer and the contributor key the suite
// created for itself through the T99 route, then verifies that a revoked key
// no longer exchanges for a token.
func (t *transport) revokeOwnKeys(ctx context.Context, prefix, adminToken string, dev, contrib createdKey) (int, Check) {
	start := time.Now()
	keys := []createdKey{dev, contrib}
	for _, k := range keys {
		path := prefix + "/auth/keys/" + url.PathEscape(k.keyID) + ":revoke"
		status, body, err := t.do(ctx, http.MethodPost, path, adminToken, nil, nil)
		if err != nil || status != http.StatusOK {
			return 0, fail("keys", checkRevokeKeys, "revoke failed: %s", httpDetail(http.MethodPost, path, status, err))
		}
		var v struct {
			KeyID   string `json:"keyId"`
			Revoked bool   `json:"revoked"`
		}
		if json.Unmarshal(body, &v) != nil || !v.Revoked || v.KeyID != k.keyID {
			return 0, fail("keys", checkRevokeKeys, "revoke response did not confirm %s as revoked", k.keyID)
		}
	}
	for _, k := range keys {
		status, _, _ := t.do(ctx, http.MethodPost, prefix+"/auth/token", "", mustJSON(struct {
			APIKey string `json:"apiKey"`
		}{k.raw}), nil)
		if status == http.StatusOK {
			return 0, fail("keys", checkRevokeKeys, "a revoked key still exchanges for a token")
		}
	}
	return len(keys), okCheck("keys", checkRevokeKeys,
		fmt.Sprintf("%d own keys revoked via the T99 route; both refused on exchange", len(keys)), start)
}
