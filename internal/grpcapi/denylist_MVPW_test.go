// Path: internal/grpcapi/denylist_MVPW_test.go | Author: cmdc for Claude Opus 5.5 | 2026-11-19
// What/why: MVPW slice A -- a denied token is Unauthenticated on the gRPC path too.
// There is no gRPC interceptor doing auth: claimsFromMetadata validates per call,
// and it shares auth.Service.Validate with HTTP, which is where the deny lives.
package grpcapi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/store/sqlite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestMVPWADeniedTokenIsUnauthenticatedOnGRPC(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "grpc_deny.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := auth.New(db, config.Default().JWTSecret, "s1")
	ctx := context.Background()

	raw, _, err := svc.CreateKey(ctx, auth.User{ID: "dev", AccountStore: "B", Roles: []string{"developer"}}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.Exchange(ctx, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.Validate(ctx, tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}

	md := metadata.Pairs("authorization", "Bearer "+tok.AccessToken)
	live := metadata.NewIncomingContext(ctx, md)

	// The token authenticates before the deny.
	if _, err = claimsFromMetadata(live, svc, nil); err != nil {
		t.Fatalf("token refused before the deny: %v", err)
	}

	if _, err = svc.DenyToken(ctx, claims.ID, "leaked", "tester", time.Time{}); err != nil {
		t.Fatal(err)
	}

	_, err = claimsFromMetadata(live, svc, nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("denied token code = %v, want Unauthenticated", status.Code(err))
	}
	st, ok := status.FromError(err)
	if !ok || len(st.Details()) != 1 {
		t.Fatalf("denied token error has no detail: %v", err)
	}
	if detail, ok := st.Details()[0].(*ErrorDetail); !ok || detail.Code != "unauthorized" {
		t.Fatalf("denied token detail = %#v", st.Details()[0])
	}
}
