// Path: cmd/pools-browser-smoke/server.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the local pools-core of the -start-local mode.
//
// It starts TWO instances of pools-core from THIS tree:
//
//   - the main instance, which serves the contributor page and the whole REST
//     and SSE surface;
//   - the run-origin instance, the "fourth host" of WWPP-R29.1, which serves
//     ONLY /runner.html with the strict sandbox CSP.
//
// Two instances are needed because the sandbox page must have an origin of
// its own: internal/api/sandbox_MVPW.go serves the run origin to every
// request whose Host matches Config.RunOrigin, so the run origin cannot be
// the address of the main instance (that would send every request of the
// main instance into the run-origin branch, which answers 404 for anything
// but /runner.html).
//
// Both processes are started as the compiled binary, never through "go run":
// a go run wrapper spawns the server as a child, and killing the wrapper
// leaves that child alive. The binary is killed directly, so no server
// survives the harness, also on Ctrl-C.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// localServer is the set of pools-core processes the harness owns.
type localServer struct {
	// Base is the base URL of the main instance.
	Base string
	// RunOrigin is the base URL of the run-origin instance.
	RunOrigin string
	// AdminKey is the bootstrap key the main instance accepts.
	AdminKey string
	// DataDir is the temp dir the instances store their data in.
	DataDir string

	binary    string
	instances []*instance
	stopped   bool
}

// instance is one started pools-core process.
type instance struct {
	cmd     *exec.Cmd
	logFile *os.File
	addr    string
}

// bootstrapKey is the admin key of the local server. It is a throwaway key of
// a throwaway local database: the harness generates it per run, so no secret
// is ever written to disk or reused between runs. The key never leaves the
// harness except as the Authorization header of its own requests.
//
// The key must be at least 24 characters long: internal/auth.saveKey refuses
// a shorter one with "invalid user/store roles or short key", so a short key
// would leave the local server without an admin key.
var bootstrapKey = "browser-smoke-local-" + fmt.Sprint(time.Now().UnixNano()) + "-bootstrap"

// freePort answers a free TCP port on the loopback interface.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve a free port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// repoRoot answers the root of the pools-core tree of this harness. The
// harness lives in <root>/cmd/pools-browser-smoke, so the root is the
// nearest ancestor directory that carries the go.mod of the module.
//
// The search starts at the working directory, because go run and go test both
// run the harness from inside the tree: go test from the package directory,
// go run from the tree root. The binary path is only the fallback, because a
// go run binary lives in a temp dir.
func repoRoot() (string, error) {
	if wd, err := os.Getwd(); err == nil {
		if root, ok := findModuleRoot(wd); ok {
			return root, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		if root, ok := findModuleRoot(filepath.Dir(exe)); ok {
			return root, nil
		}
	}
	return "", errors.New("locate the pools-core tree: no go.mod found")
}

// findModuleRoot walks up from dir and answers the nearest directory that
// carries a go.mod.
func findModuleRoot(dir string) (string, bool) {
	for candidate := dir; candidate != "" && candidate != filepath.Dir(candidate); candidate = filepath.Dir(candidate) {
		if _, err := os.Stat(filepath.Join(candidate, "go.mod")); err == nil {
			return candidate, true
		}
	}
	return "", false
}

// startLocalServer starts the local pools-core instances and waits until they
// answer. The returned server must be stopped by the caller.
func startLocalServer(ctx context.Context, root string) (*localServer, error) {
	dataDir, err := os.MkdirTemp("", "pools-browser-smoke-")
	if err != nil {
		return nil, fmt.Errorf("create the temp data dir: %w", err)
	}
	srv := &localServer{DataDir: dataDir}

	// The binary is compiled once and executed directly. A "go run" wrapper
	// would spawn the server as a child process, and killing the wrapper
	// would leave that child alive: a stray server, which the owner rule
	// forbids.
	srv.binary = filepath.Join(dataDir, "pools-core")
	if os.Getenv("GOOS") == "" && runtimeIsWindows() {
		srv.binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", srv.binary, "./cmd/pools-core")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dataDir)
		return nil, fmt.Errorf("build pools-core: %w\n%s", err, out)
	}

	mainPort, err := freePort()
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, err
	}
	runPort, err := freePort()
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, err
	}
	mainAddr := fmt.Sprintf("127.0.0.1:%d", mainPort)
	runAddr := fmt.Sprintf("127.0.0.1:%d", runPort)
	srv.Base = "http://" + mainAddr
	srv.RunOrigin = "http://" + runAddr
	srv.AdminKey = bootstrapKey

	// The main instance: the example config of the tree with the listen
	// address, the database, the log dir and the two origins rewritten. The
	// run origin points at the run-origin instance, so the page loads the
	// sandbox from an origin of its own.
	mainCfg, err := loadExampleConfig(root, mainAddr, filepath.Join(dataDir, "main"), srv.RunOrigin, srv.Base)
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, err
	}
	// The run-origin instance: the same example config, but the run origin
	// is its OWN address, so every request it receives is a run-origin
	// request and it serves only /runner.html. The public origin is the
	// address of the main instance, so the sandbox page may be framed by
	// the contributor page.
	runCfg, err := loadExampleConfig(root, runAddr, filepath.Join(dataDir, "run"), srv.RunOrigin, srv.Base)
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, err
	}

	if err := srv.startInstance(ctx, root, "main", mainCfg, mainAddr, "/pools/runner-config"); err != nil {
		srv.Stop()
		return nil, err
	}
	// The run-origin instance serves ONLY /runner.html, so it is probed on
	// that route.
	if err := srv.startInstance(ctx, root, "run", runCfg, runAddr, "/runner.html"); err != nil {
		srv.Stop()
		return nil, err
	}
	return srv, nil
}

