// Path: internal/auth/admin.go | Author: Codex for Claude Opus 5.5 | 2026-11-18
// What/why: Store A account authentication, sessions, and rate limiting; WO_FIX_C_ADMIN_AUTH.
package auth

import (
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/store"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"
)

var (
	ErrForbiddenRole   = errors.New("forbidden: store A administrative role required")
	ErrSessionNotFound = errors.New("admin session not found or expired")
	ErrUserNotFound    = errors.New("admin user not found")
)

const (
	AdminUsersBucket    = "admin_users"
	AdminSessionsBucket = "admin_sessions"
)

type AdminAccount struct {
	Username           string    `json:"username"`
	PasswordHash       string    `json:"passwordHash"`
	AccountStore       string    `json:"accountStore"` // "A" or "B"
	Roles              []string  `json:"roles"`
	MustChangePassword bool      `json:"mustChangePassword"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type AdminSession struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Roles     []string  `json:"roles"`
	CSRFToken string    `json:"csrfToken"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	limit    int
	window   time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	if limit <= 0 {
		limit = 5
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		attempts: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	times := rl.attempts[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	rl.attempts[ip] = valid
	return len(valid) < rl.limit
}

// AllowAndRecord checks and spends one unit of the quota in one critical
// section (T110). Allow followed by a later Record let N concurrent requests
// all pass Allow before any Record landed, and requests that were refused
// after Allow (quota, mint error) were never counted.
func (rl *RateLimiter) AllowAndRecord(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	times := rl.attempts[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= rl.limit {
		rl.attempts[ip] = valid
		return false
	}
	rl.attempts[ip] = append(valid, now)
	return true
}

func (rl *RateLimiter) RecordFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.attempts[ip] = append(rl.attempts[ip], time.Now())
}

// Record counts one use of a quota of the client, whether it succeeded or not.
//
// The login limiter counts failures only, because a successful login clears the
// counter. The anonymous join limiter (WO_T96) counts every issue: a volunteer tab
// that asks for a token and gets one has used the quota of its client IP.
func (rl *RateLimiter) Record(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.attempts[ip] = append(rl.attempts[ip], time.Now())
}

func (rl *RateLimiter) RecordSuccess(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.attempts, ip)
}

// InitStoreA provisions Store A users from config or generates a debug seed user on first start.
func (s *Service) InitStoreA(ctx context.Context, configUsers []config.AdminUserConfig) (bool, string, string, error) {
	var existing []store.Record
	// F5: a failed account-list read must never be interpreted as an empty
	// account database, which could create an additional seed admin account.
	if err := s.Store.View(ctx, func(r store.Reader) error {
		var err error
		existing, err = r.List(AdminUsersBucket)
		return err
	}); err != nil {
		return false, "", "", err
	}

	if len(configUsers) > 0 {
		err := s.Store.Update(ctx, func(tx store.Tx) error {
			for _, cu := range configUsers {
				roles := cu.Roles
				if len(roles) == 0 {
					roles = []string{"admin"}
				}
				acc := AdminAccount{
					Username:     cu.Username,
					PasswordHash: cu.PasswordHash,
					AccountStore: "A",
					Roles:        roles,
					CreatedAt:    s.Now(),
					UpdatedAt:    s.Now(),
				}
				if err := tx.Put(AdminUsersBucket, cu.Username, acc); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return false, "", "", err
		}
	}

	if len(existing) == 0 && len(configUsers) == 0 {
		seedPass, err := GenerateRandomPassword(16)
		if err != nil {
			return false, "", "", err
		}
		hash, err := HashPassword(seedPass)
		if err != nil {
			return false, "", "", err
		}
		seedUser := "admin"
		acc := AdminAccount{
			Username:           seedUser,
			PasswordHash:       hash,
			AccountStore:       "A",
			Roles:              []string{"admin", "director"},
			MustChangePassword: true,
			CreatedAt:          s.Now(),
			UpdatedAt:          s.Now(),
		}
		err = s.Store.Update(ctx, func(tx store.Tx) error {
			return tx.Put(AdminUsersBucket, seedUser, acc)
		})
		if err != nil {
			return false, "", "", err
		}
		return true, seedUser, seedPass, nil
	}

	return false, "", "", nil
}

// SaveAdminAccount saves or updates an account directly in AdminUsersBucket.
func (s *Service) SaveAdminAccount(ctx context.Context, acc AdminAccount) error {
	if acc.AccountStore == "" {
		acc.AccountStore = "A"
	}
	if acc.CreatedAt.IsZero() {
		acc.CreatedAt = s.Now()
	}
	acc.UpdatedAt = s.Now()
	return s.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(AdminUsersBucket, acc.Username, acc)
	})
}

// AuthenticateStoreA verifies credentials and returns the AdminAccount if authorized for Store A.
func (s *Service) AuthenticateStoreA(ctx context.Context, username, password string) (*AdminAccount, error) {
	var acc AdminAccount
	err := s.Store.View(ctx, func(r store.Reader) error {
		return r.Get(AdminUsersBucket, username, &acc)
	})
	if err != nil {
		// A database outage must not be disguised as bad credentials (which
		// could repeatedly lock legitimate users out while suppressing alerts).
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		// If not in AdminUsersBucket, check whether this principal belongs
		// to the contributor/developer account store.
		var u User
		lookupErr := s.Store.View(ctx, func(r store.Reader) error {
			if e := r.Get(store.Users, "B:"+username, &u); e == nil {
				return nil
			} else if !errors.Is(e, store.ErrNotFound) {
				return e
			}
			return r.Get(store.Users, "A:"+username, &u)
		})
		if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
			return nil, lookupErr
		}
		if u.ID != "" && (u.AccountStore == "B" || slices.Contains(u.Roles, "contributor")) {
			return nil, ErrForbiddenRole
		}
		return nil, ErrUnauthorized
	}

	if acc.AccountStore == "B" || (!slices.Contains(acc.Roles, "admin") && !slices.Contains(acc.Roles, "director")) {
		return nil, ErrForbiddenRole
	}

	if !CheckPassword(acc.PasswordHash, password) {
		return nil, ErrUnauthorized
	}

	return &acc, nil
}

