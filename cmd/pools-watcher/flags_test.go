// Path: cmd/pools-watcher/flags_test.go | WO F16 (cmdc)
// What/why: the unit tests of the flag parsing, which needs no Chrome and no
// server.
package main

import (
	"testing"
	"time"
)

func TestParseFlagsDefaults(t *testing.T) {
	o, err := parseFlags("pools-watcher", nil)
	if err != nil {
		t.Fatalf("parseFlags with no arguments: %v", err)
	}
	if o.Server != defaultServer {
		t.Errorf("the default server is %q, want %q", o.Server, defaultServer)
	}
	if o.MaxTabs != 1 {
		t.Errorf("the default max tabs is %d, want 1", o.MaxTabs)
	}
	if o.Idle != 60*time.Second {
		t.Errorf("the default idle is %s, want 60s", o.Idle)
	}
	if o.Poll != 5*time.Second {
		t.Errorf("the default poll is %s, want 5s", o.Poll)
	}
	if len(o.Pools) != 2 || o.Pools[0] != poolThread || o.Pools[1] != poolProcess {
		t.Errorf("the default pools are %v, want [tp pp]", o.Pools)
	}
}

func TestParseFlagsValues(t *testing.T) {
	o, err := parseFlags("pools-watcher", []string{
		"-server", "http://127.0.0.1:9999/",
		"-pools", "pp",
		"-chrome", `C:\Program Files\Google\Chrome\Application\chrome.exe`,
		"-max-tabs", "3",
		"-idle", "30s",
		"-poll", "2s",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if o.Server != "http://127.0.0.1:9999" {
		t.Errorf("the trailing slash of the server must be trimmed, got %q", o.Server)
	}
	if len(o.Pools) != 1 || o.Pools[0] != poolProcess {
		t.Errorf("the pools are %v, want [pp]", o.Pools)
	}
	if o.Chrome == "" {
		t.Error("-chrome was not parsed")
	}
	if o.MaxTabs != 3 {
		t.Errorf("-max-tabs is %d, want 3", o.MaxTabs)
	}
	if o.Idle != 30*time.Second {
		t.Errorf("-idle is %s, want 30s", o.Idle)
	}
	if o.Poll != 2*time.Second {
		t.Errorf("-poll is %s, want 2s", o.Poll)
	}
}

func TestParseFlagsRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"empty server", []string{"-server", ""}},
		{"server without a scheme", []string{"-server", "127.0.0.1:8091"}},
		{"unknown pool", []string{"-pools", "tp,xx"}},
		{"empty pool list", []string{"-pools", ""}},
		{"zero max tabs", []string{"-max-tabs", "0"}},
		{"negative max tabs", []string{"-max-tabs", "-1"}},
		{"zero idle", []string{"-idle", "0s"}},
		{"negative idle", []string{"-idle", "-5s"}},
		{"zero poll", []string{"-poll", "0s"}},
		{"stray argument", []string{"extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFlags("pools-watcher", tc.args); err == nil {
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
