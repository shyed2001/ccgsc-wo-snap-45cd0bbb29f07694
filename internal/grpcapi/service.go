// Path: internal/grpcapi/service.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the shared gRPC logic of the pools, mirroring the HTTP routes of
// internal/api/api.go: the same auth claims, the same engine calls and the same
// answers; WO T42 F10.
package grpcapi

import (
	"context"
	"encoding/base64"
	"strconv"
	"sync"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/pool"
)

// Service is the shared gRPC adapter of the pools. It owns no state of its own:
// every call goes to the same engine that the REST surface uses.
type Service struct {
	Engine *pool.Engine
	Auth   *auth.Service
	// RateLimit is the rate limiter of the REST surface. A nil limiter means
	// that the gRPC surface is not rate limited.
	RateLimit *auth.RateLimiter
	// AllowPrivateWebhooks permits a webhook that addresses a private address.
	// The REST surface reads it from the server configuration.
	AllowPrivateWebhooks bool
	// bus broadcasts the committed events to the streams of the gRPC surface.
	bus     *eventBus
	busOnce sync.Once
	// now is the clock of the service; the tests replace it.
	now func() time.Time
}

// New builds the gRPC service over the engine and the auth service.
func New(engine *pool.Engine, a *auth.Service) *Service {
	s := &Service{Engine: engine, Auth: a, now: time.Now}
	// Install event forwarding before REST/gRPC handlers begin serving.
	// Lazy replacement of Engine.Store on the first streaming RPC raced with
	// concurrent REST SSE handlers (checksuite -race regression).
	s.installEventBus()
	return s
}

// clock returns the clock of the service.
func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// authenticate reads and validates the claims of the call.
func (s *Service) authenticate(ctx context.Context) (auth.Claims, error) {
	return claimsFromMetadata(ctx, s.Auth, s.RateLimit)
}

// authorize applies the role and scope rule of the REST surface.
func (s *Service) authorize(c auth.Claims, role, scope string) error {
	return authorize(c, role, scope)
}

// ---------------------------------------------------------------- auth

// exchangeToken exchanges a developer API key for a 15-minute JWT. It is the
// gRPC twin of POST /{pool}/v1/auth/token.
func (s *Service) exchangeToken(ctx context.Context, req *TokenRequest, p string) (*TokenResponse, error) {
	token, err := s.Auth.Exchange(ctx, req.GetApiKey(), req.GetScopes())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	token.PoolID = p
	return &TokenResponse{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		ExpiresIn:   int64(token.ExpiresIn),
		ExpiresAt:   timestamp(token.ExpiresAt),
		Scopes:      token.Scopes,
		ServerId:    token.ServerID,
		PoolId:      token.PoolID,
	}, nil
}

// refreshToken exchanges a refresh token for a new JWT. The REST route
// POST /{pool}/v1/auth/refresh exists and the engine issues no refresh token
// yet, so the call is refused as not implemented instead of inventing a token.
func (s *Service) refreshToken(ctx context.Context, req *RefreshTokenRequest) (*TokenResponse, error) {
	return nil, notImplemented("the refresh token is not issued yet")
}

// createKey issues an API key for a developer. It is the gRPC twin of
// POST /{pool}/v1/auth/keys and needs the admin role with the keys:write
// scope.
func (s *Service) createKey(ctx context.Context, req *CreateKeyRequest) (*ApiKey, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "admin", "keys:write"); err != nil {
		return nil, err
	}
	user := auth.User{ID: req.GetUserId(), AccountStore: req.GetAccountStore(), Roles: req.GetRoles()}
	raw, key, err := s.Auth.CreateKey(ctx, user, req.GetScopes())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &ApiKey{ApiKey: raw, KeyId: key.ID, UserId: key.User.ID}, nil
}

// ---------------------------------------------------------------- artifacts

