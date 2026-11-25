// Path: internal/api/runner_config_W10d_test.go | W10d D-W10-10.
// What/why: the runner-config route is the only place a deployment can state the
// wasm-abi memory bound. The value must be the platform default (4096 pages =
// 256 MiB), it must be present even when nothing else is configured, and it must
// stay equal to the runner's own constant - a drifting pair would silently
// change the cap on one side of the sandbox boundary only.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ccgsc/poolscore/internal/config"
)

func TestW10dRunnerConfigCarriesWasmMemoryPages(t *testing.T) {
	s := &Server{Config: config.Default()}
	req := httptest.NewRequest(http.MethodGet, "/pools/runner-config", nil)
	w := httptest.NewRecorder()
	// The route handler itself, not the assembled mux: the mux needs the whole
	// server (auth, engine), and this contract is the handler's alone.
	s.runnerConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("runner-config = HTTP %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		RunOrigin          *string `json:"runOrigin"`
		WasmMaxMemoryPages int     `json:"wasmMaxMemoryPages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %s", w.Body.String())
	}
	if body.WasmMaxMemoryPages != 4096 {
		t.Fatalf("wasmMaxMemoryPages = %d, want 4096 (256 MiB)", body.WasmMaxMemoryPages)
	}
	if body.WasmMaxMemoryPages != wasmMaxMemoryPagesDefault {
		t.Fatalf("handler and constant disagree")
	}
}
