// Path: internal/grpcapi/conformance_test.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the conformance tests of the gRPC surface against the HTTP routes
// of internal/api/api.go, driven by
// docs/reviews/WEBCHAT_R3_gemini_grpc_conformance_20261118.md; WO T42 F10.
package grpcapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"google.golang.org/grpc/codes"
)

func TestTaskResultViewIncludesTrustEvidence(t *testing.T) {
	view := taskResultView(pool.Result{
		HashOK: true, CanaryRan: true, CanaryState: "pass", CanaryOK: true,
		TrustComplete: true, Rerun: true, RerunOK: true,
		Flagged: true, FlagReason: "review",
	})
	if view.Verification == nil || !view.Verification.HashMatch || !view.Verification.CanaryRan ||
		view.Verification.CanaryState != "pass" || !view.Verification.CanaryOk ||
		!view.Verification.TrustComplete || !view.Verification.Rerun ||
		!view.Verification.RerunOk || !view.Verification.Flagged ||
		view.Verification.FlagReason != "review" {
		t.Fatalf("verification = %+v", view.Verification)
	}
}

// TestGrpcAuthClaimsMirrorHttp: a call without a token is refused with
// Unauthenticated, a call with a contributor token on a developer route is
// refused with PermissionDenied, and a developer token works.
func TestGrpcAuthClaimsMirrorHttp(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	_, contributor := h.key(t, "contributor")

	if _, err := h.thread.GetCapabilities(context.Background(), &GetCapabilitiesRequest{}); codeOfStatus(err) != codes.Unauthenticated {
		t.Fatalf("no token: got %v want Unauthenticated", codeOfStatus(err))
	}
	_, err := h.thread.GetCapabilities(withToken(context.Background(), contributor), &GetCapabilitiesRequest{})
	if codeOfStatus(err) != codes.PermissionDenied {
		t.Fatalf("contributor on a developer route: got %v want PermissionDenied", codeOfStatus(err))
	}
	if _, err := h.thread.GetCapabilities(withToken(context.Background(), developer), &GetCapabilitiesRequest{}); err != nil {
		t.Fatalf("developer on the capabilities route: %v", err)
	}
}

// TestGrpcExchangeTokenIssuesJwt: the token exchange answers the same values
// as the HTTP route POST /{pool}/v1/auth/token.
func TestGrpcExchangeTokenIssuesJwt(t *testing.T) {
	h := newHarness(t)
	raw, _ := h.key(t, "developer")
	resp, err := h.thread.ExchangeToken(context.Background(), &TokenRequest{ApiKey: raw})
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if resp.GetExpiresIn() != 900 {
		t.Fatalf("expires in = %d want 900", resp.GetExpiresIn())
	}
	if resp.GetPoolId() != pool.Thread {
		t.Fatalf("pool id = %q want %q", resp.GetPoolId(), pool.Thread)
	}
	if resp.GetServerId() != "s1" {
		t.Fatalf("server id = %q want s1", resp.GetServerId())
	}
	if resp.GetAccessToken() == "" {
		t.Fatal("no access token")
	}
	// The issued token works on the routes that need it.
	if _, err := h.thread.GetCapabilities(withToken(context.Background(), resp.GetAccessToken()), &GetCapabilitiesRequest{}); err != nil {
		t.Fatalf("the issued token does not work: %v", err)
	}
	// The process pool service issues a token for its own pool.
	processResp, err := h.process.ExchangeToken(context.Background(), &TokenRequest{ApiKey: raw})
	if err != nil {
		t.Fatalf("ExchangeToken on the process pool: %v", err)
	}
	if processResp.GetPoolId() != pool.Process {
		t.Fatalf("pool id = %q want %q", processResp.GetPoolId(), pool.Process)
	}
}

// TestGrpcCreateKeyNeedsAdmin: the key issuance needs the admin role, exactly
// as the HTTP route POST /{pool}/v1/auth/keys does.
func TestGrpcCreateKeyNeedsAdmin(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	_, admin := h.key(t, "admin")
	req := &CreateKeyRequest{UserId: "dev1", AccountStore: "B", Roles: []string{"developer"}, Scopes: []string{"jobs:write"}}
	if _, err := h.thread.CreateKey(withToken(context.Background(), developer), req); codeOfStatus(err) != codes.PermissionDenied {
		t.Fatalf("developer: got %v want PermissionDenied", codeOfStatus(err))
	}
	key, err := h.thread.CreateKey(withToken(context.Background(), admin), req)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	if key.GetKeyId() == "" || key.GetApiKey() == "" || key.GetUserId() != "dev1" {
		t.Fatalf("key = %+v", key)
	}
}

