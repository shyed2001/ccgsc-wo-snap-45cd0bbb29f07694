// Path: tests/metrics_MVPW_test.go
// What/why: Slice B acceptance tests for pool_metrics persistence, /admin/v1/metrics, and /metrics.
package tests

import (
	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type metricsAdminResponse struct {
	Scope    string                        `json:"scope"`
	JobID    string                        `json:"jobId"`
	Since    string                        `json:"since"`
	Samples  []store.MetricSample          `json:"samples"`
	Metrics  map[string]metricsAggregateJS `json:"metrics"`
	Counters map[string]float64            `json:"counters"`
}

type metricsAggregateJS struct {
	Name   string    `json:"name"`
	Unit   string    `json:"unit"`
	Source string    `json:"source"`
	Count  int       `json:"count"`
	Sum    float64   `json:"sum"`
	Avg    float64   `json:"avg"`
	Min    float64   `json:"min"`
	Max    float64   `json:"max"`
	Last   float64   `json:"last"`
	LastTS time.Time `json:"lastTs"`
}

func doMetricsHTTP(t *testing.T, handler http.Handler, method, target, remoteAddr, token string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	if remoteAddr != "" {
		r.RemoteAddr = remoteAddr
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

type parsedPrometheus struct {
	helps  map[string]int
	types  map[string]string
	typeN  map[string]int
	values map[string][]float64
}

func parsePrometheusText(t *testing.T, body string) parsedPrometheus {
	t.Helper()
	out := parsedPrometheus{
		helps:  make(map[string]int),
		types:  make(map[string]string),
		typeN:  make(map[string]int),
		values: make(map[string][]float64),
	}
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			fields := strings.Fields(line[len("# HELP "):])
			if len(fields) < 1 {
				t.Fatalf("malformed # HELP line: %q", line)
			}
			out.helps[fields[0]]++
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line[len("# TYPE "):])
			if len(fields) != 2 {
				t.Fatalf("malformed # TYPE line: %q", line)
			}
			name, mtype := fields[0], fields[1]
			if mtype != "counter" && mtype != "gauge" {
				t.Fatalf("unexpected Prometheus type %q for %q", mtype, name)
			}
			out.typeN[name]++
			out.types[name] = mtype
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		namePart := line
		if idx := strings.IndexByte(line, '{'); idx >= 0 {
			endIdx := strings.IndexByte(line[idx:], '}')
			if endIdx < 0 {
				t.Fatalf("unclosed Prometheus labels on line: %q", line)
			}
			namePart = line[:idx]
			rest := strings.TrimSpace(line[idx+endIdx+1:])
			val, err := strconv.ParseFloat(strings.Fields(rest)[0], 64)
			if err != nil {
				t.Fatalf("invalid Prometheus value on line %q: %v", line, err)
			}
			out.values[namePart] = append(out.values[namePart], val)
		} else {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				t.Fatalf("malformed Prometheus sample line: %q", line)
			}
			val, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				t.Fatalf("invalid Prometheus value on line %q: %v", line, err)
			}
			namePart = fields[0]
			out.values[namePart] = append(out.values[namePart], val)
		}
	}
	for name, cnt := range out.typeN {
		if cnt != 1 {
			t.Fatalf("metric %q declared # TYPE %d times, want exactly 1", name, cnt)
		}
	}
	for name := range out.values {
		if out.typeN[name] != 1 {
			t.Fatalf("metric %q emitted value without single # TYPE declaration", name)
		}
	}
	return out
}

func sumFloatSlice(vals []float64) float64 {
	var total float64
	for _, v := range vals {
		total += v
	}
	return total
}

