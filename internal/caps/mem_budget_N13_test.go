package caps

import (
	"runtime"
	"testing"
)

// Owner N13 policy: CPU-1 applies to CPU workers, not database connections.
// A bounded DB pool still needs at least four connections to avoid starvation.
func TestN13DBPoolKeepsExplicitConnectionCapIndependentOfCPUWorkers(t *testing.T) {
	n := runtime.NumCPU()
	if n < 4 {
		t.Skip("explicit N-1 comparison needs 4+ logical CPUs")
	}
	want := n
	if want > maxDBMaxOpenConns {
		want = maxDBMaxOpenConns
	}
	c, err := Resolve(Config{DBMaxOpenConns: n})
	if err != nil {
		t.Fatal(err)
	}
	if c.DBMaxOpenConns != want {
		t.Fatalf("DBMaxOpenConns=%d, want explicit %d (not CPU-1 worker ceiling)", c.DBMaxOpenConns, want)
	}
}

func TestN13DBPoolFourConnectionFloorAndBoundedCeiling(t *testing.T) {
	c, err := Resolve(Config{DBMaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if c.DBMaxOpenConns != 4 {
		t.Fatalf("DBMaxOpenConns=%d for configured 1; want min 4", c.DBMaxOpenConns)
	}
	c, err = Resolve(Config{DBMaxOpenConns: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if c.DBMaxOpenConns != maxDBMaxOpenConns {
		t.Fatalf("DBMaxOpenConns=%d for configured 1000; want bounded max %d", c.DBMaxOpenConns, maxDBMaxOpenConns)
	}
}