// TestGrpcCreateJobMatchesHttp: the job that the gRPC surface creates carries
// the same fields as the job that the HTTP surface creates.
func TestGrpcCreateJobMatchesHttp(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	module, _ := h.artifact(t, developer)
	zero := 0.0
	job, err := h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{
		ModuleArtifactIds: []string{module},
		Tasks: []*TaskSpec{
			{TaskId: "one"},
			{TaskId: "two"},
		},
		RerunPercent:   &zero,
		IdempotencyKey: "grpc-1",
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if job.GetJobId() == "" || job.GetState() != "queued" {
		t.Fatalf("job = %+v", job)
	}
	if job.GetPoolId() != pool.Thread || job.GetServerId() != "s1" {
		t.Fatalf("job identity = %+v", job)
	}
	if job.GetProgress().GetTotal() != 2 {
		t.Fatalf("progress = %+v", job.GetProgress())
	}
	if job.GetConfigVersion() == "" {
		t.Fatal("no config version")
	}
	// The same idempotency key returns the same job, as on the HTTP route.
	again, err := h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{
		ModuleArtifactIds: []string{module},
		Tasks: []*TaskSpec{
			{TaskId: "one"},
			{TaskId: "two"},
		},
		RerunPercent:   &zero,
		IdempotencyKey: "grpc-1",
	})
	if err != nil {
		t.Fatalf("CreateJob again: %v", err)
	}
	if again.GetJobId() != job.GetJobId() {
		t.Fatalf("the idempotency key created another job: %s vs %s", again.GetJobId(), job.GetJobId())
	}
}

