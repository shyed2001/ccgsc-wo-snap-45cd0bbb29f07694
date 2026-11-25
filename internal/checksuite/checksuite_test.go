// Path: internal/checksuite/checksuite_test.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: the whole T108 full check suite runs against an in-process server:
// SQLite engine, REST handler and h2c gRPC behind one httptest listener, with
// the anonymous join on its default limits and no network beyond the loopback.
package checksuite

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/grpcapi"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
)

// newSuiteServer boots the whole service in-process on one port: REST and h2c
// gRPC share the httptest server through grpcapi.Server.Mux, like the real
// cmd/pools-core wiring does.
func newSuiteServer(t *testing.T) (string, string) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "suite.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, log, "suite", time.Minute)
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	as := auth.New(db, config.Default().JWTSecret, "suite")
	raw, _, err := as.CreateKey(context.Background(), auth.User{
		ID: "suite-admin", AccountStore: "A", Roles: []string{"admin"},
	}, []string{"*"})
	if err != nil {
		t.Fatalf("admin key: %v", err)
	}
	rest := (&api.Server{Engine: engine, Auth: as, Log: log, Config: config.Default()}).Handler()
	gs := grpcapi.NewServer(grpcapi.New(engine, as))
	srv := httptest.NewServer(gs.Mux(rest))
	t.Cleanup(func() { gs.Stop(); srv.Close() })
	return srv.URL, raw
}

// suiteChecks asserts every check passed and that the named pool groups ran.
func suiteChecks(t *testing.T, rep Report, wantPools ...string) {
	t.Helper()
	if !rep.Pass {
		t.Fatalf("suite failed:\n%s", rep.Table())
	}
	groups := map[string]bool{}
	for _, c := range rep.Checks {
		if c.Status != StatusPass {
			t.Fatalf("check %s failed: %s\n%s", c.ID, c.Detail, rep.Table())
		}
		groups[c.Pool] = true
	}
	for _, want := range wantPools {
		if !groups[want] {
			t.Fatalf("no checks for %q:\n%s", want, rep.Table())
		}
	}
}

func TestFullSuiteBothPoolsInProcess(t *testing.T) {
	base, admin := newSuiteServer(t)
	rep, err := Run(context.Background(), Options{
		Base:        base,
		AdminKey:    admin,
		Timeout:     60 * time.Second,
		SettleDelay: 120 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	suiteChecks(t, rep, "thread", "process", "keys")

	// The 2 pools x 3 APIs matrix is complete: per pool REST submit, gRPC
	// submit, SSE watch and gRPC watch, plus the hash, anonymous and revoke
	// checks.
	perPool := map[string]map[string]bool{"thread": {}, "process": {}}
	for _, c := range rep.Checks {
		if names, ok := perPool[c.Pool]; ok {
			names[c.Name] = true
		}
	}
	for pool, names := range perPool {
		for _, want := range []string{checkRestSubmit, checkGRPCSubmit, checkSSEWatch, checkGRPCWatch, checkResultHash, checkAnonymous} {
			if !names[want] {
				t.Fatalf("pool %s misses check %s:\n%s", pool, want, rep.Table())
			}
		}
	}

	// The JSON report carries no secret material.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), admin) || strings.Contains(string(raw), "Bearer ") {
		t.Fatal("report leaks key material:\n" + string(raw))
	}

	// The report is writable and parseable.
	path := filepath.Join(t.TempDir(), "report.json")
	if err := WriteJSON(path, rep); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if json.Unmarshal(b, &back) != nil {
		t.Fatal("written report did not parse")
	}
}

func TestFullSuiteThreadOnly(t *testing.T) {
	base, admin := newSuiteServer(t)
	rep, err := Run(context.Background(), Options{
		Base: base, AdminKey: admin, Pools: []string{ThreadPrefix},
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	suiteChecks(t, rep, "thread")
	for _, c := range rep.Checks {
		if c.Pool == "process" {
			t.Fatalf("process pool ran in thread-only mode: %+v", c)
		}
	}
}

func TestTableRendersOneLinePerCheck(t *testing.T) {
	rep := Report{Pass: true, DurationMS: 42, Checks: []Check{
		{ID: "thread/rest-submit", Pool: "thread", Name: checkRestSubmit, Status: StatusPass, JobID: "s1.tp.abc"},
		{ID: "thread/sse-watch", Pool: "thread", Name: checkSSEWatch, Status: StatusFail, Detail: "no events"},
	}}
	table := rep.Table()
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("table = %q, want one line per check plus the summary", table)
	}
	if !strings.Contains(table, "PASS") || !strings.Contains(table, checkRestSubmit) || !strings.Contains(table, "FAIL") {
		t.Fatalf("table misses status or names: %q", table)
	}
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.Size != 160 || o.Timeout != 3*time.Minute || o.SettleDelay != 150*time.Millisecond {
		t.Fatalf("defaults = %+v", o)
	}
	if len(o.Pools) != 2 || o.Pools[0] != ThreadPrefix || o.Pools[1] != ProcessPrefix {
		t.Fatalf("default pools = %v", o.Pools)
	}
	if _, err := Run(context.Background(), Options{Base: "http://127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), "admin API key") {
		t.Fatalf("run without an admin key = %v, want the admin key error", err)
	}
}

func TestMatmulReferenceMatchesPublishedHash(t *testing.T) {
	out := matmulOutput(160)
	var got matmulResult
	if json.Unmarshal(out, &got) != nil {
		t.Fatal("matmul output did not parse")
	}
	if got.SHA256 != Matmul160ReferenceSHA256 {
		t.Fatalf("160^3 matmul hash = %s, want the published %s", got.SHA256, Matmul160ReferenceSHA256)
	}
}
