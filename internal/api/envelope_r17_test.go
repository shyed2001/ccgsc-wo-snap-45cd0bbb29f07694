// Path: internal/api/envelope_r17_test.go | Author: cmdc | 2026-11-22
// WO R17-ENVELOPE: a mid-stream fault on the SSE route is reported as an
// `event: error` frame carrying {code,message,hint,requestId,retryable}
// before the stream closes; the reauth frame is unchanged.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
)

// TestR17SSEEmitsErrorFrameOnMidStreamFault revokes the bearer after the
// stream is connected: the revalidation ticker fails and the client must see
// the terminal error frame instead of a silent EOF.
func TestR17SSEEmitsErrorFrameOnMidStreamFault(t *testing.T) {
	s, _ := r15Server(t)
	raw, key, err := s.Auth.CreateKey(context.Background(),
		auth.User{ID: auth.ID(), AccountStore: "B", Roles: []string{"developer"}}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Auth.Exchange(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/thread-pool/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", res.StatusCode)
	}

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	connected := false
	event := ""
	var data string
	for scanner.Scan() {
		line := scanner.Text()
		if line == ": connected" {
			connected = true
			// The handler has subscribed and replayed the snapshot; from here
			// its 250 ms revalidation ticker is what must notice the revoke.
			if err := s.Auth.Revoke(context.Background(), key.ID); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			continue
		}
		if line != "" {
			continue
		}
		if event == "" {
			continue
		}
		if event != "error" {
			t.Fatalf("got a %q frame before the fault: %s", event, data)
		}
		if !connected {
			t.Fatal("the error frame arrived before the stream was connected")
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			t.Fatalf("error frame is not JSON: %v (%s)", err, data)
		}
		for _, key := range []string{"code", "message", "hint", "requestId"} {
			value, ok := frame[key].(string)
			if !ok || value == "" {
				t.Fatalf("error frame %q = %#v, want a non-empty string: %s", key, frame[key], data)
			}
		}
		if frame["code"] != "unauthorized" {
			t.Fatalf("error frame code = %#v, want unauthorized", frame["code"])
		}
		if retryable, ok := frame["retryable"].(bool); !ok || retryable {
			t.Fatalf("error frame retryable = %#v, want false", frame["retryable"])
		}
		if len(frame) != 5 {
			t.Fatalf("error frame carries extra fields: %v", frame)
		}
		return
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("stream read: %v", err)
	}
	t.Fatal("the stream closed without an event: error frame")
}