func TestMetricsUnmeasuredOmittedAndEmptyRoutes(t *testing.T) {
	cfg := config.Default()
	cfg.MetricsLoopbackAnonymous = true
	f, _ := setupMetricsServer(t, filepath.Join(t.TempDir(), "metrics_empty.db"), cfg)
	_, adminToken := f.key(t, "admin")

	body := f.request(t, "GET", "/admin/v1/metrics", adminToken, nil, http.StatusOK, nil)
	got := decode[metricsAdminResponse](t, body)
	if len(got.Samples) != 0 {
		t.Fatalf("fresh DB samples = %d, want 0", len(got.Samples))
	}
	if len(got.Metrics) != 0 || len(got.Counters) != 0 {
		t.Fatalf("fresh DB fabricated unmeasured metrics: metrics=%v counters=%v", got.Metrics, got.Counters)
	}

	promRec := doMetricsHTTP(t, f.handler, "GET", "/metrics", "127.0.0.1:41001", "", nil)
	if promRec.Code != http.StatusOK {
		t.Fatalf("loopback GET /metrics = %d, want 200: %s", promRec.Code, promRec.Body.String())
	}
	parsed := parsePrometheusText(t, promRec.Body.String())
	if len(parsed.values) != 0 || len(parsed.typeN) != 0 {
		t.Fatalf("fresh DB Prometheus output must omit unmeasured names, got: %s", promRec.Body.String())
	}
}

func setupMetricsServer(t *testing.T, dbPath string, cfg config.Config) (*fixture, *api.Server) {
	t.Helper()
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, l, "s1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(db, cfg.JWTSecret, "s1")
	s := &api.Server{Engine: engine, Auth: a, Log: l, Config: cfg}
	return &fixture{db: db, engine: engine, auth: a, handler: s.Handler()}, s
}

