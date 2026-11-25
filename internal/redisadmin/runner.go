// Path: internal/redisadmin/runner.go | Author: Codex | 2026-11-20
// T91: optional admin applier and separate read-only observation. No lease changes.
// T93 (2026-11-20, hermes MiniMax-M3): the mutex protects the in-memory state
// only. The network work runs without it, so a slow or dead Redis cannot block
// Status, Config or Interval.
package redisadmin

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Current struct {
	MaxMemoryBytes uint64 `json:"maxMemoryBytes"`
	Policy         string `json:"policy"`
	Persistence    string `json:"persistence"`
}

type Status struct {
	Enabled bool     `json:"enabled"`
	State   string   `json:"state"`
	Message string   `json:"message"`
	Desired Config   `json:"desired"`
	Current *Current `json:"current,omitempty"`
}

// All connection fields are private: serializing the runner cannot leak secrets.
type Runner struct {
	mu                     sync.Mutex
	applyMu                sync.Mutex
	addr, user, password   string
	readUser, readPassword string
	dial                   dialFunc
	freeRAM                func() (uint64, error)
	cfg                    Config
	state, message         string
	changed                chan struct{}
}

func NewFromEnv() *Runner {
	r := New(os.Getenv("POOLS_REDIS_ADDR"), os.Getenv("POOLS_REDIS_ADMIN_USER"), os.Getenv("POOLS_REDIS_ADMIN_PASSWORD"))
	r.readUser, r.readPassword = os.Getenv("POOLS_REDIS_USER"), os.Getenv("POOLS_REDIS_PASSWORD")
	return r
}

func New(addr, user, password string) *Runner {
	r := &Runner{addr: addr, user: user, password: password, dial: (&net.Dialer{}).DialContext,
		freeRAM: FreeRAMBytes, cfg: DefaultConfig(), changed: make(chan struct{}, 1)}
	r.state, r.message = "pending", "Redis settings have not been applied"
	if !r.Enabled() {
		r.state, r.message = "disabled", "applied by ops, not by pools-core"
	}
	return r
}

func (r *Runner) Enabled() bool            { return r != nil && r.user != "" && r.password != "" }
func (r *Runner) Changed() <-chan struct{} { return r.changed }
func (r *Runner) Config() Config           { r.mu.Lock(); defer r.mu.Unlock(); return r.cfg }

// Configure applies an audited save immediately and asks main to reset its ticker.
// A disabled applier never dials, even when a new configuration is saved.
//
// T93 (agy review): applyMu is taken before the new config is stored, so the
// r.cfg mutation and its network apply are serialized in the same order. A
// Status call can therefore never pair the new Desired with the previous
// apply's State, and two concurrent Configure calls cannot invert their
// applies: the second caller stores its config only after the first apply
// has finished.
func (r *Runner) Configure(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.mu.Lock()
	r.cfg = c
	if r.Enabled() {
		r.state, r.message = "pending", "Redis settings are being applied"
	}
	select {
	case r.changed <- struct{}{}:
	default:
	}
	r.mu.Unlock()
	return r.apply(ctx)
}

// Apply performs one scheduled apply. Like Configure, it serializes against
// the mutating applies through applyMu.
func (r *Runner) Apply(ctx context.Context) error {
	if !r.Enabled() {
		return nil
	}
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	return r.apply(ctx)
}

