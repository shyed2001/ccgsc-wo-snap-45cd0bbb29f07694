// Path: internal/config/anonymous_T96.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-20
// What/why: the abuse limits of the anonymous contributor join of WO_T96. A volunteer
// tab may join without a login, but the join is bounded: the feature can be switched
// off, the issue rate per client IP is limited, the live anonymous tabs per client IP
// are limited and the total number of anonymous tabs is limited. The values live in
// the JSON configuration and in the POOLS_* environment, like every other limit.
package config

import (
	"os"
	"strconv"
	"strings"
)

// AnonymousContributorConfig holds the limits of the anonymous join route.
//
// The defaults are the ones of WO_T96: the feature is on, ten issues per minute per
// client IP, eight live anonymous tabs per client IP and five hundred anonymous tabs
// in total. A zero value of a numeric limit means the documented default, so a
// partial JSON file never disables a limit by accident.
type AnonymousContributorConfig struct {
	// Enabled switches the anonymous join route on. Default on.
	Enabled bool `json:"enabled"`
	// IssueRatePerMinute is the number of tokens one client IP may request per
	// minute. Default 10.
	IssueRatePerMinute int `json:"issueRatePerMinute"`
	// MaxLiveTabsPerIP is the number of live anonymous tabs one client IP may hold.
	// Default 8.
	MaxLiveTabsPerIP int `json:"maxLiveTabsPerIP"`
	// MaxTotalTabs is the number of live anonymous tabs the server holds in total.
	// Default 500.
	MaxTotalTabs int `json:"maxTotalTabs"`
}

// The defaults of the anonymous join (WO_T96).
const (
	DefaultAnonymousIssueRatePerMinute = 10
	DefaultAnonymousMaxLiveTabsPerIP   = 8
	DefaultAnonymousMaxTotalTabs       = 500
)

// DefaultAnonymousContributorConfig returns the defaults of the anonymous join.
func DefaultAnonymousContributorConfig() AnonymousContributorConfig {
	return AnonymousContributorConfig{
		Enabled:            true,
		IssueRatePerMinute: DefaultAnonymousIssueRatePerMinute,
		MaxLiveTabsPerIP:   DefaultAnonymousMaxLiveTabsPerIP,
		MaxTotalTabs:       DefaultAnonymousMaxTotalTabs,
	}
}

// Normalized returns the configuration with every limit at its documented default.
// The switch is the one exception: it is off only when the operator asked for it.
func (c AnonymousContributorConfig) Normalized() AnonymousContributorConfig {
	d := DefaultAnonymousContributorConfig()
	if c.IssueRatePerMinute <= 0 {
		c.IssueRatePerMinute = d.IssueRatePerMinute
	}
	if c.MaxLiveTabsPerIP <= 0 {
		c.MaxLiveTabsPerIP = d.MaxLiveTabsPerIP
	}
	if c.MaxTotalTabs <= 0 {
		c.MaxTotalTabs = d.MaxTotalTabs
	}
	return c
}

// LoadAnonymousContributorConfig reads the anonymous join limits from the
// environment. The JSON file is applied first by config.Load; the environment wins,
// like for every other setting.
func LoadAnonymousContributorConfig(c AnonymousContributorConfig) (AnonymousContributorConfig, error) {
	if v, ok := os.LookupEnv("POOLS_ANONYMOUS_CONTRIBUTORS"); ok {
		b, e := strconv.ParseBool(strings.TrimSpace(v))
		if e != nil {
			return c, e
		}
		c.Enabled = b
	}
	if v, ok := os.LookupEnv("POOLS_ANONYMOUS_ISSUE_RATE_PER_MINUTE"); ok {
		n, e := strconv.Atoi(strings.TrimSpace(v))
		if e != nil {
			return c, e
		}
		c.IssueRatePerMinute = n
	}
	if v, ok := os.LookupEnv("POOLS_ANONYMOUS_MAX_LIVE_TABS_PER_IP"); ok {
		n, e := strconv.Atoi(strings.TrimSpace(v))
		if e != nil {
			return c, e
		}
		c.MaxLiveTabsPerIP = n
	}
	if v, ok := os.LookupEnv("POOLS_ANONYMOUS_MAX_TOTAL_TABS"); ok {
		n, e := strconv.Atoi(strings.TrimSpace(v))
		if e != nil {
			return c, e
		}
		c.MaxTotalTabs = n
	}
	return c.Normalized(), nil
}
