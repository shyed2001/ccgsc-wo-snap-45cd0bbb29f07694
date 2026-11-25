// Path: cmd/pools-browser-smoke/reference.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the two lookups the harness needs before it touches a browser: the
// published reference hash of a matmul size, and the CSP hash rule of the
// sandbox page. Both are pure functions over files and strings, so both are
// unit tested without a server and without a browser.
//
// The CSP rule is the one Chrome enforces, measured and not assumed: for an
// inline <script> the browser hashes the EXACT text between the tags, the
// leading newline and the trailing spaces included. The builder of
// web/pages/runner.html now hashes the same string it emits, so the two rules
// agree and the shipped page is accepted by Chrome. checkCSPHash reports
// both hashes: the one the page pins and the one the browser computes, so a
// stale page is visible instead of being silently accepted.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// matmulManifest is the workload manifest of the reference workload
// (web/workloads/matmul/manifest.json). It carries the published reference
// hashes of the matmul sizes.
type matmulManifest struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Reference map[string]string `json:"reference"`
}

// referenceHashes reads the published reference hashes of the matmul workload.
// The file is the only source of truth for the hashes; the harness never
// recomputes them, so a wrong published hash is reported as a failure of the
// run instead of being papered over by a local recomputation.
func referenceHashes(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the workload manifest: %w", err)
	}
	var m matmulManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse the workload manifest: %w", err)
	}
	if len(m.Reference) == 0 {
		return nil, fmt.Errorf("the workload manifest carries no reference hashes")
	}
	out := make(map[string]string, len(m.Reference))
	for size, sum := range m.Reference {
		sum = strings.ToLower(strings.TrimSpace(sum))
		if !hex64.MatchString(sum) {
			return nil, fmt.Errorf("the reference hash of size %s is not a sha256 hex digest", size)
		}
		out[size] = sum
	}
	return out, nil
}

// referenceHash answers the reference hash of one matmul size.
func referenceHash(refs map[string]string, size int) (string, error) {
	sum, ok := refs[fmt.Sprint(size)]
	if !ok {
		return "", fmt.Errorf("no published reference hash for matmul size %d", size)
	}
	return sum, nil
}

// hex64 matches a sha256 hex digest.
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// referenceSizes answers the matmul sizes the manifest publishes a reference
// hash for, in ascending order. The harness runs one job per published size,
// so a size added to the manifest is checked without a code change.
func referenceSizes(refs map[string]string) []int {
	sizes := make([]int, 0, len(refs))
	for size := range refs {
		n, err := strconv.Atoi(size)
		if err != nil || n <= 0 {
			continue
		}
		sizes = append(sizes, n)
	}
	sort.Ints(sizes)
	return sizes
}

// cspHashRE finds the sha256 source expression of a CSP script-src.
var cspHashRE = regexp.MustCompile(`'sha256-([A-Za-z0-9+/=]+)'`)

// cspMetaRE finds the CSP meta tag of a page.
var cspMetaRE = regexp.MustCompile(`<meta http-equiv="Content-Security-Policy" content="([^"]+)">`)

// inlineScriptRE finds the inline script block of a page. The block is the
// exact text between the tags, the leading newline and the trailing spaces
// included: that is the text Chrome hashes for the CSP source expression.
var inlineScriptRE = regexp.MustCompile(`(?s)<script type="module">(.*?)</script>`)

// cspVerdict is the outcome of the CSP hash check of one page.
type cspVerdict struct {
	// Present is false when the page carries no CSP meta at all.
	Present bool
	// Pinned is the source expression the page pins in its CSP.
	Pinned string
	// Computed is the source expression a browser computes over the inline
	// script text of the page.
	Computed string
	// Match is true when the pinned and the computed expression are equal.
	Match bool
	// Detail explains the verdict in one line.
	Detail string
}

// checkCSPHash applies the CSP hash rule to one page body.
//
// The rule the browser applies: the source expression of an inline script is
// the base64 sha256 of the EXACT text between the <script> tags. The rule the
// shipped builder applies: the same hash over the inline block without the
// template's leading newline and the template's trailing spaces. The two
// differ, so the verdict names both and the check fails when the page pins a
// hash the browser will not accept.
func checkCSPHash(page []byte) cspVerdict {
	html := string(page)
	meta := cspMetaRE.FindStringSubmatch(html)
	if meta == nil {
		return cspVerdict{Present: false, Detail: "the page carries no Content-Security-Policy meta"}
	}
	csp := meta[1]
	hash := cspHashRE.FindStringSubmatch(csp)
	if hash == nil {
		return cspVerdict{Present: true, Detail: "the CSP pins no sha256 source expression"}
	}
	pinned := hash[1]
	script := inlineScriptRE.FindStringSubmatch(html)
	if script == nil {
		return cspVerdict{Present: true, Pinned: pinned,
			Detail: "the page carries no inline module script to hash"}
	}
	computed := scriptHash(script[1])
	verdict := cspVerdict{
		Present:  true,
		Pinned:   pinned,
		Computed: computed,
		Match:    pinned == computed,
	}
	if verdict.Match {
		verdict.Detail = "the CSP hash matches the inline script the browser hashes"
		return verdict
	}
	verdict.Detail = fmt.Sprintf(
		"the page pins sha256-%s but the browser computes sha256-%s over the inline script text",
		pinned, computed)
	return verdict
}

// scriptHash answers the base64 sha256 of an inline script text, the way a
// browser computes a CSP source expression.
//
// The browser hashes the EXACT text between the <script> tags, whitespace
// included. The shipped builder of web/pages/runner.html now hashes the same
// string it emits, so the two rules agree.
func scriptHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// sortedHashes answers the reference hashes in a stable order, for a report.
func sortedHashes(refs map[string]string) []string {
	sizes := make([]string, 0, len(refs))
	for size := range refs {
		sizes = append(sizes, size)
	}
	sort.Strings(sizes)
	out := make([]string, 0, len(sizes))
	for _, size := range sizes {
		out = append(out, size+"="+refs[size])
	}
	return out
}
