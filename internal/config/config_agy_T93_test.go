// Path: internal/config/config_agy_T93_test.go | Author: GLM (z.ai) | 2026-11-21
// T93 (agy review, fixed in the T101 batch): Validate must not accept a blank
// or tenant-less leaseRedisPrefix. Validate takes the Config by value, so it
// cannot normalize the way Load does; a Config built directly or mutated by
// flags after Load would otherwise pass and later send unprefixed lease keys
// to the shared Redis.
package config

import "testing"

func TestValidateRejectsBlankLeaseRedisPrefix(t *testing.T) {
	for _, prefix := range []string{"", "   ", "\t"} {
		c := Default()
		c.LeaseRedisPrefix = prefix
		if err := c.Validate(); err == nil {
			t.Fatalf("Validate accepted leaseRedisPrefix %q", prefix)
		}
	}
	// Load keeps its documented behaviour: a blank prefix becomes the default.
	t.Setenv("POOLS_LEASE_REDIS_PREFIX", "")
	loaded, err := Load("")
	if err != nil {
		t.Fatalf("Load with a blank prefix: %v", err)
	}
	if loaded.LeaseRedisPrefix != "pools:" {
		t.Fatalf("loaded prefix = %q, want pools:", loaded.LeaseRedisPrefix)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("the normalized config does not validate: %v", err)
	}
}

func TestValidateRejectsTenantlessLeaseRedisPrefix(t *testing.T) {
	for _, prefix := range []string{":", ":pools", "_pool:", "-pool:"} {
		c := Default()
		c.LeaseRedisPrefix = prefix
		if err := c.Validate(); err == nil {
			t.Fatalf("Validate accepted leaseRedisPrefix %q", prefix)
		}
		t.Setenv("POOLS_LEASE_REDIS_PREFIX", prefix)
		if _, err := Load(""); err == nil {
			t.Fatalf("Load accepted leaseRedisPrefix %q", prefix)
		}
	}
	// A digit-led prefix stays valid.
	c := Default()
	c.LeaseRedisPrefix = "9tenant:"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate rejected 9tenant:: %v", err)
	}
}
