// Path: internal/grpcapi/events_silent_R15_test.go | Author: cmdc | 2026-11-22
// R15-FIX-SILENT: the terminal reauth frame of the stream must not be swallowed.
// streamEvents returned nil when Send failed, so a client whose connection was
// already broken never learned that the frame (and with it the refresh signal)
// was lost.
package grpcapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"google.golang.org/grpc/metadata"
)

// r15StreamService builds the service over a real SQLite store and a developer
// bearer, with the service clock placed past the stream's renewal window: the
// stream context is already done, so its terminal reauth frame is attempted on
// the first select without racing the 250 ms revalidation ticker. The empty
// store ignores the context, so the snapshot read before that select cannot
// fail on the already-cancelled stream context.
func r15StreamService(t *testing.T) (*Service, string) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "r15_events.db"))
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
	raw, _, err := svc.CreateKey(t.Context(), auth.User{ID: auth.ID(), AccountStore: "B", Roles: []string{"developer"}}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.Exchange(t.Context(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.Validate(t.Context(), tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	s := New(engine, svc)
	s.Engine.Store = r15EmptyStore{}
	s.now = func() time.Time { return time.Unix(claims.Expires-29, 0) }
	return s, tok.AccessToken
}

// r15EmptyStore is the store the stream reads its snapshot from: no rows, and
// no context deadline, so the terminal branch is reached deterministically.
type r15EmptyStore struct{}

func (r15EmptyStore) View(context.Context, func(store.Reader) error) error { return nil }
func (r15EmptyStore) Update(context.Context, func(store.Tx) error) error   { return nil }
func (r15EmptyStore) Close() error                                         { return nil }

func TestR15TerminalReauthSendErrorPropagates(t *testing.T) {
	s, token := r15StreamService(t)
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token))
	attempted := false
	sendErr := errors.New("client gone")
	err := s.streamEvents(ctx, &EventStreamRequest{}, pool.Thread, func(msg *EventMessage) error {
		if msg.GetType() == "reauth" {
			attempted = true
		}
		return sendErr
	})
	if !attempted {
		t.Fatal("the stream never attempted the terminal reauth frame")
	}
	if !errors.Is(err, sendErr) {
		t.Fatalf("terminal send error = %v, want %v propagated to the caller", err, sendErr)
	}
}
