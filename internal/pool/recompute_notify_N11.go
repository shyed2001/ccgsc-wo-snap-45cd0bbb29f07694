package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ccgsc/poolscore/internal/redact"
	"ccgsc/poolscore/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const recomputeNotifyChannel = "ccgsc_recompute_settings_n11"
const recomputeNotifyMaxBackoff = 5 * time.Second

// One generation reserves startup before doing network I/O. The publisher
// pool is immutable once installed, and one connection is reused per run.
type recomputeNotifyState struct {
	mu  sync.RWMutex
	run *recomputeNotifyRun
}
type recomputeNotifyRun struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	publisher *pgxpool.Pool // guarded by state.mu during publication
}
type recomputeNotifyPayload struct {
	Pool    string `json:"pool"`
	Version int    `json:"version"`
}

func (e *Engine) RecomputeNotifyEnabled() bool {
	e.recomputeNotify.mu.RLock()
	defer e.recomputeNotify.mu.RUnlock()
	return e.recomputeNotify.run != nil && e.recomputeNotify.run.publisher != nil
}

func nextRecomputeNotifyBackoff(backoff time.Duration) time.Duration {
	return min(backoff*2, recomputeNotifyMaxBackoff)
}

func (e *Engine) closeRecomputeNotifyConn(conn *pgx.Conn, dsn string) {
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Close(ctx); err != nil {
		e.Log.Warn("recompute.notifyCloseFailed", "error", redact.RedactDSNError(err, dsn))
	}
}

func (e *Engine) openRecomputeNotifyConn(ctx context.Context, dsn string) (*pgx.Conn, error) {
	connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(connCtx, dsn)
	if err != nil {
		return nil, redact.RedactDSNError(err, dsn)
	}
	if _, err = conn.Exec(connCtx, "LISTEN "+recomputeNotifyChannel); err != nil {
		e.closeRecomputeNotifyConn(conn, dsn)
		return nil, redact.RedactDSNError(err, dsn)
	}
	return conn, nil
}

// finishRecomputeNotify is owned by the startup failure path OR the
// listener goroutine, never both. The generation check protects a restart
// against a late stop call from a previous generation.
func (e *Engine) finishRecomputeNotify(run *recomputeNotifyRun, conn *pgx.Conn, dsn string) {
	run.cancel()
	e.closeRecomputeNotifyConn(conn, dsn)
	// pgxpool.Close drains borrowers. All publish operations have a bounded
	// context cancelled by run.ctx; no unbounded caller can retain the connection.
	if run.publisher != nil {
		run.publisher.Close()
	}
	e.recomputeNotify.mu.Lock()
	if e.recomputeNotify.run == run {
		e.recomputeNotify.run = nil
	}
	e.recomputeNotify.mu.Unlock()
	close(run.done)
}

