// Path: internal/grpcapi/envelope_r17_test.go | Author: cmdc | 2026-11-22
// WO R17-ENVELOPE: 413 and 422 must keep their identity on gRPC, ErrorDetail
// gains hint and request_id (message already exists and trace_id stays), and
// a mid-stream fault is sent as a terminal EventMessage{Type: "error"} before
// the stream closes.
package grpcapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// r17StatusCase is one HTTP-equivalent status of the gRPC mapping. R18B review row 21:
// the retryable answer is pinned per code instead of recomputed from the 429/503 rule,
// so a wrong per-error mapping can no longer pass.
type r17StatusCase struct {
	status    int
	code      codes.Code
	name      string
	retryable bool
}

var r17StatusCases = []r17StatusCase{
	{http.StatusBadRequest, codes.InvalidArgument, "invalid-request", false},
	{http.StatusUnauthorized, codes.Unauthenticated, "unauthorized", false},
	{http.StatusForbidden, codes.PermissionDenied, "forbidden", false},
	{http.StatusNotFound, codes.NotFound, "not-found", false},
	{http.StatusConflict, codes.Aborted, "state-conflict", false},
	{http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "artifact-too-large", false},
	{http.StatusUnprocessableEntity, codes.FailedPrecondition, "artifact-rejected", false},
	{http.StatusTooManyRequests, codes.ResourceExhausted, "quota-exceeded", true},
	{http.StatusInternalServerError, codes.Internal, "internal-error", false},
	{http.StatusNotImplemented, codes.Unimplemented, "not-implemented", false},
	{http.StatusServiceUnavailable, codes.Unavailable, "service-unavailable", true},
	// The code-level overrides of the REST surface: a 409 that resolves when a
	// worker joins is retryable, a 503 that needs provisioning is not.
	{http.StatusConflict, codes.Aborted, "capacity-unavailable", true},
	{http.StatusServiceUnavailable, codes.Unavailable, "config-schema-unavailable", false},
}

// r17Detail extracts the ErrorDetail of a status built by detailStatusFor.
func r17Detail(t *testing.T, err error) *ErrorDetail {
	t.Helper()
	st := status.Convert(err)
	details := st.Details()
	if len(details) != 1 {
		t.Fatalf("status carries %d details, want 1", len(details))
	}
	detail, ok := details[0].(*ErrorDetail)
	if !ok {
		t.Fatalf("detail type = %T", details[0])
	}
	return detail
}

// TestR17StatusMappingCarriesFullDetail is the table test of every gRPC code:
// the 413/422 identity is fixed, every detail field is present, message keeps the
// legacy status text (R18B review row 12) and retryable is pinned per code (row 21).
func TestR17StatusMappingCarriesFullDetail(t *testing.T) {
	const causeText = "the detail of the failure"
	for _, tc := range r17StatusCases {
		t.Run(tc.name, func(t *testing.T) {
			err := detailStatusFor(tc.status, tc.name, errors.New(causeText), "")
			st := status.Convert(err)
			if st.Code() != tc.code {
				t.Fatalf("codes.Code = %v, want %v", st.Code(), tc.code)
			}
			detail := r17Detail(t, err)
			if detail.GetCode() != tc.name {
				t.Fatalf("detail code = %q, want %q", detail.GetCode(), tc.name)
			}
			if detail.GetMessage() != http.StatusText(tc.status) {
				t.Fatalf("message = %q, want the legacy status text %q", detail.GetMessage(), http.StatusText(tc.status))
			}
			wantDetail := causeText
			if tc.status >= http.StatusInternalServerError { // R18B row12: sanitize every 5xx, not just 500
				// A 5xx detail is generic; an internal cause never leaks (row 12).
				wantDetail = "The operation could not be completed."
			}
			if detail.GetDetail() != wantDetail {
				t.Fatalf("detail = %q, want %q", detail.GetDetail(), wantDetail)
			}
			if detail.GetHint() == "" {
				t.Fatal("hint is empty")
			}
			if detail.GetRequestId() != "" {
				t.Fatalf("request_id = %q, want empty (no x-request-id metadata was sent)", detail.GetRequestId())
			}
			if detail.GetStatus() != int32(tc.status) {
				t.Fatalf("status = %d, want %d", detail.GetStatus(), tc.status)
			}
			if detail.GetType() == "" || detail.GetTitle() == "" {
				t.Fatalf("type/title = %q/%q", detail.GetType(), detail.GetTitle())
			}
			if detail.GetRetryable() != tc.retryable {
				t.Fatalf("retryable = %v, want %v for %s/%d", detail.GetRetryable(), tc.retryable, tc.name, tc.status)
			}
		})
	}
}

