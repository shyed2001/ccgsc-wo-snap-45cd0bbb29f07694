//go:build browser

// Path: cmd/pools-browser-smoke/browser_test.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the integration test of the harness. It starts a real headless
// Chrome against a local pools-core and runs the whole check suite, so the
// browser side is proven by a test and not only by a manual run.
//
// The test lives behind the "browser" build tag, because it needs a Chrome
// binary and a free port and it takes seconds:
//
//	go test -tags browser ./cmd/pools-browser-smoke/
//
// The plain test run (go test ./...) never starts a browser.
package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestBrowserSmoke runs the whole harness against a local pools-core.
//
// The harness itself starts and stops the server, so the test never leaves a
// server behind: the local server is killed and its temp dir is removed on
// every exit path of the harness, also when the test fails.
func TestBrowserSmoke(t *testing.T) {
	if os.Getenv("POOLS_BROWSER_SMOKE") == "0" {
		t.Skip("the browser smoke is switched off by POOLS_BROWSER_SMOKE=0")
	}
	chrome := os.Getenv("POOLS_CHROME_PATH")
	o, err := parseFlags("pools-browser-smoke", []string{
		"-start-local",
		"-pools", "tp,pp",
		"-timeout", "180s",
		"-chrome", chrome,
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rep := &report{Checks: []check{}}
	if err := run(ctx, o, rep); err != nil {
		t.Fatalf("the browser smoke run failed: %v", err)
	}
	rep.print()
	for _, c := range rep.Checks {
		if !c.Pass && !c.Skipped {
			t.Errorf("the check %s failed: %s", c.Name, c.Reason)
		}
	}
}
