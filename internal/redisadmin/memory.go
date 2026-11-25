// Path: internal/redisadmin/memory.go | Author: Codex | 2026-11-20
// T91: MemAvailable is the dynamic budget; unsupported OSes never apply it.
package redisadmin

import (
	"bufio"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
)

var ErrUnsupported = errors.New("dynamic Redis memory mode unsupported on this OS")

func FreeRAMBytes() (uint64, error) {
	if runtime.GOOS != "linux" {
		return 0, ErrUnsupported
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, errors.New("MemAvailable unavailable")
	}
	defer f.Close()
	return parseMemAvailable(f)
}

func parseMemAvailable(r io.Reader) (uint64, error) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) == 0 || fields[0] != "MemAvailable:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			break
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || n > ^uint64(0)/1024 {
			break
		}
		return n * 1024, nil
	}
	return 0, errors.New("MemAvailable unavailable")
}
