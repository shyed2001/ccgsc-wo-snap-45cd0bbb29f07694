// Path: internal/redisadmin/config.go | Author: Codex | 2026-11-20
// T91 / R42.8: validated shared Redis settings and a deterministic, key-free plan.
package redisadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

const GiB uint64 = 1 << 30

var ErrInvalid = errors.New("invalid Redis admin configuration")

type Config struct {
	Mode           string `json:"redis.memory.mode"`
	FixedGB        int    `json:"redis.memory.fixed_gb"`
	DynamicPercent int    `json:"redis.memory.dynamic_percent"`
	IntervalMin    int    `json:"redis.memory.interval_min"`
	Persistence    string `json:"redis.persistence.mode"`
}

func DefaultConfig() Config { return Config{"fixed", 1, 10, 5, "aof_everysec"} }

func IsPage(page string) bool { return page == "thread-pool-admin" || page == "process-pool-admin" }

func (c Config) Validate() error {
	if (c.Mode != "fixed" && c.Mode != "dynamic") || c.FixedGB < 1 || c.FixedGB > 4 ||
		c.DynamicPercent < 10 || c.DynamicPercent > 40 || c.IntervalMin < 1 || c.IntervalMin > 60 ||
		(c.Persistence != "aof_everysec" && c.Persistence != "snapshot") {
		return ErrInvalid
	}
	return nil
}

// DecodeAdminConfig follows the GPU page pattern: absent settings use defaults.
// Unknown Redis keys are rejected, including any attempt to store credentials.
func DecodeAdminConfig(page string, data json.RawMessage) (Config, bool, error) {
	c := DefaultConfig()
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil || doc == nil {
		return c, false, ErrInvalid
	}
	found := false
	for key, raw := range doc {
		if len(key) < 6 || key[:6] != "redis." {
			continue
		}
		if !IsPage(page) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return c, false, ErrInvalid
		}
		var target any
		switch key {
		case "redis.memory.mode":
			target = &c.Mode
		case "redis.memory.fixed_gb":
			target = &c.FixedGB
		case "redis.memory.dynamic_percent":
			target = &c.DynamicPercent
		case "redis.memory.interval_min":
			target = &c.IntervalMin
		case "redis.persistence.mode":
			target = &c.Persistence
		default:
			return c, false, ErrInvalid
		}
		if json.Unmarshal(raw, target) != nil {
			return c, false, ErrInvalid
		}
		found = true
	}
	return c, found, c.Validate()
}

func ValidateAdminConfig(page string, data json.RawMessage) error {
	_, _, err := DecodeAdminConfig(page, data)
	return err
}

type Command []string

// Plan emits no commands for invalid input. GB means binary GiB, like Redis 1gb.
// Snapshot keeps the operator's existing save rules; it never rewrites them.
func Plan(c Config, freeRAMBytes uint64) []Command {
	if c.Validate() != nil {
		return nil
	}
	capBytes := uint64(c.FixedGB) * GiB
	if c.Mode == "dynamic" {
		// Divide before multiplying to avoid overflow even for arbitrary test input.
		p := uint64(c.DynamicPercent)
		capBytes = freeRAMBytes/100*p + freeRAMBytes%100*p/100
		if capBytes < GiB {
			capBytes = GiB
		}
		if capBytes > 4*GiB {
			capBytes = 4 * GiB
		}
	}
	aof := "yes"
	if c.Persistence == "snapshot" {
		aof = "no"
	}
	return []Command{
		{"CONFIG", "SET", "maxmemory-policy", "noeviction"},
		{"CONFIG", "SET", "maxmemory", strconv.FormatUint(capBytes, 10)},
		{"CONFIG", "SET", "appendfsync", "everysec"},
		{"CONFIG", "SET", "appendonly", aof},
	}
}
