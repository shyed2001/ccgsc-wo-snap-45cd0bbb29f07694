// Path: internal/grpcapi/session.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the session RPCs of the process pool, the gRPC twins of the HTTP
// routes POST /process-pool/v1/sessions, GET /process-pool/v1/sessions/{id},
// POST /process-pool/v1/sessions/{id}/tasks and
// POST /process-pool/v1/sessions/{id}:close; WO T42 F10.
package grpcapi

import (
	"context"

	"ccgsc/poolscore/internal/pool"
)

// openSession opens a session that leases tabs and keeps them warm. It is the
// gRPC twin of POST /process-pool/v1/sessions.
func (s *Service) openSession(ctx context.Context, req *OpenSessionRequest) (*Session, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:write"); err != nil {
		return nil, err
	}
	// The engine carries neither the warm initializer arguments nor the warm
	// timeout, so a request that sets them is refused instead of dropping the
	// values silently.
	if req.GetWarmInitializerArgs() != nil || req.GetWarmTimeoutMs() != 0 {
		return nil, toStatus(ctx, pool.ErrUnsupported)
	}
	query, err := sessionRequest(req)
	if err != nil {
		return nil, err
	}
	session, err := s.Engine.OpenSession(ctx, claims.KeyID, req.GetIdempotencyKey(), query)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return sessionView(session), nil
}

// getSession reads the state of a session. It is the gRPC twin of
// GET /process-pool/v1/sessions/{sessionId}.
func (s *Service) getSession(ctx context.Context, req *GetSessionRequest) (*Session, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:read"); err != nil {
		return nil, err
	}
	session, err := s.Engine.GetSession(ctx, claims.KeyID, req.GetSessionId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return sessionView(session), nil
}

// streamSessionTasks streams tasks into an open session, in batches. It is the
// gRPC twin of POST /process-pool/v1/sessions/{sessionId}/tasks.
func (s *Service) streamSessionTasks(ctx context.Context, req *StreamSessionTasksRequest) (*SessionTaskBatch, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:write"); err != nil {
		return nil, err
	}
	query, err := sessionTaskRequest(req)
	if err != nil {
		return nil, err
	}
	job, err := s.Engine.SessionTasks(ctx, claims.KeyID, req.GetSessionId(), "", query)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	batch := &SessionTaskBatch{
		SessionId:  req.GetSessionId(),
		JobId:      job.JobID,
		BatchId:    job.JobID,
		State:      "accepted",
		AcceptedAt: timestamp(job.CreatedAt),
		Warnings:   warningView(job.Warnings),
	}
	for _, task := range job.Tasks {
		batch.AcceptedTaskIds = append(batch.AcceptedTaskIds, task.TaskID)
	}
	batch.TaskCount = int64(len(batch.AcceptedTaskIds))
	return batch, nil
}

// closeSession closes a session and returns its tabs to the pool. It is the
// gRPC twin of POST /process-pool/v1/sessions/{sessionId}:close.
func (s *Service) closeSession(ctx context.Context, req *CloseSessionRequest) (*Session, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:write"); err != nil {
		return nil, err
	}
	// The REST route refuses a close that carries a grace period, because the
	// engine does not carry it yet. The gRPC route refuses it the same way.
	if req.GetGraceMs() != 0 {
		return nil, toStatus(ctx, pool.ErrUnsupported)
	}
	session, err := s.Engine.CloseSession(ctx, claims.KeyID, req.GetSessionId(), req.GetDrain())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return sessionView(session), nil
}
