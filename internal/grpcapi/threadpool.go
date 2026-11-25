// Path: internal/grpcapi/threadpool.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the ThreadPool gRPC service, the gRPC twin of the HTTP routes under
// /thread-pool/v1; WO T42 F10.
package grpcapi

import (
	"context"

	"ccgsc/poolscore/internal/pool"
)

// ThreadPoolService is the gRPC service of the browser thread pool.
type ThreadPoolService struct {
	UnimplementedThreadPoolServer
	service *Service
}

// NewThreadPoolService builds the thread pool service over the shared service.
func NewThreadPoolService(s *Service) *ThreadPoolService {
	return &ThreadPoolService{service: s}
}

// pool is the pool id that this service answers for.
func (t *ThreadPoolService) pool() string { return pool.Thread }

// ExchangeToken exchanges a developer API key for a 15-minute JWT.
func (t *ThreadPoolService) ExchangeToken(ctx context.Context, req *TokenRequest) (*TokenResponse, error) {
	return t.service.exchangeToken(ctx, req, t.pool())
}

// RefreshToken exchanges a refresh token for a new JWT.
func (t *ThreadPoolService) RefreshToken(ctx context.Context, req *RefreshTokenRequest) (*TokenResponse, error) {
	return t.service.refreshToken(ctx, req)
}

// CreateKey issues an API key for a developer.
func (t *ThreadPoolService) CreateKey(ctx context.Context, req *CreateKeyRequest) (*ApiKey, error) {
	return t.service.createKey(ctx, req)
}

// UploadArtifact uploads one chunk of a content-addressed artifact.
func (t *ThreadPoolService) UploadArtifact(ctx context.Context, req *ArtifactUploadRequest) (*Artifact, error) {
	return t.service.uploadArtifact(ctx, req)
}

// GetArtifact reads one artifact.
func (t *ThreadPoolService) GetArtifact(ctx context.Context, req *GetArtifactRequest) (*ArtifactData, error) {
	return t.service.getArtifact(ctx, req)
}

// DeleteArtifact removes the caller's reference to an artifact (T100).
func (t *ThreadPoolService) DeleteArtifact(ctx context.Context, req *DeleteArtifactRequest) (*DeleteArtifactResponse, error) {
	return t.service.deleteArtifact(ctx, req)
}

// CreateJob submits a job.
func (t *ThreadPoolService) CreateJob(ctx context.Context, req *CreateJobRequest) (*Job, error) {
	return t.service.createJob(ctx, req, t.pool())
}

// GetJob reads the state of a job.
func (t *ThreadPoolService) GetJob(ctx context.Context, req *GetJobRequest) (*Job, error) {
	return t.service.getJob(ctx, req, t.pool())
}

// CancelJob cancels a queued or running job.
func (t *ThreadPoolService) CancelJob(ctx context.Context, req *CancelJobRequest) (*Job, error) {
	return t.service.cancelJob(ctx, req, t.pool())
}

// GetResults reads one page of the results of a job.
func (t *ThreadPoolService) GetResults(ctx context.Context, req *GetResultsRequest) (*ResultsPage, error) {
	return t.service.getResults(ctx, req.GetJobId(), req.GetCursor(), int(req.GetLimit()), includeFailed(req.IncludeFailed), t.pool())
}

// GetResultsBulk reads the results of a job.
func (t *ThreadPoolService) GetResultsBulk(ctx context.Context, req *GetResultsBulkRequest) (*ResultsPage, error) {
	return t.service.getResults(ctx, req.GetJobId(), req.GetCursor(), int(req.GetLimit()), includeFailed(req.IncludeFailed), t.pool())
}

// ValidateJob lints a job request without submitting it.
func (t *ThreadPoolService) ValidateJob(ctx context.Context, req *ValidateJobRequest) (*ValidationResult, error) {
	return t.service.validateJob(ctx, req, t.pool())
}

// GetJobChain reads the audit chain of one job.
func (t *ThreadPoolService) GetJobChain(ctx context.Context, req *GetJobChainRequest) (*ChainReport, error) {
	return t.service.getJobChain(ctx, req, t.pool())
}

// GetMerkleCheckpoints reads the global Merkle checkpoints.
func (t *ThreadPoolService) GetMerkleCheckpoints(ctx context.Context, req *GetMerkleCheckpointsRequest) (*MerkleReport, error) {
	return t.service.getMerkleCheckpoints(ctx, req, t.pool())
}

// GetCapabilities reads the server limits and the quotas.
func (t *ThreadPoolService) GetCapabilities(ctx context.Context, req *GetCapabilitiesRequest) (*Capabilities, error) {
	return t.service.getCapabilities(ctx, t.pool())
}

// StreamEvents streams the live server events.
func (t *ThreadPoolService) StreamEvents(req *EventStreamRequest, stream ThreadPool_StreamEventsServer) error {
	return t.service.streamEvents(ctxOf(stream), req, t.pool(), func(msg *EventMessage) error {
		return stream.Send(msg)
	})
}
