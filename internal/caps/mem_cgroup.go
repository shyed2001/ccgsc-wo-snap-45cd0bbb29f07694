// Path: internal/caps/mem_cgroup.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (a) - the cgroup memory budget of the process.
//
// Two hierarchies are read:
//   - cgroup v2 (the unified hierarchy): the file memory.max of the cgroup of the
//     process. The value "max" means "no limit".
//   - cgroup v1: the file memory.limit_in_bytes of the memory controller. The
//     page aligned LONG_MAX of an unlimited controller means "no limit".
//
// The reader is portable (it reads files by name) so the tests drive it with
// fixtures and need no real cgroup.
package caps

import (
	"strconv"
	"strings"
)

// The names of the files that carry the budget.
const (
	// cgroupSelfPath is the file that maps the process to its cgroups.
	cgroupSelfPath = "/proc/self/cgroup"
	// cgroupV2Root is the mount point of the unified hierarchy.
	cgroupV2Root = "/sys/fs/cgroup"
	// cgroupV1Root is the mount point of the memory controller of cgroup v1.
	cgroupV1Root = "/sys/fs/cgroup/memory"
)

// cgroupV2MaxFile is the file of the unified hierarchy that carries the budget.
const cgroupV2MaxFile = "memory.max"

// cgroupV1LimitFile is the file of the memory controller of cgroup v1 that carries
// the budget.
const cgroupV1LimitFile = "memory.limit_in_bytes"

// cgroupUnlimitedV1 is the sentinel of an unlimited cgroup v1 controller: the
// page aligned LONG_MAX (9223372036854771712 on a 4 KiB page).
const cgroupUnlimitedV1 = int64(9223372036854771712)

// cgroupEntry is one line of /proc/self/cgroup.
type cgroupEntry struct {
	// hierarchyID is the number of the hierarchy ("0" is the unified one).
	hierarchyID string
	// controllers are the controllers of the hierarchy (empty for the unified
	// hierarchy).
	controllers []string
	// path is the path of the process inside the hierarchy.
	path string
}

// cgroupMemoryMB reads the cgroup budget of the process in megabytes. The second
// value is the source ("cgroup-v2" or "cgroup-v1"), the third value is false when
// the machine has no cgroup budget.
func cgroupMemoryMB() (int64, string, bool) {
	return cgroupMemoryMBWith(osReadFile)
}

// cgroupMemoryMBWith reads the cgroup budget of the process with the given reader.
// The reader is a parameter, so the tests drive the detector with fixtures and need
// no real cgroup.
func cgroupMemoryMBWith(read func(string) ([]byte, error)) (int64, string, bool) {
	entries := parseProcSelfCgroup(readString(read, cgroupSelfPath))
	// The unified hierarchy wins when a machine mounts both.
	if rel, ok := cgroupV2Path(entries); ok {
		if b, limited := readCgroupV2Limit(read, rel); limited {
			return bytesToMB(b), "cgroup-v2", true
		}
	}
	if rel, ok := cgroupV1MemoryPath(entries); ok {
		if b, limited := readCgroupV1Limit(read, rel); limited {
			return bytesToMB(b), "cgroup-v1", true
		}
	}
	// /proc/self/cgroup is not readable (a hardened container): the mount point of
	// the hierarchy is then the cgroup of the process.
	if b, limited := readCgroupV2Limit(read, "/"); limited {
		return bytesToMB(b), "cgroup-v2", true
	}
	if b, limited := readCgroupV1Limit(read, "/"); limited {
		return bytesToMB(b), "cgroup-v1", true
	}
	return 0, "", false
}

// bytesToMB converts a byte count into megabytes (rounded down).
func bytesToMB(b int64) int64 {
	return b / (1 << 20)
}

