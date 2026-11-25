package tests

import (
	"context"
	"testing"

	"ccgsc/poolscore/internal/auth"
)

func TestT99RevokeKeyRoute(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, admin := f.key(t, "admin")
	_, developer := f.key(t, "developer")

	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		raw, key, err := f.auth.CreateKey(ctx, auth.User{
			ID: "t99-developer-" + base, AccountStore: "B", Roles: []string{"developer"},
		}, []string{"jobs:read"})
		if err != nil {
			t.Fatal(err)
		}
		tokenBefore, err := f.auth.Exchange(ctx, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.auth.Validate(ctx, tokenBefore.AccessToken); err != nil {
			t.Fatalf("key token before revoke rejected: %v", err)
		}
		path := base + "/auth/keys/" + key.ID + ":revoke"
		f.request(t, "POST", path, "", nil, 401, nil)
		f.request(t, "POST", path, developer, nil, 403, nil)

		got := decode[map[string]any](t, f.request(t, "POST", path, admin, nil, 200, nil))
		if got["keyId"] != key.ID || got["revoked"] != true {
			t.Fatalf("unexpected revoke response: %v", got)
		}
		if _, err := f.auth.Exchange(ctx, raw, nil); err != auth.ErrUnauthorized {
			t.Fatalf("revoked key exchange: got %v, want ErrUnauthorized", err)
		}
		if _, err := f.auth.Validate(ctx, tokenBefore.AccessToken); err == nil {
			t.Fatal("existing token remains valid after key revocation")
		}

		f.request(t, "POST", base+"/auth/keys/not-a-known-key:revoke", admin, nil, 404, nil)
	}
}
