// Path: internal/caps/mem_other.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: the physical RAM is only detectable on Linux (the /proc/meminfo reader),
// on Windows (the kernel32 reader) and on the BSD family (the sysctl reader).
//
// R15-FIX-CAPS (b): on every other platform the automatic memory limit stays off,
// and the reason is logged by the caller as "memLimit auto: unavailable on <os>".
//go:build !linux && !windows && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package caps

// physicalMemoryMB returns the physical RAM of the machine in megabytes. The second
// value is false when the machine does not report it.
func physicalMemoryMB() (int64, bool) {
	return 0, false
}
