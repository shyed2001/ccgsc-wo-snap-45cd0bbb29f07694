// Path: tests/core_test.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: offline end-to-end and vital-system tests for WO-CORE-00, WWPP-R32.4/7/9, R37.1.
// WO-T2.2 (hermes MiniMax-M3, 2026-11-17): the static-page expectations now follow the
// /pools/ routes; the page acceptance itself lives in tests/pages_test.go.
package tests

import (
	"bytes"
	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/logx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	db      *sqlite.DB
	engine  *pool.Engine
	auth    *auth.Service
	handler http.Handler
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db, e := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, e := pool.New(db, l, "s1", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	a := auth.New(db, config.Default().JWTSecret, "s1")
	s := &api.Server{Engine: engine, Auth: a, Log: l}
	return &fixture{db, engine, a, s.Handler()}
}
func (f *fixture) request(t *testing.T, method, path, token string, body any, want int, headers map[string]string) []byte {
	t.Helper()
	var b []byte
	switch v := body.(type) {
	case []byte:
		b = v
	case nil:
	default:
		var e error
		b, e = json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	if want >= 400 && w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatal("missing RFC 9457 content type")
	}
	return w.Body.Bytes()
}
func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if e := json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func (f *fixture) key(t *testing.T, role string) (string, string) {
	t.Helper()
	account := "B"
	if role == "admin" || role == "director" {
		account = "A"
	}
	raw, _, e := f.auth.CreateKey(context.Background(), auth.User{ID: auth.ID(), AccountStore: account, Roles: []string{role}}, []string{"*"})
	if e != nil {
		t.Fatal(e)
	}
	token, e := f.auth.Exchange(context.Background(), raw, nil)
	if e != nil {
		t.Fatal(e)
	}
	return raw, token.AccessToken
}
func (f *fixture) artifact(t *testing.T, base, token string) string {
	t.Helper()
	b := []byte("debug module fixture; never executed by server")
	id := verify.SHA256(b)
	f.request(t, "PUT", base+"/artifacts", token, b, 201, map[string]string{"X-Artifact-Sha256": id, "Upload-Offset": "0", "Upload-Length": fmt.Sprint(len(b))})
	return id
}

// The complete required acceptance flow uses the real HTTP handler, auth, engine and
// file-backed SQLite. In-memory HTTP recorders avoid any network listener.
func TestEndToEndMapThreeTasks(t *testing.T) {
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		t.Run(base, func(t *testing.T) {
			f := setup(t)
			_, admin := f.key(t, "admin")
			issued := decode[struct {
				APIKey string `json:"apiKey"`
			}](t, f.request(t, "POST", base+"/auth/keys", admin, map[string]any{"user": auth.User{ID: "developer", AccountStore: "B", Roles: []string{"developer"}}, "scopes": []string{"artifacts:write", "jobs:write", "jobs:read"}}, 201, nil))
			token := decode[auth.Token](t, f.request(t, "POST", base+"/auth/token", "", map[string]any{"apiKey": issued.APIKey}, 200, nil))
			if token.ExpiresIn != 900 {
				t.Fatal("wrong JWT lifetime")
			}
			_, workerToken := f.key(t, "contributor")
			module := f.artifact(t, base, token.AccessToken)
			zeroRerun := 0.0
			q := pool.CreateRequest{ModuleArtifactIDs: []string{module}, Tasks: []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}, {TaskID: "two", Input: json.RawMessage(`{"json":{"n":2}}`)}, {TaskID: "three", Input: json.RawMessage(`{"json":{"n":3}}`)}}, BatchSize: 3, RerunPercent: &zeroRerun}
			j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", token.AccessToken, q, 201, map[string]string{"Idempotency-Key": "map-1"}))
			again := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", token.AccessToken, q, 201, map[string]string{"Idempotency-Key": "map-1"}))
			if again.JobID != j.JobID {
				t.Fatal("idempotency created another job")
			}
			w := decode[pool.Worker](t, f.request(t, "POST", base+"/workers/register", workerToken, map[string]any{"hostId": "fake-host", "slots": 3}, 201, nil))
			var last pool.Assignment
			batch := decode[pool.Assignment](t, f.request(t, "POST", base+"/workers/"+w.TabID+"/leases", workerToken, nil, 200, nil))
			if len(batch.Batch) != 3 {
				t.Fatalf("batch size = %d, want 3", len(batch.Batch))
			}
			for _, member := range batch.Batch {
				last = member
				if !verify.VerifyEnvelope(last.Envelope, f.engine.Signer.Public) {
					t.Fatal("invalid task signature")
				}
				f.request(t, "POST", base+"/workers/"+w.TabID+"/results", workerToken, pool.Submission{Lease: last.Lease, TaskEnvelope: last.TaskEnvelope, LeaseReceipt: last.LeaseReceipt, Output: json.RawMessage(`{"json":{"answer":42}}`), SHA256: verify.SHA256([]byte(`{"json":{"answer":42}}`))}, 200, nil)
			}
			j = decode[pool.Job](t, f.request(t, "GET", base+"/jobs/"+j.JobID, token.AccessToken, nil, 200, nil))
			if j.State != "completed" || j.Progress.Completed != 3 {
				t.Fatalf("bad completion: %+v", j)
			}
			page := decode[struct {
				Items      []pool.Result `json:"items"`
				Order      string        `json:"order"`
				NextCursor string        `json:"nextCursor"`
				HasMore    bool          `json:"hasMore"`
			}](t, f.request(t, "GET", base+"/jobs/"+j.JobID+"/results?limit=2", token.AccessToken, nil, 200, nil))
			if len(page.Items) != 2 || !page.HasMore || page.Order != "as_completed" {
				t.Fatalf("bad page: %+v", page)
			}
			page2 := decode[struct {
				Items []pool.Result `json:"items"`
			}](t, f.request(t, "GET", base+"/jobs/"+j.JobID+"/results?cursor="+page.NextCursor, token.AccessToken, nil, 200, nil))
			if len(page2.Items) != 1 {
				t.Fatal("cursor lost result")
			}
			f.request(t, "POST", base+"/workers/"+w.TabID+"/results", workerToken, pool.Submission{Lease: last.Lease, TaskEnvelope: last.TaskEnvelope, LeaseReceipt: last.LeaseReceipt, Output: json.RawMessage(`{}`), SHA256: verify.SHA256([]byte(`{}`))}, 409, nil)
			otherBase := "/thread-pool/v1"
			if base == otherBase {
				otherBase = "/process-pool/v1"
			}
			f.request(t, "GET", otherBase+"/jobs/"+j.JobID, token.AccessToken, nil, 403, nil)
			_, stranger := f.key(t, "developer")
			f.request(t, "GET", base+"/jobs/"+j.JobID, stranger, nil, 403, nil)
			// Replay from the durable event cursor, without opening a socket.
			ctx, cancel := context.WithCancel(context.Background())
			r := httptest.NewRequest("GET", base+"/events?types=task.done", nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+token.AccessToken)
			sink := &streamRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, stopAfter: 3}
			f.handler.ServeHTTP(sink, r)
			if strings.Count(sink.Body.String(), "event: task.done") != 3 {
				t.Fatal("missing SSE completion events")
			}
			var dead []store.Record
			if e := f.db.View(context.Background(), func(rd store.Reader) error { var e error; dead, e = rd.List(store.DeadLetters); return e }); e != nil || len(dead) != 1 {
				t.Fatalf("dead letter missing: %v", e)
			}
		})
	}
}

