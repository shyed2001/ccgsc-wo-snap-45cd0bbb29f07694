// Path: internal/caps/mem_bsd.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (b) - the BSD family (Darwin, FreeBSD, NetBSD, OpenBSD,
// DragonFly) reports the physical RAM of the machine with the sysctl hw.memsize, so
// the automatic memory limit works there too.
//
// The dependency is golang.org/x/sys/unix, which is already in go.mod (it comes
// with the other dependencies of the tree); no new module is added by this file.
//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package caps

import (
	"golang.org/x/sys/unix"
)

// physicalMemoryMB returns the physical RAM of the machine in megabytes. The second
// value is false when the machine does not report it.
func physicalMemoryMB() (int64, bool) {
	bytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil || bytes == 0 {
		return 0, false
	}
	return bytesToMB(int64(bytes)), true
}
