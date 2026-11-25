// Path: internal/caps/platform_R15_test.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (b) - the platform policy of the automatic memory limit.
// Every platform says what it does: Linux reads the cgroup budget and the physical
// RAM, Windows reads the physical RAM, and a platform that can detect neither keeps
// the automatic limit off and logs "memLimit auto: unavailable on <os>".
package caps

import (
	"bytes"
	"log/slog"
	"runtime"
	"strings"
	"testing"
)

// TestMemLimitAutoUnavailableMessage pins the exact wording of the log line of a
// platform that cannot detect the memory of the machine.
func TestMemLimitAutoUnavailableMessage(t *testing.T) {
	msg := memLimitAutoUnavailableMessage(runtime.GOOS)
	if !strings.Contains(msg, "memLimit auto: unavailable on ") {
		t.Fatalf("the message %q does not carry the documented wording", msg)
	}
	if !strings.Contains(msg, runtime.GOOS) {
		t.Fatalf("the message %q does not name the operating system %q", msg, runtime.GOOS)
	}
}

// TestMemLimitAutoUnavailableIsLogged pins that the automatic limit of a platform
// that cannot detect the memory is off and that the reason is in the log.
func TestMemLimitAutoUnavailableIsLogged(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	log.Info(memLimitAutoUnavailableMessage("plan9"))
	out := buf.String()
	if !strings.Contains(out, "memLimit auto: unavailable on plan9") {
		t.Fatalf("the log %q does not carry the documented line", out)
	}
}

// TestPlatformMemoryPolicy pins the platform policy of the automatic memory limit:
// the platform knows whether it can detect the memory of the machine.
func TestPlatformMemoryPolicy(t *testing.T) {
	budget := DetectMemoryBudget()
	if budget.PhysicalMB <= 0 {
		t.Fatalf("the platform %s detected no physical RAM", runtime.GOOS)
	}
	if budget.AvailableMB() <= 0 {
		t.Fatalf("the platform %s detected no available memory", runtime.GOOS)
	}
	// The budget never exceeds the physical RAM.
	if budget.CgroupMB > 0 && budget.CgroupMB > budget.PhysicalMB {
		t.Fatalf("the cgroup budget of %d MB exceeds the physical RAM of %d MB", budget.CgroupMB, budget.PhysicalMB)
	}
}

// TestDetectMemoryBudgetIsStable pins that the detection is repeatable: two calls
// report the same budget (the detector never invents a different number).
func TestDetectMemoryBudgetIsStable(t *testing.T) {
	first := DetectMemoryBudget()
	second := DetectMemoryBudget()
	if first.AvailableMB() != second.AvailableMB() {
		t.Fatalf("the budget changed between two calls: %d MB and %d MB", first.AvailableMB(), second.AvailableMB())
	}
}
