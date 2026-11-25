// Path: internal/auth/admin_test.go | Author: Codex for Claude Opus 5.5 | 2026-11-18
// What/why: tests for bcrypt/argon2 verification, store A seed/auth, and rate limiting.
package auth

import (
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPasswordHashingAndVerification(t *testing.T) {
	pass := "SecretP@ssw0rd123!"

	// Bcrypt
	bHash, err := HashPassword(pass)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if !CheckPassword(bHash, pass) {
		t.Errorf("CheckPassword failed with bcrypt hash and correct password")
	}
	if CheckPassword(bHash, "wrongpass") {
		t.Errorf("CheckPassword succeeded with bcrypt hash and wrong password")
	}

	// Argon2id
	aHash, err := HashPasswordArgon2id(pass)
	if err != nil {
		t.Fatalf("HashPasswordArgon2id failed: %v", err)
	}
	if !CheckPassword(aHash, pass) {
		t.Errorf("CheckPassword failed with argon2id hash and correct password")
	}
	if CheckPassword(aHash, "wrongpass") {
		t.Errorf("CheckPassword succeeded with argon2id hash and wrong password")
	}

	// Empty checks
	if CheckPassword("", pass) || CheckPassword(bHash, "") {
		t.Errorf("CheckPassword should return false for empty inputs")
	}
}

func TestStoreAAuthenticationAndSessions(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "auth_test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := New(db, config.Default().JWTSecret, "s1")
	ctx := context.Background()

	// 1. First start: creates seed user
	created, seedUser, seedPass, err := svc.InitStoreA(ctx, nil)
	if err != nil || !created {
		t.Fatalf("InitStoreA first start: created=%v, err=%v", created, err)
	}
	if seedUser != "admin" || seedPass == "" {
		t.Fatalf("InitStoreA returned invalid user/pass: %s / %s", seedUser, seedPass)
	}

	// 2. Second start: does NOT recreate seed user
	created2, _, _, err := svc.InitStoreA(ctx, nil)
	if err != nil || created2 {
		t.Fatalf("InitStoreA second start should not create seed: created=%v, err=%v", created2, err)
	}

	// 3. Authenticate seed user: wrong password -> ErrUnauthorized
	_, err = svc.AuthenticateStoreA(ctx, seedUser, "wrong-password")
	if err != ErrUnauthorized {
		t.Fatalf("Authenticate wrong pass: got %v want %v", err, ErrUnauthorized)
	}

	// 4. Authenticate seed user: right password -> success
	acc, err := svc.AuthenticateStoreA(ctx, seedUser, seedPass)
	if err != nil {
		t.Fatalf("Authenticate right pass failed: %v", err)
	}
	if !acc.MustChangePassword {
		t.Fatalf("Seed user should have MustChangePassword=true")
	}

	// 5. Create and validate session
	sess, err := svc.CreateSession(ctx, acc, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	validSess, err := svc.ValidateSession(ctx, sess.ID)
	if err != nil || validSess.Username != seedUser {
		t.Fatalf("ValidateSession failed: %v, sess=%+v", err, validSess)
	}

	// 6. Revoke session
	if err := svc.RevokeSession(ctx, sess.ID); err != nil {
		t.Fatalf("RevokeSession failed: %v", err)
	}
	if _, err := svc.ValidateSession(ctx, sess.ID); err != ErrSessionNotFound {
		t.Fatalf("ValidateSession after revoke: got %v want %v", err, ErrSessionNotFound)
	}

	// 7. Contributor role -> ErrForbiddenRole
	contribHash, _ := HashPassword("contribpass")
	contribAcc := AdminAccount{
		Username:     "worker1",
		PasswordHash: contribHash,
		AccountStore: "B",
		Roles:        []string{"contributor"},
	}
	if err := svc.SaveAdminAccount(ctx, contribAcc); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateStoreA(ctx, "worker1", "contribpass"); err != ErrForbiddenRole {
		t.Fatalf("Authenticate contributor: got %v want %v", err, ErrForbiddenRole)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	ip := "192.0.2.1"

	if !rl.Allow(ip) {
		t.Fatalf("Initial allow failed")
	}

	rl.RecordFailure(ip)
	if !rl.Allow(ip) {
		t.Fatalf("Allow after 1 failure failed")
	}
	rl.RecordFailure(ip)
	if !rl.Allow(ip) {
		t.Fatalf("Allow after 2 failures failed")
	}
	rl.RecordFailure(ip)
	// Now 3 failures recorded: next allow should return false
	if rl.Allow(ip) {
		t.Fatalf("Allow after 3 failures should have been blocked")
	}

	// Record success clears attempts
	rl.RecordSuccess(ip)
	if !rl.Allow(ip) {
		t.Fatalf("Allow after success should pass")
	}
}

func TestChangePasswordRevokesAllUserSessions(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "password_sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := New(db, config.Default().JWTSecret, "s1")
	ctx := context.Background()
	hash, err := HashPassword("old-password")
	if err != nil {
		t.Fatal(err)
	}
	acc := AdminAccount{Username: "admin", PasswordHash: hash, Roles: []string{"admin"}}
	if err := svc.SaveAdminAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateSession(ctx, &acc, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateSession(ctx, &acc, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangePassword(ctx, acc.Username, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.ID, second.ID} {
		if _, err := svc.ValidateSession(ctx, id); err != ErrSessionNotFound {
			t.Fatalf("ValidateSession(%q) = %v, want revoked", id, err)
		}
	}
}
