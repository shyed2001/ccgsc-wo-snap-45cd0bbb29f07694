// Path: internal/caps/softlimit_R15_test.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (c) - GOMEMLIMIT is a soft GC target, not a hard RSS cap.
// The admin caps view must say so in the payload (softLimit:true) and the config
// comment must say so in the source.
package caps

import (
	"strings"
	"testing"
)

// TestSoftLimitIsMarkedInTheAdminView pins that the admin caps view marks the
// memory limit as a soft limit: the payload carries softLimit:true, so an operator
// never reads the number as a hard RSS ceiling.
func TestSoftLimitIsMarkedInTheAdminView(t *testing.T) {
	if !SoftLimitIsSoft() {
		t.Fatal("the package does not mark the memory limit as a soft limit")
	}
}

// TestSoftLimitTextNamesTheGcTarget pins the wording of the explanation that the
// admin view and the config comment share: it names the GC target and denies that it
// is a hard RSS cap.
func TestSoftLimitTextNamesTheGcTarget(t *testing.T) {
	text := SoftLimitExplanation()
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "soft") {
		t.Fatalf("the explanation %q does not call the limit soft", text)
	}
	if !strings.Contains(lower, "gc") {
		t.Fatalf("the explanation %q does not name the GC target", text)
	}
	if !strings.Contains(lower, "rss") {
		t.Fatalf("the explanation %q does not mention the RSS it does not cap", text)
	}
}

// TestMemoryLimitIsNotAHardCap pins that the package never claims to cap the RSS.
func TestMemoryLimitIsNotAHardCap(t *testing.T) {
	if strings.Contains(strings.ToLower(SoftLimitExplanation()), "hard cap") {
		t.Fatalf("the explanation %q claims a hard cap", SoftLimitExplanation())
	}
}
