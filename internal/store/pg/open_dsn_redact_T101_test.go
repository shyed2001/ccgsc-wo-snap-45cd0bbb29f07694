// Path: internal/store/pg/open_dsn_redact_T101_test.go | Author: GLM (z.ai) | 2026-11-21
// T101 (cmdc leak sweep): a PostgreSQL DSN may carry a password, and pgx
// parse errors echo the whole connection string. OpenPool must never return
// an error whose text still contains the password. Both accepted DSN forms
// are exercised with parse-time failures, so the test needs no PostgreSQL.
package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpenPoolRedactsPasswordInParseError(t *testing.T) {
	cases := []struct{ name, dsn string }{
		{"url form", "postgres://pools:super-secret-pw@db.example.com:5432/pools?sslmode=bogus"},
		{"keyword form", "host=db.example.com user=pools password=super-secret-pw dbname=pools sslmode=bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := OpenPool(context.Background(), tc.dsn)
			if err == nil {
				t.Fatal("OpenPool accepted an invalid DSN")
			}
			msg := err.Error()
			if strings.Contains(msg, "super-secret-pw") {
				t.Fatalf("OpenPool error still carries the password: %q", msg)
			}
			if strings.Contains(msg, tc.dsn) {
				t.Fatalf("OpenPool error still echoes the raw DSN: %q", msg)
			}
			// Diagnosis stays possible: host, user and the failure point.
			if !strings.Contains(msg, "db.example.com") || !strings.Contains(msg, "sslmode") {
				t.Fatalf("OpenPool error lost its diagnosis: %q", msg)
			}
			if !strings.Contains(msg, "open PostgreSQL") {
				t.Fatalf("OpenPool error lost its context: %q", msg)
			}
			if errors.Unwrap(err) == nil {
				t.Fatal("OpenPool error lost the wrapped pgx cause")
			}
		})
	}
}

func TestOpenPoolRedactsPasswordInPingError(t *testing.T) {
	// A syntactically valid DSN pointing at a closed port: the ping failure
	// path must be redacted the same way. No server is needed: nothing is
	// listening on port 1 of the loopback address in the test environment.
	dsn := "postgres://pools:super-secret-pw@127.0.0.1:1/pools?connect_timeout=1"
	_, err := OpenPool(context.Background(), dsn)
	if err == nil {
		t.Fatal("OpenPool connected to a closed port")
	}
	msg := err.Error()
	if strings.Contains(msg, "super-secret-pw") || strings.Contains(msg, dsn) {
		t.Fatalf("OpenPool ping error still carries the DSN or its password: %q", msg)
	}
}
