// Path: cmd/pools-e2e/main.go | Author: Copilot | Date: 2026-11-20
// What/why: Live REST end-to-end verifier for both pool implementations, including
// the builtin CPU worker path and a secret-safe JSON report.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/checksuite"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/verify"
)

type options struct {
	base, pool, worker, out string
	size                    int
	timeout                 time.Duration
	full                    bool
}

type report struct {
	Base        string       `json:"base"`
	Pool        string       `json:"pool"`
	Worker      string       `json:"worker"`
	Pools       []poolReport `json:"pools"`
	Pass        bool         `json:"pass"`
	RevokedKeys int          `json:"revokedKeys"`
	// WorkerKeyFile names the separate 0600 sidecar file that carries the
	// contributor API key in -worker none mode (T101). The report itself must
	// never contain key material; the field is the pointer for the operator,
	// not the secret.
	WorkerKeyFile string `json:"workerKeyFile,omitempty"`
}
type poolReport struct {
	Pool            string `json:"pool"`
	JobID           string `json:"jobId,omitempty"`
	TaskCount       int    `json:"taskCount"`
	AcceptedResults int    `json:"acceptedResults"`
	RejectedResults int    `json:"rejectedResults"`
	DurationMS      int64  `json:"durationMs"`
	FinalState      string `json:"finalState,omitempty"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
}

type client struct {
	base string
	http *http.Client
}

type createdKey struct {
	raw   string
	keyID string
}

func main() {
	var o options
	flag.StringVar(&o.base, "base", "http://127.0.0.1:8091", "pool server base URL")
	flag.StringVar(&o.pool, "pool", "both", "thread, process, or both")
	flag.StringVar(&o.worker, "worker", "builtin", "builtin or none")
	flag.IntVar(&o.size, "size", 160, "matmul size (160 or 240)")
	flag.DurationVar(&o.timeout, "timeout", 3*time.Minute, "overall timeout")
	flag.StringVar(&o.out, "out", "pools-e2e.json", "JSON report path")
	flag.BoolVar(&o.full, "full", false, "run the T108 full 2-pool x 3-API check suite (REST + SSE + gRPC)")
	flag.Parse()
	if o.full {
		runFullAndExit(o)
		return
	}
	r, err := run(context.Background(), o, os.Getenv("POOLS_E2E_ADMIN_KEY"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeReport(o.out, r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if r.WorkerKeyFile != "" {
		fmt.Printf("[E2E] browser-worker contributor key written to %s (mode 0600); the JSON report does not contain it\n", r.WorkerKeyFile)
	}
	if !r.Pass {
		os.Exit(1)
	}
}

// runFullAndExit executes the shared T108 check suite (internal/checksuite) and
// exits non-zero when any check fails. The suite covers both pools and all three
// APIs: REST submit, SSE watch with Last-Event-ID, gRPC submit and gRPC watch,
// the queued -> leased -> done event order, the matmul reference hash, the
// anonymous contributor path and the T99 revocation of its own keys. The JSON
// report and the table carry no secrets.
func runFullAndExit(o options) {
	adminKey := os.Getenv("POOLS_E2E_ADMIN_KEY")
	var pools []string
	switch o.pool {
	case "thread":
		pools = []string{checksuite.ThreadPrefix}
	case "process":
		pools = []string{checksuite.ProcessPrefix}
	case "both":
	default:
		fmt.Fprintln(os.Stderr, "invalid -pool")
		os.Exit(1)
	}
	rep, err := runFull(context.Background(), o, adminKey, pools)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeFullReport(o.out, rep); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(rep.Table())
	if !rep.Pass {
		os.Exit(1)
	}
}

func runFull(parent context.Context, o options, adminKey string, pools []string) (checksuite.Report, error) {
	return checksuite.Run(parent, checksuite.Options{
		Base:     o.base,
		AdminKey: adminKey,
		Pools:    pools,
		Size:     o.size,
		Timeout:  o.timeout,
	})
}

func writeFullReport(path string, r checksuite.Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0600)
}

func run(parent context.Context, o options, adminKey string) (report, error) {
	if adminKey == "" {
		return report{}, errors.New("POOLS_E2E_ADMIN_KEY is required")
	}
	if o.worker != "builtin" && o.worker != "none" {
		return report{}, errors.New("invalid -worker")
	}
	if o.pool != "thread" && o.pool != "process" && o.pool != "both" {
		return report{}, errors.New("invalid -pool")
	}
	if o.size != 160 && o.size != 240 {
		return report{}, errors.New("-size must be 160 or 240")
	}
	pools := []string{"/thread-pool/v1", "/process-pool/v1"}
	if o.pool == "thread" {
		pools = pools[:1]
	} else if o.pool == "process" {
		pools = pools[1:]
	}
	ctx, cancel := context.WithTimeout(parent, o.timeout)
	defer cancel()
	c := &client{strings.TrimRight(o.base, "/"), &http.Client{}}

	adminTokens := make(map[string]string)
	for _, p := range pools {
		t, err := c.exchange(ctx, p, adminKey)
		if err != nil {
			return report{}, err
		}
		adminTokens[p] = t
	}
	devKey, err := c.createKey(ctx, pools[0], adminTokens[pools[0]], auth.User{ID: "e2e-developer-" + auth.ID(), AccountStore: "B", Roles: []string{"developer"}}, []string{"artifacts:write", "jobs:write", "jobs:read"})
	if err != nil {
		return report{}, err
	}
	contribKey, err := c.createKey(ctx, pools[0], adminTokens[pools[0]], auth.User{ID: "e2e-contributor-" + auth.ID(), AccountStore: "B", Roles: []string{"contributor"}}, []string{"worker"})
	if err != nil {
		return report{}, err
	}
	devTokens, contribTokens := make(map[string]string), make(map[string]string)
	for _, p := range pools {
		if devTokens[p], err = c.exchange(ctx, p, devKey.raw); err != nil {
			return report{}, err
		}
		if contribTokens[p], err = c.exchange(ctx, p, contribKey.raw); err != nil {
			return report{}, err
		}
	}
	r := report{Base: o.base, Pool: o.pool, Worker: o.worker, Pools: make([]poolReport, 0, len(pools))}
	if o.worker == "none" {
		// T101 (cmdc leak sweep): in none mode the browser worker owns the
		// contributor key. It is handed over through a separate 0600 sidecar
		// file next to the report, never through the JSON report itself and
		// never through stdout.
		keyPath, err := writeWorkerKeyFile(o.out, contribKey.raw)
		if err != nil {
			return report{}, err
		}
		r.WorkerKeyFile = keyPath
	}
	for _, p := range pools {
		pr := c.runPool(ctx, p, devTokens[p], contribTokens[p], o)
		r.Pools = append(r.Pools, pr)
	}
	for _, key := range []createdKey{devKey, contribKey} {
		if err := c.revokeKey(ctx, pools[0], adminTokens[pools[0]], key.keyID); err != nil {
			r.Pass = false
			r.Pools = append(r.Pools, poolReport{Pool: "key-revoke", Status: "FAIL", Error: err.Error()})
			break
		}
		r.RevokedKeys++
	}
	if _, err := c.exchange(ctx, pools[0], devKey.raw); err == nil {
		r.Pass = false
		r.Pools = append(r.Pools, poolReport{Pool: "key-revoke", Status: "FAIL", Error: "developer key still exchanges after revocation"})
	}
	if _, err := c.exchange(ctx, pools[0], contribKey.raw); err == nil {
		r.Pass = false
		r.Pools = append(r.Pools, poolReport{Pool: "key-revoke", Status: "FAIL", Error: "contributor key still exchanges after revocation"})
	}
	r.Pass = len(r.Pools) > 0
	for _, p := range r.Pools {
		if p.Status != "PASS" {
			r.Pass = false
		}
	}
	return r, nil
}

func (c *client) runPool(ctx context.Context, prefix, devToken, workerToken string, o options) poolReport {
	start := time.Now()
	pr := poolReport{Pool: strings.TrimPrefix(prefix, "/")}
	// Fixed artifact bytes (T100, owner decision 2026-11-21 option A): the blob
	// is content-addressed once and every run's new developer key joins it with
	// its own per-owner reference, so a rerun no longer needs unique bytes.
	artifact := []byte("pools-e2e accepted artifact")
	artifactID := verify.SHA256(artifact)
	if _, err := c.request(ctx, http.MethodPut, prefix+"/artifacts", devToken, artifact, map[string]string{
		"Content-Type": "application/octet-stream", "X-Artifact-Sha256": artifactID,
		"Upload-Offset": "0", "Upload-Length": fmt.Sprint(len(artifact)),
	}); err != nil {
		pr.Error = err.Error()
		pr.DurationMS = time.Since(start).Milliseconds()
		return pr
	}
	input := json.RawMessage(fmt.Sprintf(`{"json":{"kind":"matmul-f32-gpu","n":%d}}`, o.size))
	zero := 0.0
	jobBody := pool.CreateRequest{
		ModuleArtifactIDs: []string{artifactID},
		Tasks:             []pool.TaskSpec{{TaskID: "matmul", Input: input}},
		BatchSize:         1,
		Needs:             "cpu",
		RerunPercent:      &zero,
	}
	body, _ := json.Marshal(jobBody)
	raw, err := c.request(ctx, http.MethodPost, prefix+"/jobs", devToken, body, map[string]string{"Idempotency-Key": "pools-e2e-" + auth.ID()})
	if err != nil {
		pr.Error = err.Error()
		pr.DurationMS = time.Since(start).Milliseconds()
		return pr
	}
	var job pool.Job
	if err = json.Unmarshal(raw, &job); err != nil {
		pr.Error = "invalid job response"
		pr.DurationMS = time.Since(start).Milliseconds()
		return pr
	}
	pr.JobID, pr.TaskCount = job.JobID, job.Progress.Total
	if o.worker == "builtin" {
		var worker pool.Worker
		raw, err = c.request(ctx, http.MethodPost, prefix+"/workers/register", workerToken, []byte(`{"hostId":"pools-e2e","slots":1}`), nil)
		if err == nil {
			err = json.Unmarshal(raw, &worker)
		}
		if err == nil {
			_, err = c.request(ctx, http.MethodPost, prefix+"/workers/"+worker.TabID+"/heartbeat", workerToken, []byte(`{}`), nil)
		}
		for err == nil && time.Now().Before(start.Add(o.timeout)) {
			raw, reqErr := c.request(ctx, http.MethodPost, prefix+"/workers/"+worker.TabID+"/leases", workerToken, nil, nil)
			if errors.Is(reqErr, errNoContent) {
				if isDone(ctx, c, prefix, job.JobID, devToken, &pr) {
					break
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if reqErr != nil {
				err = reqErr
				break
			}
			var assignment pool.Assignment
			if err = json.Unmarshal(raw, &assignment); err != nil {
				break
			}
			members := assignment.Batch
			if len(members) == 0 {
				members = []pool.Assignment{assignment}
			}
			for _, a := range members {
				output := matmulOutput(o.size)
				sub := pool.Submission{
					Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: output,
					SHA256: shaJSON(output),
				}
				b, _ := json.Marshal(sub)
				if _, submitErr := c.request(ctx, http.MethodPost, prefix+"/workers/"+worker.TabID+"/results", workerToken, b, nil); submitErr != nil {
					pr.RejectedResults++
				} else {
					pr.AcceptedResults++
				}
			}
			if isDone(ctx, c, prefix, job.JobID, devToken, &pr) {
				break
			}
		}
		if err != nil {
			pr.Error = err.Error()
		}
	} else {
		// In none mode the browser worker owns the contributor key; this process only observes.
		for !isDone(ctx, c, prefix, job.JobID, devToken, &pr) && time.Now().Before(start.Add(o.timeout)) {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if pr.FinalState == "" {
		_ = isDone(ctx, c, prefix, job.JobID, devToken, &pr)
	}
	if pr.FinalState == "completed" && pr.Error == "" {
		pr.Status = "PASS"
	} else if pr.Status == "" {
		pr.Status = "FAIL"
	}
	pr.DurationMS = time.Since(start).Milliseconds()
	return pr
}

var errNoContent = errors.New("no content")

func isDone(ctx context.Context, c *client, prefix, id, token string, pr *poolReport) bool {
	raw, err := c.request(ctx, http.MethodGet, prefix+"/jobs/"+id, token, nil, nil)
	if err != nil {
		if pr.Error == "" {
			pr.Error = err.Error()
		}
		return false
	}
	var j pool.Job
	if json.Unmarshal(raw, &j) != nil {
		return false
	}
	pr.FinalState = j.State
	if pr.TaskCount == 0 {
		pr.TaskCount = j.Progress.Total
	}
	done := j.State == "completed" || j.State == "failed" || j.State == "cancelled"
	if done {
		if results, resultErr := c.request(ctx, http.MethodGet, prefix+"/jobs/"+id+"/results", token, nil, nil); resultErr == nil {
			var page struct {
				Items []pool.Result `json:"items"`
			}
			if json.Unmarshal(results, &page) == nil {
				pr.AcceptedResults = 0
				for _, result := range page.Items {
					if result.Status == "done" {
						pr.AcceptedResults++
					}
				}
			}
		}
	}
	return done
}

func (c *client) exchange(ctx context.Context, prefix, key string) (string, error) {
	raw, err := c.request(ctx, http.MethodPost, prefix+"/auth/token", "", mustJSON(struct {
		APIKey string `json:"apiKey"`
	}{key}), nil)
	if err != nil {
		return "", err
	}
	var t auth.Token
	if err := json.Unmarshal(raw, &t); err != nil || t.AccessToken == "" {
		return "", errors.New("invalid token response")
	}
	return t.AccessToken, nil
}

func (c *client) createKey(ctx context.Context, prefix, admin string, user auth.User, scopes []string) (createdKey, error) {
	raw, err := c.request(ctx, http.MethodPost, prefix+"/auth/keys", admin, mustJSON(struct {
		User   auth.User `json:"user"`
		Scopes []string  `json:"scopes"`
	}{user, scopes}), nil)
	if err != nil {
		return createdKey{}, err
	}
	var v struct {
		APIKey string `json:"apiKey"`
		KeyID  string `json:"keyId"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.APIKey == "" || v.KeyID == "" {
		return createdKey{}, errors.New("invalid key response")
	}
	return createdKey{raw: v.APIKey, keyID: v.KeyID}, nil
}