// uploadArtifact uploads one chunk of a content-addressed artifact. It is the
// gRPC twin of PUT /{pool}/v1/artifacts.
func (s *Service) uploadArtifact(ctx context.Context, req *ArtifactUploadRequest) (*Artifact, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "artifacts:write"); err != nil {
		return nil, err
	}
	if req.GetContentRange() != "" {
		want := "bytes " + strconv.FormatInt(req.GetUploadOffset(), 10) + "-" +
			strconv.FormatInt(req.GetUploadOffset()+int64(len(req.GetChunk()))-1, 10) + "/" +
			strconv.FormatInt(req.GetUploadLength(), 10)
		if req.GetContentRange() != want {
			return nil, toStatus(ctx, pool.ErrInvalid)
		}
	}
	artifact, err := s.Engine.UploadOwned(ctx, req.GetSha256(), req.GetContentType(),
		req.GetUploadOffset(), req.GetUploadLength(), req.GetChunk(), claims.KeyID, uploadScanOwnerCode(claims))
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return artifactView(artifact), nil
}

// getArtifact reads one artifact. It is the gRPC twin of
// GET /{pool}/v1/artifacts/{artifactId}. T100: a Store A admin reads every
// artifact, a contributor tab keeps the worker read rule it has today, and a
// developer must hold a per-owner reference for the artifact.
func (s *Service) getArtifact(ctx context.Context, req *GetArtifactRequest) (*ArtifactData, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := authorizeArtifactRead(claims); err != nil {
		return nil, err
	}
	// The contributor worker rule stays as it is today (T100); only the
	// developer path carries the reference rule.
	if !claims.HasRole("contributor") {
		if err := s.requireArtifactUse(ctx, claims, []string{req.GetArtifactId()}); err != nil {
			return nil, toStatus(ctx, err)
		}
	}
	artifact, err := s.Engine.Artifact(ctx, req.GetArtifactId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &ArtifactData{
		ArtifactId:  artifact.ArtifactID,
		Sha256:      artifact.SHA256,
		ContentType: artifact.ContentType,
		Data:        artifact.Data,
	}, nil
}

// requireArtifactUse enforces the T100 owner-reference rule for developer
// callers. Store A admins and directors bypass it; the contributor worker rule
// is checked by authorizeArtifactRead before this helper runs.
func (s *Service) requireArtifactUse(ctx context.Context, claims auth.Claims, ids []string) error {
	if claims.HasRole("admin") || claims.HasRole("director") {
		return nil
	}
	return s.Engine.CanUseArtifacts(ctx, claims.KeyID, false, ids)
}

// deleteArtifact removes the caller's reference. It is the gRPC twin of
// DELETE /{pool}/v1/artifacts/{artifactId} (T100): the blob is deleted when no
// reference remains.
func (s *Service) deleteArtifact(ctx context.Context, req *DeleteArtifactRequest) (*DeleteArtifactResponse, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := authorize(claims, "developer", "artifacts:write"); err != nil {
		return nil, err
	}
	out, err := s.Engine.DeleteArtifactReference(ctx, req.GetArtifactId(), claims.KeyID)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return &DeleteArtifactResponse{
		ArtifactId:          out.ArtifactID,
		Sha256:              out.SHA256,
		Removed:             out.Removed,
		BlobDeleted:         out.BlobDeleted,
		RemainingReferences: out.RemainingRefs,
	}, nil
}

// ---------------------------------------------------------------- jobs

// createJob submits a job. It is the gRPC twin of POST /{pool}/v1/jobs.
func (s *Service) createJob(ctx context.Context, req *CreateJobRequest, p string) (*Job, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:write"); err != nil {
		return nil, err
	}
	query, err := createRequest(req)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	// T100: job use of a module artifact requires that the caller holds a
	// reference for it (a Store A admin bypasses). The module_sha256 alias
	// resolves to the same check the engine applies.
	ids := query.ModuleArtifactIDs
	if len(ids) == 0 && query.ModuleSHA256 != "" {
		ids = []string{query.ModuleSHA256}
	}
	if err := s.requireArtifactUse(ctx, claims, ids); err != nil {
		return nil, toStatus(ctx, err)
	}
	if query.Webhook != nil {
		if err := query.Webhook.Validate(s.AllowPrivateWebhooks); err != nil {
			return nil, toStatus(ctx, pool.ErrInvalid)
		}
	}
	job, err := s.Engine.Create(ctx, p, claims.KeyID, req.GetIdempotencyKey(), query)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return jobView(job, s.Engine.JobRerunPercent(job)), nil
}

