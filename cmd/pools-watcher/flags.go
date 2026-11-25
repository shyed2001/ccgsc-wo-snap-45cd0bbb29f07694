// Path: cmd/pools-watcher/flags.go | WO F16 (cmdc)
// What/why: the flag surface of the contributor-browser watcher. The parsing is
// kept apart from the run so it can be unit tested without Chrome and without a
// server: parseFlags applies the defaults, validates the values and answers the
// route prefixes of the requested pools.
//
// The bearer token is deliberately NOT a flag: it is read from the environment
// (POOLS_TOKEN) in main.go and never printed.
package main

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// Defaults of the watcher. A light machine opens one tab, keeps tabs while the
// pools are busy, and only falls back to polling the queue status while the SSE
// stream is down.
const (
	defaultServer  = "http://127.0.0.1:8091"
	defaultPools   = "tp,pp"
	defaultMaxTabs = 1
	defaultIdle    = 60 * time.Second
	defaultPoll    = 5 * time.Second
	// tokenEnv is the environment variable that carries the developer bearer
	// token of the watcher. Its value is never logged.
	tokenEnv = "POOLS_TOKEN"
	// maxBackoff caps the poll/reconnect backoff while SSE is down.
	maxBackoff = 30 * time.Second
)

// Pool ids of the two pools and their REST route prefixes. They are the ids the
// contribute page uses in ?pools=tp,pp (web/pages/js/contribute.js) and the
// ids the server routes under (internal/api/api.go).
const (
	poolThread  = "tp"
	poolProcess = "pp"
)

var poolPrefix = map[string]string{
	poolThread:  "/thread-pool/v1",
	poolProcess: "/process-pool/v1",
}

// options is the parsed command line of the watcher.
type options struct {
	// Server is the pool server base URL, for example http://127.0.0.1:8091.
	Server string
	// Pools are the pool ids to watch: tp (thread) and pp (process).
	Pools []string
	// Chrome is the optional path of the Chrome binary.
	Chrome string
	// MaxTabs is the maximum number of concurrent contributor tabs.
	MaxTabs int
	// Idle closes the tabs after every watched pool has been idle this long.
	Idle time.Duration
	// Poll is the queue-status poll interval while SSE is down. The reconnect
	// backs off from Poll up to maxBackoff.
	Poll time.Duration

	// poolsRaw is the raw -pools flag value, before parsePools splits it.
	poolsRaw string
}

// parseFlags parses argv (without the program name) and answers the options.
// It never touches the filesystem and never starts a process, so it is unit
// testable on its own.
func parseFlags(name string, args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(discardWriter{})
	fs.StringVar(&o.Server, "server", defaultServer, "pool server base URL")
	fs.StringVar(&o.poolsRaw, "pools", defaultPools, "comma separated pool ids to watch: tp,pp")
	fs.StringVar(&o.Chrome, "chrome", "", "path of the Chrome binary (optional)")
	fs.IntVar(&o.MaxTabs, "max-tabs", defaultMaxTabs, "maximum concurrent contributor tabs")
	fs.DurationVar(&o.Idle, "idle", defaultIdle, "close tabs after every watched pool is idle this long")
	fs.DurationVar(&o.Poll, "poll", defaultPoll, "queue status poll interval while SSE is down")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	o.Server = strings.TrimRight(strings.TrimSpace(o.Server), "/")
	if o.Server == "" {
		return o, fmt.Errorf("-server must not be empty")
	}
	if !strings.HasPrefix(o.Server, "http://") && !strings.HasPrefix(o.Server, "https://") {
		return o, fmt.Errorf("-server must be an http(s) URL, got %q", o.Server)
	}
	pools, err := parsePools(o.poolsRaw)
	if err != nil {
		return o, err
	}
	o.Pools = pools
	if o.MaxTabs < 1 {
		return o, fmt.Errorf("-max-tabs must be at least 1, got %d", o.MaxTabs)
	}
	if o.Idle <= 0 {
		return o, fmt.Errorf("-idle must be positive, got %s", o.Idle)
	}
	if o.Poll <= 0 {
		return o, fmt.Errorf("-poll must be positive, got %s", o.Poll)
	}
	return o, nil
}

// parsePools reads the comma separated pool list. Unknown ids are an error, so
// a typo never silently reduces the set of watched pools.
func parsePools(raw string) ([]string, error) {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.ToLower(strings.TrimSpace(part))
		if id == "" {
			continue
		}
		if _, ok := poolPrefix[id]; !ok {
			return nil, fmt.Errorf("unknown pool id %q: use tp and/or pp", part)
		}
		if !contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-pools must name at least one pool")
	}
	return out, nil
}

// prefixes answers the REST route prefixes of the watched pools, in the order
// of the pool list.
func (o options) prefixes() []string {
	out := make([]string, 0, len(o.Pools))
	for _, p := range o.Pools {
		out = append(out, poolPrefix[p])
	}
	return out
}

// discardWriter drops the flag set output. The watcher prints its own errors,
// so the flag package must not write to stderr behind its back.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
