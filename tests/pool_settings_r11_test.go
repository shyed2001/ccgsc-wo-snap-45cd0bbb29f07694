// File: pools-core/tests/pool_settings_r11_test.go (go)
// R11-D1/D2: a settings PUT merges its delta on the record under settingsMu, so
// two concurrent partial writes never lose the other field; a stale
// expectedVersion answers 409 even when the body is invalid.
package tests

import (
	"sync"
	"testing"

	"ccgsc/poolscore/internal/pool"
)

// TestW10CConcurrentPartialWriteLostUpdate drives the merge semantics of
// writePoolSettings: writers touch disjoint fields with expectedVersion null, so
// none may be refused, and the final record must carry every value. The third
// writer covers the W10D-CTL contributorControls field.
func TestW10CConcurrentPartialWriteLostUpdate(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	path := "/thread-pool/v1/admin/settings"

	for i := 0; i < 50; i++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(3)
		for _, body := range []map[string]any{
			{"sampleRate": 0.5},
			{"redundantRate": 0.3},
			{"contributorControls": "pauseResume"},
		} {
			body := body
			go func() {
				defer wg.Done()
				<-start
				f.request(t, "PUT", path, admin, body, 200, nil)
			}()
		}
		close(start)
		wg.Wait()

		view := decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
		if view["sampleRate"] != 0.5 || view["redundantRate"] != 0.3 || view["contributorControls"] != "pauseResume" {
			t.Fatalf("iteration %d: settings = %v, want every partial write preserved", i, view)
		}
	}
}

// TestW10CStaleVersionBeatsInvalidInput pins the order of the two refusals: a
// stale expectedVersion answers 409 before the body is validated, and a fresh
// version still answers 400 for invalid input without writing anything.
func TestW10CStaleVersionBeatsInvalidInput(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	path := "/thread-pool/v1/admin/settings"

	f.request(t, "PUT", path, admin, map[string]any{"verifyMode": "both", "expectedVersion": 0}, 200, nil)

	f.request(t, "PUT", path, admin, map[string]any{"verifyMode": "sometimes", "expectedVersion": 0}, 409, nil)
	f.request(t, "PUT", path, admin, map[string]any{"redundantRate": 1.5, "expectedVersion": 0}, 409, nil)
	f.request(t, "PUT", path, admin, map[string]any{"isolationMode": "none", "expectedVersion": 0}, 409, nil)

	f.request(t, "PUT", path, admin, map[string]any{"verifyMode": "sometimes", "expectedVersion": 1}, 400, nil)
	f.request(t, "PUT", path, admin, map[string]any{"redundantRate": 1.5, "expectedVersion": 1}, 400, nil)

	view := decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["version"] != float64(1) || view["verifyMode"] != "both" || view["redundantRate"] != pool.DefaultRedundantRate ||
		view["sampleRate"] != float64(0) {
		t.Fatalf("settings = %v, want the refused writes to change nothing", view)
	}
}

// TestW10CUpdatePoolSettingsKeepsContributorControls pins the W10D-CTL carry
// over: a partial write of an unrelated field never resets the contributor
// controls, and a delta that names them still writes them.
func TestW10CUpdatePoolSettingsKeepsContributorControls(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	path := "/thread-pool/v1/admin/settings"

	f.request(t, "PUT", path, admin, map[string]any{"contributorControls": "pauseResume"}, 200, nil)
	f.request(t, "PUT", path, admin, map[string]any{"sampleRate": 0.5}, 200, nil)

	view := decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["contributorControls"] != "pauseResume" || view["sampleRate"] != 0.5 || view["version"] != float64(2) {
		t.Fatalf("settings = %v, want the contributor controls carried across a partial write", view)
	}

	f.request(t, "PUT", path, admin, map[string]any{"contributorControls": "startStopOptIn"}, 200, nil)
	view = decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["contributorControls"] != "startStopOptIn" || view["sampleRate"] != 0.5 {
		t.Fatalf("settings = %v, want the named contributor controls written", view)
	}
}
