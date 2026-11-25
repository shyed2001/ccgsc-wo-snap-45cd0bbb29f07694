package pool

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"errors"
	"sync"
	"testing"
)

type f6BlockingUpdateStore struct {
	store.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *f6BlockingUpdateStore) Update(_ context.Context, _ func(store.Tx) error) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return errors.New("synthetic unavailable DB")
}

// F6 review #3: the admin's slow PG write MUST NOT seize the tiny cache-read
// mutex used on the worker/claim hot path. This test uses explicit barriers,
// not scheduler-dependent durations.
func TestF6RecomputeReadLockFreeWhileAdminWriteWaitsForDB(t *testing.T) {
	s := &f6BlockingUpdateStore{entered: make(chan struct{}), release: make(chan struct{})}
	e := &Engine{Store: s, recomputeSettings: map[string]RecomputeSettings{}}
	done := make(chan error, 1)
	go func() {
		_, err := e.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{}, nil, "f6-test")
		done <- err
	}()
	<-s.entered
	acquired := e.recomputeMu.TryLock()
	if acquired {
		e.recomputeMu.Unlock()
	}
	close(s.release)
	if err := <-done; err == nil {
		t.Fatal("the deliberately broken database was accepted")
	}
	if !acquired {
		t.Fatal("recomputeMu was held during the slow Store.Update; worker readers were blocked")
	}
}
