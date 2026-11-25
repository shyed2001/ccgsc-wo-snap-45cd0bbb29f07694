// Path: internal/config/anonymous_T96_test.go | Author: agy Gemini 3.8 Flash high for Claude Opus 5.5 | 2026-11-20
// What/why: unit tests for anonymous contributor configuration limits; WO_T96.
package config

import (
	"testing"
)

func TestAnonymousContributorConfigDefaults(t *testing.T) {
	d := DefaultAnonymousContributorConfig()
	if !d.Enabled {
		t.Fatal("expected Enabled to be true by default")
	}
	if d.IssueRatePerMinute != DefaultAnonymousIssueRatePerMinute {
		t.Fatalf("expected IssueRatePerMinute=%d, got %d", DefaultAnonymousIssueRatePerMinute, d.IssueRatePerMinute)
	}
	if d.MaxLiveTabsPerIP != DefaultAnonymousMaxLiveTabsPerIP {
		t.Fatalf("expected MaxLiveTabsPerIP=%d, got %d", DefaultAnonymousMaxLiveTabsPerIP, d.MaxLiveTabsPerIP)
	}
	if d.MaxTotalTabs != DefaultAnonymousMaxTotalTabs {
		t.Fatalf("expected MaxTotalTabs=%d, got %d", DefaultAnonymousMaxTotalTabs, d.MaxTotalTabs)
	}
}

func TestAnonymousContributorConfigNormalized(t *testing.T) {
	c := AnonymousContributorConfig{
		Enabled: false,
	}
	n := c.Normalized()
	if n.Enabled != false {
		t.Fatal("Normalized should preserve Enabled false")
	}
	if n.IssueRatePerMinute != DefaultAnonymousIssueRatePerMinute {
		t.Fatalf("expected normalized IssueRatePerMinute=%d, got %d", DefaultAnonymousIssueRatePerMinute, n.IssueRatePerMinute)
	}
	if n.MaxLiveTabsPerIP != DefaultAnonymousMaxLiveTabsPerIP {
		t.Fatalf("expected normalized MaxLiveTabsPerIP=%d, got %d", DefaultAnonymousMaxLiveTabsPerIP, n.MaxLiveTabsPerIP)
	}
	if n.MaxTotalTabs != DefaultAnonymousMaxTotalTabs {
		t.Fatalf("expected normalized MaxTotalTabs=%d, got %d", DefaultAnonymousMaxTotalTabs, n.MaxTotalTabs)
	}

	custom := AnonymousContributorConfig{
		Enabled:            true,
		IssueRatePerMinute: 20,
		MaxLiveTabsPerIP:   16,
		MaxTotalTabs:       1000,
	}
	cn := custom.Normalized()
	if cn.IssueRatePerMinute != 20 || cn.MaxLiveTabsPerIP != 16 || cn.MaxTotalTabs != 1000 {
		t.Fatalf("Normalized altered custom values: %+v", cn)
	}
}

func TestAnonymousContributorConfigEnvOverrides(t *testing.T) {
	t.Setenv("POOLS_ANONYMOUS_CONTRIBUTORS", "false")
	t.Setenv("POOLS_ANONYMOUS_ISSUE_RATE_PER_MINUTE", "25")
	t.Setenv("POOLS_ANONYMOUS_MAX_LIVE_TABS_PER_IP", "12")
	t.Setenv("POOLS_ANONYMOUS_MAX_TOTAL_TABS", "250")

	loaded, err := LoadAnonymousContributorConfig(DefaultAnonymousContributorConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.Enabled {
		t.Fatal("expected Enabled to be false from env")
	}
	if loaded.IssueRatePerMinute != 25 {
		t.Fatalf("expected IssueRatePerMinute=25, got %d", loaded.IssueRatePerMinute)
	}
	if loaded.MaxLiveTabsPerIP != 12 {
		t.Fatalf("expected MaxLiveTabsPerIP=12, got %d", loaded.MaxLiveTabsPerIP)
	}
	if loaded.MaxTotalTabs != 250 {
		t.Fatalf("expected MaxTotalTabs=250, got %d", loaded.MaxTotalTabs)
	}
}
