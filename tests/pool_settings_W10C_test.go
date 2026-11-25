// File: pools-core/tests/pool_settings_W10C_test.go (go)
// W10C-SERVER row 3/4: HTTP round-trip of the Director pool settings, the
// expectedVersion conflict rule and the authorization of both route families.
package tests

import (
	"testing"

	"ccgsc/poolscore/internal/pool"
)

func TestW10CPoolSettingsRoutes(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	_, dev := f.key(t, "developer")
	path := "/thread-pool/v1/admin/settings"

	f.request(t, "GET", path, "", nil, 401, nil)
	f.request(t, "GET", path, dev, nil, 403, nil)
	view := decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["version"] != float64(0) || view["verifyMode"] != string(pool.VerifyModeSample) ||
		view["isolationMode"] != string(pool.IsolationSeparateLock) ||
		view["redundantRate"] != pool.DefaultRedundantRate || view["sampleRate"] != float64(0) {
		t.Fatalf("defaults = %v", view)
	}

	f.request(t, "PUT", path, dev, map[string]any{"verifyMode": "both"}, 403, nil)
	updated := decode[map[string]any](t, f.request(t, "PUT", path, admin, map[string]any{
		"verifyMode":      "both",
		"redundantRate":   0.25,
		"sampleRate":      0.5,
		"isolationMode":   "lock-only",
		"expectedVersion": 0,
	}, 200, nil))
	if updated["version"] != float64(1) || updated["verifyMode"] != "both" || updated["redundantRate"] != 0.25 ||
		updated["sampleRate"] != 0.5 || updated["isolationMode"] != "lock-only" {
		t.Fatalf("saved = %v", updated)
	}
	view = decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["version"] != float64(1) || view["verifyMode"] != "both" || view["isolationMode"] != "lock-only" {
		t.Fatalf("round-trip = %v", view)
	}

	// The conflict rule of the existing settings endpoints.
	f.request(t, "PUT", path, admin, map[string]any{"verifyMode": "off", "expectedVersion": 0}, 409, nil)
	f.request(t, "PUT", path, admin, map[string]any{"verifyMode": "sometimes"}, 400, nil)
	f.request(t, "PUT", path, admin, map[string]any{"redundantRate": 1.5}, 400, nil)
	f.request(t, "PUT", path, admin, map[string]any{"isolationMode": "none"}, 400, nil)

	// The pool named in the path form and the other pool keep their own settings.
	other := decode[map[string]any](t, f.request(t, "GET", "/admin/v1/pools/process-pool/settings", admin, nil, 200, nil))
	if other["poolId"] != "pp" || other["version"] != float64(0) || other["verifyMode"] != "sample" {
		t.Fatalf("process pool defaults = %v", other)
	}
	f.request(t, "GET", "/admin/v1/pools/unknown/settings", admin, nil, 404, nil)

	engineView := f.engine.PoolSettings(pool.Thread)
	if engineView.Version != 1 || engineView.VerifyMode != pool.VerifyModeBoth || engineView.IsolationMode != pool.IsolationLockOnly {
		t.Fatalf("engine settings = %+v, want the saved version", engineView)
	}
}