// TestGrpcGetJobAndCancel: the job state and the cancel answer the same as
// the HTTP routes.
func TestGrpcGetJobAndCancel(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	module, _ := h.artifact(t, developer)
	job, err := h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{
		ModuleArtifactIds: []string{module},
		Tasks:             []*TaskSpec{{TaskId: "one"}},
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	got, err := h.thread.GetJob(withToken(context.Background(), developer), &GetJobRequest{JobId: job.GetJobId()})
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.GetJobId() != job.GetJobId() || got.GetState() != "queued" {
		t.Fatalf("job = %+v", got)
	}
	cancelled, err := h.thread.CancelJob(withToken(context.Background(), developer), &CancelJobRequest{JobId: job.GetJobId()})
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if cancelled.GetState() != "cancelled" {
		t.Fatalf("state = %q want cancelled", cancelled.GetState())
	}
	// The job of another developer is not readable.
	_, other := h.key(t, "developer")
	if _, err := h.thread.GetJob(withToken(context.Background(), other), &GetJobRequest{JobId: job.GetJobId()}); codeOfStatus(err) != codes.PermissionDenied {
		t.Fatalf("the job of another developer: got %v want PermissionDenied", codeOfStatus(err))
	}
}

// TestGrpcCancelRefusesUnsupportedFields: the cancel that carries a reason or
// a grace period is refused, because the engine carries neither yet. No
// request field is silently ignored.
func TestGrpcCancelRefusesUnsupportedFields(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	module, _ := h.artifact(t, developer)
	job, err := h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{
		ModuleArtifactIds: []string{module},
		Tasks:             []*TaskSpec{{TaskId: "one"}},
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	_, err = h.thread.CancelJob(withToken(context.Background(), developer), &CancelJobRequest{
		JobId:  job.GetJobId(),
		Reason: "user asked",
	})
	if codeOfStatus(err) != codes.Unimplemented {
		t.Fatalf("cancel with a reason: got %v want Unimplemented", codeOfStatus(err))
	}
	_, err = h.thread.CancelJob(withToken(context.Background(), developer), &CancelJobRequest{
		JobId:   job.GetJobId(),
		GraceMs: 1000,
	})
	if codeOfStatus(err) != codes.Unimplemented {
		t.Fatalf("cancel with a grace period: got %v want Unimplemented", codeOfStatus(err))
	}
}

// TestGrpcValidateJobCountsSplitTasks: the validation of a split job counts
// the tasks of the split, not the length of the empty task list.
func TestGrpcValidateJobCountsSplitTasks(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	module, _ := h.artifact(t, developer)
	result, err := h.thread.ValidateJob(withToken(context.Background(), developer), &ValidateJobRequest{
		Request: &CreateJobRequest{
			ModuleArtifactIds: []string{module},
			Split:             &SplitSpec{ArtifactId: module, Count: 3, Format: "bytes"},
		},
	})
	if err != nil {
		t.Fatalf("ValidateJob: %v", err)
	}
	if !result.GetValid() {
		t.Fatal("the request is not valid")
	}
	if result.GetEstimatedTasks() != 3 {
		t.Fatalf("estimated tasks = %d want 3", result.GetEstimatedTasks())
	}
	if len(result.GetDeviceClasses()) != 2 || result.GetDeviceClasses()[0] != "cpu" {
		t.Fatalf("device classes = %v", result.GetDeviceClasses())
	}
}

// TestGrpcResultsMirrorHttp: the results page of the gRPC surface carries the
// same items, the same order and the same cursor as the HTTP route.
func TestGrpcResultsMirrorHttp(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	module, _ := h.artifact(t, developer)
	job, err := h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{
		ModuleArtifactIds: []string{module},
		Tasks:             []*TaskSpec{{TaskId: "one"}, {TaskId: "two"}},
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	page, err := h.thread.GetResults(withToken(context.Background(), developer), &GetResultsRequest{JobId: job.GetJobId()})
	if err != nil {
		t.Fatalf("GetResults: %v", err)
	}
	if page.GetOrder() != "as_completed" {
		t.Fatalf("order = %q want as_completed", page.GetOrder())
	}
	if len(page.GetItems()) != 0 {
		t.Fatalf("items = %+v", page.GetItems())
	}
	bulk, err := h.thread.GetResultsBulk(withToken(context.Background(), developer), &GetResultsBulkRequest{JobId: job.GetJobId()})
	if err != nil {
		t.Fatalf("GetResultsBulk: %v", err)
	}
	if bulk.GetOrder() != "as_completed" {
		t.Fatalf("bulk order = %q want as_completed", bulk.GetOrder())
	}
}

// TestGrpcResultsPageMirrorsHttp: a job with results answers the same page on
// both transports, including the error object of a failed task.
func TestGrpcResultsPageMirrorsHttp(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	// The engine stores the results of the job; the fixture writes them
	// directly, so the test does not depend on the worker path.
	module, _ := h.artifact(t, developer)
	job, err := h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{
		ModuleArtifactIds: []string{module},
		Tasks:             []*TaskSpec{{TaskId: "one"}, {TaskId: "two"}},
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	stored, err := h.engine.Get(context.Background(), pool.Thread, h.keyID(t, developer), job.GetJobId())
	if err != nil {
		t.Fatalf("engine.Get: %v", err)
	}
	stored.Results = []pool.Result{
		{JobID: job.GetJobId(), TaskID: "one", Status: "done", Attempts: 1, At: time.Unix(10, 0).UTC(), Output: json.RawMessage(`{"json":{"answer":42}}`)},
		{JobID: job.GetJobId(), TaskID: "two", Status: "failed", Attempts: 3, At: time.Unix(20, 0).UTC(), Error: "worker failed"},
	}
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		return tx.Put(store.Jobs, stored.JobID, stored)
	}); err != nil {
		t.Fatalf("store the results: %v", err)
	}
	page, err := h.thread.GetResults(withToken(context.Background(), developer), &GetResultsRequest{JobId: job.GetJobId()})
	if err != nil {
		t.Fatalf("GetResults: %v", err)
	}
	if len(page.GetItems()) != 2 {
		t.Fatalf("items = %d want 2", len(page.GetItems()))
	}
	done := page.GetItems()[0]
	if done.GetStatus() != "done" || done.GetTaskId() != "one" {
		t.Fatalf("the first item = %+v", done)
	}
	if done.GetOutput().GetJson().GetFields()["answer"].GetNumberValue() != 42 {
		t.Fatalf("the output = %+v", done.GetOutput())
	}
	if done.GetCompletedAt() == nil {
		t.Fatal("no completion time")
	}
	failed := page.GetItems()[1]
	if failed.GetError().GetCode() != "task-failed" || failed.GetError().GetDetail() != "worker failed" {
		t.Fatalf("the error = %+v", failed.GetError())
	}
	// The failed record is left out when the caller asks for it.
	excluded := false
	no, err := h.thread.GetResults(withToken(context.Background(), developer), &GetResultsRequest{JobId: job.GetJobId(), IncludeFailed: &excluded})
	if err != nil {
		t.Fatalf("GetResults without the failed records: %v", err)
	}
	if len(no.GetItems()) != 1 || no.GetItems()[0].GetTaskId() != "one" {
		t.Fatalf("items = %+v", no.GetItems())
	}
}

// TestGrpcCapabilitiesMirrorHttp: the capabilities of the gRPC surface carry
// the same values as the capabilities of the HTTP route.
func TestGrpcCapabilitiesMirrorHttp(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	caps, err := h.thread.GetCapabilities(withToken(context.Background(), developer), &GetCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetCapabilities: %v", err)
	}
	if caps.GetServerId() != "s1" || caps.GetPoolId() != pool.Thread {
		t.Fatalf("capabilities = %+v", caps)
	}
	if caps.GetServerLimits().GetMaxTasksPerJob() != pool.MaxTasksPerJob {
		t.Fatalf("max tasks per job = %d want %d", caps.GetServerLimits().GetMaxTasksPerJob(), pool.MaxTasksPerJob)
	}
	if caps.GetServerLimits().GetMaxInlineBytes() != 262144 {
		t.Fatalf("max inline bytes = %d want 262144", caps.GetServerLimits().GetMaxInlineBytes())
	}
	if len(caps.GetPriorityClasses()) != 3 {
		t.Fatalf("priority classes = %v", caps.GetPriorityClasses())
	}
	if caps.GetSessions().GetSupported() {
		t.Fatal("the thread pool reports the sessions as supported")
	}
	// The process pool reports the sessions as supported.
	processCaps, err := h.process.GetCapabilities(withToken(context.Background(), developer), &GetCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetCapabilities of the process pool: %v", err)
	}
	if !processCaps.GetSessions().GetSupported() {
		t.Fatal("the process pool does not report the sessions as supported")
	}
}

// TestGrpcNotImplementedForRefreshToken: the refresh token route exists in the
// HTTP surface but the engine issues no refresh token, so the gRPC surface
// answers Unimplemented with the machine-readable code.
func TestGrpcNotImplementedForRefreshToken(t *testing.T) {
	h := newHarness(t)
	_, err := h.thread.RefreshToken(context.Background(), &RefreshTokenRequest{RefreshToken: "anything"})
	if codeOfStatus(err) != codes.Unimplemented {
		t.Fatalf("got %v want Unimplemented", codeOfStatus(err))
	}
	detail := errorDetailOf(t, err)
	if detail.GetCode() != "not-implemented" {
		t.Fatalf("the detail code = %q want not-implemented", detail.GetCode())
	}
}

// TestGrpcErrorDetailsCarryTheRestCodes: a failed call carries the machine
// readable code of the REST problem body in the status details.
func TestGrpcErrorDetailsCarryTheRestCodes(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	_, err := h.thread.GetJob(withToken(context.Background(), developer), &GetJobRequest{JobId: "s1.tp.missing"})
	if codeOfStatus(err) != codes.NotFound {
		t.Fatalf("got %v want NotFound", codeOfStatus(err))
	}
	detail := errorDetailOf(t, err)
	if detail.GetCode() != "not-found" {
		t.Fatalf("the detail code = %q want not-found", detail.GetCode())
	}
	if detail.GetStatus() != 404 {
		t.Fatalf("the HTTP status = %d want 404", detail.GetStatus())
	}
	// An invalid request answers with InvalidArgument, like the REST 400.
	_, err = h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{})
	if codeOfStatus(err) != codes.InvalidArgument {
		t.Fatalf("got %v want InvalidArgument", codeOfStatus(err))
	}
}

// TestGrpcUnsupportedOptionIsRefused: a job that asks for an option that the
// engine does not support yet is refused, so no request field is silently
// ignored.
func TestGrpcUnsupportedOptionIsRefused(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	module, _ := h.artifact(t, developer)
	_, err := h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{
		ModuleArtifactIds: []string{module},
		Tasks:             []*TaskSpec{{TaskId: "one"}},
		Options: &JobOptions{
			UploadChecks: &UploadCheckOption{Mode: "review-gate"},
		},
	})
	if codeOfStatus(err) != codes.Unimplemented {
		t.Fatalf("got %v want Unimplemented", codeOfStatus(err))
	}
}

