// File: pools-core/tests/contributor_controls_W10D_test.go (go)
// W10D row 1/4 (D-W10-11, CR-51): the contributorControls pool setting. The
// suite pins the default (off), the saved round-trip, the unknown-value refusal,
// the expectedVersion conflict rule of the same settings endpoint, and the
// read-only exposure on the register and heartbeat replies a contributor tab
// reads at boot and on every heartbeat.
package tests

import (
	"testing"

	"ccgsc/poolscore/internal/pool"
)

func TestW10DContributorControlsSetting(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	_, dev := f.key(t, "developer")
	path := "/thread-pool/v1/admin/settings"

	// Default: off. The page of every earlier round stays exactly as it was.
	view := decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["contributorControls"] != string(pool.ContributorControlsOff) {
		t.Fatalf("default contributorControls = %v, want off", view["contributorControls"])
	}
	modes, _ := view["contributorControlModes"].([]any)
	want := []string{"off", "pauseResume", "startStopOptIn"}
	if len(modes) != len(want) {
		t.Fatalf("contributorControlModes = %v, want %v", modes, want)
	}
	for i, mode := range want {
		if modes[i] != mode {
			t.Fatalf("contributorControlModes[%d] = %v, want %s", i, modes[i], mode)
		}
	}

	// Round-trip: a partial write touches only the contributor controls.
	updated := decode[map[string]any](t, f.request(t, "PUT", path, admin, map[string]any{
		"contributorControls": "pauseResume",
		"expectedVersion":     0,
	}, 200, nil))
	if updated["version"] != float64(1) || updated["contributorControls"] != "pauseResume" {
		t.Fatalf("saved = %v", updated)
	}
	if updated["verifyMode"] != string(pool.VerifyModeSample) || updated["isolationMode"] != string(pool.IsolationSeparateLock) {
		t.Fatalf("partial write changed another field: %v", updated)
	}
	view = decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["version"] != float64(1) || view["contributorControls"] != "pauseResume" {
		t.Fatalf("round-trip = %v", view)
	}

	// Unknown and explicitly empty values are refused, and nothing is written.
	f.request(t, "PUT", path, admin, map[string]any{"contributorControls": "alwaysOn"}, 400, nil)
	f.request(t, "PUT", path, admin, map[string]any{"contributorControls": ""}, 400, nil)
	// The expectedVersion rule is the same one verifyMode/isolationMode use.
	f.request(t, "PUT", path, admin, map[string]any{"contributorControls": "off", "expectedVersion": 0}, 409, nil)
	view = decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["version"] != float64(1) || view["contributorControls"] != "pauseResume" {
		t.Fatalf("a refused write changed the settings: %v", view)
	}

	// A developer may not write the setting, and the other pool keeps its default.
	f.request(t, "PUT", path, dev, map[string]any{"contributorControls": "off"}, 403, nil)
	other := decode[map[string]any](t, f.request(t, "GET", "/admin/v1/pools/process-pool/settings", admin, nil, 200, nil))
	if other["contributorControls"] != "off" {
		t.Fatalf("process pool defaults = %v", other)
	}

	// The path form writes the other pool independently.
	f.request(t, "PUT", "/admin/v1/pools/process-pool/settings", admin, map[string]any{"contributorControls": "startStopOptIn"}, 200, nil)
	if got := f.engine.PoolSettings(pool.Process).ContributorControls; got != pool.ContributorControlsStartStopOptIn {
		t.Fatalf("process pool setting = %q, want startStopOptIn", got)
	}
	if got := f.engine.PoolSettings(pool.Thread).ContributorControls; got != pool.ContributorControlsPauseResume {
		t.Fatalf("thread pool setting = %q, want pauseResume", got)
	}
}

func TestW10DContributorControlsReadByTabs(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	_, contrib := f.key(t, "contributor")

	type registerReply struct {
		pool.Worker
		ContributorControls string `json:"contributorControls"`
	}
	type heartbeatReply struct {
		pool.Worker
		ContributorControls string `json:"contributorControls"`
	}

	// Default off is what a tab reads before the Director changes anything.
	first := decode[registerReply](t, f.request(t, "POST", "/thread-pool/v1/workers/register", contrib,
		map[string]any{"hostId": "w10d-default", "slots": 1}, 201, nil))
	if first.TabID == "" || first.ContributorControls != string(pool.ContributorControlsOff) {
		t.Fatalf("register default = %+v, want tabId plus contributorControls off", first)
	}

	f.request(t, "PUT", "/thread-pool/v1/admin/settings", admin, map[string]any{
		"contributorControls": "startStopOptIn",
	}, 200, nil)
	second := decode[registerReply](t, f.request(t, "POST", "/thread-pool/v1/workers/register", contrib,
		map[string]any{"hostId": "w10d-saved", "slots": 1}, 201, nil))
	if second.ContributorControls != "startStopOptIn" {
		t.Fatalf("register after save = %q, want startStopOptIn", second.ContributorControls)
	}

	// The heartbeat carries the live value, so a change applies on the next one.
	hb := decode[heartbeatReply](t, f.request(t, "POST", "/thread-pool/v1/workers/"+second.TabID+"/heartbeat",
		contrib, map[string]any{}, 200, nil))
	if hb.TabID != second.TabID || hb.ContributorControls != "startStopOptIn" {
		t.Fatalf("heartbeat = %+v", hb)
	}
	f.request(t, "PUT", "/thread-pool/v1/admin/settings", admin, map[string]any{"contributorControls": "pauseResume"}, 200, nil)
	hb = decode[heartbeatReply](t, f.request(t, "POST", "/thread-pool/v1/workers/"+second.TabID+"/heartbeat",
		contrib, map[string]any{}, 200, nil))
	if hb.ContributorControls != "pauseResume" {
		t.Fatalf("heartbeat after change = %q, want pauseResume", hb.ContributorControls)
	}

	// The setting is per pool: the process pool reply stays off.
	process := decode[registerReply](t, f.request(t, "POST", "/process-pool/v1/workers/register", contrib,
		map[string]any{"hostId": "w10d-process", "slots": 1}, 201, nil))
	if process.ContributorControls != "off" {
		t.Fatalf("process pool reply = %q, want off", process.ContributorControls)
	}
}
