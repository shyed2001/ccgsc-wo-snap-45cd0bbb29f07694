// Path: internal/auth/password.go | Author: Codex for Claude Opus 5.5 | 2026-11-18
// What/why: bcrypt and argon2 password verification and generation; WO_FIX_C_ADMIN_AUTH.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// HashPassword creates a bcrypt password hash using default cost.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// HashPasswordArgon2id creates an Argon2id PHC-formatted password hash.
func HashPasswordArgon2id(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const timeCost uint32 = 1
	const memory uint32 = 64 * 1024
	const threads uint8 = 2
	const keyLen uint32 = 32

	key := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, keyLen)

	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	keyB64 := base64.RawStdEncoding.EncodeToString(key)

	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, timeCost, threads, saltB64, keyB64), nil
}

// CheckPassword verifies a plaintext password against a bcrypt or argon2 hash.
func CheckPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	if strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$") {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}
	if strings.HasPrefix(hash, "$argon2id$") || strings.HasPrefix(hash, "$argon2i$") {
		return verifyArgon2(hash, password)
	}
	return false
}

func decodeB64(s string) ([]byte, error) {
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func verifyArgon2(hash, password string) bool {
	parts := strings.Split(hash, "$")
	// Expected parts: ["", variant, version(optional), params, salt, key] or ["", variant, params, salt, key]
	if len(parts) < 5 {
		return false
	}
	variant := parts[1]
	if variant != "argon2id" && variant != "argon2i" {
		return false
	}

	idx := 2
	if strings.HasPrefix(parts[idx], "v=") {
		idx++
	}
	if len(parts) <= idx+2 {
		return false
	}

	paramsPart := parts[idx]
	saltPart := parts[idx+1]
	keyPart := parts[idx+2]

	var memory uint32 = 65536
	var timeCost uint32 = 1
	var threads uint8 = 1

	for _, param := range strings.Split(paramsPart, ",") {
		kv := strings.SplitN(param, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "m":
			if val, err := strconv.ParseUint(kv[1], 10, 32); err == nil {
				memory = uint32(val)
			}
		case "t":
			if val, err := strconv.ParseUint(kv[1], 10, 32); err == nil {
				timeCost = uint32(val)
			}
		case "p":
			if val, err := strconv.ParseUint(kv[1], 10, 8); err == nil {
				threads = uint8(val)
			}
		}
	}

	salt, err := decodeB64(saltPart)
	if err != nil || len(salt) == 0 {
		return false
	}
	expectedKey, err := decodeB64(keyPart)
	if err != nil || len(expectedKey) == 0 {
		return false
	}

	var key []byte
	if variant == "argon2id" {
		key = argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(expectedKey)))
	} else {
		key = argon2.Key([]byte(password), salt, timeCost, memory, threads, uint32(len(expectedKey)))
	}

	return subtle.ConstantTimeCompare(key, expectedKey) == 1
}

// GenerateRandomPassword produces a cryptographically secure random alphanumeric string.
func GenerateRandomPassword(length int) (string, error) {
	if length < 8 {
		length = 16
	}
	const charset = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%^&*"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("failed to read random bytes")
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b), nil
}