type streamRecorder struct {
	*httptest.ResponseRecorder
	cancel    context.CancelFunc
	stopAfter int
}

func (w *streamRecorder) Flush() {
	w.ResponseRecorder.Flush()
	if strings.Count(w.Body.String(), "event: task.done") >= w.stopAfter {
		w.cancel()
	}
}

func TestAuthRolesScopesExpiryRevocation(t *testing.T) {
	f := setup(t)
	raw, token := f.key(t, "developer")
	ctx := context.Background()
	c, e := f.auth.Validate(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	small, e := f.auth.Exchange(ctx, raw, []string{"jobs:read"})
	if e != nil {
		t.Fatal(e)
	}
	f.request(t, "POST", "/thread-pool/v1/jobs", small.AccessToken, map[string]any{}, 403, nil)
	f.request(t, "POST", "/thread-pool/v1/auth/keys", token, map[string]any{}, 403, nil)
	_, _, e = f.auth.CreateKey(ctx, auth.User{ID: "bad", AccountStore: "B", Roles: []string{"admin"}}, nil)
	if e == nil {
		t.Fatal("cross-store role accepted")
	}
	refreshed := decode[auth.Token](t, f.request(t, "POST", "/thread-pool/v1/auth/refresh", "", map[string]any{"apiKey": raw}, 200, nil))
	if refreshed.AccessToken == token {
		t.Fatal("refresh reused jti")
	}
	now := time.Now()
	f.auth.Now = func() time.Time { return now.Add(16 * time.Minute) }
	if _, e = f.auth.Validate(ctx, token); e == nil {
		t.Fatal("expired token accepted")
	}
	f.auth.Now = time.Now
	if e = f.auth.Revoke(ctx, c.KeyID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.auth.Validate(ctx, refreshed.AccessToken); e == nil {
		t.Fatal("revoked key token accepted")
	}
	if _, e = f.auth.Exchange(ctx, raw, nil); e == nil {
		t.Fatal("revoked key refreshed")
	}
}
func TestChunkUploadAndHash(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := []byte("abcdef")
	id := verify.SHA256(b)
	if _, e := f.engine.Upload(ctx, id, "application/octet-stream", 2, 6, b[:2]); !errors.Is(e, pool.ErrConflict) {
		t.Fatalf("out-of-order upload: %v", e)
	}
	a, e := f.engine.Upload(ctx, id, "application/octet-stream", 0, 6, b[:3])
	if e != nil || a.Complete || a.NextOffset != 3 {
		t.Fatalf("chunk: %v %+v", e, a)
	}
	if _, e = f.engine.Upload(ctx, id, "application/octet-stream", 0, 6, b[:3]); e != nil {
		t.Fatal(e)
	}
	if _, e = f.engine.Upload(ctx, id, "application/octet-stream", 3, 6, []byte("xxx")); !errors.Is(e, pool.ErrInvalid) {
		t.Fatal("tamper accepted")
	}
	a, e = f.engine.Upload(ctx, id, "application/octet-stream", 3, 6, b[3:])
	if e != nil || !a.Complete {
		t.Fatalf("complete: %v", e)
	}
	loaded, e := f.engine.Artifact(ctx, id)
	if e != nil || !bytes.Equal(loaded.Data, b) {
		t.Fatal("bytes changed")
	}
}

func TestEnginePreservesRawOutputBytes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	j, w := seed(t, f, 1)
	assignment, e := f.engine.Claim(ctx, pool.Thread, "contributor", w.TabID)
	if e != nil {
		t.Fatal(e)
	}
	raw := json.RawMessage("{ \n  \"answer\": 42,\n  \"ok\": true\n}")
	if _, e = f.engine.Submit(ctx, pool.Thread, "contributor", w.TabID, pool.Submission{
		Lease:        assignment.Lease,
		TaskEnvelope: assignment.TaskEnvelope, LeaseReceipt: assignment.LeaseReceipt,
		Output: raw,
		SHA256: verify.SHA256(raw),
	}); e != nil {
		t.Fatal(e)
	}

	var stored pool.Job
	if e = f.db.View(ctx, func(rd store.Reader) error {
		return rd.Get(store.Jobs, j.JobID, &stored)
	}); e != nil {
		t.Fatal(e)
	}
	if len(stored.Results) != 1 {
		t.Fatalf("stored results = %d, want 1", len(stored.Results))
	}
	if !bytes.Equal(stored.Results[0].Output, raw) {
		t.Fatalf("stored output = %q, want %q", stored.Results[0].Output, raw)
	}
	if got := verify.SHA256(stored.Results[0].Output); got != stored.Results[0].SHA256 {
		t.Fatalf("stored output sha256 = %s, want %s", got, stored.Results[0].SHA256)
	}
}

func seed(t *testing.T, f *fixture, n int) (pool.Job, pool.Worker) {
	return seedWithOptions(t, f, n, nil)
}

func seedWithOptions(t *testing.T, f *fixture, n int, options json.RawMessage) (pool.Job, pool.Worker) {
	t.Helper()
	ctx := context.Background()
	data := []byte("module")
	id := verify.SHA256(data)
	if _, e := f.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatal(e)
	}
	zeroRerun := 0.0
	q := pool.CreateRequest{ModuleArtifactIDs: []string{id}, RerunPercent: &zeroRerun, Options: options}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, pool.TaskSpec{TaskID: fmt.Sprint(i)})
	}
	j, e := f.engine.Create(ctx, pool.Thread, "developer", "", q)
	if e != nil {
		t.Fatal(e)
	}
	w, e := f.engine.Register(ctx, pool.Thread, "contributor", pool.Worker{HostID: "host", Slots: 1})
	if e != nil {
		t.Fatal(e)
	}
	return j, w
}
func TestFenceRetryQuarantineAndRestart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now()
	f.engine.Now = func() time.Time { return now }
	// G-F12-5: this test is specifically the 60-second heartbeat fence; turn
	// Dew off so re-assignment remains valid at that boundary.
	j, w := seedWithOptions(t, f, 1, json.RawMessage(`{"dew":{"enabled":false}}`))
	a, e := f.engine.Claim(ctx, pool.Thread, "contributor", w.TabID)
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(61 * time.Second)
	if _, e = f.engine.Heartbeat(ctx, pool.Thread, "contributor", w.TabID, &a.Lease); !errors.Is(e, pool.ErrConflict) {
		t.Fatal("expired renewal accepted")
	}
	b, e := f.engine.Claim(ctx, pool.Thread, "contributor", w.TabID)
	if e != nil || b.Lease.Generation <= a.Lease.Generation {
		t.Fatalf("reassign fence: %v", e)
	}
	if _, e = f.engine.Submit(ctx, pool.Thread, "contributor", w.TabID, pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: json.RawMessage(`{}`), SHA256: verify.SHA256([]byte(`{}`))}); !errors.Is(e, pool.ErrConflict) {
		t.Fatal("stale settlement accepted")
	}
	// A fresh engine shares the durable generation, not an in-memory counter.
	oldEngine := f.engine
	newEngine, e := pool.New(f.db, f.engine.Log, "s1", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = newEngine.Verifier.Keyring().Add(oldEngine.TaskKeyID, oldEngine.TaskKey.Public().(ed25519.PublicKey)); e != nil {
		t.Fatal(e)
	}
	newEngine.Now = func() time.Time { return now }
	f.engine = newEngine
	if _, e = f.engine.Submit(ctx, pool.Thread, "contributor", w.TabID, pool.Submission{Lease: b.Lease, TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt, Error: "crash"}); e != nil {
		t.Fatal(e)
	}
	c, e := f.engine.Claim(ctx, pool.Thread, "contributor", w.TabID)
	if e != nil || c.Lease.Generation != 3 {
		t.Fatalf("restart generation: %v", e)
	}
	if _, e = f.engine.Submit(ctx, pool.Thread, "contributor", w.TabID, pool.Submission{Lease: c.Lease, TaskEnvelope: c.TaskEnvelope, LeaseReceipt: c.LeaseReceipt, Error: "crash"}); e != nil {
		t.Fatal(e)
	}
	j, e = f.engine.Get(ctx, pool.Thread, "developer", j.JobID)
	if e != nil || !j.Tasks[0].Quarantined || j.State != "partial" {
		t.Fatalf("poison not quarantined: %v %+v", e, j)
	}
}
func TestConcurrentClaimsAndSettlement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	j, w := seed(t, f, 1)
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := []pool.Assignment{}
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			a, e := f.engine.Claim(ctx, pool.Thread, "contributor", w.TabID)
			if e == nil {
				mu.Lock()
				claims = append(claims, a)
				mu.Unlock()
			} else if !errors.Is(e, pool.ErrEmpty) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if len(claims) != 1 {
		t.Fatalf("duplicate grants: %d", len(claims))
	}
	accepted := 0
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			_, e := f.engine.Submit(ctx, pool.Thread, "contributor", w.TabID, pool.Submission{Lease: claims[0].Lease, TaskEnvelope: claims[0].TaskEnvelope, LeaseReceipt: claims[0].LeaseReceipt, Output: json.RawMessage(`{}`), SHA256: verify.SHA256([]byte(`{}`))})
			if e == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			} else if !errors.Is(e, pool.ErrConflict) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("accepted %d results", accepted)
	}
	j, e := f.engine.Get(ctx, pool.Thread, "developer", j.JobID)
	if e != nil || len(j.Results) != 1 {
		t.Fatal("unique result invariant failed")
	}
}
func TestSessionsReserveDrainAndExpire(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now()
	f.engine.Now = func() time.Time { return now }
	data := []byte("module")
	id := verify.SHA256(data)
	if _, e := f.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatal(e)
	}
	w, e := f.engine.Register(ctx, pool.Process, "contributor", pool.Worker{HostID: "host", Slots: 2})
	if e != nil {
		t.Fatal(e)
	}
	s, e := f.engine.OpenSession(ctx, "developer", "session-key", pool.SessionRequest{TabCount: 1, ModuleArtifactIDs: []string{id}})
	if e != nil || s.HostCount != 1 || s.Tabs[0].TabID != w.TabID {
		t.Fatalf("session: %v %+v", e, s)
	}
	zeroRerun := 0.0
	j, e := f.engine.SessionTasks(ctx, "developer", s.SessionID, "", pool.CreateRequest{Tasks: []pool.TaskSpec{{TaskID: "a"}}, RerunPercent: &zeroRerun})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.engine.CloseSession(ctx, "developer", s.SessionID, true); e != nil {
		t.Fatal(e)
	}
	if _, e = f.engine.SessionTasks(ctx, "developer", s.SessionID, "", pool.CreateRequest{}); !errors.Is(e, pool.ErrConflict) {
		t.Fatal("draining session accepted work")
	}
	a, e := f.engine.Claim(ctx, pool.Process, "contributor", w.TabID)
	if e != nil || a.Lease.JobID != j.JobID {
		t.Fatalf("session claim: %v", e)
	}
	if _, e = f.engine.Submit(ctx, pool.Process, "contributor", w.TabID, pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: json.RawMessage(`{}`), SHA256: verify.SHA256([]byte(`{}`))}); e != nil {
		t.Fatal(e)
	}
	if e = f.engine.Sweep(ctx); e != nil {
		t.Fatal(e)
	}
	s, e = f.engine.GetSession(ctx, "developer", s.SessionID)
	if e != nil || s.State != "closed" {
		t.Fatalf("drain: %v %+v", e, s)
	}
	s, e = f.engine.OpenSession(ctx, "developer", "", pool.SessionRequest{TabCount: 1, TTLSeconds: 30})
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(31 * time.Second)
	s, e = f.engine.GetSession(ctx, "developer", s.SessionID)
	if e != nil || s.State != "expired" {
		t.Fatal("session expiry failed")
	}
}
func TestStoreRollbackPersistenceAndPGStub(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, e := sqlite.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	e = db.Update(ctx, func(tx store.Tx) error {
		if e := tx.Put(store.ConfigVersions, "one", map[string]any{"version": 1}); e != nil {
			return e
		}
		return pool.ErrInvalid
	})
	if !errors.Is(e, pool.ErrInvalid) {
		t.Fatal(e)
	}
	e = db.View(ctx, func(rd store.Reader) error { var v any; return rd.Get(store.ConfigVersions, "one", &v) })
	if !errors.Is(e, store.ErrNotFound) {
		t.Fatal("rollback failed")
	}
	if e = db.Update(ctx, func(tx store.Tx) error { return tx.Put(store.Audit, "one", map[string]any{"event": "test"}) }); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = sqlite.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.View(ctx, func(rd store.Reader) error { var v any; return rd.Get(store.Audit, "one", &v) }); e != nil {
		t.Fatal("persistence failed:", e)
	}
	if _, e = pg.Open(""); e == nil {
		t.Fatal("pg Open must reject an empty DSN")
	}
}
func TestConfigurationLoggingAndStaticPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if e := os.WriteFile(path, []byte(`{"addr":"127.0.0.1:8000","leaseSeconds":30}`), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("POOLS_ADDR", "127.0.0.1:8001")
	c, e := config.Load(path)
	if e != nil || c.Addr != "127.0.0.1:8001" || !c.Debug || c.LeaseSeconds != 30 {
		t.Fatalf("config precedence: %v %+v", e, c)
	}
	dir := t.TempDir()
	logger, closer, e := logx.New(dir, true)
	if e != nil {
		t.Fatal(e)
	}
	logger.Debug("request", "path", "/healthz")
	if e = closer.Close(); e != nil {
		t.Fatal(e)
	}
	files, e := filepath.Glob(filepath.Join(dir, "pools-core-*.jsonl"))
	if e != nil || len(files) != 1 {
		t.Fatal("log file absent")
	}
	b, e := os.ReadFile(files[0])
	if e != nil || !json.Valid(bytes.TrimSpace(b)) {
		t.Fatal("invalid JSON log")
	}
	f := setup(t)
	for _, path := range []string{"/healthz", "/web/contribute/", "/web/nav.json", "/nav/nav.json"} {
		f.request(t, "GET", path, "", nil, 200, nil)
	}
	// WO-T2.2: the admin and monitor placeholders now redirect to the real page.
	for _, path := range []string{"/web/admin/", "/web/monitor/", "/pools/"} {
		f.request(t, "GET", path, "", nil, 307, nil)
	}
	f.request(t, "GET", "/thread-pool/v1/jobs/missing", "", nil, 401, nil)
	f.request(t, "GET", "/missing", "", nil, 404, nil)
}
