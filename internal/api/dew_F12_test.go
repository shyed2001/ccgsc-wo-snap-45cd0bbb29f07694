package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
)

// This runs the real contributor settlement code in one continuing JS process,
// over HTTP and production auth, while the server AND its SQLite connection are
// stopped/reopened. No browser reload or workload execution is simulated.
func TestF12DewClientReconnectPersistedServer(t *testing.T) {
	for _, p := range []string{pool.Thread, pool.Process} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired=%t", p, expired), func(t *testing.T) {
				ctx := context.Background()
				dbPath := filepath.Join(t.TempDir(), "dew.db")
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				now := time.Now().UTC().Truncate(time.Second)
				open := func() (*Server, error) {
					db, err := sqlite.Open(dbPath)
					if err != nil {
						return nil, err
					}
					e, err := pool.New(db, logger, "f12", time.Minute)
					if err != nil {
						_ = db.Close()
						return nil, err
					}
					e.Now = func() time.Time { return now }
					e.Verifier.SetClock(func() time.Time { return now })
					cfg := config.Default()
					return &Server{Engine: e, Auth: auth.New(db, cfg.JWTSecret, "f12"), Config: cfg, Log: logger}, nil
				}
				s, err := open()
				if err != nil {
					t.Fatal(err)
				}
				token := bearerKey(t, s.Auth, "B", "contributor")
				data := []byte("F12 transport fixture; workload execution is outside this test")
				id := verify.SHA256(data)
				if _, err := s.Engine.Upload(ctx, id, "", 0, int64(len(data)), data); err != nil {
					t.Fatal(err)
				}
				zero := 0.0
				j, err := s.Engine.Create(ctx, p, "developer", "", pool.CreateRequest{
					ModuleArtifactIDs: []string{id}, RerunPercent: &zero,
					Tasks: []pool.TaskSpec{{TaskID: "f12-task", Input: json.RawMessage(`{"json":{"n":1}}`)}},
				})
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(s.Handler())
				base := server.URL
				// Control is test-only and separate from the stopped production API.
				control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/disconnect":
						server.Close()
						if err := s.Engine.Store.Close(); err != nil {
							http.Error(w, "close failed", 500)
							return
						}
						w.WriteHeader(204)
					case "/reconnect":
						// F12 (G-F12-5): "expired" means past the 1 min lease TTL
						// but still inside the 10 min receipt window, so the
						// sweep must not have re-assigned the task and the
						// retained result settles as late, never rejected.
						if expired {
							now = now.Add(2 * time.Minute)
						}
						var err error
						s, err = open()
						if err != nil {
							http.Error(w, "reopen failed", 500)
							return
						}
						if expired {
							if err := s.Engine.Sweep(ctx); err != nil {
								http.Error(w, "sweep failed", 500)
								return
							}
						}
						server = httptest.NewServer(s.Handler())
						_ = json.NewEncoder(w).Encode(map[string]string{"base": server.URL})
					default:
						http.NotFound(w, r)
					}
				}))
				t.Cleanup(func() { control.Close(); server.Close(); _ = s.Engine.Store.Close() })
				input, _ := json.Marshal(map[string]any{"base": base, "control": control.URL, "token": token, "pool": p, "expired": expired})
				runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(runCtx, "node", "../../web/pages/tests/fixtures/dew_F12_client.mjs")
				cmd.Stdin = strings.NewReader(string(input))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("contributor reconnect: %v\n%s", err, out)
				}
				t.Log(strings.TrimSpace(string(out)))
				got, err := s.Engine.Get(ctx, p, "developer", j.JobID)
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Results) != 1 || got.State != "completed" || got.Results[0].Late != expired {
					t.Fatalf("settled result count=%d state=%s", len(got.Results), got.State)
				}
				if n := s.Engine.Metering().CompletedTasks; n != 1 {
					t.Fatalf("completed counter=%d, want 1", n)
				}
			})
		}
	}
}
