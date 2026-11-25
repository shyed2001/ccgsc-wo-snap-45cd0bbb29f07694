// Path: cmd/pools-core/main_test.go | Author: GLM (z.ai) | 2026-11-21
// T101 (cmdc leak sweep): the first-start seed admin password must never be
// printed (it lands in journald); it is written to a 0600 file next to the
// database instead. The tests pin the file location, its mode and its
// restart behaviour.
package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteSeedCredentialsCreatesFileNextToDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "pools.db")
	path, err := writeSeedCredentials(dbPath, "admin", "top-secret-pw")
	if err != nil {
		t.Fatalf("writeSeedCredentials: %v", err)
	}
	if want := filepath.Join(dir, "data", seedCredentialsFileName); path != want {
		t.Fatalf("credentials path = %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0600 {
		t.Fatalf("credentials file mode = %o, want 600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "username=admin\n") || !strings.Contains(content, "password=top-secret-pw\n") {
		t.Fatalf("credentials file content = %q", content)
	}
	// The parent directory was created with owner-only permissions too.
	dirInfo, err := os.Stat(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); runtime.GOOS != "windows" && got != 0700 {
		t.Fatalf("credentials directory mode = %o, want 700", got)
	}
}

func TestWriteSeedCredentialsForcesModeOnExistingFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pools.db")
	path, err := writeSeedCredentials(dbPath, "admin", "first-secret-pw")
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	// A leaked file from an earlier run with wide permissions gets tightened.
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	path2, err := writeSeedCredentials(dbPath, "admin", "second-secret-pw")
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if path2 != path {
		t.Fatalf("credentials path changed between runs: %q != %q", path2, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0600 {
		t.Fatalf("credentials file mode after rewrite = %o, want 600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "password=second-secret-pw\n") {
		t.Fatalf("credentials file was not overwritten: %q", string(raw))
	}
}

func TestWriteSeedCredentialsBareFilename(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	path, err := writeSeedCredentials("pools.db", "admin", "top-secret-pw")
	if err != nil {
		t.Fatalf("writeSeedCredentials: %v", err)
	}
	if filepath.Base(path) != seedCredentialsFileName {
		t.Fatalf("credentials path = %q", path)
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("stat = %v, %v", info, err)
	}
}
