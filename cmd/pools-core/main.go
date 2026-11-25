// Path: cmd/pools-core/main.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: local debug service and graceful SIGINT lifecycle; WO-CORE-00, WWPP-R41.1.
package main

import (
	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/caps"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/grpcapi"
	"ccgsc/poolscore/internal/logx"
	"ccgsc/poolscore/internal/matmulref"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/profiling"
	"ccgsc/poolscore/internal/recompute"
	"ccgsc/poolscore/internal/redisadmin"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
	"ccgsc/poolscore/internal/store/redislease"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "pools-core:", e)
		os.Exit(1)
	}
}

// seedCredentialsFileName is the 0600 file the first-start seed admin
// password is written to, next to the database.
const seedCredentialsFileName = "admin-seed-credentials.txt"

// writeSeedCredentials stores the first-start seed admin credentials in a
// 0600 file next to the database and returns its path (T101). The password
// itself is never printed, so it cannot reach journald or CI logs. The file
// carries both fields so the operator can log in, change the password and
// delete the file.
func writeSeedCredentials(dbPath, user, password string) (string, error) {
	dir := filepath.Dir(dbPath)
	if dir == "" {
		dir = "."
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}
	path := filepath.Join(dir, seedCredentialsFileName)
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if e != nil {
		return "", e
	}
	_, e = fmt.Fprintf(f, "username=%s\npassword=%s\n", user, password)
	if e != nil {
		_ = f.Close()
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	// OpenFile applies 0600 only at creation; force it in case a credentials
	// file from an earlier run survived with wider permissions.
	if e = os.Chmod(path, 0600); e != nil {
		return "", e
	}
	return path, nil
}
func run() error {
	path := flag.String("config", "", "JSON configuration file")
	addr := flag.String("addr", "", "HTTP listen address override")
	dbpath := flag.String("database", "", "database path override")
	debug := flag.Bool("debug", true, "enable debug logging")
	pprofAddr := flag.String("pprof", "", "pprof listen address (loopback only, off by default)")
	flag.Parse()
	c, e := config.Load(*path)
	if e != nil {
		return e
	}
	if *addr != "" {
		c.Addr = *addr
	}
	if *dbpath != "" {
		c.Database = *dbpath
	}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "debug" {
			c.Debug = *debug
		}
	})
	if e = c.Validate(); e != nil {
		return e
	}
	l, logs, e := logx.New(c.LogDir, c.Debug)
	if e != nil {
		return e
	}
	defer logs.Close()
	// R15 AT22-GM2-3 (TD-17): the resource caps of the server are resolved and
	// applied before anything else starts, so every goroutine of the process runs
	// inside the cap. The applied values are logged with the start line.
	//
	// R16-FIX (F01): the resolution happens after the store opens, because the
	// admin record is durable there. The precedence is saved admin value >
	// configuration file > default, so a restart keeps an approved cap.
	configuredCaps, e := caps.Resolve(c.Caps())
	if e != nil {
		return fmt.Errorf("resolve the resource caps: %w", e)
	}
	var st store.Store
	// T113: log the store actually opened, not the config driver field.
	storeDriver := "sqlite"
	if dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN")); dsn != "" {
		storeDriver = "postgres"
		// The store opens before the engine can read the durable admin record, so
		// the pool uses the configured connection cap. A saved dbMaxOpenConns
		// stays restart-bound, exactly as the caps write reply says.
		st, e = pg.OpenWithMaxConns(dsn, configuredCaps.DBMaxOpenConns)
	} else {
		st, e = sqlite.Open(c.Database)
	}
	if e != nil {
		return e
	}
	defer st.Close()
	a := auth.New(st, c.JWTSecret, c.ServerID)
	if e = a.Bootstrap(context.Background(), c.BootstrapKey); e != nil {
		return e
	}
	seedCreated, seedUser, seedPass, e := a.InitStoreA(context.Background(), c.AdminUsers)
	if e != nil {
		return e
	}
	if seedCreated {
		// T101 (cmdc leak sweep): the seed password never goes to stdout -
		// journald and CI logs keep everything the service prints. It is
		// written to a 0600 file next to the database and only the file path
		// is logged.
		credsFile, e := writeSeedCredentials(c.Database, seedUser, seedPass)
		if e != nil {
			return fmt.Errorf("persist the seed admin credentials: %w", e)
		}
		fmt.Printf("[ADMIN AUTH] Initial seed user created: username=%s; password written to %s (mode 0600); force change required on first login\n", seedUser, credsFile)
	}
	engine, e := pool.New(st, l, c.ServerID, time.Duration(c.LeaseSeconds)*time.Second)
	if e != nil {
		return e
	}
	// WO R17-RECOMPUTE (G-R16-4) row 1: the generic reference-recompute
	// registry of the platform. The references register themselves here, in
	// the wiring OUTSIDE internal/pool, so the platform never imports
	// workload code (owner hard rule CR-53). The registry is generic: it
	// knows a module SHA-256 and a verifier, never a workload kind.
	recomputeRegistry := recompute.New()
	if e = matmulref.Register(recomputeRegistry); e != nil {
		return fmt.Errorf("register the matmul reference recompute: %w", e)
	}
	engine.SetRecomputeRegistry(recomputeRegistry)
	l.Info("recompute.referencesRegistered", "references", recomputeRegistry.Len(), "kinds", recomputeRegistry.Kinds())
	// R16-FIX (F01): the engine loaded the durable admin caps record, so the
	// effective caps are resolved here and applied before any worker of the
	// platform starts. saved admin value > configuration file > default.
	engine.SetConfiguredCaps(c.Caps())
	effectiveCaps, e := engine.EffectiveCaps()
	if e != nil {
		return fmt.Errorf("resolve the effective resource caps: %w", e)
	}
	caps.Apply(effectiveCaps, l)
	engine.SetAppliedCaps(effectiveCaps, 0)
	// R42.7 shared VPS Redis safe use: the client authenticates (AUTH), selects
	// the configured logical DB (SELECT) and prefixes every lease key on the
	// wire. Credentials come from the environment only; they are never logged
	// and never reported by /capabilities.
	rc, e := redislease.Connect(c.LeaseStore, os.Getenv("POOLS_REDIS_ADDR"), redislease.Options{
		Username: os.Getenv("POOLS_REDIS_USER"),
		Password: os.Getenv("POOLS_REDIS_PASSWORD"),
		DB:       c.LeaseRedisDB,
		Prefix:   c.LeaseRedisPrefix,
	})
	if e != nil {
		return e
	}
	if rc != nil {
		defer rc.Close()
	}
	engine.SetLeaseClient(c.LeaseStore, rc)
	// R15 AT22-GM2-3 (TD-17): the bounded worker pool of the CPU heavy platform
	// work (the native recompute) runs inside the resolved cap.
	engine.SetWorkerPool(effectiveCaps.WorkerPool)
	if e := engine.SetGPUConfig(pool.GPUConfig{Enabled: c.GPUEnabled, MaxSlotsPerTab: c.MaxGPUSlotsPerTab, OnBatteryAllowed: c.GPUOnBatteryAllowed}); e != nil {
		return e
	}
	// T91 / R42.8 (Codex, 2026-11-20): optional separate admin identity.
	redisAdmin := redisadmin.NewFromEnv()
	s := &api.Server{Engine: engine, Auth: a, Log: l, Config: c, RedisAdmin: redisAdmin, StoreDriver: storeDriver}
	if postgres, ok := st.(*pg.DB); ok {
		s.P2PDatabase = postgres.Pool
	}
	// W10: register the embedded reference workload as a job module artifact, so a
	// developer can submit it and a contributor fetches exactly those bytes. It
	// runs once per start and is idempotent (content-addressed blob, pinned sha256).
	if e := s.SeedWorkloadArtifacts(); e != nil {
		return fmt.Errorf("register the reference workload: %w", e)
	}
	grpcService := grpcapi.New(engine, a)
	grpcService.AllowPrivateWebhooks = c.WebhookAllowPrivateIPs
	grpcServer := grpcapi.NewServer(grpcService)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// N11 optional multi-server cache invalidation. Single-server starts no
	// LISTEN connection. An explicit opt-in requires the PostgreSQL store.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("POOLS_MULTI_SERVER_SYNC")), "true") {
		if storeDriver != "postgres" {
			return fmt.Errorf("POOLS_MULTI_SERVER_SYNC requires PostgreSQL")
		}
		stopNotify, err := engine.StartRecomputeSettingsNotify(ctx, os.Getenv("POOLS_PG_DSN"))
		if err != nil {
			return fmt.Errorf("start PostgreSQL recompute notifications: %w", err)
		}
		defer stopNotify()
		l.Info("recompute.pgNotifyEnabled", "mode", "multi-server")
	}
	srv := &http.Server{Addr: c.Addr, Handler: grpcServer.Mux(s.Handler()), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return ctx }}
	if e := s.InitializeRedisAdmin(ctx); e != nil {
		return e
	}
	var wg sync.WaitGroup
	wg.Go(func() { s.RunMaintenance(ctx) })
	if redisAdmin.Enabled() {
		wg.Go(func() { runRedisAdminTicker(ctx, redisAdmin) })
	}
	// R15 AT22-GM2-1 (GM2 row 1.1): the optional pprof endpoint. It is off by default
	// and it may only listen on a loopback address.
	var profiler *profiling.Server
	if *pprofAddr != "" {
		if e = profiling.ValidateAddr(*pprofAddr); e != nil {
			return fmt.Errorf("the pprof address: %w", e)
		}
		profiler, e = profiling.Start(*pprofAddr, l)
		if e != nil {
			return fmt.Errorf("start the pprof endpoint: %w", e)
		}
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = profiler.Close(shutdown)
		}()
	}
	defer wg.Wait()
	defer stop()
	done := make(chan error, 1)
	go func() {
		l.Info("server.start",
			"addr", c.Addr,
			"debug", c.Debug,
			"driver", storeDriver,
			"bootstrapConfigured", c.BootstrapKey != "",
			"grpc", true,
			// R15 AT22-GM2-3: the applied caps travel with the start line.
			"numCPU", effectiveCaps.NumCPU,
			"maxProcs", effectiveCaps.MaxProcs,
			"gomaxprocs", runtime.GOMAXPROCS(0),
			"memLimitMB", effectiveCaps.MemLimitMB,
			"workerPool", effectiveCaps.WorkerPool,
			"dbMaxOpenConns", effectiveCaps.DBMaxOpenConns,
		)
		done <- srv.ListenAndServe()
	}()
	select {
	case e = <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grpcServer.GracefulStop()
	e = srv.Shutdown(shutdown)
	if e != nil {
		_ = srv.Close()
	}
	l.Info("server.stopped")
	return e
}

// Only main owns scheduling. Changes reset the interval; fixed mode has no ticker.
func runRedisAdminTicker(ctx context.Context, r *redisadmin.Runner) {
	var ticker *time.Ticker
	var ticks <-chan time.Time
	reset := func() {
		if ticker != nil {
			ticker.Stop()
			ticker = nil
		}
		ticks = nil
		if interval := r.Interval(); interval > 0 {
			ticker = time.NewTicker(interval)
			ticks = ticker.C
		}
	}
	reset()
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.Changed():
			reset()
		case <-ticks:
			applyRedisAdminTick(ctx, r)
		}
	}
}

// applyRedisAdminTick performs one scheduled Redis admin apply. The tick has no
// response to carry a failure and its only other trace is a Status field nobody
// polls, so the error is logged. The runner's error text never carries
// credentials.
func applyRedisAdminTick(ctx context.Context, r *redisadmin.Runner) {
	if err := r.Apply(ctx); err != nil {
		slog.Default().Warn("redisadmin.scheduled_apply_failed", "error", err)
	}
}