// TestR17ErrorDetailKeepsLegacyFields is the backward-compatibility test: the
// old ErrorDetail fields keep their numbers and names, and the new fields take
// new numbers only.
func TestR17ErrorDetailKeepsLegacyFields(t *testing.T) {
	want := map[protoreflect.FieldNumber]string{
		1: "code", 2: "trace_id", 3: "retryable", 4: "message", 5: "detail",
		6: "status", 7: "type", 8: "title", 9: "instance", 10: "retry_after_seconds",
		11: "hint", 12: "request_id",
	}
	fields := (&ErrorDetail{}).ProtoReflect().Descriptor().Fields()
	for number, name := range want {
		field := fields.ByNumber(number)
		if field == nil {
			t.Fatalf("ErrorDetail field %d is gone", number)
		}
		if string(field.Name()) != name {
			t.Fatalf("ErrorDetail field %d = %q, want %q", number, field.Name(), name)
		}
	}
	// An old client body (fields 1..10 only) still round-trips.
	old := &ErrorDetail{Code: "quota-exceeded", TraceId: "old-trace", Retryable: true, Message: "Too Many Requests", Detail: "slow down", Status: 429, Type: "about:blank", Title: "Too Many Requests", Instance: "/x", RetryAfterSeconds: 60}
	raw, err := proto.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var round ErrorDetail
	if err := proto.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	if round.GetCode() != old.GetCode() || round.GetTraceId() != old.GetTraceId() || !round.GetRetryable() ||
		round.GetMessage() != old.GetMessage() || round.GetDetail() != old.GetDetail() ||
		round.GetStatus() != old.GetStatus() || round.GetType() != old.GetType() ||
		round.GetTitle() != old.GetTitle() || round.GetInstance() != old.GetInstance() ||
		round.GetRetryAfterSeconds() != old.GetRetryAfterSeconds() {
		t.Fatalf("legacy ErrorDetail did not round-trip: %+v", &round)
	}
}

// TestR17UnaryErrorKeepsLegacyMessageStatusText is the unary-level assertion of R18B
// review row 22: the message field of a unary failure keeps the legacy status text (it
// never carries the raw cause), and the request id comes from the x-request-id metadata
// of the call instead of a freshly invented random value (row 16).
func TestR17UnaryErrorKeepsLegacyMessageStatusText(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "r17_unary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.New(db, config.Default().JWTSecret, "s1")
	s := New(engine, svc)
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("x-request-id", "req-123"))
	_, err = s.exchangeToken(ctx, &TokenRequest{ApiKey: "not-a-key"}, pool.Thread)
	if err == nil {
		t.Fatal("exchangeToken accepted a bad api key")
	}
	detail := r17Detail(t, err)
	if detail.GetMessage() != http.StatusText(int(detail.GetStatus())) {
		t.Fatalf("message = %q, want the legacy status text %q", detail.GetMessage(), http.StatusText(int(detail.GetStatus())))
	}
	if detail.GetRequestId() != "req-123" {
		t.Fatalf("request_id = %q, want the x-request-id metadata value req-123", detail.GetRequestId())
	}
	if detail.GetTraceId() != "" {
		t.Fatalf("trace_id = %q, want empty (the engine attaches none)", detail.GetTraceId())
	}
}

// r17SignalStore signals the first snapshot read, so the test knows that the
// stream passed authentication and is waiting in its select loop.
type r17SignalStore struct {
	store.Store
	once   sync.Once
	viewed chan struct{}
}

func (s *r17SignalStore) View(ctx context.Context, fn func(store.Reader) error) error {
	s.once.Do(func() { close(s.viewed) })
	return s.Store.View(ctx, fn)
}

// TestR17StreamSendsErrorEventOnRevokedToken covers a mid-stream fault: the
// revalidation fails and the client gets EventMessage{Type: "error"} before
// the stream closes, not a silent EOF.
func TestR17StreamSendsErrorEventOnRevokedToken(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "r17_events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.New(db, config.Default().JWTSecret, "s1")
	raw, key, err := svc.CreateKey(t.Context(), auth.User{ID: auth.ID(), AccountStore: "B", Roles: []string{"developer"}}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.Exchange(t.Context(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := New(engine, svc)
	viewed := make(chan struct{})
	s.Engine.Store = &r17SignalStore{Store: s.Engine.Store, viewed: viewed}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+tok.AccessToken, "x-request-id", "stream-req-7"))
	msgs := make(chan *EventMessage, 8)
	errs := make(chan error, 1)
	go func() {
		errs <- s.streamEvents(ctx, &EventStreamRequest{}, pool.Thread, func(msg *EventMessage) error {
			msgs <- msg
			return nil
		})
	}()
	<-viewed
	if err := svc.Revoke(t.Context(), key.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-msgs:
		if msg.GetType() != "error" {
			t.Fatalf("stream event type = %q, want error", msg.GetType())
		}
		detail := msg.GetError()
		if detail == nil {
			t.Fatal("the error event carries no detail")
		}
		if detail.GetCode() != "unauthorized" || detail.GetStatus() != http.StatusUnauthorized {
			t.Fatalf("code/status = %q/%d", detail.GetCode(), detail.GetStatus())
		}
		for name, value := range map[string]string{
			"message": detail.GetMessage(), "hint": detail.GetHint(), "requestId": detail.GetRequestId(), "detail": detail.GetDetail(),
		} {
			if value == "" {
				t.Fatalf("stream error field %s is empty", name)
			}
		}
		if detail.GetRequestId() != "stream-req-7" {
			t.Fatalf("stream error request_id = %q, want the x-request-id metadata value stream-req-7", detail.GetRequestId())
		}
		if detail.GetMessage() != http.StatusText(http.StatusUnauthorized) {
			t.Fatalf("stream error message = %q, want the legacy status text %q", detail.GetMessage(), http.StatusText(http.StatusUnauthorized))
		}
		if detail.GetRetryable() {
			t.Fatal("a revoked token must not be retryable")
		}
	case err := <-errs:
		t.Fatalf("the stream ended before sending the error event: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no error event within 5s")
	}
	select {
	case err := <-errs:
		// R18B review row 14: the revoked stream must end with Unauthenticated, not
		// with an OK status after the error frame.
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("stream returned %v after the error event, want codes.Unauthenticated", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not close after the error event")
	}
}
