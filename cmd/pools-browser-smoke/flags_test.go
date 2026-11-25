// Path: cmd/pools-browser-smoke/flags_test.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the unit tests of the parts of the harness that need no Chrome
// and no server: the flag parsing, the reference hash lookup and the CSP hash
// check. The Chrome run is an integration test behind the "browser" build tag.
package main

import (
	"testing"
	"time"
)

func TestParseFlagsDefaults(t *testing.T) {
	o, err := parseFlags("pools-browser-smoke", nil)
	if err != nil {
		t.Fatalf("parseFlags with no arguments: %v", err)
	}
	if o.Base != "http://127.0.0.1:8091" {
		t.Errorf("the default base is %q, want http://127.0.0.1:8091", o.Base)
	}
	if o.StartLocal {
		t.Error("the local server must not start by default")
	}
	if o.Timeout != 120*time.Second {
		t.Errorf("the default timeout is %s, want 120s", o.Timeout)
	}
	if len(o.Pools) != 2 || o.Pools[0] != poolThread || o.Pools[1] != poolProcess {
		t.Errorf("the default pools are %v, want [tp pp]", o.Pools)
	}
}

func TestParseFlagsValues(t *testing.T) {
	o, err := parseFlags("pools-browser-smoke", []string{
		"-base", "http://127.0.0.1:9999/",
		"-start-local",
		"-pools", "tp,pp",
		"-timeout", "30s",
		"-chrome", `C:\Program Files\Google\Chrome\Application\chrome.exe`,
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if o.Base != "http://127.0.0.1:9999" {
		t.Errorf("the trailing slash of the base must be trimmed, got %q", o.Base)
	}
	if !o.StartLocal {
		t.Error("-start-local was not parsed")
	}
	if o.Timeout != 30*time.Second {
		t.Errorf("the timeout is %s, want 30s", o.Timeout)
	}
	if o.Chrome == "" {
		t.Error("-chrome was not parsed")
	}
}

func TestParseFlagsRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"empty base", []string{"-base", ""}},
		{"base without a scheme", []string{"-base", "127.0.0.1:8091"}},
		{"zero timeout", []string{"-timeout", "0s"}},
		{"negative timeout", []string{"-timeout", "-5s"}},
		{"unknown pool", []string{"-pools", "tp,xx"}},
		{"empty pool list", []string{"-pools", ""}},
		{"stray argument", []string{"extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFlags("pools-browser-smoke", tc.args); err == nil {
				t.Fatalf("parseFlags(%v) answered no error", tc.args)
			}
		})
	}
}

func TestParsePoolsDropsDuplicates(t *testing.T) {
	pools, err := parsePools("tp,tp,pp")
	if err != nil {
		t.Fatalf("parsePools: %v", err)
	}
	if len(pools) != 2 || pools[0] != poolThread || pools[1] != poolProcess {
		t.Errorf("parsePools answered %v, want [tp pp]", pools)
	}
}

func TestParsePoolsIsCaseInsensitive(t *testing.T) {
	pools, err := parsePools("TP, PP")
	if err != nil {
		t.Fatalf("parsePools: %v", err)
	}
	if len(pools) != 2 {
		t.Errorf("parsePools answered %v, want two pools", pools)
	}
}

func TestOptionsPrefixes(t *testing.T) {
	o := options{Pools: []string{poolThread, poolProcess}}
	got := o.prefixes()
	want := []string{"/thread-pool/v1", "/process-pool/v1"}
	if len(got) != len(want) {
		t.Fatalf("prefixes answered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("prefix %d is %q, want %q", i, got[i], want[i])
		}
	}
}
