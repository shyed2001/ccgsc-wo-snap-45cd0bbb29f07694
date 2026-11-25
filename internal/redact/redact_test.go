// Path: internal/redact/redact_test.go | Author: GLM (z.ai) | 2026-11-21
// T101: the redaction helper is the last line of defence for every error and
// log line that could carry a credential, so every form it claims to handle
// gets a test here: URL DSNs, keyword/value DSNs, quoted values, userinfo
// addresses and free-text wrapping.
package redact

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPasswordNeverShowsValue(t *testing.T) {
	if got := Password("hunter2"); got != Marker {
		t.Fatalf("Password(hunter2) = %q, want %q", got, Marker)
	}
	if got := Password(""); got != `""` {
		t.Fatalf("Password(empty) = %q, want %q", got, `""`)
	}
}

func TestTextReplacesEveryOccurrenceAndSkipsEmpty(t *testing.T) {
	got := Text("k1 and k1, k2", "k1", "")
	if got != Marker+" and "+Marker+", k2" {
		t.Fatalf("Text = %q", got)
	}
	if got := Text("unchanged"); got != "unchanged" {
		t.Fatalf("Text with no secrets changed %q", got)
	}
}

func TestWrapRedactsAndKeepsChain(t *testing.T) {
	if Wrap(nil, "secret") != nil {
		t.Fatal("Wrap(nil) is not nil")
	}
	base := errors.New("connect to postgres://u:secret@h/db failed")
	err := Wrap(base, "secret")
	if err.Error() != "connect to postgres://u:"+Marker+"@h/db failed" {
		t.Fatalf("Wrap message = %q", err.Error())
	}
	// The original error must stay reachable for errors.Is / errors.As.
	sentinel := errors.New("sentinel")
	wrapped := Wrap(fmt.Errorf("body: %w", sentinel), "secret")
	if !errors.Is(wrapped, sentinel) {
		t.Fatal("Wrap lost the error chain")
	}
}

func TestURLRedactsUserinfoAndPasswordParam(t *testing.T) {
	cases := []struct{ name, in, mustNotContain, wantContain string }{
		{"postgres url", "postgres://pools:secret@db.example.com:5432/pools", "secret", "postgres://pools:redacted@db.example.com:5432/pools"},
		{"redis url no user", "redis://:secret@127.0.0.1:6379", "secret", "redis://:redacted@127.0.0.1:6379"},
		{"no userinfo", "redis://127.0.0.1:6379", "", "redis://127.0.0.1:6379"},
		{"query password upper", "postgres://db.example.com/pools?PASSWORD=secret&sslmode=require", "secret", "PASSWORD=redacted"},
		{"query password lower", "postgres://db.example.com/pools?sslmode=require&password=secret", "secret", "password=redacted"},
		{"sslpassword untouched", "postgres://db.example.com/pools?sslpassword=secret", "", "sslpassword=secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := URL(tc.in)
			if tc.mustNotContain != "" && strings.Contains(got, tc.mustNotContain) {
				t.Fatalf("URL = %q still carries the secret", got)
			}
			if tc.wantContain != "" && !strings.Contains(got, tc.wantContain) {
				t.Fatalf("URL = %q, want it to contain %q", got, tc.wantContain)
			}
		})
	}
	// Unparsable input falls back to the userinfo removal, never a raw secret.
	got := URL("redis://user:sec ret@h:1") // raw space makes url.Parse fail
	if strings.Contains(got, "sec ret") {
		t.Fatalf("fallback URL = %q still carries the secret", got)
	}
}

func TestDSNRedactsBothForms(t *testing.T) {
	cases := []struct{ name, in, mustNotContain, wantContain string }{
		{"url form", "postgres://pools:secret@db:5432/pools", "secret", "postgres://pools:redacted@db:5432/pools"},
		{"keyword bare", "host=db user=pools password=secret dbname=pools sslmode=require", "secret", "password=" + Marker},
		{"keyword quoted", "host=db password='sec ret' user=pools", "sec ret", "password=" + Marker},
		{"keyword upper", "host=db PASSWORD=secret", "secret", "PASSWORD=" + Marker},
		{"sslpassword untouched", "host=db sslpassword=secret", "", "sslpassword=secret"},
		{"passfile untouched", "host=db passfile=secret.pem", "", "passfile=secret.pem"},
		{"value at end", "host=db password=secret", "secret", "password=" + Marker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DSN(tc.in)
			if tc.mustNotContain != "" && strings.Contains(got, tc.mustNotContain) {
				t.Fatalf("DSN = %q still carries the secret", got)
			}
			if tc.wantContain != "" && !strings.Contains(got, tc.wantContain) {
				t.Fatalf("DSN = %q, want it to contain %q", got, tc.wantContain)
			}
		})
	}
	if got := DSN(""); got != "" {
		t.Fatalf("DSN(empty) = %q", got)
	}
}

func TestDSNSecretsFindsEveryForm(t *testing.T) {
	if got := DSNSecrets(""); got != nil {
		t.Fatalf("DSNSecrets(empty) = %v", got)
	}
	got := DSNSecrets("postgres://pools:secret@db:5432/pools?password=querysecret")
	if len(got) != 2 || got[0] != "secret" || got[1] != "querysecret" {
		t.Fatalf("DSNSecrets(url) = %v", got)
	}
	got = DSNSecrets("host=db password='sec ret' user=u")
	if len(got) != 1 || got[0] != "sec ret" {
		t.Fatalf("DSNSecrets(keyword quoted) = %v", got)
	}
	got = DSNSecrets("host=db password=secret")
	if len(got) != 1 || got[0] != "secret" {
		t.Fatalf("DSNSecrets(keyword bare) = %v", got)
	}
}

func TestRedactDSNErrorKillsEchoAndKeepsChain(t *testing.T) {
	if RedactDSNError(nil, "postgres://u:secret@h/db") != nil {
		t.Fatal("RedactDSNError(nil) is not nil")
	}
	dsn := "postgres://pools:secret@db.example.com:5432/pools"
	base := fmt.Errorf("cannot parse `%s`: invalid dsn", dsn)
	err := RedactDSNError(base, dsn)
	msg := err.Error()
	if strings.Contains(msg, "secret") {
		t.Fatalf("redacted parse error still carries the password: %q", msg)
	}
	if !strings.Contains(msg, "cannot parse") || !strings.Contains(msg, Marker) {
		t.Fatalf("redacted parse error lost its diagnosis: %q", msg)
	}
	// The password value alone is redacted too, even outside the full-DSN echo.
	err = RedactDSNError(errors.New("auth failed for secret"), dsn)
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("redacted auth error still carries the password: %q", err.Error())
	}
	// The chain stays intact.
	sentinel := errors.New("sentinel")
	if !errors.Is(RedactDSNError(fmt.Errorf("w: %w", sentinel), dsn), sentinel) {
		t.Fatal("RedactDSNError lost the error chain")
	}
}
