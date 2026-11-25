// Path: internal/api/artifact_owners_T100_test.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: REST tests of the T100 artifact ownership (owner decision
// 2026-11-21, option A) over the real handler, auth and SQLite store: two
// developers upload identical bytes and both submit jobs, a third developer
// without a reference is refused on read and use, the delete of one reference
// keeps the blob for the other, the contributor worker read rule is kept and
// a Store A admin reads every artifact.
package api

import (
	"bytes"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store/sqlite"
)

type t100Fixture struct {
	handler http.Handler
	auth    *auth.Service
	engine  *pool.Engine
}

func newT100Fixture(t *testing.T) *t100Fixture {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t100.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	authService := auth.New(db, config.Default().JWTSecret, "s1")
	server := &Server{Engine: engine, Auth: authService, Log: logger, Config: config.Default()}
	return &t100Fixture{handler: server.Handler(), auth: authService, engine: engine}
}

// t100Key issues an API key and exchanges it for a bearer token.
func (f *t100Fixture) t100Key(t *testing.T, role string) string {
	t.Helper()
	account := "B"
	if role == "admin" || role == "director" {
		account = "A"
	}
	raw, _, err := f.auth.CreateKey(context.Background(), auth.User{ID: "t100-" + role + "-" + auth.ID(), AccountStore: account, Roles: []string{role}}, []string{"*"})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	token, err := f.auth.Exchange(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	return token.AccessToken
}

func (f *t100Fixture) do(t *testing.T, method, path, token string, body []byte) (int, []byte) {
	t.Helper()
	return f.doHeaders(t, method, path, token, body, nil)
}

// doHeaders sends one request with explicit headers, so the chunked artifact
// upload headers travel with the PUT.
func (f *t100Fixture) doHeaders(t *testing.T, method, path, token string, body []byte, headers map[string]string) (int, []byte) {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, "http://s"+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/octet-stream")
	} else {
		req = httptest.NewRequest(method, "http://s"+path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// t100UploadHeaders are the single-chunk artifact upload headers of a body.
func t100UploadHeaders(body []byte) map[string]string {
	return map[string]string{
		"X-Artifact-Sha256": verify.SHA256(body),
		"Upload-Offset":     "0",
		"Upload-Length":     fmt.Sprintf("%d", len(body)),
	}
}

// t100Upload uploads the body with the developer token and expects success.
func (f *t100Fixture) t100Upload(t *testing.T, token string, body []byte) string {
	t.Helper()
	id := verify.SHA256(body)
	code, got := f.doHeaders(t, http.MethodPut, "/thread-pool/v1/artifacts", token, body, t100UploadHeaders(body))
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("PUT artifact status = %d: %s", code, got)
	}
	return id
}

// t100Job builds a one-task job request body for the module artifact id.
func t100Job(id, taskID string) []byte {
	job := map[string]any{
		"moduleArtifactIds": []string{id},
		"tasks":             []map[string]any{{"taskId": taskID, "input": map[string]any{"json": map[string]any{"n": 1}}}},
	}
	raw, _ := json.Marshal(job)
	return raw
}

func TestT100TwoDevelopersShareArtifactBytesREST(t *testing.T) {
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		t.Run(base, func(t *testing.T) {
			f := newT100Fixture(t)
			devA := f.t100Key(t, "developer")
			devB := f.t100Key(t, "developer")
			devC := f.t100Key(t, "developer")
			admin := f.t100Key(t, "admin")
			contrib := f.t100Key(t, "contributor")

			body := []byte("identical module bytes for both developers")
			id := verify.SHA256(body)

			// Both developers upload identical bytes; the first creates the blob
			// and the second joins it with its own reference.
			codeA, gotA := f.doHeaders(t, http.MethodPut, base+"/artifacts", devA, body, t100UploadHeaders(body))
			if codeA != http.StatusCreated {
				t.Fatalf("first upload = %d: %s", codeA, gotA)
			}
			codeB, gotB := f.doHeaders(t, http.MethodPut, base+"/artifacts", devB, body, t100UploadHeaders(body))
			if codeB != http.StatusOK && codeB != http.StatusCreated {
				t.Fatalf("second upload of identical bytes = %d: %s", codeB, gotB)
			}
			var viewB struct {
				ArtifactID string `json:"artifactId"`
				Complete   bool   `json:"complete"`
			}
			if err := json.Unmarshal(gotB, &viewB); err != nil || viewB.ArtifactID != id || !viewB.Complete {
				t.Fatalf("second upload view = %s (%v)", gotB, err)
			}

			// Both holders submit jobs with the shared artifact as the module.
			for name, token := range map[string]string{"a": devA, "b": devB} {
				req := httptest.NewRequest(http.MethodPost, "http://s"+base+"/jobs", bytes.NewReader(t100Job(id, "task-"+name)))
				req.Header.Set("Authorization", "Bearer "+token)
				rec := httptest.NewRecorder()
				f.handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusCreated {
					t.Fatalf("job of developer %s = %d: %s", name, rec.Code, rec.Body.String())
				}
			}

			// The third developer without a reference is refused on use and read.
			req := httptest.NewRequest(http.MethodPost, "http://s"+base+"/jobs", bytes.NewReader(t100Job(id, "task-c")))
			req.Header.Set("Authorization", "Bearer "+devC)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("job without a reference = %d, want 403: %s", rec.Code, rec.Body.String())
			}
			if code, _ := f.do(t, http.MethodGet, base+"/artifacts/"+id, devC, nil); code != http.StatusForbidden {
				t.Fatalf("GET artifact without a reference = %d, want 403", code)
			}

			// Holders and the admin read the blob; the contributor worker rule
			// stays as it is today.
			for name, token := range map[string]string{"a": devA, "b": devB, "admin": admin, "contributor": contrib} {
				code, got := f.do(t, http.MethodGet, base+"/artifacts/"+id, token, nil)
				if code != http.StatusOK || string(got) != string(body) {
					t.Fatalf("GET artifact by %s = %d %q, want the bytes", name, code, got)
				}
			}

			// The delete by one holder keeps the blob for the other.
			code, got := f.do(t, http.MethodDelete, base+"/artifacts/"+id, devA, nil)
			if code != http.StatusOK {
				t.Fatalf("DELETE by a = %d: %s", code, got)
			}
			var del struct {
				Removed             bool  `json:"removed"`
				BlobDeleted         bool  `json:"blobDeleted"`
				RemainingReferences int64 `json:"remainingReferences"`
			}
			if err := json.Unmarshal(got, &del); err != nil {
				t.Fatal(err)
			}
			if !del.Removed || del.BlobDeleted || del.RemainingReferences != 1 {
				t.Fatalf("delete by a = %+v, want one remaining reference", del)
			}
			if code, got := f.do(t, http.MethodGet, base+"/artifacts/"+id, devB, nil); code != http.StatusOK || string(got) != string(body) {
				t.Fatalf("GET by b after a's delete = %d %q, want the blob kept", code, got)
			}
			// The other holder deletes last: the blob goes away.
			code, got = f.do(t, http.MethodDelete, base+"/artifacts/"+id, devB, nil)
			if code != http.StatusOK {
				t.Fatalf("DELETE by b = %d: %s", code, got)
			}
			if err := json.Unmarshal(got, &del); err != nil {
				t.Fatal(err)
			}
			if !del.BlobDeleted || del.RemainingReferences != 0 {
				t.Fatalf("delete by b = %+v, want the blob deleted", del)
			}
			if code, _ := f.do(t, http.MethodGet, base+"/artifacts/"+id, devB, nil); code != http.StatusNotFound {
				t.Fatalf("GET after the last delete = %d, want 404", code)
			}
			// A job with the deleted artifact answers 404 like before T100.
			req = httptest.NewRequest(http.MethodPost, "http://s"+base+"/jobs", bytes.NewReader(t100Job(id, "task-c")))
			req.Header.Set("Authorization", "Bearer "+devA)
			rec = httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("job with a deleted artifact = %d, want 404: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestT100DeleteWithoutReferenceIsForbiddenREST(t *testing.T) {
	f := newT100Fixture(t)
	devA := f.t100Key(t, "developer")
	devC := f.t100Key(t, "developer")
	id := f.t100Upload(t, devA, []byte("delete guard bytes"))
	if code, _ := f.do(t, http.MethodDelete, "/thread-pool/v1/artifacts/"+id, devC, nil); code != http.StatusForbidden {
		t.Fatalf("DELETE without a reference = %d, want 403", code)
	}
	if code, _ := f.do(t, http.MethodGet, "/thread-pool/v1/artifacts/"+id, devA, nil); code != http.StatusOK {
		t.Fatalf("GET after a refused delete = %d, want the blob intact", code)
	}
}
