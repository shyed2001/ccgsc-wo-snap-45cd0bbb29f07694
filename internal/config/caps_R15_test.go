// Path: internal/config/caps_R15_test.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-3 (TD-17) - the four resource cap keys of the server config:
// maxProcs, memLimitMB, workerPool and dbMaxOpenConns. The keys come from the JSON file
// and from the environment, exactly like every other key of internal/config.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCapsKeysComeFromTheJSONFile pins the JSON spelling of the four cap keys.
func TestCapsKeysComeFromTheJSONFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"maxProcs":3,"memLimitMB":512,"workerPool":5,"dbMaxOpenConns":9}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MaxProcs != 3 {
		t.Fatalf("MaxProcs = %d, want 3", c.MaxProcs)
	}
	if c.MemLimitMB != 512 {
		t.Fatalf("MemLimitMB = %d, want 512", c.MemLimitMB)
	}
	if c.WorkerPool != 5 {
		t.Fatalf("WorkerPool = %d, want 5", c.WorkerPool)
	}
	if c.DBMaxOpenConns != 9 {
		t.Fatalf("DBMaxOpenConns = %d, want 9", c.DBMaxOpenConns)
	}
}

// TestCapsKeysComeFromTheEnvironment pins the environment overrides, the same way the
// other keys of internal/config work.
func TestCapsKeysComeFromTheEnvironment(t *testing.T) {
	t.Setenv("POOLS_MAX_PROCS", "4")
	t.Setenv("POOLS_MEM_LIMIT_MB", "768")
	t.Setenv("POOLS_WORKER_POOL", "6")
	t.Setenv("POOLS_DB_MAX_OPEN_CONNS", "11")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MaxProcs != 4 || c.MemLimitMB != 768 || c.WorkerPool != 6 || c.DBMaxOpenConns != 11 {
		t.Fatalf("caps = %+v, want 4/768/6/11", c.Caps())
	}
}

// TestCapsKeysDefaultToAuto pins that the zero value of every key means "the server
// decides" and is not an error.
func TestCapsKeysDefaultToAuto(t *testing.T) {
	c := Default()
	if c.MaxProcs != 0 || c.MemLimitMB != 0 || c.WorkerPool != 0 || c.DBMaxOpenConns != 0 {
		t.Fatalf("default caps = %+v, want every key zero (auto)", c.Caps())
	}
}

// TestCapsRejectInvalidValues pins that a negative cap is a clear configuration error
// and never a silent fallback.
func TestCapsRejectInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		want string
	}{
		{"negative memory limit", "POOLS_MEM_LIMIT_MB", "memLimitMB"},
		{"negative worker pool", "POOLS_WORKER_POOL", "workerPool"},
		{"negative connection cap", "POOLS_DB_MAX_OPEN_CONNS", "dbMaxOpenConns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, "-1")
			_, err := Load("")
			if err == nil {
				t.Fatalf("Load accepted %s=-1", tc.key)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want the key %q", err.Error(), tc.want)
			}
		})
	}
}

// TestCapsRejectANonNumericValue pins that a broken environment value is an error, never
// a zero.
func TestCapsRejectANonNumericValue(t *testing.T) {
	t.Setenv("POOLS_MAX_PROCS", "many")
	if _, err := Load(""); err == nil {
		t.Fatal("Load accepted POOLS_MAX_PROCS=many")
	}
}

// TestMaxProcsIsClampedNotRejected pins the clamp rule of maxProcs: the value survives
// the configuration load, the clamp happens where the runtime is applied.
func TestMaxProcsIsClampedNotRejected(t *testing.T) {
	for _, v := range []string{"0", "-3", "1024"} {
		t.Setenv("POOLS_MAX_PROCS", v)
		if _, err := Load(""); err != nil {
			t.Fatalf("Load rejected POOLS_MAX_PROCS=%s: %v", v, err)
		}
	}
}