// startInstance writes the config of one instance, starts the binary and waits
// until it answers.
func (s *localServer) startInstance(ctx context.Context, root, name string, cfg map[string]any, addr string, probePath string) error {
	dir := filepath.Join(s.DataDir, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create the data dir of the %s instance: %w", name, err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("write the config of the %s instance: %w", name, err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		return fmt.Errorf("write the config of the %s instance: %w", name, err)
	}
	logPath := filepath.Join(dir, "pools-core.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create the log of the %s instance: %w", name, err)
	}
	cmd := exec.Command(s.binary, "-config", cfgPath, "-addr", addr,
		"-database", filepath.Join(dir, "pools.db"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"POOLS_BOOTSTRAP_KEY="+bootstrapKey,
		"POOLS_LOG_DIR="+filepath.Join(dir, "logs"),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start the %s instance: %w", name, err)
	}
	inst := &instance{cmd: cmd, logFile: logFile, addr: addr}
	s.instances = append(s.instances, inst)
	if err := waitReady(ctx, "http://"+addr, 90*time.Second, inst, probePath); err != nil {
		return fmt.Errorf("the %s instance: %w (the log is in %s)\n%s",
			name, err, logPath, tailFile(logPath, 20))
	}
	return nil
}

// tailFile answers the last n lines of a file, for a startup error. The
// server writes its reason there, and the harness shows it instead of
// leaving the operator with a timeout and no clue.
func tailFile(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(the log could not be read: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// waitReady waits until the server answers.
//
// The probe route depends on the instance: the main instance answers the
// runner-config route, but the run-origin instance answers ONLY /runner.html
// (internal/api/sandbox_MVPW.go sends every request of the run origin into
// the run-origin branch, which answers 404 for anything else).
func waitReady(ctx context.Context, base string, budget time.Duration, inst *instance, probePath string) error {
	deadline := time.Now().Add(budget)
	for {
		if inst.cmd.ProcessState != nil && inst.cmd.ProcessState.Exited() {
			return errors.New("the server exited before it answered")
		}
		if err := probeRoute(ctx, base, probePath); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the server did not answer %s within %s", probePath, budget)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Stop ends every server and removes the temp dir. It is safe to call twice
// and safe to call after a Ctrl-C: the processes are killed, the wait is
// bounded and the temp dir is removed.
func (s *localServer) Stop() {
	if s == nil || s.stopped {
		return
	}
	s.stopped = true
	for _, inst := range s.instances {
		if inst == nil || inst.cmd == nil || inst.cmd.Process == nil {
			continue
		}
		_ = inst.cmd.Process.Kill()
		done := make(chan struct{})
		go func() {
			_, _ = inst.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		if inst.logFile != nil {
			inst.logFile.Close()
		}
	}
	if s.DataDir != "" {
		_ = os.RemoveAll(s.DataDir)
	}
}

// interruptContext answers a context that is cancelled on SIGINT and SIGTERM,
// so a Ctrl-C unwinds the run and stops every process the harness started.
func interruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// loadExampleConfig reads the example config of the tree and rewrites the
// fields the local run needs: the listen address, the database, the log dir
// and the two origins. Everything else comes from the example config, so the
// local server is configured exactly like the shipped example.
func loadExampleConfig(root, addr, dataDir, runOrigin, publicOrigin string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(root, "config.example.json"))
	if err != nil {
		return nil, fmt.Errorf("read config.example.json: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config.example.json: %w", err)
	}
	cfg["addr"] = addr
	cfg["database"] = filepath.Join(dataDir, "pools.db")
	cfg["logDir"] = filepath.Join(dataDir, "logs")
	cfg["debug"] = true
	// The run origin and the public origin: the sandbox page is served by
	// the run-origin instance and may be framed by the main instance.
	cfg["runOrigin"] = runOrigin
	cfg["publicOrigin"] = publicOrigin
	// The lease store of the example config is "pg". A local run has no
	// PostgreSQL, and the engine keeps its leases in its own store, so the
	// local run never dials a lease server.
	if store, ok := cfg["leaseStore"].(string); !ok || store == "" {
		cfg["leaseStore"] = "pg"
	}
	return cfg, nil
}

// probeServer answers nil when the server answers its own endpoint.
func probeServer(ctx context.Context, base string) error {
	return probeRoute(ctx, base, "/pools/runner-config")
}

// probeRoute answers nil when the server answers the given route with 200.
func probeRoute(ctx context.Context, base, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered HTTP %d", res.StatusCode)
	}
	return nil
}

// runnerOrigin answers the origin that serves the sandbox page. It is read
// from the runner-config route of the server, because the deployment decides
// it: a local run has its own run-origin instance, a deployment without a run
// origin serves the sandbox page from the page tree itself.
func runnerOrigin(ctx context.Context, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/pools/runner-config", nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the runner-config route answered HTTP %d", res.StatusCode)
	}
	var cfg struct {
		RunOrigin *string `json:"runOrigin"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", fmt.Errorf("the runner-config route answered an invalid body: %w", err)
	}
	if cfg.RunOrigin != nil && *cfg.RunOrigin != "" {
		return strings.TrimRight(*cfg.RunOrigin, "/"), nil
	}
	return "", nil
}

// runtimeIsWindows reports whether the harness runs on Windows.
func runtimeIsWindows() bool {
	return os.PathSeparator == '\\'
}

// poolIDs answers the pool ids of the checked prefixes.
func poolIDs(prefixes []string) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		switch p {
		case poolPrefix[poolThread]:
			out = append(out, poolThread)
		case poolPrefix[poolProcess]:
			out = append(out, poolProcess)
		}
	}
	return out
}

// poolBaseURL answers the base URL of a pool, for a message.
func poolBaseURL(base, prefix string) string {
	return strings.TrimRight(base, "/") + prefix
}
