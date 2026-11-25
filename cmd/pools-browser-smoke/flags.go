// Path: cmd/pools-browser-smoke/flags.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the flag surface of the browser smoke harness. The parsing is kept
// apart from the run so it can be unit tested without a browser and without a
// server: parseFlags applies the defaults, validates the values and answers the
// route prefixes of the requested pools.
package main

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// defaultTimeout is the overall budget of one harness run.
const defaultTimeout = 120 * time.Second

// options is the parsed command line of the harness.
type options struct {
	// Base is the server base URL, for example http://127.0.0.1:8091.
	Base string
	// StartLocal starts pools-core from this tree on a free 127.0.0.1 port
	// with a temp data dir and the example config, and stops it on exit.
	StartLocal bool
	// Pools are the pool ids to check: tp (thread) and pp (process).
	Pools []string
	// Timeout bounds the whole run.
	Timeout time.Duration
	// Chrome is the optional path of the Chrome binary.
	Chrome string
	// AdminKey is the Store A admin API key of the server. It is needed for
	// the developer key flow of cmd/pools-e2e. When it is empty and the
	// server is local, the harness uses the bootstrap key of the local
	// server it starts itself.
	AdminKey string

	// poolsRaw is the raw -pools flag value, before parsePools splits it.
	poolsRaw string
}

// pool ids of the two pools. They are the ids the page uses in ?pools=tp,pp
// (web/pages/js/contribute.js) and the ids the server routes under.
const (
	poolThread  = "tp"
	poolProcess = "pp"
)

// poolPrefix maps a pool id to its REST route prefix.
var poolPrefix = map[string]string{
	poolThread:  "/thread-pool/v1",
	poolProcess: "/process-pool/v1",
}

// parseFlags parses argv (without the program name) and answers the options.
// It never touches the filesystem and never starts a process, so it is unit
// testable on its own.
func parseFlags(name string, args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(discardWriter{})
	fs.StringVar(&o.Base, "base", "http://127.0.0.1:8091", "pool server base URL")
	fs.BoolVar(&o.StartLocal, "start-local", false, "start pools-core from this tree on a free 127.0.0.1 port and stop it on exit")
	fs.StringVar(&o.poolsRaw, "pools", "tp,pp", "comma separated pool ids to check: tp,pp")
	fs.DurationVar(&o.Timeout, "timeout", defaultTimeout, "overall timeout of the run")
	fs.StringVar(&o.Chrome, "chrome", "", "path of the Chrome binary (optional)")
	fs.StringVar(&o.AdminKey, "admin-key", "", "Store A admin API key (default: the local bootstrap key of the started server)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	o.Base = strings.TrimRight(strings.TrimSpace(o.Base), "/")
	if o.Base == "" {
		return o, fmt.Errorf("-base must not be empty")
	}
	if !strings.HasPrefix(o.Base, "http://") && !strings.HasPrefix(o.Base, "https://") {
		return o, fmt.Errorf("-base must be an http(s) URL, got %q", o.Base)
	}
	if o.Timeout <= 0 {
		return o, fmt.Errorf("-timeout must be positive, got %s", o.Timeout)
	}
	pools, err := parsePools(o.poolsRaw)
	if err != nil {
		return o, err
	}
	o.Pools = pools
	return o, nil
}

// parsePools reads the comma separated pool list. Unknown ids are an error, so
// a typo never silently reduces the coverage of the run.
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

// prefixes answers the REST route prefixes of the checked pools, in the order
// of the pool list.
func (o options) prefixes() []string {
	out := make([]string, 0, len(o.Pools))
	for _, p := range o.Pools {
		out = append(out, poolPrefix[p])
	}
	return out
}

// discardWriter drops the flag set output. The harness prints its own report,
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
