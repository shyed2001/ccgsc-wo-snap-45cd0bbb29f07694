// Path: internal/config/config.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: explicit JSON/env/flag configuration for WO-CORE-00, WWPP-R41.1, WO_FIX_C_ADMIN_AUTH.
// T93 (2026-11-20, hermes MiniMax-M3): leaseRedisDB is bound to the Redis logical
// database range and leaseRedisPrefix gets one explicit namespace rule set.
package config

import (
	"ccgsc/poolscore/internal/caps"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// T93 (R8 findings 2 and 3): the shared VPS Redis exposes the stock logical
// databases 0..15 ("databases 16") and the lease keys always carry a
// namespace. An empty prefix means the documented default, never an
// unprefixed mode.
const (
	maxLeaseRedisDB         = 15
	defaultLeaseRedisPrefix = "pools:"
	maxLeaseRedisPrefixLen  = 32
)

type AdminUserConfig struct {
	Username     string   `json:"username"`
	PasswordHash string   `json:"passwordHash"`
	Roles        []string `json:"roles,omitempty"`
}

type Config struct {
	Addr         string `json:"addr"`
	Debug        bool   `json:"debug"`
	Database     string `json:"database"`
	Driver       string `json:"driver"`
	LogDir       string `json:"logDir"`
	JWTSecret    string `json:"jwtSecret"`
	BootstrapKey string `json:"bootstrapKey"`
	ServerID     string `json:"serverId"`
	LeaseSeconds int    `json:"leaseSeconds"`
	LeaseStore   string `json:"leaseStore"`

	// R15 AT22-GM2-3 (TD-17): the resource caps of the server. The zero value of
	// every key means "auto": maxProcs becomes NumCPU-1, memLimitMB becomes 75 % of
	// the memory the process may plan for (the smaller of the cgroup budget and the
	// physical RAM), workerPool becomes NumCPU-1 and dbMaxOpenConns follows the
	// machine.
	//
	// R15-FIX-CAPS (c): memLimitMB is a SOFT GC target (GOMEMLIMIT), NOT a hard RSS
	// cap. The garbage collector works harder as the heap approaches the limit, but
	// the process may still grow past it; only the OS (a cgroup limit, a job object)
	// is a hard ceiling. An explicit value is never lowered silently by the server.
	MaxProcs       int `json:"maxProcs"`
	MemLimitMB     int `json:"memLimitMB"`
	WorkerPool     int `json:"workerPool"`
	DBMaxOpenConns int `json:"dbMaxOpenConns"`

	// T92 | Codex | 2026-11-20: optional read-only localhost Node pool bridge.
	LegacyPoolURL string `json:"legacyPoolURL"`

	// R42.7 shared VPS Redis safe use: logical DB index and wire key prefix
	// for the lease client. Credentials never live here; they come from the
	// POOLS_REDIS_USER / POOLS_REDIS_PASSWORD environment only.
	LeaseRedisDB             int               `json:"leaseRedisDB"`
	LeaseRedisPrefix         string            `json:"leaseRedisPrefix"`
	IPAllowlistEnabled       bool              `json:"ipAllowlistEnabled"`
	IPAllowlist              []string          `json:"ipAllowlist"`
	TrustedProxyCIDRs        []string          `json:"trustedProxyCidrs,omitempty"`
	AdminUsers               []AdminUserConfig `json:"adminUsers,omitempty"`
	LoginRateLimit           int               `json:"loginRateLimit,omitempty"`
	WebhooksEnabled          bool              `json:"webhooksEnabled"`
	WebhookAllowPrivateIPs   bool              `json:"webhookAllowPrivateIPs,omitempty"`
	RunOrigin                string            `json:"runOrigin,omitempty"`
	PublicOrigin             string            `json:"publicOrigin,omitempty"`
	MetricsLoopbackAnonymous bool              `json:"metricsLoopbackAnonymous,omitempty"`
	GPUEnabled               bool              `json:"gpuEnabled"`
	MaxGPUSlotsPerTab        int               `json:"maxGpuSlotsPerTab"`
	GPUOnBatteryAllowed      bool              `json:"gpuOnBatteryAllowed"`

	// T96 | Hermes | 2026-11-20: the abuse limits of the anonymous contributor join.
	AnonymousContributors AnonymousContributorConfig `json:"anonymousContributors,omitempty"`
}

// Caps returns the resource cap knobs of the configuration. internal/caps resolves
// them into the effective values of the running server.
func (c Config) Caps() caps.Config {
	return caps.Config{
		MaxProcs:       c.MaxProcs,
		MemLimitMB:     c.MemLimitMB,
		WorkerPool:     c.WorkerPool,
		DBMaxOpenConns: c.DBMaxOpenConns,
	}
}

func Default() Config {
	return Config{
		Addr:                  "127.0.0.1:8091",
		Debug:                 true,
		Database:              "data/pools.db",
		Driver:                "sqlite",
		LogDir:                "logs",
		JWTSecret:             "debug-only-change-before-deployment-32bytes",
		ServerID:              "s1",
		LeaseSeconds:          60,
		LeaseStore:            "pg",
		LeaseRedisPrefix:      "pools:",
		IPAllowlistEnabled:    false,
		IPAllowlist:           []string{"127.0.0.1/32", "100.64.0.0/10"},
		LoginRateLimit:        5,
		WebhooksEnabled:       true,
		GPUEnabled:            true,
		MaxGPUSlotsPerTab:     2,
		AnonymousContributors: DefaultAnonymousContributorConfig(),
	}
}

// normalizeLeaseRedisPrefix applies the one T93 prefix policy: a blank prefix
// becomes the documented default "pools:" (Load only - Validate rejects a
// blank prefix, see below), every prefix must start with [a-z0-9], end with
// ":" and consist of 1..32 characters from [a-z0-9_:-]. The namespace
// separator keeps the lease keys of one tenant apart on the shared VPS Redis
// and keeps them greppable; anything else is a configuration error instead of
// a silent fallback.
func normalizeLeaseRedisPrefix(prefix string) (string, error) {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return defaultLeaseRedisPrefix, nil
	}
	if first := p[0]; !(first >= 'a' && first <= 'z' || first >= '0' && first <= '9') {
		// T93 (agy review): a bare ":" or "_pool:" carries no tenant name and
		// is a configuration error, not a valid namespace.
		return "", fmt.Errorf("leaseRedisPrefix must start with [a-z0-9], got %q", p)
	}
	if len(p) > maxLeaseRedisPrefixLen {
		return "", fmt.Errorf("leaseRedisPrefix must be at most %d characters, got %d", maxLeaseRedisPrefixLen, len(p))
	}
	if !strings.HasSuffix(p, ":") {
		return "", fmt.Errorf("leaseRedisPrefix must end with %q, got %q", ":", p)
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == ':', r == '-':
		default:
			return "", fmt.Errorf("leaseRedisPrefix must use only [a-z0-9_:-], got %q", p)
		}
	}
	return p, nil
}

