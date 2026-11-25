// MVPW-F, Codex, 2026-11-19: exact wire bytes and fail-closed receipt validation.
package verify

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMVPWReceiptCanonicalBytes(t *testing.T) {
	r := LeaseReceipt{ReceiptID: "r", TaskIDs: []string{"b", "a"}, InputHashes: []string{strings.Repeat("b", 64), strings.Repeat("a", 64)}, IssuedAt: 1700000000, Deadline: 1700003600, WindowSeconds: 3600, Algorithm: "ed25519", KeyID: "key"}
	b, err := r.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	want := LeaseReceiptDomain + "\x00" + `{"algorithm":"ed25519","deadline":1700003600,"inputHashes":["` + strings.Repeat("b", 64) + `","` + strings.Repeat("a", 64) + `"],"issuedAt":1700000000,"keyId":"key","receiptId":"r","taskIds":["b","a"],"windowSeconds":3600}`
	if string(b) != want {
		t.Fatalf("canonical bytes = %q, want %q", b, want)
	}
	r.Signature = []byte("ignored")
	again, _ := r.CanonicalBytes()
	if string(again) != want {
		t.Fatal("signature entered signing bytes")
	}
}

func TestMVPWReceiptRejections(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	pub := key.Public().(ed25519.PublicKey)
	id := KeyID(pub)
	now := time.Unix(1700000000, 0)
	base, err := SignLeaseReceipt(LeaseReceipt{ReceiptID: "receipt", TaskIDs: []string{"task-a", "task-b"}, InputHashes: []string{SHA256([]byte("a")), SHA256([]byte("b"))}, IssuedAt: now.Unix(), Deadline: now.Unix() + 3600, WindowSeconds: 3600}, key, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*LeaseReceipt, *Keyring)
		at     time.Time
		want   error
	}{
		{"valid", func(*LeaseReceipt, *Keyring) {}, now, nil},
		{"tampered taskIds", func(r *LeaseReceipt, _ *Keyring) { r.TaskIDs[0] = "other" }, now, ErrBadSignature},
		{"tampered inputHashes", func(r *LeaseReceipt, _ *Keyring) { r.InputHashes[0] = SHA256(nil) }, now, ErrBadSignature},
		{"expired deadline", func(*LeaseReceipt, *Keyring) {}, now.Add(time.Hour), ErrExpired},
		{"unknown keyId", func(r *LeaseReceipt, _ *Keyring) { r.KeyID = "unknown" }, now, ErrUnknownKey},
		{"revoked key", func(_ *LeaseReceipt, k *Keyring) { _ = k.Revoke(id) }, now, ErrRevokedKey},
		{"retired key", func(_ *LeaseReceipt, k *Keyring) { _ = k.Retire(id, now) }, now, ErrUnknownKey},
		{"signature", func(r *LeaseReceipt, _ *Keyring) { r.Signature[0] ^= 1 }, now, ErrBadSignature},
		{"array lengths", func(r *LeaseReceipt, _ *Keyring) { r.InputHashes = r.InputHashes[:1] }, now, ErrInvalidEnv},
		{"duplicate tasks", func(r *LeaseReceipt, _ *Keyring) { r.TaskIDs[1] = r.TaskIDs[0] }, now, ErrInvalidEnv},
		{"algorithm", func(r *LeaseReceipt, _ *Keyring) { r.Algorithm = "none" }, now, ErrInvalidEnv},
		{"future issuance", func(*LeaseReceipt, *Keyring) {}, now.Add(-3 * time.Minute), ErrNotYetValid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.TaskIDs = append([]string(nil), base.TaskIDs...)
			r.InputHashes = append([]string(nil), base.InputHashes...)
			r.Signature = append([]byte(nil), base.Signature...)
			ring := NewKeyring()
			if err := ring.Add(id, pub); err != nil {
				t.Fatal(err)
			}
			tc.change(&r, ring)
			if err := VerifyLeaseReceipt(r, ring, tc.at); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