func TestMetricsSplitLeaseSubmitAndPrometheusParse(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metrics_split.db")
	cfg := config.Default()
	cfg.MetricsLoopbackAnonymous = true
	f, _ := setupMetricsServer(t, dbPath, cfg)

	_, adminToken := f.key(t, "admin")
	_, devToken := f.key(t, "developer")
	_, contribToken := f.key(t, "contributor")

	// Reject unauthorized callers on /admin/v1/metrics.
	f.request(t, "GET", "/admin/v1/metrics", "", nil, http.StatusUnauthorized, nil)
	f.request(t, "GET", "/admin/v1/metrics", devToken, nil, http.StatusForbidden, nil)
	f.request(t, "GET", "/admin/v1/metrics", contribToken, nil, http.StatusForbidden, nil)
	f.request(t, "GET", "/admin/v1/metrics?since=not-a-timestamp", adminToken, nil, http.StatusBadRequest, nil)

	startAnchor := time.Now().UTC().Add(-time.Second)

	// Run a full split / lease / submit cycle across both pool APIs to produce multi-scope samples.
	var jobIDs []string
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		module := f.artifact(t, base, devToken)
		inputArtifact := []byte("abcdefghij")
		artID := verify.SHA256(inputArtifact)
		f.request(t, "PUT", base+"/artifacts", devToken, inputArtifact, http.StatusCreated, map[string]string{
			"X-Artifact-Sha256": artID,
			"Upload-Offset":     "0",
			"Upload-Length":     fmt.Sprint(len(inputArtifact)),
		})
		zero := 0.0
		req := pool.CreateRequest{
			ModuleArtifactIDs: []string{module},
			Split:             &pool.SplitSpec{ArtifactID: artID, Format: "bytes", Count: 3, Chunksize: 4},
			BatchSize:         2,
			RerunPercent:      &zero,
		}
		j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", devToken, req, http.StatusCreated, nil))
		jobIDs = append(jobIDs, j.JobID)
		w := decode[pool.Worker](t, f.request(t, "POST", base+"/workers/register", contribToken, map[string]any{"hostId": "m-host", "slots": 3}, http.StatusCreated, nil))
		for _, wantBatch := range []int{2, 1} {
			assign := decode[pool.Assignment](t, f.request(t, "POST", base+"/workers/"+w.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
			if len(assign.Batch) != wantBatch {
				t.Fatalf("%s batch len = %d, want %d", base, len(assign.Batch), wantBatch)
			}
			for _, item := range assign.Batch {
				outBytes := json.RawMessage(`{"ok":true}`)
				f.request(t, "POST", base+"/workers/"+w.TabID+"/results", contribToken, pool.Submission{
					Lease:        item.Lease,
					TaskEnvelope: item.TaskEnvelope, LeaseReceipt: item.LeaseReceipt,
					Output: outBytes,
					SHA256: verify.SHA256(outBytes),
				}, http.StatusOK, nil)
			}
		}
	}

	// Verify pool_metrics SQLite table directly has real rows with all required columns.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	var rowCount int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM pool_metrics`).Scan(&rowCount); err != nil {
		t.Fatalf("query pool_metrics: %v", err)
	}
	if rowCount == 0 {
		t.Fatal("expected rows in pool_metrics after split/lease/submit cycle")
	}

	// Verify scoped/job/since filter on /admin/v1/metrics for first job (thread-pool).
	tpBody := f.request(
		t,
		"GET",
		fmt.Sprintf("/admin/v1/metrics?scope=tp&job=%s&since=%s", jobIDs[0], startAnchor.Format(time.RFC3339Nano)),
		adminToken,
		nil,
		http.StatusOK,
		nil,
	)
	tpResp := decode[metricsAdminResponse](t, tpBody)
	for _, wantName := range []string{
		"lease_issued",
		"lease_settled",
		"task_latency_ms",
		"verify_hash_pass",
		"verify_envelope_pass",
		"verify_reputation_pass",
	} {
		if _, ok := tpResp.Metrics[wantName]; !ok {
			t.Fatalf("missing metric %q in /admin/v1/metrics response: %+v", wantName, tpResp.Metrics)
		}
	}
	if tpResp.Counters["lease_issued"] != 3 {
		t.Fatalf("lease_issued = %v, want 3", tpResp.Counters["lease_issued"])
	}
	if tpResp.Counters["lease_settled"] != 3 {
		t.Fatalf("lease_settled = %v, want 3", tpResp.Counters["lease_settled"])
	}
	if tpResp.Metrics["task_latency_ms"].Count != 3 || tpResp.Metrics["task_latency_ms"].Unit != "ms" {
		t.Fatalf("task_latency_ms = %+v, want count=3 unit=ms", tpResp.Metrics["task_latency_ms"])
	}
	for _, omitted := range []string{"lease_expired", "canary_pass", "canary_fail", "verify_hash_fail", "verify_envelope_fail", "webhook_outbox_delivered", "webhook_outbox_failed"} {
		if _, ok := tpResp.Metrics[omitted]; ok {
			t.Fatalf("unmeasured metric %q was fabricated: %+v", omitted, tpResp.Metrics[omitted])
		}
	}

	// Future since must yield zero samples and empty metrics.
	futureSince := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	emptyResp := decode[metricsAdminResponse](t, f.request(t, "GET", "/admin/v1/metrics?since="+futureSince, adminToken, nil, http.StatusOK, nil))
	if len(emptyResp.Metrics) != 0 || len(emptyResp.Samples) != 0 {
		t.Fatalf("future since returned unexpected metrics: %+v", emptyResp)
	}

	// Verify alias scope query (?scope=thread-pool) matches tp samples.
	aliasResp := decode[metricsAdminResponse](t, f.request(t, "GET", "/admin/v1/metrics?scope=thread-pool", adminToken, nil, http.StatusOK, nil))
	if aliasResp.Counters["lease_issued"] != 3 {
		t.Fatalf("scope=thread-pool lease_issued = %v, want 3", aliasResp.Counters["lease_issued"])
	}

	// Verify Prometheus exposition format parses cleanly across multi-scope/multi-job series with ONE # TYPE per name.
	promRec := doMetricsHTTP(t, f.handler, "GET", "/metrics", "127.0.0.1:41002", "", nil)
	if promRec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d: %s", promRec.Code, promRec.Body.String())
	}
	if ct := promRec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain; version=0.0.4") {
		t.Fatalf("Content-Type = %q", ct)
	}
	parsed := parsePrometheusText(t, promRec.Body.String())
	if sum := sumFloatSlice(parsed.values["lease_issued"]); sum != 6 {
		t.Fatalf("Prometheus lease_issued sum across both pools = %v, want 6 (body=%s)", sum, promRec.Body.String())
	}
	if sum := sumFloatSlice(parsed.values["lease_settled"]); sum != 6 {
		t.Fatalf("Prometheus lease_settled sum across both pools = %v, want 6", sum)
	}
	if _, ok := parsed.typeN["lease_expired"]; ok {
		t.Fatalf("Prometheus must omit unmeasured lease_expired: %s", promRec.Body.String())
	}
}