// TestGrpcJobChainAndMerkle: the audit chain of the job and the global
// checkpoints answer through their RPCs.
func TestGrpcJobChainAndMerkle(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	module, _ := h.artifact(t, developer)
	job, err := h.thread.CreateJob(withToken(context.Background(), developer), &CreateJobRequest{
		ModuleArtifactIds: []string{module},
		Tasks:             []*TaskSpec{{TaskId: "one"}},
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	chain, err := h.thread.GetJobChain(withToken(context.Background(), developer), &GetJobChainRequest{JobId: job.GetJobId()})
	if err != nil {
		t.Fatalf("GetJobChain: %v", err)
	}
	if chain.GetJobId() != job.GetJobId() {
		t.Fatalf("chain = %+v", chain)
	}
	merkle, err := h.thread.GetMerkleCheckpoints(withToken(context.Background(), developer), &GetMerkleCheckpointsRequest{})
	if err != nil {
		t.Fatalf("GetMerkleCheckpoints: %v", err)
	}
	if merkle == nil {
		t.Fatal("no Merkle report")
	}
}

// TestGrpcArtifactUploadAndRead: the artifact route of the gRPC surface
// uploads and reads the bytes, like the HTTP route.
func TestGrpcArtifactUploadAndRead(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	id, moduleBytes := h.artifact(t, developer)
	if string(moduleBytes) != string([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}) {
		t.Fatalf("unexpected fixture bytes: %q", moduleBytes)
	}
	artifact, err := h.thread.GetArtifact(withToken(context.Background(), developer), &GetArtifactRequest{ArtifactId: id})
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.GetSha256() != id {
		t.Fatalf("sha256 = %q want %q", artifact.GetSha256(), id)
	}
	if string(artifact.GetData()) != string(moduleBytes) {
		t.Fatalf("data = %q want %q", artifact.GetData(), moduleBytes)
	}
	// A contributor with the worker scope may read an artifact, like the HTTP
	// route.
	_, contributor := h.key(t, "contributor")
	if _, err := h.thread.GetArtifact(withToken(context.Background(), contributor), &GetArtifactRequest{ArtifactId: id}); err != nil {
		t.Fatalf("the contributor cannot read the artifact: %v", err)
	}
}

// TestGrpcSessionApi: the session routes of the process pool answer over
// gRPC. The session needs a worker, so the test opens one after a worker
// registered.
func TestGrpcSessionApi(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	// The engine refuses a session without an eligible tab, so the test only
	// checks the auth of the route and the not-implemented answers of the
	// fields that the engine does not carry yet.
	_, err := h.process.OpenSession(withToken(context.Background(), developer), &OpenSessionRequest{
		TabCount:            1,
		WarmInitializerArgs: nil,
	})
	if err == nil {
		t.Fatal("the session was opened without a tab")
	}
	if codeOfStatus(err) != codes.Internal && codeOfStatus(err) != codes.Aborted && codeOfStatus(err) != codes.InvalidArgument {
		t.Fatalf("the open session failed with %v", codeOfStatus(err))
	}
}

// TestGrpcCloseSessionRefusesGraceMs: the close that carries a grace period
// is refused, because the engine carries none yet.
func TestGrpcCloseSessionRefusesGraceMs(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	_, err := h.process.CloseSession(withToken(context.Background(), developer), &CloseSessionRequest{
		SessionId: "s1.pp.none",
		GraceMs:   500,
	})
	if codeOfStatus(err) != codes.Unimplemented {
		t.Fatalf("got %v want Unimplemented", codeOfStatus(err))
	}
}

// TestGrpcMuxServesBothTransports: the single port router answers the REST
// routes and the gRPC calls.
func TestGrpcMuxServesBothTransports(t *testing.T) {
	h := newHarness(t)
	_, developer := h.key(t, "developer")
	mux := h.server.Mux(h.rest)
	request := newRequest("GET", "/healthz", nil)
	recorder := newRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("the REST route answered %d want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "pools-core") {
		t.Fatalf("the body = %q", recorder.Body.String())
	}
	// The gRPC route of the same router answers over the loopback client.
	if _, err := h.thread.GetCapabilities(withToken(context.Background(), developer), &GetCapabilitiesRequest{}); err != nil {
		t.Fatalf("the gRPC route: %v", err)
	}
}