// apply performs the Redis work while the caller holds applyMu and without
// holding r.mu, so a slow or dead Redis never blocks Status, Config or
// Interval.
func (r *Runner) apply(ctx context.Context) error {
	if !r.Enabled() {
		return nil
	}

	// Copy everything the network path needs under the lock, then release it.
	r.mu.Lock()
	cfg := r.cfg
	addr, user, password := r.addr, r.user, r.password
	dial, freeRAM := r.dial, r.freeRAM
	r.mu.Unlock()

	var free uint64
	if cfg.Mode == "dynamic" {
		var err error
		free, err = freeRAM()
		if err != nil {
			// T93 (agy review): decide the state once and set it once. The
			// previous version wrote "unavailable" first and overwrote it
			// with "unsupported" afterwards, so a concurrent Status could
			// observe the transient wrong state.
			state, message := freeRAMFailure(err)
			r.setState(state, message)
			return err
		}
	}
	err := withSession(ctx, dial, addr, user, password, func(s *session) error {
		// Never turn AOF off if there are no RDB save rules to take its place.
		if cfg.Persistence == "snapshot" {
			v, err := s.command(Command{"CONFIG", "GET", "save"})
			items, ok := v.([]any)
			if err != nil || !ok || len(items) != 2 || items[0] != "save" {
				return errRedis
			}
			rules, ok := items[1].(string)
			if !ok || strings.TrimSpace(rules) == "" {
				return errors.New("snapshot requires existing Redis save rules; no changes applied")
			}
		}
		for _, cmd := range Plan(cfg, free) {
			if err := s.ok(cmd); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.setState("failed", err.Error())
		return err
	}
	r.setState("applied", "Redis settings applied; noeviction enforced")
	return nil
}

// setState records the outcome of an apply under the lock.
func (r *Runner) setState(state, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state, r.message = state, message
}

// freeRAMFailure maps a MemAvailable read error to exactly one runner state:
// "unsupported" when the OS has no MemAvailable at all, "unavailable" for a
// real read failure (T93, agy review).
func freeRAMFailure(err error) (string, string) {
	if errors.Is(err, ErrUnsupported) {
		return "unsupported", "dynamic memory mode unsupported on this OS; no commands sent"
	}
	return "unavailable", "MemAvailable unavailable; no commands sent"
}

// Status uses read-only INFO and CONFIG GET. When the applier is disabled, this separate
// observer uses the lease ACL identity; denied INFO means unknown, never defaults.
func (r *Runner) Status(ctx context.Context) Status {
	if r == nil {
		return Status{State: "disabled", Message: "applied by ops, not by pools-core", Desired: DefaultConfig()}
	}
	// T93 (agy review): snapshot every field this method needs under the lock.
	// The previous version read addr, the credentials and dial after Unlock,
	// which apply() could not pair with the state it had just stored.
	r.mu.Lock()
	out := Status{Enabled: r.Enabled(), State: r.state, Message: r.message, Desired: r.cfg}
	addr, dial := r.addr, r.dial
	user, password := r.readUser, r.readPassword
	if r.Enabled() {
		user, password = r.user, r.password
	}
	r.mu.Unlock()
	if addr == "" {
		return out
	}
	err := withSession(ctx, dial, addr, user, password, func(s *session) error {
		memory, err := s.command(Command{"INFO", "memory"})
		if err != nil {
			return err
		}
		persistence, err := s.command(Command{"INFO", "persistence"})
		if err != nil {
			return err
		}
		m, mok := memory.(string)
		p, pok := persistence.(string)
		if !mok || !pok {
			return errRedis
		}
		current, err := parseCurrent(m, p)
		if err != nil {
			return err
		}
		out.Current = current
		// INFO does not expose appendfsync or save rules. A read-only CONFIG GET
		// may be ACL-denied; retain explicitly unverified INFO values in that case.
		key := "appendfsync"
		if strings.HasPrefix(current.Persistence, "snapshot") {
			key = "save"
		}
		v, err := s.command(Command{"CONFIG", "GET", key})
		if err != nil {
			return nil
		}
		items, ok := v.([]any)
		if !ok || len(items) != 2 || items[0] != key {
			return nil
		}
		value, ok := items[1].(string)
		if !ok {
			return nil
		}
		if key == "save" {
			current.Persistence = "snapshot"
			if strings.TrimSpace(value) == "" {
				current.Persistence = "disabled (no AOF or save rules)"
			}
		} else if value == "everysec" {
			current.Persistence = "aof_everysec"
		} else if value == "always" || value == "no" {
			current.Persistence = "aof_" + value
		}
		return nil
	})
	if err != nil {
		out.Message += "; current Redis values unavailable"
	}
	return out
}

func parseCurrent(memory, persistence string) (*Current, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(memory+"\n"+persistence, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			fields[key] = value
		}
	}
	capBytes, err := strconv.ParseUint(fields["maxmemory"], 10, 64)
	validPolicy := false
	for _, policy := range []string{"noeviction", "allkeys-lru", "allkeys-lfu", "allkeys-random", "volatile-lru", "volatile-lfu", "volatile-random", "volatile-ttl"} {
		if fields["maxmemory_policy"] == policy {
			validPolicy = true
		}
	}
	if err != nil || !validPolicy {
		return nil, errRedis
	}
	mode := ""
	switch fields["aof_enabled"] {
	case "0":
		mode = "snapshot (save rules unverified)"
	case "1":
		mode = "aof (fsync unverified)"
	default:
		return nil, errRedis
	}
	return &Current{capBytes, fields["maxmemory_policy"], mode}, nil
}

// Interval is zero for fixed mode. Scheduling belongs exclusively to main.go.
func (r *Runner) Interval() time.Duration {
	c := r.Config()
	if !r.Enabled() || c.Mode != "dynamic" {
		return 0
	}
	return time.Duration(c.IntervalMin) * time.Minute
}