func TestMetricsCanaryLeaseExpiredVerifyAndWebhook(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Unix(1795000000, 0).UTC()
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}
	clockFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}

	var webhookSuccessCalls int
	var webhookFailCalls int
	var whMu sync.Mutex
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		whMu.Lock()
		webhookSuccessCalls++
		whMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer okSrv.Close()
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		whMu.Lock()
		webhookFailCalls++
		whMu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer failSrv.Close()

	dbPath := filepath.Join(t.TempDir(), "metrics_events.db")
	cfg := config.Default()
	cfg.WebhooksEnabled = true
	cfg.WebhookAllowPrivateIPs = true
	f, _ := setupMetricsServer(t, dbPath, cfg)
	f.engine.Now = clockFn
	f.engine.Verifier.SetClock(clockFn)

	_, adminToken := f.key(t, "admin")
	_, devToken := f.key(t, "developer")
	_, contribToken := f.key(t, "contributor")

	// Deterministic canary pass & fail.
	canaryAnswer := json.RawMessage(`{"sum":42}`)
	if _, err := f.engine.RegisterCanaryWithAnswer("can-1", "task-can", json.RawMessage(`{"x":1}`), canaryAnswer); err != nil {
		t.Fatal(err)
	}
	trustCfg := f.engine.TrustConfig()
	trustCfg.CanaryEnabled = true
	trustCfg.CanaryRate = 100.0
	trustCfg.RerunPercent = 0
	f.engine.SetTrustConfig(trustCfg)

	module := f.artifact(t, "/thread-pool/v1", devToken)
	zero := 0.0
	secret := "0123456789abcdef0123456789abcdef"

	// 1. Job for canary pass + webhook delivered + task latency.
	job1 := decode[pool.Job](t, f.request(t, "POST", "/thread-pool/v1/jobs", devToken, pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "t-ok", Input: json.RawMessage(`{"n":1}`)}},
		RerunPercent:      &zero,
		Webhook:           &pool.WebhookConfig{URL: okSrv.URL + "/hook", Secret: secret},
	}, http.StatusCreated, nil))
	w := decode[pool.Worker](t, f.request(t, "POST", "/thread-pool/v1/workers/register", contribToken, map[string]any{"hostId": "w1", "slots": 4}, http.StatusCreated, nil))

	// First lease yields injected canary; submit matching canaryAnswer -> canary_pass.
	assignCanPass := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/results", contribToken, pool.Submission{
		Lease:        assignCanPass.Lease,
		TaskEnvelope: assignCanPass.TaskEnvelope, LeaseReceipt: assignCanPass.LeaseReceipt,
		Output: canaryAnswer,
		SHA256: verify.SHA256(canaryAnswer),
	}, http.StatusOK, nil)

	// Next lease yields real task; tamper envelope signature -> verify_envelope_fail recorded cleanly.
	assignReal := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	badEnv := assignReal.TaskEnvelope
	badEnv.Signature = append([]byte(nil), badEnv.Signature...)
	badEnv.Signature[0] ^= 0xff
	outReal := json.RawMessage(`{"res":7}`)
	f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/results", contribToken, pool.Submission{
		Lease:        assignReal.Lease,
		TaskEnvelope: badEnv,
		Output:       outReal,
		SHA256:       verify.SHA256(outReal),
	}, http.StatusConflict, nil)

	// Submit with wrong hash -> verify_hash_fail recorded cleanly (and retries task).
	f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/results", contribToken, pool.Submission{
		Lease:        assignReal.Lease,
		TaskEnvelope: assignReal.TaskEnvelope, LeaseReceipt: assignReal.LeaseReceipt,
		Output: outReal,
		SHA256: strings.Repeat("0", 64),
	}, http.StatusOK, nil)

	// Lease again, advance clock 150ms, submit valid result -> task_latency_ms == 150, triggers job.done webhook delivery.
	assignSettle := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	advance(150 * time.Millisecond)
	f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/results", contribToken, pool.Submission{
		Lease:        assignSettle.Lease,
		TaskEnvelope: assignSettle.TaskEnvelope, LeaseReceipt: assignSettle.LeaseReceipt,
		Output: outReal,
		SHA256: verify.SHA256(outReal),
	}, http.StatusOK, nil)

	// 2. Job for canary_fail + permanent webhook_outbox_failed + lease_expired.
	job2 := decode[pool.Job](t, f.request(t, "POST", "/thread-pool/v1/jobs", devToken, pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "t-exp", Input: json.RawMessage(`{"n":2}`)}},
		MaxAttempts:       1,
		RerunPercent:      &zero,
		Webhook:           &pool.WebhookConfig{URL: failSrv.URL + "/hook", Secret: secret},
	}, http.StatusCreated, nil))
	// First claim triggers canary failure; second claim submits worker error so MaxAttempts=1 terminates job2 failed and triggers failSrv 400 webhook.
	w2 := decode[pool.Worker](t, f.request(t, "POST", "/thread-pool/v1/workers/register", contribToken, map[string]any{"hostId": "w2", "slots": 2}, http.StatusCreated, nil))
	assignCanFail := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w2.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	wrongCanary := json.RawMessage(`{"sum":0}`)
	f.request(t, "POST", "/thread-pool/v1/workers/"+w2.TabID+"/results", contribToken, pool.Submission{
		Lease:        assignCanFail.Lease,
		TaskEnvelope: assignCanFail.TaskEnvelope, LeaseReceipt: assignCanFail.LeaseReceipt,
		Output: wrongCanary,
		SHA256: verify.SHA256(wrongCanary),
	}, http.StatusOK, nil)
	trustCfg.CanaryEnabled = false
	f.engine.SetTrustConfig(trustCfg)
	assignJob2Fail := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w2.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	f.request(t, "POST", "/thread-pool/v1/workers/"+w2.TabID+"/results", contribToken, pool.Submission{
		Lease:        assignJob2Fail.Lease,
		TaskEnvelope: assignJob2Fail.TaskEnvelope, LeaseReceipt: assignJob2Fail.LeaseReceipt,
		Error: "worker error triggers failSrv",
	}, http.StatusOK, nil)

	// Job 3 strictly for clean lease expiry on Sweep.
	job3 := decode[pool.Job](t, f.request(t, "POST", "/thread-pool/v1/jobs", devToken, pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "t-timeout", Input: json.RawMessage(`{"n":3}`)}},
		RerunPercent:      &zero,
	}, http.StatusCreated, nil))
	trustCfg.CanaryEnabled = false
	f.engine.SetTrustConfig(trustCfg)
	w3 := decode[pool.Worker](t, f.request(t, "POST", "/thread-pool/v1/workers/register", contribToken, map[string]any{"hostId": "w3", "slots": 2}, http.StatusCreated, nil))
	_ = decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w3.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	// G-F12-5: the default Dew receipt is 10 minutes, so lease expiry alone
	// must not produce a re-assignment/expiry observation. Advance past it.
	advance(11 * time.Minute)
	if err := f.engine.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Wait briefly for async webhookLoop deliveries to commit to pool_metrics.
	deadline := time.Now().Add(4 * time.Second)
	var allResp metricsAdminResponse
	for time.Now().Before(deadline) {
		body := f.request(t, "GET", "/admin/v1/metrics", adminToken, nil, http.StatusOK, nil)
		allResp = decode[metricsAdminResponse](t, body)
		if allResp.Counters["webhook_outbox_delivered"] >= 1 && allResp.Counters["webhook_outbox_failed"] >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	_ = job1
	_ = job2
	_ = job3
	for _, wantName := range []string{
		"lease_issued",
		"lease_expired",
		"lease_settled",
		"task_latency_ms",
		"verify_hash_pass",
		"verify_hash_fail",
		"verify_envelope_pass",
		"verify_envelope_fail",
		"canary_pass",
		"canary_fail",
		"verify_reputation_pass",
		"verify_reputation_fail",
		"webhook_outbox_delivered",
		"webhook_outbox_failed",
	} {
		if _, ok := allResp.Metrics[wantName]; !ok {
			t.Fatalf("missing expected metric %q in %+v", wantName, allResp.Metrics)
		}
	}
	if lat := allResp.Counters["task_latency_ms"]; lat < 150 {
		t.Fatalf("task_latency_ms = %v, want >= 150", lat)
	}
}

