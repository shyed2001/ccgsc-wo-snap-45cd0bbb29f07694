// Path: internal/grpcapi/processpool.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the ProcessPool gRPC service, the gRPC twin of the HTTP routes
// under /process-pool/v1 including the session API; WO T42 F10.
package grpcapi

import (
	"context"

	"ccgsc/poolscore/internal/pool"
)

// ProcessPoolService is the gRPC service of the browser process pool.
type ProcessPoolService struct {
	UnimplementedProcessPoolServer
	service *Service
}

// NewProcessPoolService builds the process pool service over the shared service.
func NewProcessPoolService(s *Service) *ProcessPoolService {
	return &ProcessPoolService{service: s}
}

// pool is the pool id that this service answers for.
func (p *ProcessPoolService) pool() string { return pool.Process }

// ExchangeToken exchanges a developer API key for a 15-minute JWT.
func (p *ProcessPoolService) ExchangeToken(ctx context.Context, req *TokenRequest) (*TokenResponse, error) {
	return p.service.exchangeToken(ctx, req, p.pool())
}

// RefreshToken exchanges a refresh token for a new JWT.
func (p *ProcessPoolService) RefreshToken(ctx context.Context, req *RefreshTokenRequest) (*TokenResponse, error) {
	return p.service.refreshToken(ctx, req)
}

// CreateKey issues an API key for a developer.
func (p *ProcessPoolService) CreateKey(ctx context.Context, req *CreateKeyRequest) (*ApiKey, error) {
	return p.service.createKey(ctx, req)
}

// UploadArtifact uploads one chunk of a content-addressed artifact.
func (p *ProcessPoolService) UploadArtifact(ctx context.Context, req *ArtifactUploadRequest) (*Artifact, error) {
	return p.service.uploadArtifact(ctx, req)
}

// GetArtifact reads one artifact.
func (p *ProcessPoolService) GetArtifact(ctx context.Context, req *GetArtifactRequest) (*ArtifactData, error) {
	return p.service.getArtifact(ctx, req)
}

// DeleteArtifact removes the caller's reference to an artifact (T100).
func (p *ProcessPoolService) DeleteArtifact(ctx context.Context, req *DeleteArtifactRequest) (*DeleteArtifactResponse, error) {
	return p.service.deleteArtifact(ctx, req)
}

// CreateJob submits a job.
func (p *ProcessPoolService) CreateJob(ctx context.Context, req *CreateJobRequest) (*Job, error) {
	return p.service.createJob(ctx, req, p.pool())
}

// GetJob reads the state of a job.
func (p *ProcessPoolService) GetJob(ctx context.Context, req *GetJobRequest) (*Job, error) {
	return p.service.getJob(ctx, req, p.pool())
}

// CancelJob cancels a queued or running job.
func (p *ProcessPoolService) CancelJob(ctx context.Context, req *CancelJobRequest) (*Job, error) {
	return p.service.cancelJob(ctx, req, p.pool())
}

// GetResults reads one page of the results of a job.
func (p *ProcessPoolService) GetResults(ctx context.Context, req *GetResultsRequest) (*ResultsPage, error) {
	return p.service.getResults(ctx, req.GetJobId(), req.GetCursor(), int(req.GetLimit()), includeFailed(req.IncludeFailed), p.pool())
}

// GetResultsBulk reads the results of a job.
func (p *ProcessPoolService) GetResultsBulk(ctx context.Context, req *GetResultsBulkRequest) (*ResultsPage, error) {
	return p.service.getResults(ctx, req.GetJobId(), req.GetCursor(), int(req.GetLimit()), includeFailed(req.IncludeFailed), p.pool())
}

// ValidateJob lints a job request without submitting it.
func (p *ProcessPoolService) ValidateJob(ctx context.Context, req *ValidateJobRequest) (*ValidationResult, error) {
	return p.service.validateJob(ctx, req, p.pool())
}

// GetJobChain reads the audit chain of one job.
func (p *ProcessPoolService) GetJobChain(ctx context.Context, req *GetJobChainRequest) (*ChainReport, error) {
	return p.service.getJobChain(ctx, req, p.pool())
}

// GetMerkleCheckpoints reads the global Merkle checkpoints.
func (p *ProcessPoolService) GetMerkleCheckpoints(ctx context.Context, req *GetMerkleCheckpointsRequest) (*MerkleReport, error) {
	return p.service.getMerkleCheckpoints(ctx, req, p.pool())
}

// OpenSession opens a process pool session that leases tabs and keeps them
// warm.
func (p *ProcessPoolService) OpenSession(ctx context.Context, req *OpenSessionRequest) (*Session, error) {
	return p.service.openSession(ctx, req)
}

// GetSession reads the state of a session.
func (p *ProcessPoolService) GetSession(ctx context.Context, req *GetSessionRequest) (*Session, error) {
	return p.service.getSession(ctx, req)
}

// StreamSessionTasks streams tasks into an open session, in batches.
func (p *ProcessPoolService) StreamSessionTasks(ctx context.Context, req *StreamSessionTasksRequest) (*SessionTaskBatch, error) {
	return p.service.streamSessionTasks(ctx, req)
}

// CloseSession closes a session and returns its tabs to the pool.
func (p *ProcessPoolService) CloseSession(ctx context.Context, req *CloseSessionRequest) (*Session, error) {
	return p.service.closeSession(ctx, req)
}

// GetCapabilities reads the server limits and the quotas.
func (p *ProcessPoolService) GetCapabilities(ctx context.Context, req *GetCapabilitiesRequest) (*Capabilities, error) {
	return p.service.getCapabilities(ctx, p.pool())
}

// StreamEvents streams the live server events.
func (p *ProcessPoolService) StreamEvents(req *EventStreamRequest, stream ProcessPool_StreamEventsServer) error {
	return p.service.streamEvents(ctxOf(stream), req, p.pool(), func(msg *EventMessage) error {
		return stream.Send(msg)
	})
}
