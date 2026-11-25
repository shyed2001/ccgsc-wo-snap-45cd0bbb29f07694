// Path: internal/grpcapi/events.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the server-streaming event surface of gRPC, the twin of the SSE
// route GET /{pool}/v1/events: the same filters, the same cursor and the same
// terminal reauth event; WO T42 F10.
package grpcapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// eventBus preserves the store transaction boundary and broadcasts the
// committed records. It mirrors the event bus of the REST surface, so both
// transports see the same events in the same order.
type eventBus struct {
	store.Store
	mu          sync.Mutex
	subscribers map[chan []store.Record]struct{}
}

// eventTx captures the events that a transaction wrote, so they are published
// only after the transaction commits.
type eventTx struct {
	store.Tx
	events map[string]store.Record
}

func (t *eventTx) Put(bucket, key string, value any) error {
	if err := t.Tx.Put(bucket, key, value); err != nil {
		return err
	}
	if bucket == store.Events {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		t.events[key] = store.Record{Key: key, Value: raw}
	}
	return nil
}

// PutIfAbsent preserves durable conditional insertion through the event bus.
// Never bypass committed-event capture for the Events bucket.
func (t *eventTx) PutIfAbsent(bucket, key string, value any) (bool, error) {
	atomic, ok := t.Tx.(store.PutIfAbsentTx)
	if !ok || bucket == store.Events {
		return false, store.ErrNotImplemented
	}
	return atomic.PutIfAbsent(bucket, key, value)
}

// NextSequence forwards the optional store.SequenceTx contract, so the PostgreSQL event
// sequence keeps answering through the event bus.
func (t *eventTx) NextSequence(name string) (uint64, error) {
	sequence, ok := t.Tx.(store.SequenceTx)
	if !ok {
		return 0, store.ErrNotImplemented
	}
	return sequence.NextSequence(name)
}

// Delete forwards the optional store.DeleteTx contract of the wrapped transaction.
func (t *eventTx) Delete(bucket, key string) error {
	deleter, ok := t.Tx.(store.DeleteTx)
	if !ok {
		return store.ErrNotImplemented
	}
	return deleter.Delete(bucket, key)
}

func (b *eventBus) Update(ctx context.Context, fn func(store.Tx) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var captured *eventTx
	err := b.Store.Update(ctx, func(tx store.Tx) error {
		captured = &eventTx{Tx: tx, events: make(map[string]store.Record)}
		return fn(captured)
	})
	if err != nil || captured == nil || len(captured.events) == 0 {
		return err
	}
	batch := make([]store.Record, 0, len(captured.events))
	for _, row := range captured.events {
		batch = append(batch, row)
	}
	sort.Slice(batch, func(i, j int) bool { return batch[i].Key < batch[j].Key })
	for ch := range b.subscribers {
		select {
		case ch <- batch:
		default:
			// The queue of the subscriber is full: close the stream, so the
			// client resumes with the last event id and recovers the gap.
			close(ch)
			delete(b.subscribers, ch)
		}
	}
	return nil
}

// subscribe returns the channel of the committed events and the function that
// releases the subscription.
func (b *eventBus) subscribe() (chan []store.Record, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan []store.Record, 128)
	b.subscribers[ch] = struct{}{}
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subscribers[ch]; ok {
			delete(b.subscribers, ch)
			close(ch)
		}
	}
}

// installEventBus wraps the store of the engine with the event bus, so the
// gRPC surface receives the committed events. It is safe to call it once per
// process; a second call keeps the bus that is already installed.
func (s *Service) installEventBus() {
	s.busOnce.Do(func() {
		if previous, ok := s.Engine.Store.(*eventBus); ok {
			s.bus = previous
			return
		}
		s.bus = &eventBus{Store: s.Engine.Store, subscribers: make(map[chan []store.Record]struct{})}
		s.Engine.Store = s.bus
	})
}