// getJob reads the state of a job. It is the gRPC twin of
// GET /{pool}/v1/jobs/{jobId}.
func (s *Service) getJob(ctx context.Context, req *GetJobRequest, p string) (*Job, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:read"); err != nil {
		return nil, err
	}
	job, err := s.Engine.Get(ctx, p, claims.KeyID, req.GetJobId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return jobView(job, s.Engine.JobRerunPercent(job)), nil
}

// cancelJob cancels a job. It is the gRPC twin of
// POST /{pool}/v1/jobs/{jobId}:cancel.
func (s *Service) cancelJob(ctx context.Context, req *CancelJobRequest, p string) (*Job, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:write"); err != nil {
		return nil, err
	}
	// The REST route refuses a cancel that carries a reason or a grace period,
	// because the engine does not carry them yet. The gRPC route refuses them
	// the same way, so no request field is silently ignored.
	if req.GetReason() != "" || req.GetGraceMs() != 0 {
		return nil, toStatus(ctx, pool.ErrUnsupported)
	}
	job, _, err := s.Engine.Cancel(ctx, p, claims.KeyID, req.GetJobId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return jobView(job, s.Engine.JobRerunPercent(job)), nil
}

// validateJob lints a job request. It is the gRPC twin of
// POST /{pool}/v1/jobs:validate.
func (s *Service) validateJob(ctx context.Context, req *ValidateJobRequest, p string) (*ValidationResult, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:write"); err != nil {
		return nil, err
	}
	if req.GetRequest() == nil {
		return nil, toStatus(ctx, pool.ErrInvalid)
	}
	query, err := createRequest(req.GetRequest())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	validated, warnings, err := s.Engine.Validate(query)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	estimated := len(validated.Tasks)
	if validated.Split != nil {
		estimated = validated.Split.Count
	}
	return &ValidationResult{
		Valid:          true,
		Warnings:       warningView(warnings),
		EstimatedTasks: int64(estimated),
		DeviceClasses:  []string{"cpu", "gpu"},
	}, nil
}

// ---------------------------------------------------------------- results

// getResults reads one page of the results of a job. It is the gRPC twin of
// GET /{pool}/v1/jobs/{jobId}/results and of the bulk route beside it.
func (s *Service) getResults(ctx context.Context, jobID, cursor string, limit int, failed bool, p string) (*ResultsPage, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:read"); err != nil {
		return nil, err
	}
	job, err := s.Engine.Get(ctx, p, claims.KeyID, jobID)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	items, more, next, err := resultPage(job.Results, cursor, limit, failed)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	page := &ResultsPage{Order: "as_completed", HasMore: more, NextCursor: next, ChainHead: job.ChainHead, ChainEntries: int64(job.ChainEntries), AuditGap: job.AuditGap}
	for _, item := range items {
		page.Items = append(page.Items, taskResultView(item))
	}
	return page, nil
}

// includeFailed reads the optional include_failed field. The server default is
// true, which is the default of the REST route.
func includeFailed(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

// resultPage reads one page of the results, with the same cursor encoding and
// the same limits as the REST route.
func resultPage(results []pool.Result, cursor string, limit int, includeFailed bool) ([]pool.Result, bool, string, error) {
	offset := 0
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, false, "", pool.ErrInvalid
		}
		offset, err = strconv.Atoi(string(decoded))
		if err != nil || offset < 0 || offset > len(results) {
			return nil, false, "", pool.ErrInvalid
		}
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return nil, false, "", pool.ErrInvalid
	}
	items := []pool.Result{}
	for offset < len(results) && len(items) < limit {
		value := results[offset]
		offset++
		if !includeFailed && value.Status == "failed" {
			continue
		}
		items = append(items, value)
	}
	next := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
	return items, offset < len(results), next, nil
}

// ---------------------------------------------------------------- audit chain

// getJobChain reads the audit chain of a job. It is the gRPC twin of
// GET /{pool}/v1/jobs/{jobId}/chain.
func (s *Service) getJobChain(ctx context.Context, req *GetJobChainRequest, p string) (*ChainReport, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:read"); err != nil {
		return nil, err
	}
	report, err := s.Engine.JobChain(ctx, p, claims.KeyID, req.GetJobId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return chainReportView(report), nil
}

