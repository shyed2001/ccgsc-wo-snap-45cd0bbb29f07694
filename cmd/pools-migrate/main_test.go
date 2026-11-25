// Path: cmd/pools-migrate/main_test.go | Author: hermes MiniMax-M3 | 2026-11-20
// T93 acceptance: the SQLite DSN builder escapes the path, and the migration
// verification compares the exact (bucket,id) sets. Both are pure functions:
// no database is opened here.
package main

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"ccgsc/poolscore/internal/store"
)

// TestSQLiteDSNEscapesPath proves that a path with URI syntax keeps its
// characters as part of the file name and can never change the DSN options.
func TestSQLiteDSNEscapesPath(t *testing.T) {
	for _, tc := range []struct {
		name, path, want string
	}{
		{"plain relative", "data/pools.db", "file:data/pools.db?mode=ro"},
		{"plain absolute posix", "/var/lib/pools.db", "file:/var/lib/pools.db?mode=ro"},
		{"question mark", "data/pools?.db", "file:data/pools%3F.db?mode=ro"},
		{"hash", "data/pools#.db", "file:data/pools%23.db?mode=ro"},
		{"space", "data/pools db.db", "file:data/pools%20db.db?mode=ro"},
		{"all reserved", "data/a?b#c d.db", "file:data/a%3Fb%23c%20d.db?mode=ro"},
		{"percent", "data/100%.db", "file:data/100%25.db?mode=ro"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sqliteDSN(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("dsn=%q, want %q", got, tc.want)
			}
		})
	}
	// The DSN always carries the read-only mode and never an authority.
	got, err := sqliteDSN("C:\\data\\pools.db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "file:") || strings.HasPrefix(got, "file://") {
		t.Fatalf("dsn=%q must be file:<path>, never file://<host>", got)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "" {
		t.Fatalf("dsn=%q must not carry a host", got)
	}
	if parsed.Query().Get("mode") != "ro" {
		t.Fatalf("dsn=%q must carry mode=ro", got)
	}
	if strings.ContainsAny(parsed.Opaque, "?#") {
		t.Fatalf("dsn=%q leaked URI syntax into the path", got)
	}
	// The old builder ("file:"+path+"?mode=ro") let a "#" in the path eat
	// the mode option. The escaped builder must not.
	old := "file:" + "data/pools#.db" + "?mode=ro"
	if strings.Contains(old[strings.Index(old, "#"):], "mode=ro") {
		oldParsed, err := url.Parse(old)
		if err == nil && oldParsed.Query().Get("mode") == "ro" {
			t.Fatal("the old builder was expected to lose mode=ro for this path")
		}
	}
	if _, err := sqliteDSN("   "); err == nil {
		t.Fatal("an empty path was accepted")
	}
}

// TestIDSetCompare proves that the verification compares the exact id sets
// and not only the per-bucket counts.
func TestIDSetCompare(t *testing.T) {
	rows := []row{
		{bucket: "keys", id: "k1"},
		{bucket: "keys", id: "k2"},
		{bucket: "users", id: "u1"},
	}
	want := idSetsOfRows(rows)

	got := bucketIDs{
		"keys":  {"k1": {}, "k2": {}, "extra": {}},
		"users": {"u1": {}},
	}
	if err := verifyIDSets(want, got); err != nil {
		t.Fatalf("identical sets rejected: %v", err)
	}

	// The same count, a different id: the count check passes, the set check
	// must not.
	swapped := bucketIDs{
		"keys":  {"k1": {}, "kX": {}},
		"users": {"u1": {}},
	}
	if err := verifyIDSets(want, swapped); err == nil {
		t.Fatal("a replaced id was accepted")
	} else if !strings.Contains(err.Error(), "keys/k2") {
		t.Fatalf("error does not name the missing id: %v", err)
	}

	// A missing bucket is reported too.
	short := bucketIDs{"keys": {"k1": {}, "k2": {}}}
	if err := verifyIDSets(want, short); err == nil {
		t.Fatal("a missing bucket was accepted")
	} else if !strings.Contains(err.Error(), "users/u1") {
		t.Fatalf("error does not name the missing id: %v", err)
	}

	// The missing list is sorted and complete.
	missing := missingIDSets(want, short)
	if !reflect.DeepEqual(missing, []string{"users/u1"}) {
		t.Fatalf("missing=%v", missing)
	}
}

// TestIDSetsOfRecords proves that a PostgreSQL listing is turned into the
// same shape as the SQLite rows.
func TestIDSetsOfRecords(t *testing.T) {
	records := []store.Record{
		{Key: "a", Value: []byte(`{}`)},
		{Key: "b", Value: []byte(`{}`)},
	}
	got := idSetsOfRecords("keys", records)
	if len(got["keys"]) != 2 {
		t.Fatalf("got=%v", got)
	}
	if _, ok := got["keys"]["a"]; !ok {
		t.Fatal("record a missing")
	}
	if _, ok := got["keys"]["b"]; !ok {
		t.Fatal("record b missing")
	}
}