// StartRecomputeSettingsNotify is explicit multi-server PostgreSQL opt-in.
// Parent cancellation and the returned idempotent stop both end the generation.
func (e *Engine) StartRecomputeSettingsNotify(ctx context.Context, dsn string) (func(), error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("%w: PG DSN required for multi-server notify", ErrInvalid)
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := &recomputeNotifyRun{ctx: runCtx, cancel: cancel, done: make(chan struct{})}
	e.recomputeNotify.mu.Lock()
	if e.recomputeNotify.run != nil {
		e.recomputeNotify.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("%w: recompute notify already enabled or starting", ErrInvalid)
	}
	e.recomputeNotify.run = run
	e.recomputeNotify.mu.Unlock()

	conn, err := e.openRecomputeNotifyConn(runCtx, dsn)
	if err != nil {
		e.finishRecomputeNotify(run, nil, dsn)
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		e.finishRecomputeNotify(run, conn, dsn)
		return nil, redact.RedactDSNError(err, dsn)
	}
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	cfg.ConnConfig.ConnectTimeout = 3 * time.Second
	publisher, err := pgxpool.NewWithConfig(runCtx, cfg)
	if err != nil {
		e.finishRecomputeNotify(run, conn, dsn)
		return nil, redact.RedactDSNError(err, dsn)
	}
	e.recomputeNotify.mu.Lock()
	run.publisher = publisher
	e.recomputeNotify.mu.Unlock()
	if err = runCtx.Err(); err != nil {
		e.finishRecomputeNotify(run, conn, dsn)
		return nil, err
	}

	go func() {
		defer func() { e.finishRecomputeNotify(run, conn, dsn) }()
		backoff := 250 * time.Millisecond
		if err := e.refreshRecomputeAll(runCtx); err != nil {
			e.Log.Warn("recompute.notifyReconcileFailed", "error", err)
		}
		for runCtx.Err() == nil {
			waitCtx, stopWait := context.WithTimeout(runCtx, 30*time.Second)
			note, err := conn.WaitForNotification(waitCtx)
			stopWait()
			if runCtx.Err() != nil {
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				if er := e.refreshRecomputeAll(runCtx); er != nil {
					e.Log.Warn("recompute.notifyPeriodicSyncFailed", "error", er)
				}
				continue
			}
			if err == nil {
				if note == nil {
					continue
				}
				var msg recomputeNotifyPayload
				if json.Unmarshal([]byte(note.Payload), &msg) != nil || (msg.Pool != Thread && msg.Pool != Process) || msg.Version < 1 {
					continue
				}
				if er := e.refreshRecomputePool(runCtx, msg.Pool); er != nil {
					e.Log.Warn("recompute.notifyRefreshFailed", "pool", msg.Pool, "error", er)
				}
				continue
			}
			e.Log.Warn("recompute.notifyListenLost", "error", redact.RedactDSNError(err, dsn))
			e.closeRecomputeNotifyConn(conn, dsn)
			conn = nil
			for runCtx.Err() == nil {
				delay := time.NewTimer(backoff)
				select {
				case <-runCtx.Done():
					delay.Stop()
					return
				case <-delay.C:
				}
				backoff = nextRecomputeNotifyBackoff(backoff)
				fresh, er := e.openRecomputeNotifyConn(runCtx, dsn)
				if er != nil {
					if runCtx.Err() == nil {
						e.Log.Warn("recompute.notifyReconnectFailed", "error", er)
					}
					continue
				}
				conn = fresh
				backoff = 250 * time.Millisecond
				if er = e.refreshRecomputeAll(runCtx); er != nil {
					e.Log.Warn("recompute.notifyReconnectSyncFailed", "error", er)
				}
				break
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { run.cancel(); <-run.done }) }, nil
}

// An incoming notification is only an invalidation hint. Read durable
// settings with a lifecycle-derived timeout and merge monotonically.
func (e *Engine) refreshRecomputePool(ctx context.Context, poolID string) error {
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var saved RecomputeSettings
	err := e.Store.View(readCtx, func(tx store.Reader) error { return tx.Get(store.Metrics, recomputeSettingsKey(poolID), &saved) })
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	saved = saved.normalized()
	if err = saved.validate(); err != nil {
		return err
	}
	e.recomputeMu.Lock()
	if current, ok := e.recomputeSettings[poolID]; !ok || current.Version < saved.Version {
		if e.recomputeSettings == nil {
			e.recomputeSettings = make(map[string]RecomputeSettings)
		}
		e.recomputeSettings[poolID] = saved
	}
	e.recomputeMu.Unlock()
	return nil
}
func (e *Engine) refreshRecomputeAll(ctx context.Context) error {
	var first error
	for _, p := range []string{Thread, Process} {
		if err := e.refreshRecomputePool(ctx, p); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Best-effort postcommit notification, outside both settings locks. Reuse a
// single bounded publisher connection; cancellation of the listener also
// cancels all in-flight or queued publisher acquisitions.
func (e *Engine) publishRecomputeVersion(ctx context.Context, poolID string, version int) error {
	e.recomputeNotify.mu.RLock()
	run := e.recomputeNotify.run
	var publisher *pgxpool.Pool
	if run != nil {
		publisher = run.publisher
	}
	e.recomputeNotify.mu.RUnlock()
	if publisher == nil {
		return nil
	}
	connCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	stop := context.AfterFunc(run.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := run.ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(recomputeNotifyPayload{Pool: poolID, Version: version})
	if err != nil {
		return err
	}
	_, err = publisher.Exec(connCtx, "SELECT pg_notify($1,$2)", recomputeNotifyChannel, string(payload))
	return err
}