// getMerkleCheckpoints reads the global Merkle checkpoints. It is the gRPC
// twin of GET /{pool}/v1/merkle:checkpoints.
func (s *Service) getMerkleCheckpoints(ctx context.Context, req *GetMerkleCheckpointsRequest, p string) (*MerkleReport, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:read"); err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit < 0 || limit > 1000 {
		return nil, toStatus(ctx, pool.ErrInvalid)
	}
	report, err := s.Engine.MerkleCheckpoints(ctx, limit)
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return merkleReportView(report), nil
}

// ---------------------------------------------------------------- capabilities

// getCapabilities reads the server limits and the quotas. It is the gRPC twin
// of GET /{pool}/v1/capabilities.
func (s *Service) getCapabilities(ctx context.Context, p string) (*Capabilities, error) {
	claims, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(claims, "developer", "jobs:read"); err != nil {
		return nil, err
	}
	trust := s.Engine.TrustConfig()
	quota := s.Engine.QuotaConfig()
	scan := s.Engine.UploadScanConfig()
	chain := s.Engine.ChainConfig()
	return &Capabilities{
		ServerId:   s.Engine.ServerID,
		PoolId:     p,
		ApiVersion: "1",
		ServerLimits: &Capabilities_ServerLimits{
			MaxTasksPerJob:    pool.MaxTasksPerJob,
			MaxInlineBytes:    262144,
			MaxBatchSize:      pool.MaxBatchSize,
			MaxTaskBytes:      pool.MaxArtifactBytes,
			MaxTaskDurationMs: 0,
			MaxArtifactBytes:  pool.MaxArtifactBytes,
			DefaultBatchSize:  map[string]int64{"cpu": 1, "gpu": 1},
		},
		Gpu: &Capabilities_Gpu{Supported: true, WebgpuRequired: true},
		Quotas: &Capabilities_Quotas{
			RunningJobs:         int64(quota.MaxRunningJobs),
			QueuedTasks:         int64(quota.MaxQueuedTasks),
			RequestsPerMinute:   int64(quota.RequestsPerMinute),
			TaskBytes:           0,
			TaskTimeMs:          0,
			PriorityHighAllowed: quota.HighPriority,
		},
		PriorityClasses: []string{"low", "normal", "high"},
		Sessions: &Capabilities_Sessions{
			Supported:                p == pool.Process,
			WarmInitializerSupported: p == pool.Process,
		},
		Rerun: &Capabilities_Rerun{
			Available:                 trust.RerunEnabled,
			DefaultPercent:            trust.RerunPercent,
			MaxPercent:                trust.RerunMaxPercent,
			ToleranceCompareAvailable: false,
		},
		TamperEvidence: &Capabilities_TamperEvidence{
			SecurityLogEnabled:   true,
			PerJobChainAvailable: chain.ChainEnabled,
			MerkleAvailable:      chain.MerkleEnabled,
		},
		UploadChecks: &Capabilities_UploadChecks{
			FormatSizeHash:       scan.Enabled,
			ImportPermissionScan: scan.Enabled,
			ReviewGate:           scan.RequireReview,
			MalwareAbuseScan:     false,
		},
		P2P: &Capabilities_P2P{
			Enabled:         false,
			ShareMode:       "hash-addressed-only",
			ResultsToServer: true,
		},
		Dew: &Capabilities_Dew{
			Enabled:              true,
			ReceiptWindowSeconds: pool.DefaultDewReceiptWindowSeconds,
			ReceiptAlgorithm:     "ed25519",
		},
	}, nil
}

// ---------------------------------------------------------------- helpers

// uploadScanOwnerCode reports whether the claims are owner code. It mirrors the
// rule of the REST surface: the admin and the director are owner code.
func uploadScanOwnerCode(c auth.Claims) bool {
	return c.HasRole("admin") || c.HasRole("director")
}

// retryWindow is the retry delay that the rate limiter reports.
const retryWindow = time.Minute
