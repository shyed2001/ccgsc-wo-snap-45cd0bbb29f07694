package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestValidateTrustedProxyCIDRsWithoutAllowlist(t *testing.T) {
	c := Default()
	c.IPAllowlist = nil
	c.TrustedProxyCIDRs = []string{"not-a-cidr"}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted an invalid trusted proxy CIDR without an IP allowlist")
	}
}

func TestLeaseStoreDefaultPG(t *testing.T) {
	c := Default()
	if c.LeaseStore != "pg" {
		t.Fatalf("default leaseStore=%q, want pg", c.LeaseStore)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseStoreSwitchAtStartup(t *testing.T) {
	t.Setenv("POOLS_LEASE_STORE", "redis")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.LeaseStore != "redis" {
		t.Fatalf("POOLS_LEASE_STORE=redis got %q", c.LeaseStore)
	}
	t.Setenv("POOLS_LEASE_STORE", "pg")
	c, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.LeaseStore != "pg" {
		t.Fatalf("POOLS_LEASE_STORE=pg got %q", c.LeaseStore)
	}
	t.Setenv("POOLS_LEASE_STORE", "memory")
	if _, err = Load(""); err == nil {
		t.Fatal("invalid leaseStore accepted")
	}
}

func TestLeaseRedisDefaults(t *testing.T) {
	c := Default()
	if c.LeaseRedisDB != 0 {
		t.Fatalf("default leaseRedisDB=%d, want 0", c.LeaseRedisDB)
	}
	if c.LeaseRedisPrefix != "pools:" {
		t.Fatalf("default leaseRedisPrefix=%q, want pools:", c.LeaseRedisPrefix)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRedisJSONAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"leaseRedisDB":3,"leaseRedisPrefix":"tenant9:"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.LeaseRedisDB != 3 || c.LeaseRedisPrefix != "tenant9:" {
		t.Fatalf("json leaseRedis settings = %d %q, want 3 tenant9:", c.LeaseRedisDB, c.LeaseRedisPrefix)
	}
	if err := os.WriteFile(path, []byte(`{"leaseRedisDB":-1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("negative leaseRedisDB accepted")
	}
	if err := os.WriteFile(path, []byte(`{"leaseRedisPrefix":"bad\nprefix"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("leaseRedisPrefix with a control character accepted")
	}
}

// T93 item 1: the logical Redis database index must stay inside the stock
// Redis range 0..15 ("databases 16"); anything else fails the config load.
func TestLeaseRedisDBRange(t *testing.T) {
	for _, db := range []int{0, 1, 15} {
		t.Setenv("POOLS_LEASE_REDIS_DB", strconv.Itoa(db))
		c, err := Load("")
		if err != nil {
			t.Fatalf("POOLS_LEASE_REDIS_DB=%d: %v", db, err)
		}
		if c.LeaseRedisDB != db {
			t.Fatalf("POOLS_LEASE_REDIS_DB=%d got %d", db, c.LeaseRedisDB)
		}
	}
	for _, db := range []int{-1, 16, 99} {
		t.Setenv("POOLS_LEASE_REDIS_DB", strconv.Itoa(db))
		if _, err := Load(""); err == nil {
			t.Fatalf("POOLS_LEASE_REDIS_DB=%d accepted", db)
		}
	}
	// The same rule applies to the JSON file. The environment override is
	// unset first so the JSON value is what gets validated.
	os.Unsetenv("POOLS_LEASE_REDIS_DB")
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"leaseRedisDB":16}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("leaseRedisDB=16 accepted from JSON")
	}
	if err := os.WriteFile(path, []byte(`{"leaseRedisDB":15}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.LeaseRedisDB != 15 {
		t.Fatalf("leaseRedisDB=%d, want 15", c.LeaseRedisDB)
	}
}

// T93 item 1: one prefix policy. Blank means the documented default
// "pools:"; every other prefix must end with ":" and use only [a-z0-9_:-].
func TestLeaseRedisPrefixPolicy(t *testing.T) {
	for _, prefix := range []string{"", "   "} {
		t.Setenv("POOLS_LEASE_REDIS_PREFIX", prefix)
		c, err := Load("")
		if err != nil {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
		if c.LeaseRedisPrefix != "pools:" {
			t.Fatalf("prefix %q got %q, want pools:", prefix, c.LeaseRedisPrefix)
		}
	}
	for _, prefix := range []string{"tenant9:", "pools:", "a_b-c:1:", "x:"} {
		t.Setenv("POOLS_LEASE_REDIS_PREFIX", prefix)
		c, err := Load("")
		if err != nil {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
		if c.LeaseRedisPrefix != prefix {
			t.Fatalf("prefix %q got %q", prefix, c.LeaseRedisPrefix)
		}
	}
	for _, prefix := range []string{
		"pools",                               // no trailing separator
		"Pools:",                              // uppercase
		"pools lease:",                        // space
		"pools/*:",                            // glob
		"pools\n:",                            // control character
		"0123456789012345678901234567890123:", // 33 characters
	} {
		t.Setenv("POOLS_LEASE_REDIS_PREFIX", prefix)
		if _, err := Load(""); err == nil {
			t.Fatalf("prefix %q accepted", prefix)
		}
	}
	// A doubled separator is inside the charset and keeps the trailing ":".
	t.Setenv("POOLS_LEASE_REDIS_PREFIX", "pools::")
	c, err := Load("")
	if err != nil {
		t.Fatalf("prefix %q: %v", "pools::", err)
	}
	if c.LeaseRedisPrefix != "pools::" {
		t.Fatalf("prefix %q got %q", "pools::", c.LeaseRedisPrefix)
	}
	// A 32 character prefix is the documented maximum and stays valid.
	ok := "a" + strings.Repeat("b", 30) + ":"
	t.Setenv("POOLS_LEASE_REDIS_PREFIX", ok)
	c, err = Load("")
	if err != nil {
		t.Fatalf("prefix %q: %v", ok, err)
	}
	if c.LeaseRedisPrefix != ok {
		t.Fatalf("prefix %q got %q", ok, c.LeaseRedisPrefix)
	}
}