func (c *client) revokeKey(ctx context.Context, prefix, admin, keyID string) error {
	raw, err := c.request(ctx, http.MethodPost, prefix+"/auth/keys/"+keyID+":revoke", admin, nil, nil)
	if err != nil {
		return err
	}
	var v struct {
		KeyID   string `json:"keyId"`
		Revoked bool   `json:"revoked"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.KeyID != keyID || !v.Revoked {
		return errors.New("invalid revoke response")
	}
	return nil
}

func (c *client) request(ctx context.Context, method, path, token string, body []byte, headers map[string]string) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, errors.New("request construction failed")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed", path)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusNoContent {
		return nil, errNoContent
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d", path, res.StatusCode)
	}
	return data, nil
}

type matmulResult struct {
	N      int    `json:"n"`
	SHA256 string `json:"sha256"`
}

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

func shaJSON(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func writeReport(path string, r report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0600)
}

// writeWorkerKeyFile hands the contributor API key of a -worker none run to
// the operator through a separate 0600 JSON file next to the report (T101).
// The JSON report itself must never carry key material.
func writeWorkerKeyFile(reportPath, key string) (string, error) {
	dir := filepath.Dir(reportPath)
	base := strings.TrimSuffix(filepath.Base(reportPath), ".json")
	if base == "" {
		base = "pools-e2e"
	}
	path := filepath.Join(dir, base+"-worker-key.json")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	b, err := json.Marshal(struct {
		ContributorKey string `json:"contributorKey"`
	}{key})
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0600); err != nil {
		return "", err
	}
	// WriteFile applies 0600 only at creation; force it in case a sidecar of
	// an earlier run survived with wider permissions.
	if err := os.Chmod(path, 0600); err != nil {
		return "", err
	}
	return path, nil
}
