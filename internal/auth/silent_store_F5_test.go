package auth

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"errors"
	"testing"
)

type f5FailingReadStore struct {
	store.Store
	want error
}

func (s f5FailingReadStore) View(context.Context, func(store.Reader) error) error { return s.want }

func TestF5InitStoreAFailsClosedOnAccountListReadError(t *testing.T) {
	outage := errors.New("synthetic database outage")
	svc := New(f5FailingReadStore{want: outage}, "test-secret", "f5test")
	created, user, password, err := svc.InitStoreA(context.Background(), nil)
	if !errors.Is(err, outage) || created || user != "" || password != "" {
		t.Fatalf("dangerous seed path on DB read failure: created=%v user=%q hasPassword=%t err=%v", created, user, password != "", err)
	}
}

func TestF5AuthenticateStoreAPropagatesStoreOutage(t *testing.T) {
	outage := errors.New("synthetic database outage")
	svc := New(f5FailingReadStore{want: outage}, "test-secret", "f5test")
	_, err := svc.AuthenticateStoreA(context.Background(), "admin", "anything")
	if !errors.Is(err, outage) {
		t.Fatalf("account-store outage masked as a login failure: %v", err)
	}
}
