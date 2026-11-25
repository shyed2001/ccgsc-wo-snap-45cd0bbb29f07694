// Path: internal/config/legacy_pool_T92_test.go | Author: Codex | 2026-11-20
// What/why: enforce the localhost-only bridge at JSON/env configuration load.
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestT92LegacyPoolOrigins(t *testing.T) {
	for _, value := range []string{"", "http://127.0.0.1", "http://localhost:8081/", "http://[::1]:8081"} {
		t.Run("valid/"+value, func(t *testing.T) {
			t.Setenv("POOLS_LEGACY_POOL_URL", value)
			c, err := Load("")
			if err != nil || c.LegacyPoolURL != value {
				t.Fatalf("Load: %q, %v", c.LegacyPoolURL, err)
			}
		})
	}
	for _, value := range []string{"https://localhost", "http://example.com", "http://192.168.1.1", "http://127.0.0.2", "http://127.1", "http://localhost.evil", "http://localhost@evil", "http://user@localhost", "http://localhost/path", "http://localhost?", "http://localhost#", "http://localhost?x=1", "http://localhost:0", "http://localhost:65536", "http://localhost:", "http://::1", "http://[::ffff:127.0.0.1]", "http://[::1%25eth0]", "//localhost", " http://localhost", "http://localhost/%2f"} {
		t.Run("invalid/"+value, func(t *testing.T) {
			t.Setenv("POOLS_LEGACY_POOL_URL", value)
			if _, err := Load(""); err == nil {
				t.Fatalf("accepted %q", value)
			}
		})
	}
}

func TestT92LegacyPoolJSONAndEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"legacyPoolURL":"http://localhost:8081"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Preserve the real environment while exercising JSON without an override.
	value, present := os.LookupEnv("POOLS_LEGACY_POOL_URL")
	os.Unsetenv("POOLS_LEGACY_POOL_URL")
	t.Cleanup(func() {
		if present {
			os.Setenv("POOLS_LEGACY_POOL_URL", value)
		} else {
			os.Unsetenv("POOLS_LEGACY_POOL_URL")
		}
	})
	c, err := Load(path)
	if err != nil || c.LegacyPoolURL != "http://localhost:8081" {
		t.Fatalf("JSON: %q %v", c.LegacyPoolURL, err)
	}
	t.Setenv("POOLS_LEGACY_POOL_URL", "")
	c, err = Load(path)
	if err != nil || c.LegacyPoolURL != "" {
		t.Fatalf("empty env must disable: %q %v", c.LegacyPoolURL, err)
	}
	os.Unsetenv("POOLS_LEGACY_POOL_URL")
	if err := os.WriteFile(path, []byte(`{"legacyPoolURL":"http://example.com"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("non-loopback JSON accepted")
	}
}
