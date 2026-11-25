// Path: internal/pool/runner_pin_contract_r10_test.go | R10 fix lane (head row).
// What/why: the D-W10-9 pin contract. The runner page carries its own identity as
// <html data-runner-pin="<sha256>:<keyId>">, where the sha is the hash of the
// INLINE RUNNER SCRIPT the page executes (the same exact-body rule as the CSP).
// The server compared the reported sha against sha256 of the WHOLE runner.html
// file, so the two could never be equal and every stale-runner refund failed
// closed on a truthful page.
//
// The one source of truth is the embedded runner.html: the pin sha is the page's
// data-runner-pin attribute (baked by build-runner-html.mjs), and the tests below
// read it from the real file instead of restating it.
package pool

import (
	"encoding/hex"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"ccgsc/poolscore/internal/verify"
	"ccgsc/poolscore/web"
)

// r10RunnerPage reads the embedded runner page.
func r10RunnerPage(t *testing.T) string {
	t.Helper()
	data, err := fs.ReadFile(web.Pages, "pages/runner.html")
	if err != nil {
		t.Fatalf("embedded runner page: %v", err)
	}
	return string(data)
}

// r10PinSHA is the sha the served page claims for itself: the data-runner-pin
// attribute when the page carries it (D-W10-9 JS half), otherwise the hash of
// the exact inline runner script, which is the same identity basis.
func r10PinSHA(t *testing.T) string {
	t.Helper()
	page := r10RunnerPage(t)
	if m := regexp.MustCompile(`data-runner-pin="([0-9a-f]{64}):([0-9a-f]{16})"`).FindStringSubmatch(page); m != nil {
		return m[1]
	}
	return r10InlineSHA(t)
}

// r10InlineSHA is the sha of the exact inline runner script body, which is what
// the page's pin (and the CSP hash) cover.
func r10InlineSHA(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<script type="module">(.*?)</script>`).FindStringSubmatch(r10RunnerPage(t))
	if m == nil {
		t.Fatal("runner.html carries no inline runner script")
	}
	return verify.SHA256([]byte(m[1]))
}

// TestR10RunnerPinPageOKUsesThePageContract is the head row.
func TestR10RunnerPinPageOKUsesThePageContract(t *testing.T) {
	pinSHA := r10PinSHA(t)
	if !runnerPinPageOK(pinSHA + ":" + "0123456789abcdef") {
		t.Fatalf("the pin of the served runner page %s is not accepted", pinSHA)
	}
	if !runnerPinPageOK(r10InlineSHA(t) + ":" + "0123456789abcdef") {
		t.Fatal("the sha of the exact inline runner script is not accepted")
	}
	// The old rule hashed the whole file, so that value must not be the contract.
	whole := verify.SHA256([]byte(r10RunnerPage(t)))
	if whole == pinSHA {
		t.Fatal("the fixture cannot distinguish the file hash from the pin hash")
	}
	if runnerPinPageOK(whole + ":" + "0123456789abcdef") {
		t.Fatalf("the whole-file hash %s is accepted; the pin must cover the inline script", whole)
	}
	if runnerPinPageOK(strings.Repeat("0", 64) + ":" + "0123456789abcdef") {
		t.Fatal("an arbitrary sha is accepted")
	}
	if _, err := hex.DecodeString(pinSHA); err != nil {
		t.Fatalf("the baked pin sha is not hex: %v", err)
	}
}
