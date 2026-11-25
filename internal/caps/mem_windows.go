// Path: internal/caps/mem_windows.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (b) - the Windows policy of the automatic memory limit.
//
// On Windows the machine reports its physical RAM (kernel32 GlobalMemoryStatusEx),
// so the automatic memory limit is derived from it, exactly like on Linux: 75 % of
// the memory the process may plan for.
//
// The dependency is golang.org/x/sys/windows, which is already in go.mod (it comes
// with the other dependencies of the tree); no new module is added by this file.
// It is used for the lazy system DLL binding of the kernel32 call.
//go:build windows

package caps

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsMemoryStatusEx is the MEMORYSTATUSEX structure of kernel32.
type windowsMemoryStatusEx struct {
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

// physicalMemoryMB returns the physical RAM of the machine in megabytes. The second
// value is false when the machine does not report it.
func physicalMemoryMB() (int64, bool) {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	st := windowsMemoryStatusEx{}
	st.dwLength = uint32(unsafe.Sizeof(st))
	r1, _, err := proc.Call(uintptr(unsafe.Pointer(&st)))
	if r1 == 0 {
		_ = err
		return 0, false
	}
	if st.ullTotalPhys == 0 {
		return 0, false
	}
	return int64(st.ullTotalPhys / (1 << 20)), true
}
