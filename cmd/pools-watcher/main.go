// Path: cmd/pools-watcher/main.go | WO F16 (cmdc)
// What/why: the entry point of the contributor-browser watcher. It parses the
// flags, reads the developer bearer token from the environment (never printed),
// wires the real SSE source, the queue-status prober and the headless Chrome
// launcher, and runs the watcher until SIGINT/SIGTERM. On exit every launched
// tab is closed, so no Chrome child process survives.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	o, err := parseFlags("pools-watcher", os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "pools-watcher:", err)
		os.Exit(2)
	}
	// The token is a secret: it is read once and never logged.
	token := os.Getenv(tokenEnv)
	if token == "" {
		fmt.Fprintln(os.Stderr, "pools-watcher:", tokenEnv, "must be set to a developer bearer token (it is never logged)")
		os.Exit(2)
	}

	ctx, cancel := interruptContext(context.Background())
	defer cancel()

	launcher, err := newChromeLauncher(o.Chrome)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pools-watcher:", err)
		os.Exit(1)
	}

	w := newWatcher(o, newSSESource(o, token), newHTTPProber(o, token), launcher)
	w.Run(ctx)
}

// interruptContext answers a context that is cancelled on SIGINT and SIGTERM,
// so a Ctrl-C ends the run and closes every launched Chrome process.
func interruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