// readString reads a file with the given reader and returns its content as a string.
// A missing or unreadable file is an empty string.
func readString(read func(string) ([]byte, error), name string) string {
	b, err := read(name)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseCgroupV2Max parses the content of a cgroup v2 memory.max file. The second
// value is false when the file carries no limit ("max", empty or broken).
func parseCgroupV2Max(content string) (int64, bool) {
	s := strings.TrimSpace(content)
	if s == "" || s == "max" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// parseCgroupV1Limit parses the content of a cgroup v1 memory.limit_in_bytes file.
// The second value is false when the file carries no limit (the page aligned LONG_MAX
// of an unlimited controller, empty or broken).
func parseCgroupV1Limit(content string) (int64, bool) {
	s := strings.TrimSpace(content)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	// The unlimited sentinel of cgroup v1 (and every value above it) is no limit.
	if v >= cgroupUnlimitedV1 {
		return 0, false
	}
	return v, true
}

// parseProcSelfCgroup parses the content of /proc/self/cgroup. The lines that do not
// carry the expected three fields are skipped.
func parseProcSelfCgroup(content string) []cgroupEntry {
	var out []cgroupEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		e := cgroupEntry{hierarchyID: parts[0], path: parts[2]}
		if parts[1] != "" {
			e.controllers = strings.Split(parts[1], ",")
		}
		out = append(out, e)
	}
	return out
}

// cgroupV2Path returns the path of the process in the unified hierarchy.
func cgroupV2Path(entries []cgroupEntry) (string, bool) {
	for _, e := range entries {
		if e.hierarchyID == "0" {
			return e.path, true
		}
	}
	return "", false
}

// cgroupV1MemoryPath returns the path of the process in the memory controller of
// cgroup v1.
func cgroupV1MemoryPath(entries []cgroupEntry) (string, bool) {
	for _, e := range entries {
		for _, c := range e.controllers {
			if c == "memory" {
				return e.path, true
			}
		}
	}
	return "", false
}

// cgroupV2Candidates returns the files that may carry the budget of the unified
// hierarchy: the cgroup of the process first, then its parents, then the root of
// the hierarchy.
func cgroupV2Candidates(rel string) []string {
	var out []string
	cur := strings.TrimSuffix(rel, "/")
	for cur != "" {
		out = append(out, cgroupV2Root+cur+"/"+cgroupV2MaxFile)
		idx := strings.LastIndex(cur, "/")
		if idx <= 0 {
			break
		}
		cur = cur[:idx]
	}
	out = append(out, cgroupV2Root+"/"+cgroupV2MaxFile)
	return out
}

// cgroupV1Candidates returns the files that may carry the budget of the memory
// controller of cgroup v1: the cgroup of the process first, then its parents, then
// the root of the hierarchy.
func cgroupV1Candidates(rel string) []string {
	var out []string
	cur := strings.TrimSuffix(rel, "/")
	for cur != "" {
		out = append(out, cgroupV1Root+cur+"/"+cgroupV1LimitFile)
		idx := strings.LastIndex(cur, "/")
		if idx <= 0 {
			break
		}
		cur = cur[:idx]
	}
	out = append(out, cgroupV1Root+"/"+cgroupV1LimitFile)
	return out
}

// readCgroupV2Limit reads the budget of the unified hierarchy. The second value is
// false when the hierarchy carries no limit.
func readCgroupV2Limit(read func(string) ([]byte, error), rel string) (int64, bool) {
	for _, name := range cgroupV2Candidates(rel) {
		if v, limited := parseCgroupV2Max(readString(read, name)); limited {
			return v, true
		}
	}
	return 0, false
}

// readCgroupV1Limit reads the budget of the memory controller of cgroup v1. The
// second value is false when the controller carries no limit.
func readCgroupV1Limit(read func(string) ([]byte, error), rel string) (int64, bool) {
	for _, name := range cgroupV1Candidates(rel) {
		if v, limited := parseCgroupV1Limit(readString(read, name)); limited {
			return v, true
		}
	}
	return 0, false
}
