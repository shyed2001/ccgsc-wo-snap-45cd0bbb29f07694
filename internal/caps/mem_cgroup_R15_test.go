// Path: internal/caps/mem_cgroup_R15_test.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (a) - the cgroup memory budget of the process. The tests
// drive the reader with fixtures, so they need no real cgroup and they run the same
// way on every operating system.
package caps

import (
	"errors"
	"io/fs"
	"testing"
)

// fakeFS is a fixture of the cgroup hierarchy: the name of a file maps to its
// content. A file that is not in the fixture does not exist.
type fakeFS map[string]string

// read is the fileReader of the fixture.
func (f fakeFS) read(name string) ([]byte, error) {
	if v, ok := f[name]; ok {
		return []byte(v), nil
	}
	return nil, fs.ErrNotExist
}

// TestParseCgroupV2Max pins the parsing of a cgroup v2 memory.max file: a byte count
// is a limit, "max" is no limit at all.
func TestParseCgroupV2Max(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   string
		wantBytes int64
		wantLimit bool
	}{
		{"one gigabyte", "1073741824\n", 1073741824, true},
		{"without a newline", "536870912", 536870912, true},
		{"with spaces", "  268435456  \n", 268435456, true},
		{"max is no limit", "max\n", 0, false},
		{"empty is no limit", "", 0, false},
		{"broken is no limit", "not-a-number\n", 0, false},
		{"negative is no limit", "-1\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, limited := parseCgroupV2Max(tc.content)
			if limited != tc.wantLimit {
				t.Fatalf("parseCgroupV2Max(%q) limited = %t, want %t", tc.content, limited, tc.wantLimit)
			}
			if limited && got != tc.wantBytes {
				t.Fatalf("parseCgroupV2Max(%q) = %d, want %d", tc.content, got, tc.wantBytes)
			}
		})
	}
}

// TestParseCgroupV1Limit pins the parsing of a cgroup v1 memory.limit_in_bytes file:
// the page aligned LONG_MAX of an unlimited controller is no limit at all.
func TestParseCgroupV1Limit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   string
		wantBytes int64
		wantLimit bool
	}{
		{"half a gigabyte", "536870912\n", 536870912, true},
		{"without a newline", "1073741824", 1073741824, true},
		{"the unlimited sentinel is no limit", "9223372036854771712\n", 0, false},
		{"a value above the sentinel is no limit", "9223372036854775807\n", 0, false},
		{"empty is no limit", "", 0, false},
		{"broken is no limit", "nope\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, limited := parseCgroupV1Limit(tc.content)
			if limited != tc.wantLimit {
				t.Fatalf("parseCgroupV1Limit(%q) limited = %t, want %t", tc.content, limited, tc.wantLimit)
			}
			if limited && got != tc.wantBytes {
				t.Fatalf("parseCgroupV1Limit(%q) = %d, want %d", tc.content, got, tc.wantBytes)
			}
		})
	}
}

// TestParseProcSelfCgroup pins the parsing of /proc/self/cgroup: the line of the
// unified hierarchy (0::/path) and the line of the memory controller (5:memory:/path).
func TestParseProcSelfCgroup(t *testing.T) {
	content := "0::/user.slice/user-1000.slice\n" +
		"1:name=systemd:/init.scope\n" +
		"4:cpu,cpuacct:/cpu,cpuacct\n" +
		"5:memory:/docker/abc123\n"
	entries := parseProcSelfCgroup(content)
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(entries), entries)
	}
	if entries[0].hierarchyID != "0" || entries[0].path != "/user.slice/user-1000.slice" {
		t.Fatalf("the unified hierarchy line = %+v", entries[0])
	}
	if len(entries[0].controllers) != 0 {
		t.Fatalf("the unified hierarchy line carries controllers: %+v", entries[0].controllers)
	}
	if entries[2].hierarchyID != "4" || len(entries[2].controllers) != 2 {
		t.Fatalf("the cpu line = %+v", entries[2])
	}
	if entries[3].hierarchyID != "5" || len(entries[3].controllers) != 1 || entries[3].controllers[0] != "memory" {
		t.Fatalf("the memory line = %+v", entries[3])
	}
	if entries[3].path != "/docker/abc123" {
		t.Fatalf("the memory path = %q", entries[3].path)
	}
}

// TestCgroupPathsFromProcSelfCgroup pins which path of the hierarchy belongs to the
// unified hierarchy and which one belongs to the memory controller.
func TestCgroupPathsFromProcSelfCgroup(t *testing.T) {
	entries := parseProcSelfCgroup("0::/unified/path\n5:memory:/v1/path\n")
	rel, ok := cgroupV2Path(entries)
	if !ok || rel != "/unified/path" {
		t.Fatalf("cgroupV2Path = %q/%t, want /unified/path/true", rel, ok)
	}
	rel, ok = cgroupV1MemoryPath(entries)
	if !ok || rel != "/v1/path" {
		t.Fatalf("cgroupV1MemoryPath = %q/%t, want /v1/path/true", rel, ok)
	}
	// A v1 only machine has no unified hierarchy line.
	entries = parseProcSelfCgroup("5:memory:/v1/path\n")
	if _, ok := cgroupV2Path(entries); ok {
		t.Fatal("cgroupV2Path found a unified hierarchy on a v1 only machine")
	}
}

