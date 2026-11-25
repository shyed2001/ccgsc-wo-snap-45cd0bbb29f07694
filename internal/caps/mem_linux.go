// Path: internal/caps/mem_linux.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: the physical RAM of the machine, read from /proc/meminfo. The default
// memory limit (75 % of the memory the process may plan for) needs it; a machine
// that does not report it runs without a soft memory limit (off).
//
// R15-FIX-CAPS (a): the physical RAM is only half of the budget on Linux - the
// cgroup budget of the container is read by mem_cgroup.go, and the process plans
// for the smaller of the two.
package caps

import (
	"os"
	"strconv"
	"strings"
)

// memInfoPath is the file that carries the physical RAM of the machine.
const memInfoPath = "/proc/meminfo"

// physicalMemoryMB returns the physical RAM of the machine in megabytes. The second
// value is false when the machine does not report it.
func physicalMemoryMB() (int64, bool) {
	b, err := os.ReadFile(memInfoPath)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb <= 0 {
			return 0, false
		}
		return kb / 1024, true
	}
	return 0, false
}
