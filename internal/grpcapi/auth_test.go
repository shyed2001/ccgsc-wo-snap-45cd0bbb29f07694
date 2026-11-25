package grpcapi

import (
	"context"
	"path/filepath"
	"testing"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestClaimsFromMetadataRequiresBearerAndErrorDetail(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "grpc_auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := auth.New(db, config.Default().JWTSecret, "s1")

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "bare-token"))
	_, err = claimsFromMetadata(ctx, svc, nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("bare token code = %v, want unauthenticated", status.Code(err))
	}
	st, ok := status.FromError(err)
	if !ok || len(st.Details()) != 1 {
		t.Fatalf("bare token error has no detail: %v", err)
	}
	if detail, ok := st.Details()[0].(*ErrorDetail); !ok || detail.Code != "invalid-authorization" {
		t.Fatalf("bare token detail = %#v", st.Details()[0])
	}
}

func TestTaskResultViewIncludesTrustEvidenceS1(t *testing.T) {
	view := taskResultView(pool.Result{
		CanaryRan:     true,
		CanaryState:   "pass",
		CanaryOK:      true,
		TrustComplete: true,
		Rerun:         true,
		RerunOK:       true,
		Flagged:       true,
		FlagReason:    "unverified",
	})
	if view.Verification == nil || !view.Verification.CanaryRan || view.Verification.CanaryState != "pass" ||
		!view.Verification.CanaryOk || !view.Verification.TrustComplete || !view.Verification.Rerun ||
		!view.Verification.RerunOk || !view.Verification.Flagged || view.Verification.FlagReason != "unverified" {
		t.Fatalf("trust evidence missing from gRPC result: %#v", view.Verification)
	}
}