// streamEvents streams the events of the pool to the caller. The filters, the
// cursor and the terminal reauth event are the same as on the SSE route.
func (s *Service) streamEvents(ctx context.Context, req *EventStreamRequest, p string, send func(*EventMessage) error) error {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return err
	}
	if err := s.authorize(claims, "developer", "jobs:read"); err != nil {
		return err
	}
	s.installEventBus()
	// The token of the caller bounds the life of the stream, exactly as the
	// renewal protocol of the SSE route does.
	remaining := time.Unix(claims.Expires, 0).Sub(s.clock()) - 30*time.Second
	streamCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	// Subscribe before the replay, so the snapshot and the live stream have no
	// gap between them.
	updates, unsubscribe := s.bus.subscribe()
	defer unsubscribe()
	last := req.GetLastEventId()
	// last0 is the resume cursor of the client; last is the highest id this stream has
	// consumed. R18B review row 4: rows are deduplicated by exact id instead of by the
	// cursor, because a transaction may commit out of id order (another coordinator
	// consumed a higher id first): a row below the cursor can still be new, and skipping
	// it would lose the event forever. The cursor only ever advances forward.
	last0 := last
	sent := make(map[string]struct{})
	var rows []store.Record
	if err := s.Engine.Store.View(streamCtx, func(rd store.Reader) error {
		var err error
		rows, err = rd.List(store.Events)
		return err
	}); err != nil {
		return toStatus(ctx, err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	matches := eventFilter(req, claims.KeyID, p)
	emit := func(batch []store.Record) error {
		for _, row := range batch {
			if row.Key <= last0 {
				continue
			}
			if _, seen := sent[row.Key]; seen {
				continue
			}
			if row.Key > last {
				last = row.Key
			}
			sent[row.Key] = struct{}{}
			var event pool.Event
			if err := json.Unmarshal(row.Value, &event); err != nil {
				return err
			}
			// The cursor advances past the filtered records too, so the resume
			// point stays exact.
			if !matches(event) {
				continue
			}
			if err := send(eventMessage(row.Key, event)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emit(rows); err != nil {
		return err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-streamCtx.Done():
			// The token is about to expire: the terminal reauth event tells the
			// client to refresh and to reconnect. A send failure must reach the
			// caller: a client that never sees the frame never refreshes.
			if ctx.Err() == nil {
				if err := send(&EventMessage{Type: "reauth", EventId: last, At: timestamp(s.clock())}); err != nil {
					return err
				}
			}
			return nil
		case batch, open := <-updates:
			if !open {
				if err := send(eventStreamError(s.clock(), last, http.StatusServiceUnavailable, "The event stream fell behind; reconnect with the last event id.", requestIDFromMetadata(ctx))); err != nil {
					return err
				}
				return nil
			}
			if _, err := s.Auth.Validate(streamCtx, s.tokenOf(ctx)); err != nil {
				if sendErr := send(eventStreamError(s.clock(), last, http.StatusUnauthorized, "invalid or revoked credentials", requestIDFromMetadata(ctx))); sendErr != nil {
					return sendErr
				}
				// R18B review row 14: the RPC must end with Unauthenticated after the
				// error frame; a nil return ended a revoked stream with an OK status.
				return status.Error(codes.Unauthenticated, "invalid or revoked credentials")
			}
			if err := emit(batch); err != nil {
				return err
			}
		case <-ticker.C:
			if _, err := s.Auth.Validate(streamCtx, s.tokenOf(ctx)); err != nil {
				if sendErr := send(eventStreamError(s.clock(), last, http.StatusUnauthorized, "invalid or revoked credentials", requestIDFromMetadata(ctx))); sendErr != nil {
					return sendErr
				}
				// R18B review row 14: the RPC must end with Unauthenticated after the
				// error frame; a nil return ended a revoked stream with an OK status.
				return status.Error(codes.Unauthenticated, "invalid or revoked credentials")
			}
		}
	}
}

// eventFilter builds the predicate that selects the events of the caller.
func eventFilter(req *EventStreamRequest, owner, p string) func(pool.Event) bool {
	jobIDs := splitFilter(req.GetJobIds())
	sessionIDs := splitFilter(req.GetSessionIds())
	types := splitFilter(req.GetTypes())
	return func(event pool.Event) bool {
		if event.Owner != owner || event.PoolID != p {
			return false
		}
		if len(jobIDs) > 0 && !jobIDs[event.JobID] {
			return false
		}
		if len(sessionIDs) > 0 && !sessionIDs[event.SessionID] {
			return false
		}
		if len(types) > 0 && !types[event.Type] {
			return false
		}
		return true
	}
}

// splitFilter reads a comma separated filter into a set.
func splitFilter(raw []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range raw {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out[part] = true
			}
		}
	}
	return out
}

// eventMessage converts one event of the engine into its proto form.
func eventMessage(id string, event pool.Event) *EventMessage {
	message := &EventMessage{
		Type:      event.Type,
		JobId:     event.JobID,
		SessionId: event.SessionID,
		TaskId:    event.TaskID,
		At:        timestamp(event.At),
		EventId:   id,
	}
	if event.Result != nil {
		message.Result = taskResultView(*event.Result)
	}
	return message
}

// tokenOf reads the bearer token of the call from the metadata, so the stream
// can revalidate it while it runs.
func (s *Service) tokenOf(ctx context.Context) string {
	md, ok := metadataFromIncoming(ctx)
	if !ok {
		return ""
	}
	values := md.Get(metadataAuthorization)
	if len(values) == 0 {
		return ""
	}
	raw := strings.TrimSpace(values[0])
	if strings.HasPrefix(raw, "Bearer ") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	}
	return raw
}

// eventStreamError builds the terminal EventMessage of a mid-stream fault. The
// error is the same ErrorDetail a unary failure carries, so the client learns
// the code, message, hint and request id instead of seeing a silent EOF.
func eventStreamError(at time.Time, last string, httpStatus int, text string, requestID string) *EventMessage {
	detail, _ := errorDetailFor(httpStatus, "", errors.New(text), requestID)
	return &EventMessage{Type: "error", EventId: last, At: timestamp(at), Error: detail}
}
