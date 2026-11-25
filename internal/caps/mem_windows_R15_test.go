// Path: internal/caps/mem_windows_R15_test.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (b) - the Windows memory policy of the automatic memory
// limit: on Windows the automatic limit is derived from the physical RAM of the
// machine (the same 75 % rule as on Linux), because the machine reports its RAM.
//
// The physical RAM of this test is read with the raw kernel32 call of the standard
// library (an oracle that is independent of the x/sys path of the implementation),
// so the test really pins the policy and not one implementation of it.
//go:build windows

package caps

import (
	"syscall"
	"testing"
	"unsafe"
)

// oracleMemoryStatusEx is the MEMORYSTATUSEX structure of kernel32. It is the oracle
// of the test: it is the same structure the implementation uses, but the test reads
// it with the raw standard library call, so the test pins the policy and not one
// implementation of it.
type oracleMemoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// oraclePhysicalMemoryMB reads the physical RAM of the machine with the raw
// GlobalMemoryStatusEx call of kernel32.
func oraclePhysicalMemoryMB(t *testing.T) int64 {
	t.Helper()
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	st := oracleMemoryStatusEx{}
	st.dwLength = uint32(unsafe.Sizeof(st))
	r1, _, err := proc.Call(uintptr(unsafe.Pointer(&st)))
	if r1 == 0 {
		t.Skipf("GlobalMemoryStatusEx failed: %v", err)
	}
	if st.ullTotalPhys == 0 {
		t.Skip("the machine reports no physical RAM")
	}
	return int64(st.ullTotalPhys / (1 << 20))
}

// TestWindowsAutoMemoryLimitComesFromThePhysicalRAM pins the Windows policy of the
// automatic memory limit: the machine reports its RAM, so the automatic limit is the
// documented 75 % of it and never "off".
func TestWindowsAutoMemoryLimitComesFromThePhysicalRAM(t *testing.T) {
	phys := oraclePhysicalMemoryMB(t)
	got, err := Resolve(Config{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := int(phys * 3 / 4)
	if want < 1 {
		want = 1
	}
	if got.MemLimitMB != want {
		t.Fatalf("MemLimitMB = %d, want %d (75 %% of the %d MB of physical RAM)", got.MemLimitMB, want, phys)
	}
	if got.MemLimitBytes != int64(got.MemLimitMB)<<20 {
		t.Fatalf("MemLimitBytes = %d, want %d", got.MemLimitBytes, int64(got.MemLimitMB)<<20)
	}
	if got.PhysicalMemMB != phys {
		t.Fatalf("PhysicalMemMB = %d, want %d", got.PhysicalMemMB, phys)
	}
}

// TestWindowsPhysicalMemoryMBMatchesTheOracle pins that the package reads the
// physical RAM of the machine, not a constant and not a wrong value.
func TestWindowsPhysicalMemoryMBMatchesTheOracle(t *testing.T) {
	phys := oraclePhysicalMemoryMB(t)
	got, ok := PhysicalMemoryMB()
	if !ok {
		t.Fatal("PhysicalMemoryMB reported no RAM on a machine that reports its RAM")
	}
	// The kernel rounds the physical RAM down to a megabyte; a small drift is
	// allowed, a wrong order of magnitude is not.
	if got < phys-16 || got > phys+16 {
		t.Fatalf("PhysicalMemoryMB = %d, want about %d", got, phys)
	}
}

// TestWindowsMemoryBudgetHasNoCgroup pins that the Windows budget carries the
// physical RAM and no cgroup budget (Windows has no cgroup v1/v2 hierarchy).
func TestWindowsMemoryBudgetHasNoCgroup(t *testing.T) {
	b := DetectMemoryBudget()
	if b.CgroupMB != 0 {
		t.Fatalf("CgroupMB = %d, want 0 on Windows", b.CgroupMB)
	}
	if b.PhysicalMB <= 0 {
		t.Fatalf("PhysicalMB = %d, want the physical RAM of the machine", b.PhysicalMB)
	}
}
