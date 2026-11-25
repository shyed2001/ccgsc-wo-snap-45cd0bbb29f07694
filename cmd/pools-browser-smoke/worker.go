// Path: cmd/pools-browser-smoke/worker.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the builtin CPU worker of the harness. It settles the matmul job
// the harness submits through REST, so the reference hash check of the job is
// deterministic and independent of the browser.
//
// The worker is the one of cmd/pools-e2e: it registers one contributor tab,
// heartbeats, pulls the leases, computes the reference matmul and submits the
// result with the hash of the submitted bytes. The algorithm is byte for byte
// the one of cmd/pools-e2e, so the settled hash must match the published
// reference of web/workloads/matmul/manifest.json.
//
// The worker routes are contributor routes: they need a key of role
// "contributor" with the scope "worker", not the developer key of the job.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"ccgsc/poolscore/internal/pool"
)

// matmulResult is the output envelope of the reference workload.
type matmulResult struct {
	N      int    `json:"n"`
	SHA256 string `json:"sha256"`
}

// matmulOutput computes the reference matmul: the matrix bytes are little
// endian float32 and the returned JSON carries their sha256.
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

// shaJSON answers the hex sha256 of the submitted bytes.
func shaJSON(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// builtinWorker registers one contributor tab and settles the leases it
// pulls, until the job is done or the budget passes.
type builtinWorker struct {
	client *client
	prefix string
	token  string
	size   int
	// settled counts the accepted results.
	settled int
}

// runBuiltinWorker registers the tab, heartbeats and serves the leases.
//
// The flow is the one of cmd/pools-e2e: register, heartbeat, then pull the
// leases in a loop. Every member of a batch is submitted on its own, because
// the server settles every member separately.
func (w *builtinWorker) runBuiltinWorker(ctx context.Context, budget time.Duration) error {
	status, raw, err := w.client.do(ctx, http.MethodPost, w.prefix+"/workers/register", w.token,
		[]byte(`{"hostId":"browser-smoke","slots":1}`), nil)
	if err != nil {
		return fmt.Errorf("worker register: %w", err)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return fmt.Errorf("worker register: HTTP %d: %s", status, problemDetail(raw))
	}
	var worker pool.Worker
	if json.Unmarshal(raw, &worker) != nil || worker.TabID == "" {
		return fmt.Errorf("worker register: invalid worker response")
	}
	// The heartbeat keeps the tab alive while it pulls the leases.
	if status, raw, err := w.client.do(ctx, http.MethodPost,
		w.prefix+"/workers/"+worker.TabID+"/heartbeat", w.token, []byte(`{}`), nil); err != nil {
		return fmt.Errorf("worker heartbeat: %w", err)
	} else if status != http.StatusOK && status != http.StatusNoContent && status != http.StatusCreated {
		return fmt.Errorf("worker heartbeat: HTTP %d: %s", status, problemDetail(raw))
	}

	deadline := time.Now().Add(budget)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status, raw, err := w.client.do(ctx, http.MethodPost,
			w.prefix+"/workers/"+worker.TabID+"/leases", w.token, nil, nil)
		if err != nil {
			return fmt.Errorf("lease pull: %w", err)
		}
		if status == http.StatusNoContent {
			// No lease is ready yet: wait and pull again.
			if time.Now().After(deadline) {
				return fmt.Errorf("no lease arrived within %s", budget)
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if status != http.StatusOK {
			return fmt.Errorf("lease pull: HTTP %d: %s", status, problemDetail(raw))
		}
		var assignment pool.Assignment
		if err := json.Unmarshal(raw, &assignment); err != nil {
			return fmt.Errorf("lease pull: invalid assignment response")
		}
		// The batch carries every member, and the top level mirrors the
		// first one. Every member is submitted on its own.
		members := assignment.Batch
		if len(members) == 0 {
			members = []pool.Assignment{assignment}
		}
		for _, a := range members {
			output := matmulOutput(assignedSize(a.Task.Input, w.size))
			sub := pool.Submission{
				Lease:        a.Lease,
				TaskEnvelope: a.TaskEnvelope,
				LeaseReceipt: a.LeaseReceipt,
				Output:       output,
				SHA256:       shaJSON(output),
			}
			body, err := json.Marshal(sub)
			if err != nil {
				return fmt.Errorf("result submit: %w", err)
			}
			status, raw, err := w.client.do(ctx, http.MethodPost,
				w.prefix+"/workers/"+worker.TabID+"/results", w.token, body, nil)
			if err != nil {
				return fmt.Errorf("result submit: %w", err)
			}
			if status != http.StatusOK && status != http.StatusCreated {
				return fmt.Errorf("result submit: HTTP %d: %s", status, problemDetail(raw))
			}
			w.settled++
		}
		return nil
	}
}

// assignedSize reads the matmul size out of the leased task input; anything
// else falls back to the size of the run.
func assignedSize(input json.RawMessage, fallback int) int {
	var env struct {
		JSON struct {
			N float64 `json:"n"`
		} `json:"json"`
	}
	if json.Unmarshal(input, &env) == nil && env.JSON.N > 0 {
		return int(env.JSON.N)
	}
	return fallback
}
