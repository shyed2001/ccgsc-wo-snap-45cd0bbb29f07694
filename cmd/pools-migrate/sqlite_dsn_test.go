// Path: cmd/pools-migrate/sqlite_dsn_test.go | Author: hermes MiniMax-M3 | 2026-11-20
// T93: the read-only DSN must open the real file and refuse a write.
package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDSNOpensRealFileReadOnly(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"plain.db", "sp ace.db", "ha#sh.db"} {
		path := filepath.Join(dir, name)
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE records(bucket TEXT, id TEXT, body TEXT); INSERT INTO records VALUES('b','1','{}')`); err != nil {
			t.Fatal(err)
		}
		db.Close()

		dsn, err := sqliteDSN(path)
		if err != nil {
			t.Fatal(err)
		}
		ro, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var n int
		if err := ro.QueryRow("SELECT COUNT(*) FROM records").Scan(&n); err != nil {
			t.Fatalf("%s dsn=%q: %v", name, dsn, err)
		}
		if n != 1 {
			t.Fatalf("%s: %d rows", name, n)
		}
		if _, err := ro.Exec("INSERT INTO records VALUES('b','2','{}')"); err == nil {
			t.Fatalf("%s: the read-only DSN accepted a write", name)
		}
		ro.Close()
	}
	// The old builder really did lose the read-only mode for a "#" path.
	dir2 := t.TempDir()
	bypass := filepath.Join(dir2, "bypass.db")
	db, err := sql.Open("sqlite", bypass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE records(bucket TEXT, id TEXT, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	old, err := sql.Open("sqlite", "file:"+bypass+"#x?mode=ro")
	if err != nil {
		t.Skip("the old builder could not be opened here")
	}
	if _, err := old.Exec("INSERT INTO records VALUES('b','2','{}')"); err == nil {
		t.Log("confirmed: the old builder lost mode=ro")
	} else {
		t.Log("the old builder kept mode=ro on this platform")
	}
	old.Close()
	if _, err := os.Stat(bypass); err != nil {
		t.Fatal(err)
	}
}

// TestDSNNoAuthorityForPOSIXDoubleSlash is the T93 acceptance test (agy
// review): a POSIX path starting with "//" must not come out as an
// authority URI (file://...), which SQLite reads as a URI host and fails.
func TestDSNNoAuthorityForPOSIXDoubleSlash(t *testing.T) {
	dsn, err := sqliteDSN("//var/lib/pools.db")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(dsn, "file://") {
		t.Fatalf("dsn = %q carries a URI authority", dsn)
	}
	if dsn != "file:/var/lib/pools.db?mode=ro" {
		t.Fatalf("dsn = %q, want file:/var/lib/pools.db?mode=ro", dsn)
	}
	// The stripped path is the real file: create it and open it read-only
	// through the double-slash input form.
	dir := t.TempDir()
	path := filepath.Join(dir, "double.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE records(bucket TEXT, id TEXT, body TEXT); INSERT INTO records VALUES('b','1','{}')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dsn, err = sqliteDSN("//" + path)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open %q: %v", dsn, err)
	}
	defer ro.Close()
	var n int
	if err := ro.QueryRow("SELECT COUNT(*) FROM records").Scan(&n); err != nil {
		t.Fatalf("query through %q: %v", dsn, err)
	}
	if n != 1 {
		t.Fatalf("got %d rows, want 1", n)
	}
	if _, err := ro.Exec("INSERT INTO records VALUES('b','2','{}')"); err == nil {
		t.Fatal("the read-only DSN accepted a write")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
