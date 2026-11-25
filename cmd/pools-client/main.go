// Path: cmd/pools-client/main.go | Author: Copilot | Date: 2026-11-20
// What/why: Small native CLI/TUI for both pool APIs over REST or gRPC.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ccgsc/poolscore/internal/checksuite"
	"ccgsc/poolscore/internal/client"
	"ccgsc/poolscore/internal/redact"
)

type options struct {
	base, keyFile, keyPath string
	key                    string // loaded key; kept for the error redaction boundary (T101)
	grpc                   bool
	pool                   string
	size, slots            int
	out                    string
	timeout                time.Duration
}

func main() {
	var o options
	flag.StringVar(&o.base, "base", "http://127.0.0.1:8091", "pool server base URL")
	flag.StringVar(&o.keyFile, "key-file", "", "file containing the API key")
	flag.StringVar(&o.keyPath, "key", "", "file containing the API key")
	flag.BoolVar(&o.grpc, "grpc", false, "use gRPC where available")
	flag.StringVar(&o.pool, "pool", "both", "thread, process, or both")
	flag.IntVar(&o.size, "size", 160, "matmul size used by submit (worker tasks follow the assigned payload)")
	flag.IntVar(&o.slots, "slots", 1, "worker slots")
	flag.Parse()
	keyFile := o.keyFile
	if keyFile == "" {
		keyFile = o.keyPath
	}
	key, err := loadKey(keyFile)
	if err != nil {
		fatal(err)
	}
	o.key = key
	if flag.NArg() == 0 {
		fatal(errors.New("command is required"))
	}
	args := flag.Args()
	var commandArgs []string
	commandArgs, err = parseCommandOptions(&o, args[1:])
	if err != nil {
		fatal(err)
	}
	c, err := client.New(o.base, key, o.grpc)
	if err != nil {
		fatal(sanitize(err, key))
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, c, o, args[0], commandArgs); err != nil {
		fatal(sanitize(err, key))
	}
}

// sanitize keeps client errors credential-free at the print boundary (T101,
// cmdc leak sweep): the API key may travel in a request body, and a server
// that echoes the body (or an error page) back would otherwise leak the key
// through fatal() or the TUI loop. The key never appears in the output.
func sanitize(err error, key string) error {
	return redact.Wrap(err, key)
}

func parseCommandOptions(o *options, args []string) ([]string, error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-pool" || args[i] == "--pool" {
			if i+1 >= len(args) {
				return nil, errors.New("-pool requires a value")
			}
			o.pool = args[i+1]
			i++
		} else if args[i] == "-size" || args[i] == "--size" {
			if i+1 >= len(args) {
				return nil, errors.New("-size requires a value")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid -size: %w", err)
			}
			o.size = v
			i++
		} else if args[i] == "-slots" || args[i] == "--slots" {
			if i+1 >= len(args) {
				return nil, errors.New("-slots requires a value")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid -slots: %w", err)
			}
			o.slots = v
			i++
		} else if args[i] == "-out" || args[i] == "--out" {
			if i+1 >= len(args) {
				return nil, errors.New("-out requires a value")
			}
			o.out = args[i+1]
			i++
		} else if args[i] == "-timeout" || args[i] == "--timeout" {
			if i+1 >= len(args) {
				return nil, errors.New("-timeout requires a value")
			}
			v, err := time.ParseDuration(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid -timeout: %w", err)
			}
			o.timeout = v
			i++
		} else {
			rest = append(rest, args[i])
		}
	}
	return rest, nil
}

func execute(ctx context.Context, c *client.Client, o options, command string, args []string) error {
	switch command {
	case "check":
		// T108: the check subcommand runs the same shared suite as
		// cmd/pools-e2e -full; no duplicated suite code.
		return runCheck(ctx, o)
	case "caps":
		for _, p := range pools(o.pool) {
			v, err := c.Capabilities(ctx, p)
			if err != nil {
				return err
			}
			printJSON(v)
		}
	case "submit":
		for _, p := range pools(o.pool) {
			id, err := c.Submit(ctx, p, o.size)
			if err != nil {
				return err
			}
			fmt.Println(id)
		}
	case "status", "results", "watch":
		if len(args) != 1 {
			return errors.New(command + " requires jobId")
		}
		p := client.Pool(o.pool)
		if p != client.Thread && p != client.Process {
			return errors.New("-pool must be thread or process for " + command)
		}
		var v any
		var err error
		switch command {
		case "status":
			v, err = c.Status(ctx, p, args[0])
			if err == nil {
				printJSON(v)
			}
		case "results":
			v, err = c.Results(ctx, p, args[0])
			if err == nil {
				printJSON(v)
			}
		case "watch":
			err = c.Watch(ctx, p, args[0], func(v any) error { printJSON(v); return nil })
		}
		return err
	case "worker":
		ps := pools(o.pool)
		if len(ps) == 1 {
			return c.Worker(ctx, ps[0], o.slots)
		}
		errs := make(chan error, len(ps))
		for _, p := range ps {
			go func(poolID client.Pool) { errs <- c.Worker(ctx, poolID, o.slots) }(p)
		}
		for range ps {
			if err := <-errs; err != nil {
				return err
			}
		}
	case "tui":
		return tui(ctx, c, o)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	return nil
}

// runCheck executes the shared T108 full check suite with the CLI options.
// The API key of the CLI is the admin key of the suite: it creates the suite
// developer and contributor keys, runs the whole 2 pools x 3 APIs matrix and
// revokes its own keys again through the T99 route.
func runCheck(ctx context.Context, o options) error {
	var pools []string
	switch o.pool {
	case "thread":
		pools = []string{checksuite.ThreadPrefix}
	case "process":
		pools = []string{checksuite.ProcessPrefix}
	case "both":
	default:
		return errors.New("-pool must be thread, process or both for check")
	}
	rep, err := checksuite.Run(ctx, checksuite.Options{
		Base:     o.base,
		AdminKey: o.key,
		Pools:    pools,
		Size:     o.size,
		Timeout:  o.timeout,
	})
	if err != nil {
		return err
	}
	if o.out != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(o.out, append(b, '\n'), 0600); err != nil {
			return err
		}
	}
	fmt.Print(rep.Table())
	if !rep.Pass {
		return errors.New("check suite failed")
	}
	return nil
}

func tui(ctx context.Context, c *client.Client, o options) error {
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("pools-client [caps|submit|status|results|watch|quit]> ")
		if !in.Scan() {
			return in.Err()
		}
		fields := strings.Fields(in.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "quit" || fields[0] == "exit" {
			return nil
		}
		if err := execute(ctx, c, o, fields[0], fields[1:]); err != nil {
			fmt.Fprintln(os.Stderr, sanitize(err, o.key))
		}
	}
}

func pools(value string) []client.Pool {
	switch value {
	case "thread":
		return []client.Pool{client.Thread}
	case "process":
		return []client.Pool{client.Process}
	default:
		return []client.Pool{client.Thread, client.Process}
	}
}

func loadKey(path string) (string, error) {
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(raw)) == "" {
			return "", errors.New("key file is empty")
		}
		return strings.TrimSpace(string(raw)), nil
	}
	key := strings.TrimSpace(os.Getenv("POOLS_CLIENT_KEY"))
	if key == "" {
		return "", errors.New("POOLS_CLIENT_KEY or -key-file is required")
	}
	return key, nil
}

func printJSON(v any) {
	switch raw := v.(type) {
	case []byte:
		fmt.Println(string(raw))
	default:
		b, _ := json.Marshal(raw)
		fmt.Println(string(b))
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
