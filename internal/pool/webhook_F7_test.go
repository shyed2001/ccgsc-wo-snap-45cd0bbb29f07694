package pool

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestWebhookSignatureAndBoundedRetry(t *testing.T) {
	var calls atomic.Int32
	const secret = "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(r.Header.Get("X-WWPP-Timestamp") + "."))
		_, _ = mac.Write(body)
		want := hex.EncodeToString(mac.Sum(nil))
		if got := r.Header.Get("X-WWPP-Signature"); got != want {
			t.Errorf("signature = %q, want %q", got, want)
		}
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "retry", http.StatusBadGateway)
			return
		}
		if got := r.Header.Get("X-WWPP-Attempt"); got != strconv.Itoa(int(calls.Load())) {
			t.Errorf("attempt = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := DeliverWebhook(context.Background(), WebhookConfig{URL: server.URL + "/done", Secret: secret}, Job{JobID: "job-1", State: "completed"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestValidateWebhookURLRejectsPrivateAddress(t *testing.T) {
	if err := ValidateWebhookURL("http://127.0.0.1:8080/done", false); err == nil {
		t.Fatal("private webhook address was accepted")
	}
	if err := ValidateWebhookURL("http://127.0.0.1:8080/done", true); err != nil {
		t.Fatalf("admin-allowed private address rejected: %v", err)
	}
}