// CreateSession generates a new AdminSession stored in AdminSessionsBucket.
func (s *Service) CreateSession(ctx context.Context, acc *AdminAccount, ttl time.Duration) (*AdminSession, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	sess := &AdminSession{
		ID:        ID() + ID(),
		Username:  acc.Username,
		Roles:     acc.Roles,
		CSRFToken: ID() + ID(),
		ExpiresAt: s.Now().Add(ttl),
		CreatedAt: s.Now(),
	}
	err := s.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(AdminSessionsBucket, sess.ID, sess)
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// ValidateSession verifies that a session exists and is not expired.
func (s *Service) ValidateSession(ctx context.Context, sessionID string) (*AdminSession, error) {
	if sessionID == "" {
		return nil, ErrSessionNotFound
	}
	var sess AdminSession
	err := s.Store.View(ctx, func(r store.Reader) error {
		return r.Get(AdminSessionsBucket, sessionID, &sess)
	})
	if err != nil {
		return nil, ErrSessionNotFound
	}
	if sess.ExpiresAt.Before(s.Now()) {
		return nil, ErrSessionNotFound
	}
	return &sess, nil
}

// RevokeSession expires an active session.
func (s *Service) RevokeSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return s.Store.Update(ctx, func(tx store.Tx) error {
		var sess AdminSession
		if err := tx.Get(AdminSessionsBucket, sessionID, &sess); err != nil {
			return nil
		}
		sess.ExpiresAt = s.Now().Add(-time.Hour)
		return tx.Put(AdminSessionsBucket, sessionID, sess)
	})
}

// RevokeSessionsForUser expires every active session belonging to username.
func (s *Service) RevokeSessionsForUser(ctx context.Context, username string) error {
	if username == "" {
		return nil
	}
	return s.Store.Update(ctx, func(tx store.Tx) error {
		records, err := tx.List(AdminSessionsBucket)
		if err != nil {
			return err
		}
		for _, record := range records {
			var sess AdminSession
			if err := json.Unmarshal(record.Value, &sess); err != nil || sess.Username != username || sess.ExpiresAt.Before(s.Now()) {
				continue
			}
			sess.ExpiresAt = s.Now().Add(-time.Hour)
			if err := tx.Put(AdminSessionsBucket, sess.ID, sess); err != nil {
				return err
			}
		}
		return nil
	})
}

// ChangePassword changes the password for an existing admin user and clears forced change.
func (s *Service) ChangePassword(ctx context.Context, username, oldPassword, newPassword string) error {
	var acc AdminAccount
	err := s.Store.View(ctx, func(r store.Reader) error {
		return r.Get(AdminUsersBucket, username, &acc)
	})
	if err != nil {
		return ErrUserNotFound
	}
	if !CheckPassword(acc.PasswordHash, oldPassword) {
		return ErrUnauthorized
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	acc.PasswordHash = newHash
	acc.MustChangePassword = false
	acc.UpdatedAt = s.Now()
	return s.Store.Update(ctx, func(tx store.Tx) error {
		if err := tx.Put(AdminUsersBucket, username, acc); err != nil {
			return err
		}
		records, err := tx.List(AdminSessionsBucket)
		if err != nil {
			return err
		}
		for _, record := range records {
			var sess AdminSession
			if err := json.Unmarshal(record.Value, &sess); err != nil || sess.Username != username || sess.ExpiresAt.Before(s.Now()) {
				continue
			}
			sess.ExpiresAt = s.Now().Add(-time.Hour)
			if err := tx.Put(AdminSessionsBucket, sess.ID, sess); err != nil {
				return err
			}
		}
		return nil
	})
}
