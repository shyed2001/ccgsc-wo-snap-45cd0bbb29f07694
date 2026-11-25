package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// CR-53: the platform delivers workload source only as runtime artifact data.
// The mock handler must prove source bytes match the separately owned manifest.
func TestServeMockArtifactVerifiedAndSeparate(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	s.serveMockArtifact(rr, httptest.NewRequest(http.MethodGet, "/pools/mock-artifact", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("mock artifact HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var payload struct {
		ID        string `json:"id"`
		Version   int    `json:"version"`
		Source    string `json:"source"`
		SHA256    string `json:"sha256"`
		InputJSON string `json:"inputJSON"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID == "" || payload.Version < 1 || payload.Source == "" || !json.Valid([]byte(payload.InputJSON)) {
		t.Fatalf("invalid runtime artifact payload: id=%q version=%d", payload.ID, payload.Version)
	}
	sum := sha256.Sum256([]byte(payload.Source))
	if got := hex.EncodeToString(sum[:]); got != payload.SHA256 {
		t.Fatalf("runtime artifact hash %s differs from manifest pin %s", got, payload.SHA256)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("mock artifact must not be cached across pin rotation")
	}
}
