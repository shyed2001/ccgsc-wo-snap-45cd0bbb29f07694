// Path: internal/store/pg/conn_cap_R15_test.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-3 (TD-17) - the connection cap of the PostgreSQL pool. The
// owner rule is "never all threads": the pool of the database may not grow without a
// cap, and the cap is the configured dbMaxOpenConns.
package pg

import "testing"

// TestParsePoolConfigAppliesTheConnectionCap pins that the configured cap reaches the
// pgx pool.
func TestParsePoolConfigAppliesTheConnectionCap(t *testing.T) {
	cfg, err := ParsePoolConfig("postgres://user@127.0.0.1:5432/pools", 9)
	if err != nil {
		t.Fatalf("ParsePoolConfig: %v", err)
	}
	if cfg.MaxConns != 9 {
		t.Fatalf("MaxConns = %d, want 9", cfg.MaxConns)
	}
}

// TestParsePoolConfigKeepsTheDefaultWithoutACap pins that a cap of zero keeps the pgx
// default (the caller of the server always passes a resolved cap).
func TestParsePoolConfigKeepsTheDefaultWithoutACap(t *testing.T) {
	cfg, err := ParsePoolConfig("postgres://user@127.0.0.1:5432/pools", 0)
	if err != nil {
		t.Fatalf("ParsePoolConfig: %v", err)
	}
	if cfg.MaxConns <= 0 {
		t.Fatalf("MaxConns = %d, want the pgx default", cfg.MaxConns)
	}
}

// TestParsePoolConfigRefusesAnEmptyDSN pins that an empty DSN is an error, never a
// silent fallback to another database.
func TestParsePoolConfigRefusesAnEmptyDSN(t *testing.T) {
	if _, err := ParsePoolConfig("", 4); err == nil {
		t.Fatal("ParsePoolConfig accepted an empty DSN")
	}
}
