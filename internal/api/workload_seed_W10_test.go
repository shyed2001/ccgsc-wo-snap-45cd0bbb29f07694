// Path: internal/api/workload_seed_W10_test.go | W10 platform/workload split.
// What/why: the reference workload reaches the sandbox as a job module artifact, so
// the seed has to register the embedded kernel under its manifest digest, serve
// those exact bytes to a contributor, and stay idempotent across server starts.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/web"
)

// workloadSeedFixture boots an engine and a server against one SQLite file.
func workloadSeedFixture(t *testing.T) (*Server, *pool.Engine, *auth.Service) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, log, "seed", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	as := auth.New(db, config.Default().JWTSecret, "seed")
	return &Server{Engine: engine, Auth: as, Log: log, Config: config.Default()}, engine, as
}

// embeddedWorkload reads the kernel and its manifest out of the embed: these are
// the bytes the seed registers and the contributor fetches.
func embeddedWorkload(t *testing.T) ([]byte, workloadManifest) {
	t.Helper()
	root, err := web.WorkloadRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(root, "matmul/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest workloadManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(root, "matmul/"+manifest.Entry)
	if err != nil {
		t.Fatal(err)
	}
	return data, manifest
}

func TestW10KernelMatchesManifestPin(t *testing.T) {
	data, manifest := embeddedWorkload(t)
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != manifest.SHA256 {
		t.Fatalf("kernel digest %s does not match the manifest pin %s", got, manifest.SHA256)
	}
	if manifest.Kind == "" || manifest.ID == "" || manifest.Version != 1 {
		t.Fatalf("manifest is incomplete: %+v", manifest)
	}
	// The workload imports nothing from the platform (design section 2).
	if bytes.Contains(data, []byte("pages/js")) || bytes.Contains(data, []byte("platform/")) {
		t.Fatal("the kernel must not reference the platform")
	}
}

func TestW10SeedIsIdempotentAndServesTheArtifact(t *testing.T) {
	s, engine, as := workloadSeedFixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.SeedWorkloadArtifacts(); err != nil {
			t.Fatalf("seed run %d: %v", i+1, err)
		}
	}
	data, manifest := embeddedWorkload(t)
	stored, err := engine.Artifact(ctx, manifest.SHA256)
	if err != nil {
		t.Fatalf("artifact %s not registered: %v", manifest.SHA256, err)
	}
	if !bytes.Equal(stored.Data, data) {
		t.Fatal("the registered artifact is not the embedded kernel")
	}
	if stored.ContentType != workloadContentType {
		t.Fatalf("content type = %q, want %q", stored.ContentType, workloadContentType)
	}

	// A contributor fetches exactly those bytes over the real route, and the
	// developer can name the artifact as a module of a job.
	contribKey, _, err := as.CreateKey(ctx, auth.User{ID: "w10-contributor", AccountStore: "B", Roles: []string{"contributor"}}, auth.ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	token := exchangeToken(t, s, contribKey)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/thread-pool/v1/artifacts/"+manifest.SHA256, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("contributor artifact read = HTTP %d: %s", res.StatusCode, body)
	}
	if !bytes.Equal(body, data) {
		t.Fatal("the route did not serve the kernel bytes")
	}
	if got := res.Header.Get("X-Artifact-Sha256"); got != manifest.SHA256 {
		t.Fatalf("X-Artifact-Sha256 = %q, want %q", got, manifest.SHA256)
	}
}

// exchangeToken trades a raw API key for a bearer token through the real route.
func exchangeToken(t *testing.T, s *Server, key string) string {
	t.Helper()
	body, err := json.Marshal(struct {
		APIKey string `json:"apiKey"`
	}{key})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/thread-pool/v1/auth/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("token exchange = HTTP %d: %s", w.Code, w.Body.String())
	}
	var tok auth.Token
	if err := json.Unmarshal(w.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("invalid token response: %s", w.Body.String())
	}
	return tok.AccessToken
}