// TestCgroupCandidatePaths pins the files that carry the budget: the memory.max of
// the cgroup of the process and the memory.limit_in_bytes of the memory controller.
func TestCgroupCandidatePaths(t *testing.T) {
	v2 := cgroupV2Candidates("/user.slice")
	if len(v2) == 0 || v2[0] != "/sys/fs/cgroup/user.slice/memory.max" {
		t.Fatalf("the v2 candidates = %+v", v2)
	}
	if v2[len(v2)-1] != "/sys/fs/cgroup/memory.max" {
		t.Fatalf("the v2 candidates = %+v", v2)
	}
	v1 := cgroupV1Candidates("/docker/abc123")
	if len(v1) == 0 || v1[0] != "/sys/fs/cgroup/memory/docker/abc123/memory.limit_in_bytes" {
		t.Fatalf("the v1 candidates = %+v", v1)
	}
	if v1[len(v1)-1] != "/sys/fs/cgroup/memory/memory.limit_in_bytes" {
		t.Fatalf("the v1 candidates = %+v", v1)
	}
	// The root of a hierarchy is the file of the mount point itself.
	v2 = cgroupV2Candidates("/")
	if v2[0] != "/sys/fs/cgroup/memory.max" {
		t.Fatalf("the v2 candidates of the root = %+v", v2)
	}
}

// TestCgroupMemoryMBReadsTheV2Fixture pins that the detector reads the cgroup v2
// memory.max of the cgroup of the process.
func TestCgroupMemoryMBReadsTheV2Fixture(t *testing.T) {
	fixture := fakeFS{
		cgroupSelfPath: "0::/docker/abc123\n",
		"/sys/fs/cgroup/docker/abc123/memory.max": "1073741824\n",
	}
	mb, source, ok := cgroupMemoryMBWith(fixture.read)
	if !ok {
		t.Fatal("the v2 fixture was not read")
	}
	if source != "cgroup-v2" {
		t.Fatalf("source = %q, want cgroup-v2", source)
	}
	if mb != 1024 {
		t.Fatalf("the budget = %d MB, want 1024 MB", mb)
	}
}

// TestCgroupMemoryMBReadsTheV1Fixture pins that the detector reads the cgroup v1
// memory.limit_in_bytes of the memory controller.
func TestCgroupMemoryMBReadsTheV1Fixture(t *testing.T) {
	fixture := fakeFS{
		cgroupSelfPath: "5:memory:/docker/abc123\n",
		"/sys/fs/cgroup/memory/docker/abc123/memory.limit_in_bytes": "536870912\n",
	}
	mb, source, ok := cgroupMemoryMBWith(fixture.read)
	if !ok {
		t.Fatal("the v1 fixture was not read")
	}
	if source != "cgroup-v1" {
		t.Fatalf("source = %q, want cgroup-v1", source)
	}
	if mb != 512 {
		t.Fatalf("the budget = %d MB, want 512 MB", mb)
	}
}

// TestCgroupMemoryMBPrefersV2 pins that the unified hierarchy wins when a machine
// mounts both.
func TestCgroupMemoryMBPrefersV2(t *testing.T) {
	fixture := fakeFS{
		cgroupSelfPath: "0::/docker/abc123\n5:memory:/docker/abc123\n",
		"/sys/fs/cgroup/docker/abc123/memory.max":                   "1073741824\n",
		"/sys/fs/cgroup/memory/docker/abc123/memory.limit_in_bytes": "536870912\n",
	}
	mb, source, ok := cgroupMemoryMBWith(fixture.read)
	if !ok {
		t.Fatal("the fixture was not read")
	}
	if source != "cgroup-v2" {
		t.Fatalf("source = %q, want cgroup-v2", source)
	}
	if mb != 1024 {
		t.Fatalf("the budget = %d MB, want 1024 MB", mb)
	}
}

// TestCgroupMemoryMBFallsBackToTheMountPoint pins that the detector still finds the
// budget when /proc/self/cgroup is not readable (the mount point of the hierarchy is
// then the cgroup of the process).
func TestCgroupMemoryMBFallsBackToTheMountPoint(t *testing.T) {
	fixture := fakeFS{"/sys/fs/cgroup/memory.max": "268435456\n"}
	mb, source, ok := cgroupMemoryMBWith(fixture.read)
	if !ok {
		t.Fatal("the mount point fixture was not read")
	}
	if source != "cgroup-v2" {
		t.Fatalf("source = %q, want cgroup-v2", source)
	}
	if mb != 256 {
		t.Fatalf("the budget = %d MB, want 256 MB", mb)
	}
}

// TestCgroupMemoryMBReportsNoBudget pins that a machine without a cgroup budget (an
// unlimited cgroup or no cgroup at all) reports no budget instead of a wrong one.
func TestCgroupMemoryMBReportsNoBudget(t *testing.T) {
	for _, fixture := range []fakeFS{
		{},
		{cgroupSelfPath: "0::/docker/abc123\n", "/sys/fs/cgroup/docker/abc123/memory.max": "max\n"},
		{cgroupSelfPath: "5:memory:/docker/abc123\n", "/sys/fs/cgroup/memory/docker/abc123/memory.limit_in_bytes": "9223372036854771712\n"},
	} {
		mb, source, ok := cgroupMemoryMBWith(fixture.read)
		if ok {
			t.Fatalf("the fixture reported a budget of %d MB (%s), want none", mb, source)
		}
	}
}

// TestCgroupMemoryMBReadsNothingWhenTheFilesAreMissing pins that a missing hierarchy
// is not an error and never a fabricated budget.
func TestCgroupMemoryMBReadsNothingWhenTheFilesAreMissing(t *testing.T) {
	mb, source, ok := cgroupMemoryMBWith(func(string) ([]byte, error) {
		return nil, errors.New("no hierarchy here")
	})
	if ok {
		t.Fatalf("the reader reported a budget of %d MB (%s), want none", mb, source)
	}
}
