// Path: cmd/buildrunner/main_test.go | Author: CommandCode for the owner | 2026-11-21
// What/why: D-W10-12 gate - the Go build must match the JS reference byte for byte,
// the CSP hash must be the sha256 of the exact inline body, and -check must pass on
// fresh output and name the part that differs after a one-byte change.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// tempRoot copies the build inputs into a temp dir shaped like pools-core.
func tempRoot(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	dst := t.TempDir()
	files := []string{
		runnerRel,
		gpuPickerRel,
		hashRel,
		"web/pages/js/build-runner-html.mjs",
	}
	for _, rel := range files {
		src := filepath.Join(root, filepath.FromSlash(rel))
		out := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(out), err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", out, err)
		}
	}
	return dst
}

func TestBuildMatchesNodeReference(t *testing.T) {
	root := tempRoot(t)
	got, err := Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	outPath := filepath.Join(root, filepath.FromSlash(runnerHTMLRel))
	if err := os.WriteFile(outPath, got, 0o644); err != nil {
		t.Fatalf("write Go output: %v", err)
	}

	t.Run("go output is a full page", func(t *testing.T) {
		html := string(got)
		for _, want := range []string{
			"<!DOCTYPE html>",
			`<meta charset="UTF-8">`,
			`<script type="module">`,
			runnerInlineBegin,
			"<!-- END OF runner.html -->",
			"<!-- build id: ",
		} {
			if !strings.Contains(html, want) {
				t.Fatalf("output misses %q", want)
			}
		}
	})

	t.Run("bytes equal the node build", func(t *testing.T) {
		nodePath, err := exec.LookPath("node")
		if err != nil {
			t.Skip("node is not on PATH; skipping the byte-for-byte comparison with the JS reference")
		}
		cmd := exec.Command(nodePath, filepath.Join(root, filepath.FromSlash("web/pages/js/build-runner-html.mjs")))
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("node build: %v\n%s", err, out)
		}
		want, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("read node output: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("Go output differs from the node reference at byte %d (Go %d bytes, node %d bytes)",
				commonPrefixLen(string(got), string(want)), len(got), len(want))
		}
	})
}

func TestCSPHashMatchesInlineBody(t *testing.T) {
	root := tempRoot(t)
	htmlBytes, err := Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	html := string(htmlBytes)

	inline := extractInline(html)
	if !inline.ok {
		t.Fatal("no inline module script found")
	}
	if !strings.HasPrefix(inline.text, runnerInlineBegin+"\n") {
		t.Fatalf("inline body does not start with the marker: %q", inline.text[:min(60, len(inline.text))])
	}
	if !strings.HasSuffix(inline.text, "\n") {
		t.Fatal("inline body does not end with a newline")
	}

	// The browser hashes the whole text between the tags, padding included - the
	// same string the builder hashes, not the bare generated block.
	elementText := "\n" + inline.text + "  "
	sum := sha256.Sum256([]byte(elementText))
	want := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	csp := extractCSP(html)
	if !csp.ok {
		t.Fatal("no Content-Security-Policy meta found")
	}
	for _, part := range []string{
		"default-src 'none'",
		"script-src '" + want + "' blob:",
		"worker-src blob:",
		"connect-src 'none'",
	} {
		if !strings.Contains(csp.text, part) {
			t.Fatalf("CSP %q misses %q", csp.text, part)
		}
	}
}

// TestRunnerPinMatchesInlineBody: data-runner-pin is "<hex sha256 of the exact
// script element text>:<first operator key id>" (D-W10-9), the same bytes the CSP
// hash covers, and the key id is the first key of the pinned ring in runner.js.
func TestRunnerPinMatchesInlineBody(t *testing.T) {
	root := tempRoot(t)
	htmlBytes, err := Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	html := string(htmlBytes)

	inline := extractInline(html)
	if !inline.ok || inline.text == "" {
		t.Fatal("no inline module script found")
	}
	elementText := "\n" + inline.text + "  "
	sum := sha256.Sum256([]byte(elementText))
	wantHash := hex.EncodeToString(sum[:])

	pin := extractPin(html)
	if !pin.ok || pin.text == "" {
		t.Fatal("no data-runner-pin attribute found")
	}
	gotHash, gotKey, ok := strings.Cut(pin.text, ":")
	if !ok {
		t.Fatalf("pin %q has no <sha256>:<keyId> shape", pin.text)
	}
	if gotHash != wantHash {
		t.Fatalf("pin sha256 = %s, want %s (sha256 of the exact element text)", gotHash, wantHash)
	}

	source, err := readJS(filepath.Join(root, filepath.FromSlash(runnerRel)))
	if err != nil {
		t.Fatalf("read runner.js: %v", err)
	}
	wantKey, err := firstOperatorKeyID(source)
	if err != nil {
		t.Fatalf("firstOperatorKeyID: %v", err)
	}
	if wantKey == "" {
		t.Fatal("runner.js pins no operator key")
	}
	if gotKey != wantKey {
		t.Fatalf("pin key id = %q, want the first OPERATOR_KEYS key %q", gotKey, wantKey)
	}
	if len(gotHash) != 64 {
		t.Fatalf("pin sha256 is %d hex chars, want 64", len(gotHash))
	}
}

func TestCheckPassesOnFreshOutputAndFailsAfterOneByteChange(t *testing.T) {
	root := tempRoot(t)
	outPath := filepath.Join(root, filepath.FromSlash(runnerHTMLRel))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("build exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wrote ") {
		t.Fatalf("build stdout = %q, want a wrote line", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-root", root, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-check on fresh output exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "runner.html is up to date") {
		t.Fatalf("-check stdout = %q", stdout.String())
	}

	fresh, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read fresh output: %v", err)
	}

	cases := []struct {
		name string
		edit func(*testing.T, string) string
		want string
	}{
		{
			name: "inline script",
			edit: func(t *testing.T, html string) string {
				part := extractInline(html)
				if !part.ok || part.text == "" {
					t.Fatal("no inline body")
				}
				return html[:part.start] + flipByte(part.text[:1]) + part.text[1:] + html[part.start+len(part.text):]
			},
			want: "inline script differs at line",
		},
		{
			name: "CSP hash",
			edit: func(t *testing.T, html string) string {
				part := extractCSP(html)
				if !part.ok || part.text == "" {
					t.Fatal("no CSP text")
				}
				return html[:part.start] + flipByte(part.text[:1]) + part.text[1:] + html[part.start+len(part.text):]
			},
			want: "CSP hash differs at line",
		},
		{
			name: "build id",
			edit: func(t *testing.T, html string) string {
				part := extractBuildID(html)
				if !part.ok || part.text == "" {
					t.Fatal("no build id text")
				}
				return html[:part.start] + flipByte(part.text[:1]) + part.text[1:] + html[part.start+len(part.text):]
			},
			want: "build id differs at line",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(outPath, []byte(tc.edit(t, string(fresh))), 0o644); err != nil {
				t.Fatalf("write mutated page: %v", err)
			}
			stdout.Reset()
			stderr.Reset()
			if code := run([]string{"-root", root, "-check"}, &stdout, &stderr); code != 1 {
				t.Fatalf("-check exit = %d, want 1; stderr = %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("-check stderr = %q, want %q", stderr.String(), tc.want)
			}
		})
	}

	if err := os.WriteFile(outPath, fresh, 0o644); err != nil {
		t.Fatalf("restore fresh output: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-root", root, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-check after restore exit = %d, stderr = %s", code, stderr.String())
	}
}

func flipByte(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] == 'a' {
		b[0] = 'b'
	} else {
		b[0] = 'a'
	}
	return string(b)
}