// Load applies environment overrides after the optional JSON file. Flags are applied by main.
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return c, e
		}
		if e = json.Unmarshal(b, &c); e != nil {
			return c, e
		}
	}
	for name, dst := range map[string]*string{"ADDR": &c.Addr, "DATABASE": &c.Database, "DRIVER": &c.Driver, "LOG_DIR": &c.LogDir, "JWT_SECRET": &c.JWTSecret, "BOOTSTRAP_KEY": &c.BootstrapKey, "SERVER_ID": &c.ServerID} {
		if v, ok := os.LookupEnv("POOLS_" + name); ok {
			*dst = v
		}
	}
	if v, ok := os.LookupEnv("POOLS_RUN_ORIGIN"); ok {
		c.RunOrigin = v
	}
	if v, ok := os.LookupEnv("POOLS_LEGACY_POOL_URL"); ok {
		c.LegacyPoolURL = v
	}
	if v, ok := os.LookupEnv("POOLS_PUBLIC_ORIGIN"); ok {
		c.PublicOrigin = v
	}
	if v, ok := os.LookupEnv("POOLS_METRICS_LOOPBACK_ANONYMOUS"); ok {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, e
		}
		c.MetricsLoopbackAnonymous = b
	}
	if v, ok := os.LookupEnv("POOLS_DEBUG"); ok {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, e
		}
		c.Debug = b
	}
	if v, ok := os.LookupEnv("POOLS_LEASE_SECONDS"); ok {
		n, e := strconv.Atoi(v)
		if e != nil {
			return c, e
		}
		c.LeaseSeconds = n
	}
	if v, ok := os.LookupEnv("POOLS_LEASE_STORE"); ok {
		c.LeaseStore = strings.TrimSpace(v)
	}
	// R42.7 2026-11-19: live VPS runs without a JSON config file, so the Redis
	// logical DB index and key prefix are settable from pools.env too.
	if v, ok := os.LookupEnv("POOLS_LEASE_REDIS_DB"); ok {
		n, e := strconv.Atoi(strings.TrimSpace(v))
		if e != nil {
			return c, e
		}
		c.LeaseRedisDB = n
	}
	if v, ok := os.LookupEnv("POOLS_LEASE_REDIS_PREFIX"); ok {
		c.LeaseRedisPrefix = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("POOLS_IP_ALLOWLIST_ENABLED"); ok {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, e
		}
		c.IPAllowlistEnabled = b
	}
	if v, ok := os.LookupEnv("POOLS_IP_ALLOWLIST"); ok {
		list := []string{}
		for _, part := range strings.Split(v, ",") {
			if s := strings.TrimSpace(part); s != "" {
				list = append(list, s)
			}
		}
		c.IPAllowlist = list
	}
	if v, ok := os.LookupEnv("POOLS_TRUSTED_PROXY_CIDRS"); ok {
		list := []string{}
		for _, part := range strings.Split(v, ",") {
			if s := strings.TrimSpace(part); s != "" {
				list = append(list, s)
			}
		}
		c.TrustedProxyCIDRs = list
	}
	if v, ok := os.LookupEnv("POOLS_LOGIN_RATE_LIMIT"); ok {
		n, e := strconv.Atoi(v)
		if e != nil {
			return c, e
		}
		c.LoginRateLimit = n
	}
	if v, ok := os.LookupEnv("POOLS_WEBHOOKS_ENABLED"); ok {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, e
		}
		c.WebhooksEnabled = b
	}
	if v, ok := os.LookupEnv("POOLS_WEBHOOK_ALLOW_PRIVATE_IPS"); ok {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, e
		}
		c.WebhookAllowPrivateIPs = b
	}
	if v, ok := os.LookupEnv("POOLS_GPU_ENABLED"); ok {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, e
		}
		c.GPUEnabled = b
	}
	if v, ok := os.LookupEnv("POOLS_MAX_GPU_SLOTS_PER_TAB"); ok {
		n, e := strconv.Atoi(v)
		if e != nil {
			return c, e
		}
		c.MaxGPUSlotsPerTab = n
	}
	if v, ok := os.LookupEnv("POOLS_GPU_ON_BATTERY_ALLOWED"); ok {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, e
		}
		c.GPUOnBatteryAllowed = b
	}
	// R15 AT22-GM2-3 (TD-17): the resource caps come from the environment too, the
	// same way every other key of this file works. A broken value is an error, never
	// a zero.
	for name, dst := range map[string]*int{
		"MAX_PROCS":         &c.MaxProcs,
		"MEM_LIMIT_MB":      &c.MemLimitMB,
		"WORKER_POOL":       &c.WorkerPool,
		"DB_MAX_OPEN_CONNS": &c.DBMaxOpenConns,
	} {
		if v, ok := os.LookupEnv("POOLS_" + name); ok {
			n, e := strconv.Atoi(strings.TrimSpace(v))
			if e != nil {
				return c, fmt.Errorf("POOLS_%s must be a whole number, got %q", name, v)
			}
			*dst = n
		}
	}
	// T93: one prefix policy for the JSON file and the environment alike. A
	// blank prefix is the documented default; anything else must be a valid
	// namespace or the load fails.
	prefix, e := normalizeLeaseRedisPrefix(c.LeaseRedisPrefix)
	if e != nil {
		return c, e
	}
	c.LeaseRedisPrefix = prefix
	// T96 2026-11-20: the limits of the anonymous contributor join.
	anonymous, e := LoadAnonymousContributorConfig(c.AnonymousContributors)
	if e != nil {
		return c, e
	}
	c.AnonymousContributors = anonymous
	return c, c.Validate()
}

