// Path: internal/grpcapi/artifact_owners_T100_test.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: gRPC tests of the T100 artifact ownership (owner decision
// 2026-11-21, option A): the join upload of identical bytes by a second
// developer, the reference rule on GetArtifact and CreateJob, the
// DeleteArtifact RPC that keeps the blob for the other holder and deletes it
// with the last reference, and the admin bypass. Both pool services answer the
// same way.
package grpcapi

import (
	"context"
	"testing"

	"ccgsc/poolscore/internal/verify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// poolClient is the artifact/job surface shared by both generated pool clients.
type poolClient interface {
	UploadArtifact(ctx context.Context, in *ArtifactUploadRequest, opts ...grpc.CallOption) (*Artifact, error)
	GetArtifact(ctx context.Context, in *GetArtifactRequest, opts ...grpc.CallOption) (*ArtifactData, error)
	DeleteArtifact(ctx context.Context, in *DeleteArtifactRequest, opts ...grpc.CallOption) (*DeleteArtifactResponse, error)
	CreateJob(ctx context.Context, in *CreateJobRequest, opts ...grpc.CallOption) (*Job, error)
}

// t100Module is the module artifact fixture shared by both developers.
var t100Module = []byte("t100 shared module bytes")

// t100Upload uploads the module bytes with the token and returns the artifact id.
func t100Upload(t *testing.T, c poolClient, token string) string {
	t.Helper()
	id := verify.SHA256(t100Module)
	if _, err := c.UploadArtifact(withToken(context.Background(), token), &ArtifactUploadRequest{
		Sha256:       id,
		UploadOffset: 0,
		UploadLength: int64(len(t100Module)),
		Chunk:        t100Module,
		ContentType:  "application/octet-stream",
	}); err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	return id
}

// t100Job builds a one-task job for the module artifact.
func t100Job(taskID, artifactID string) *CreateJobRequest {
	input, err := structpb.NewStruct(map[string]any{"n": 1})
	if err != nil {
		panic(err)
	}
	return &CreateJobRequest{
		ModuleArtifactIds: []string{artifactID},
		Tasks:             []*TaskSpec{{TaskId: taskID, Input: &TaskInput{Source: &TaskInput_Json{Json: input}}}},
	}
}

func TestT100GrpcSharedArtifactLifecycle(t *testing.T) {
	h := newHarness(t)
	clients := []struct {
		name   string
		client poolClient
	}{{"thread", h.thread}, {"process", h.process}}

	for _, tc := range clients {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.client
			ctx := context.Background()
			id := verify.SHA256(t100Module)

			_, devA := h.key(t, "developer")
			_, devB := h.key(t, "developer")
			_, devC := h.key(t, "developer")
			_, admin := h.key(t, "admin")

			// The first upload creates the blob, the second joins it.
			if got := t100Upload(t, c, devA); got != id {
				t.Fatalf("first upload id = %s, want %s", got, id)
			}
			if got := t100Upload(t, c, devB); got != id {
				t.Fatalf("join upload id = %s, want the shared blob %s", got, id)
			}

			// Both holders submit jobs.
			for name, token := range map[string]string{"a": devA, "b": devB} {
				if _, err := c.CreateJob(withToken(ctx, token), t100Job("task-"+name, id)); err != nil {
					t.Fatalf("CreateJob(holder %s): %v", name, err)
				}
			}

			// The third developer without a reference is refused on use and read.
			if _, err := c.CreateJob(withToken(ctx, devC), t100Job("task-c", id)); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("CreateJob without a reference = %v, want PermissionDenied", err)
			}
			if _, err := c.GetArtifact(withToken(ctx, devC), &GetArtifactRequest{ArtifactId: id}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("GetArtifact without a reference = %v, want PermissionDenied", err)
			}

			// Holders read the shared bytes; a Store A admin bypasses the rule.
			for name, token := range map[string]string{"a": devA, "b": devB, "admin": admin} {
				data, err := c.GetArtifact(withToken(ctx, token), &GetArtifactRequest{ArtifactId: id})
				if err != nil {
					t.Fatalf("GetArtifact(%s): %v", name, err)
				}
				if string(data.GetData()) != string(t100Module) {
					t.Fatalf("GetArtifact(%s) bytes = %q, want the module bytes", name, data.GetData())
				}
			}

			// The delete of one reference keeps the blob for the other holder.
			out, err := c.DeleteArtifact(withToken(ctx, devA), &DeleteArtifactRequest{ArtifactId: id})
			if err != nil {
				t.Fatalf("DeleteArtifact(a): %v", err)
			}
			if !out.GetRemoved() || out.GetBlobDeleted() || out.GetRemainingReferences() != 1 {
				t.Fatalf("DeleteArtifact(a) = %+v, want one remaining reference", out)
			}
			if _, err := c.GetArtifact(withToken(ctx, devB), &GetArtifactRequest{ArtifactId: id}); err != nil {
				t.Fatalf("blob gone although b holds a reference: %v", err)
			}
			// The last delete removes the blob.
			out, err = c.DeleteArtifact(withToken(ctx, devB), &DeleteArtifactRequest{ArtifactId: id})
			if err != nil {
				t.Fatalf("DeleteArtifact(b): %v", err)
			}
			if !out.GetRemoved() || !out.GetBlobDeleted() || out.GetRemainingReferences() != 0 {
				t.Fatalf("DeleteArtifact(b) = %+v, want the blob deleted", out)
			}
			if _, err := c.GetArtifact(withToken(ctx, devB), &GetArtifactRequest{ArtifactId: id}); status.Code(err) != codes.NotFound {
				t.Fatalf("GetArtifact after the last delete = %v, want NotFound", err)
			}
			// A job with the deleted artifact answers NotFound like before T100.
			if _, err := c.CreateJob(withToken(ctx, devA), t100Job("task-after-delete", id)); status.Code(err) != codes.NotFound {
				t.Fatalf("CreateJob with a deleted artifact = %v, want NotFound", err)
			}
		})
	}
}

func TestT100GrpcDeleteWithoutReferenceIsRefused(t *testing.T) {
	h := newHarness(t)
	_, devA := h.key(t, "developer")
	_, devC := h.key(t, "developer")
	id := t100Upload(t, h.thread, devA)

	if out, err := h.thread.DeleteArtifact(withToken(context.Background(), devC), &DeleteArtifactRequest{ArtifactId: id}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("DeleteArtifact without a reference = %v (%+v), want PermissionDenied", err, out)
	}
	// The blob stays intact for its holder.
	if _, err := h.thread.GetArtifact(withToken(context.Background(), devA), &GetArtifactRequest{ArtifactId: id}); err != nil {
		t.Fatalf("blob not intact after a refused delete: %v", err)
	}
}