func TestMetricsPrometheusAuthAndLoopback(t *testing.T) {
	f := setup(t)
	_, adminToken := f.key(t, "admin")
	_, contribToken := f.key(t, "contributor")

	// Anonymous remote caller must be refused (401).
	remoteAnon := doMetricsHTTP(t, f.handler, "GET", "/metrics", "203.0.113.5:5555", "", nil)
	if remoteAnon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous remote GET /metrics = %d, want 401", remoteAnon.Code)
	}

	// Proxied remote caller arriving on loopback socket must also be refused (401).
	proxied := doMetricsHTTP(t, f.handler, "GET", "/metrics", "127.0.0.1:5555", "", map[string]string{
		"X-Forwarded-For": "203.0.113.5",
	})
	if proxied.Code != http.StatusUnauthorized {
		t.Fatalf("proxied remote GET /metrics = %d, want 401", proxied.Code)
	}

	// Authenticated non-admin role (even from loopback) must be refused (403).
	contribLoopback := doMetricsHTTP(t, f.handler, "GET", "/metrics", "127.0.0.1:5555", contribToken, nil)
	if contribLoopback.Code != http.StatusForbidden {
		t.Fatalf("contributor GET /metrics = %d, want 403", contribLoopback.Code)
	}

	// Loopback is not anonymous by default; opt-in is covered by the empty-route test.
	for _, addr := range []string{"127.0.0.1:5555", "[::1]:5555"} {
		res := doMetricsHTTP(t, f.handler, "GET", "/metrics", addr, "", nil)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("loopback %s GET /metrics = %d, want 401", addr, res.Code)
		}
	}

	// Remote admin Bearer token allowed (200).
	remoteAdmin := doMetricsHTTP(t, f.handler, "GET", "/metrics", "203.0.113.5:5555", adminToken, nil)
	if remoteAdmin.Code != http.StatusOK {
		t.Fatalf("remote admin bearer GET /metrics = %d, want 200", remoteAdmin.Code)
	}

	// Remote admin cookie session allowed (200).
	sess, err := f.auth.CreateSession(context.Background(), &auth.AdminAccount{
		Username:     "admin-metrics",
		AccountStore: "A",
		Roles:        []string{"admin"},
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	remoteSession := doMetricsHTTP(t, f.handler, "GET", "/metrics", "203.0.113.5:5555", "", nil, &http.Cookie{
		Name:  "ccgsc_admin_session",
		Value: sess.ID,
	})
	if remoteSession.Code != http.StatusOK {
		t.Fatalf("remote admin cookie GET /metrics = %d, want 200", remoteSession.Code)
	}

	// Migration SQL files exist cleanly on disk.
	pgSQL, err := os.ReadFile(filepath.Join("..", "db", "pool_metrics_MVPW.sql"))
	if err != nil || !strings.Contains(string(pgSQL), "CREATE TABLE IF NOT EXISTS pool_metrics") {
		t.Fatalf("db/pool_metrics_MVPW.sql invalid: err=%v", err)
	}
	sqliteSQL, err := os.ReadFile(filepath.Join("..", "internal", "store", "sqlite", "migrations", "002_pool_metrics_MVPW.sql"))
	if err != nil || !strings.Contains(string(sqliteSQL), "CREATE TABLE IF NOT EXISTS pool_metrics") {
		t.Fatalf("002_pool_metrics_MVPW.sql invalid: err=%v", err)
	}
}

func TestMetricsRerunVerifyOutcomes(t *testing.T) {
	cfg := config.Default()
	cfg.MetricsLoopbackAnonymous = true
	f, _ := setupMetricsServer(t, filepath.Join(t.TempDir(), "metrics_rerun.db"), cfg)
	_, adminToken := f.key(t, "admin")
	_, devToken := f.key(t, "developer")
	_, contrib1Token := f.key(t, "contributor")
	_, contrib2Token := f.key(t, "contributor")

	trustCfg := f.engine.TrustConfig()
	trustCfg.CanaryEnabled = false
	trustCfg.CanaryRate = 0
	trustCfg.RerunPercent = 100.0
	f.engine.SetTrustConfig(trustCfg)

	module := f.artifact(t, "/thread-pool/v1", devToken)
	hundred := 100.0

	w1 := decode[pool.Worker](t, f.request(t, "POST", "/thread-pool/v1/workers/register", contrib1Token, map[string]any{"hostId": "w1", "slots": 4}, http.StatusCreated, nil))
	w2 := decode[pool.Worker](t, f.request(t, "POST", "/thread-pool/v1/workers/register", contrib2Token, map[string]any{"hostId": "w2", "slots": 4}, http.StatusCreated, nil))

	// Job 1: matching rerun outputs -> verify_rerun_pass.
	f.request(t, "POST", "/thread-pool/v1/jobs", devToken, pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "t-rerun-ok", Input: json.RawMessage(`{"n":1}`)}},
		RerunPercent:      &hundred,
	}, http.StatusCreated, nil)

	outMatch := json.RawMessage(`{"ans":10}`)
	a1 := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w1.TabID+"/leases", contrib1Token, nil, http.StatusOK, nil))
	f.request(t, "POST", "/thread-pool/v1/workers/"+w1.TabID+"/results", contrib1Token, pool.Submission{
		Lease:        a1.Lease,
		TaskEnvelope: a1.TaskEnvelope, LeaseReceipt: a1.LeaseReceipt,
		Output: outMatch,
		SHA256: verify.SHA256(outMatch),
	}, http.StatusOK, nil)

	a2 := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w2.TabID+"/leases", contrib2Token, nil, http.StatusOK, nil))
	f.request(t, "POST", "/thread-pool/v1/workers/"+w2.TabID+"/results", contrib2Token, pool.Submission{
		Lease:        a2.Lease,
		TaskEnvelope: a2.TaskEnvelope, LeaseReceipt: a2.LeaseReceipt,
		Output: outMatch,
		SHA256: verify.SHA256(outMatch),
	}, http.StatusOK, nil)

	// Job 2: mismatched rerun outputs -> verify_rerun_fail.
	f.request(t, "POST", "/thread-pool/v1/jobs", devToken, pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "t-rerun-bad", Input: json.RawMessage(`{"n":2}`)}},
		RerunPercent:      &hundred,
	}, http.StatusCreated, nil)

	b1 := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w1.TabID+"/leases", contrib1Token, nil, http.StatusOK, nil))
	f.request(t, "POST", "/thread-pool/v1/workers/"+w1.TabID+"/results", contrib1Token, pool.Submission{
		Lease:        b1.Lease,
		TaskEnvelope: b1.TaskEnvelope, LeaseReceipt: b1.LeaseReceipt,
		Output: outMatch,
		SHA256: verify.SHA256(outMatch),
	}, http.StatusOK, nil)

	outMismatch := json.RawMessage(`{"ans":99}`)
	b2 := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w2.TabID+"/leases", contrib2Token, nil, http.StatusOK, nil))
	f.request(t, "POST", "/thread-pool/v1/workers/"+w2.TabID+"/results", contrib2Token, pool.Submission{
		Lease:        b2.Lease,
		TaskEnvelope: b2.TaskEnvelope, LeaseReceipt: b2.LeaseReceipt,
		Output: outMismatch,
		SHA256: verify.SHA256(outMismatch),
	}, http.StatusOK, nil)

	resp := decode[metricsAdminResponse](t, f.request(t, "GET", "/admin/v1/metrics?scope=tp", adminToken, nil, http.StatusOK, nil))
	if resp.Counters["verify_rerun_pass"] != 1 {
		t.Fatalf("verify_rerun_pass = %v, want 1 (counters=%v)", resp.Counters["verify_rerun_pass"], resp.Counters)
	}
	if resp.Counters["verify_rerun_fail"] != 1 {
		t.Fatalf("verify_rerun_fail = %v, want 1 (counters=%v)", resp.Counters["verify_rerun_fail"], resp.Counters)
	}

	promRec := doMetricsHTTP(t, f.handler, "GET", "/metrics", "127.0.0.1:41009", "", nil)
	if promRec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d: %s", promRec.Code, promRec.Body.String())
	}
	parsed := parsePrometheusText(t, promRec.Body.String())
	if sum := sumFloatSlice(parsed.values["verify_rerun_pass"]); sum != 1 {
		t.Fatalf("Prometheus verify_rerun_pass = %v, want 1", sum)
	}
	if sum := sumFloatSlice(parsed.values["verify_rerun_fail"]); sum != 1 {
		t.Fatalf("Prometheus verify_rerun_fail = %v, want 1", sum)
	}
}