func (c Config) Validate() error {
	if err := ValidateLegacyPoolURL(c.LegacyPoolURL); err != nil {
		return err
	}
	if c.Addr == "" || c.Database == "" || c.ServerID == "" || c.LogDir == "" || c.LeaseSeconds < 1 || c.LeaseSeconds > 86400 {
		return fmt.Errorf("invalid empty configuration or leaseSeconds")
	}
	if c.LeaseStore != "pg" && c.LeaseStore != "redis" {
		return fmt.Errorf("leaseStore must be pg or redis, got %q", c.LeaseStore)
	}
	if c.LeaseRedisDB < 0 || c.LeaseRedisDB > maxLeaseRedisDB {
		return fmt.Errorf("leaseRedisDB must be between 0 and %d, got %d", maxLeaseRedisDB, c.LeaseRedisDB)
	}
	// T93 (agy review): Validate takes the Config by value, so it cannot
	// normalize a blank prefix into the default the way Load does. A Config
	// built directly (or mutated by flags after Load) with a blank prefix
	// would pass here and later send unprefixed lease keys to the shared
	// Redis. Only Load may default a blank prefix; Validate rejects it.
	if strings.TrimSpace(c.LeaseRedisPrefix) == "" {
		return fmt.Errorf("leaseRedisPrefix must not be empty (only Load defaults it to %q)", defaultLeaseRedisPrefix)
	}
	if _, err := normalizeLeaseRedisPrefix(c.LeaseRedisPrefix); err != nil {
		return err
	}
	if len(c.JWTSecret) < 32 {
		return fmt.Errorf("jwtSecret must contain at least 32 bytes")
	}
	if !c.Debug && c.JWTSecret == Default().JWTSecret {
		return fmt.Errorf("non-debug mode requires an explicit jwtSecret")
	}
	if c.Driver != "sqlite" && c.Driver != "pg" {
		return fmt.Errorf("unsupported driver %q", c.Driver)
	}
	if c.RunOrigin != "" {
		u, err := url.Parse(c.RunOrigin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("invalid runOrigin %q", c.RunOrigin)
		}
		if c.PublicOrigin == "" {
			return fmt.Errorf("publicOrigin is required when runOrigin is set")
		}
		u, err = url.Parse(c.PublicOrigin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("invalid publicOrigin %q", c.PublicOrigin)
		}
	}
	if c.LoginRateLimit < 0 {
		return fmt.Errorf("invalid negative loginRateLimit")
	}
	// R15 AT22-GM2-3 (TD-17): the caps that would take every core are clamped where
	// the runtime is applied (internal/caps), the caps that make no sense at all are
	// a configuration error here.
	if c.MemLimitMB < 0 {
		return fmt.Errorf("memLimitMB must not be negative, got %d", c.MemLimitMB)
	}
	if c.WorkerPool < 0 {
		return fmt.Errorf("workerPool must not be negative, got %d", c.WorkerPool)
	}
	if c.DBMaxOpenConns < 0 {
		return fmt.Errorf("dbMaxOpenConns must not be negative, got %d", c.DBMaxOpenConns)
	}
	if c.MaxGPUSlotsPerTab < 1 || c.MaxGPUSlotsPerTab > 4 {
		return fmt.Errorf("maxGpuSlotsPerTab must be between 1 and 4")
	}
	for _, cidr := range c.IPAllowlist {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid IP allowlist CIDR %q: %w", cidr, err)
		}
	}
	for _, cidr := range c.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid trusted proxy CIDR %q: %w", cidr, err)
		}
	}
	// T96 2026-11-20: a negative limit is a configuration error, not a silent
	// "unlimited" value.
	if c.AnonymousContributors.IssueRatePerMinute < 0 || c.AnonymousContributors.MaxLiveTabsPerIP < 0 || c.AnonymousContributors.MaxTotalTabs < 0 {
		return fmt.Errorf("invalid negative anonymousContributors limit")
	}
	return nil
}
