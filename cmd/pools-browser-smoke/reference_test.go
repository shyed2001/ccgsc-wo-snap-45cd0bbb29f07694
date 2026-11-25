// Path: cmd/pools-browser-smoke/reference_test.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the unit tests of the reference hash lookup and of the CSP hash
// check. Both are pure functions over files and strings, so they run without a
// browser and without a server.
package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRootForTest answers the tree root of the harness.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate the pools-core tree: %v", err)
	}
	return root
}

func TestReferenceHashesReadsTheShippedManifest(t *testing.T) {
	root := repoRootForTest(t)
	refs, err := referenceHashes(filepath.Join(root, "web", "workloads", "matmul", "manifest.json"))
	if err != nil {
		t.Fatalf("referenceHashes: %v", err)
	}
	// The published reference hashes of the two supported sizes. They are the
	// values the workload manifest ships and the e2e binary checks against.
	const want160 = "1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2"
	const want240 = "049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c"
	got, err := referenceHash(refs, 160)
	if err != nil {
		t.Fatalf("referenceHash(160): %v", err)
	}
	if got != want160 {
		t.Errorf("the reference hash of 160 is %s, want %s", got, want160)
	}
	got, err = referenceHash(refs, 240)
	if err != nil {
		t.Fatalf("referenceHash(240): %v", err)
	}
	if got != want240 {
		t.Errorf("the reference hash of 240 is %s, want %s", got, want240)
	}
}

func TestReferenceHashRejectsAnUnknownSize(t *testing.T) {
	if _, err := referenceHash(map[string]string{"160": "aa"}, 123); err == nil {
		t.Fatal("referenceHash answered no error for an unpublished size")
	}
}

func TestReferenceHashesRejectsAMissingFile(t *testing.T) {
	if _, err := referenceHashes(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("referenceHashes answered no error for a missing manifest")
	}
}

func TestReferenceHashesRejectsABrokenManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write the broken manifest: %v", err)
	}
	if _, err := referenceHashes(path); err == nil {
		t.Fatal("referenceHashes answered no error for a broken manifest")
	}
}

func TestReferenceHashesRejectsANonHexHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	body := `{"reference":{"160":"not-a-hex-digest"}}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write the broken manifest: %v", err)
	}
	if _, err := referenceHashes(path); err == nil {
		t.Fatal("referenceHashes answered no error for a non-hex reference hash")
	}
}

// cspPage answers a page with the given CSP and inline script.
func cspPage(cspHash, script string) []byte {
	csp := "default-src 'none'; script-src 'sha256-" + cspHash + "' blob:; worker-src blob:; connect-src 'none'"
	page := "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n  <meta charset=\"UTF-8\">\n" +
		"  <meta http-equiv=\"Content-Security-Policy\" content=\"" + csp + "\">\n" +
		"  <title>t</title>\n</head>\n<body>\n  <script type=\"module\">\n" + script + "  </script>\n</body>\n</html>\n"
	return []byte(page)
}

func TestCheckCSPHashMatchesTheBrowserRule(t *testing.T) {
	// The browser hashes the EXACT text between the script tags, the leading
	// newline and the trailing spaces included. A page that pins that hash
	// must pass.
	script := "document.title='x';\n"
	raw := "\n" + script + "  "
	hash := scriptHash(raw)
	verdict := checkCSPHash(cspPage(hash, script))
	if !verdict.Present {
		t.Fatal("the verdict reports no CSP meta")
	}
	if !verdict.Match {
		t.Errorf("the verdict does not match: %s", verdict.Detail)
	}
}

func TestCheckCSPHashDetectsTheBuilderRule(t *testing.T) {
	// The shipped builder hashes the inline block WITHOUT the leading
	// newline and WITHOUT the trailing spaces of the template. A page built
	// that way does not pass, because the browser hashes the raw text.
	script := "document.title='x';\n"
	builderStyle := scriptHash(script)
	verdict := checkCSPHash(cspPage(builderStyle, script))
	if verdict.Match {
		t.Error("the builder rule must not pass the browser rule")
	}
	if !strings.Contains(verdict.Detail, "the browser computes") {
		t.Errorf("the detail does not name the computed hash: %s", verdict.Detail)
	}
}

func TestReferenceSizes(t *testing.T) {
	// The sizes come from the manifest, so a size added there is checked
	// without a code change.
	refs := map[string]string{
		"240": "049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c",
		"160": "1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2",
	}
	got := referenceSizes(refs)
	if len(got) != 2 || got[0] != 160 || got[1] != 240 {
		t.Fatalf("the sizes are %v, want [160 240]", got)
	}
	// A size that is not a number is skipped, not a crash.
	if got := referenceSizes(map[string]string{"abc": "x", "0": "y", "8": "z"}); len(got) != 1 || got[0] != 8 {
		t.Fatalf("the sizes are %v, want [8]", got)
	}
}

func TestCheckCSPHashOnTheShippedRunnerPage(t *testing.T) {
	root := repoRootForTest(t)
	raw, err := os.ReadFile(filepath.Join(root, "web", "pages", "runner.html"))
	if err != nil {
		t.Fatalf("read the shipped runner page: %v", err)
	}
	verdict := checkCSPHash(raw)
	if !verdict.Present {
		t.Fatal("the shipped runner page carries no CSP meta")
	}
	// The shipped page must pin the hash the browser computes over the raw
	// script text. The builder now hashes the exact string it emits, so the
	// two rules agree and Chrome accepts the inline module.
	if verdict.Computed != scriptHash(scriptBodyOf(raw)) {
		t.Errorf("the computed hash %s is not the hash of the raw script text", verdict.Computed)
	}
	if !verdict.Match {
		t.Fatalf("the shipped runner page pins %s but the browser computes %s: %s",
			verdict.Pinned, verdict.Computed, verdict.Detail)
	}
}

// scriptBodyOf answers the raw text between the script tags of a page.
func scriptBodyOf(page []byte) string {
	m := inlineScriptRE.FindStringSubmatch(string(page))
	if m == nil {
		return ""
	}
	return m[1]
}

func TestCheckCSPHashWithoutAMeta(t *testing.T) {
	verdict := checkCSPHash([]byte("<html><body>nothing</body></html>"))
	if verdict.Present {
		t.Error("a page without a CSP meta must be reported as absent")
	}
	if verdict.Detail == "" {
		t.Error("the verdict carries no reason")
	}
}

func TestCheckCSPHashWithoutASourceExpression(t *testing.T) {
	page := []byte(`<meta http-equiv="Content-Security-Policy" content="default-src 'none'">`)
	verdict := checkCSPHash(page)
	if !verdict.Present {
		t.Fatal("the CSP meta was not found")
	}
	if verdict.Match {
		t.Error("a CSP without a source expression must not match")
	}
}

func TestScriptHashIsTheBase64SHA256(t *testing.T) {
	// The hash is the base64 form of the raw sha256 digest, the way a
	// browser computes a CSP source expression.
	sum := scriptHash("abc")
	if sum != "ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=" {
		t.Errorf("scriptHash(abc) is %s, want the base64 sha256 of abc", sum)
	}
	if _, err := base64.StdEncoding.DecodeString(sum); err != nil {
		t.Errorf("the hash is not base64: %v", err)
	}
}

func TestSortedHashesIsStable(t *testing.T) {
	refs := map[string]string{"240": "b", "160": "a"}
	got := sortedHashes(refs)
	if len(got) != 2 || got[0] != "160=a" || got[1] != "240=b" {
		t.Errorf("sortedHashes answered %v, want [160=a 240=b]", got)
	}
}
