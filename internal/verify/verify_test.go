// Path: internal/verify/verify_test.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: table tests and benchmarks for the verify module; WWPP-R37.1 (four tamper
// measures), R37.2 (measured overhead), R35.1 (re-run sampling).
package verify

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// stepClock is a deterministic clock that advances by a fixed step on every call.
// The measurement host has a coarse system clock (sub-millisecond operations measure
// as 0 ns), so the tests drive the clock to make the durations deterministic.
type stepClock struct {
	mu   sync.Mutex
	base time.Time
	step time.Duration
}

func newStepClock(base time.Time, step time.Duration) *stepClock {
	return &stepClock{base: base, step: step}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.base = c.base.Add(c.step)
	return c.base
}

// --- 1. Hash -----------------------------------------------------------------

func TestSHA256(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		want  string
		check error
	}{
		{name: "empty", data: []byte{}, want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "abc", data: []byte("abc"), want: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{name: "wasm header", data: []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, want: "93a44bbb96c751218e4c00d479e4c14358122a389acca16205b1e4d0dc5f9476"},
		{name: "json artifact", data: []byte(`{"a":1}`), want: "015abd7f5cc57a2dd94b7590f04ad8084273905ee33ec5cebeae62276a97f862"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SHA256(tc.data)
			if len(got) != 64 {
				t.Fatalf("digest length = %d, want 64", len(got))
			}
			if tc.want != "" && got != tc.want {
				t.Errorf("SHA256 = %s, want %s", got, tc.want)
			}
			if err := Check(tc.data, got); err != nil {
				t.Errorf("Check with own digest = %v, want nil", err)
			}
			if err := Check(tc.data, strings.Repeat("0", 64)); err == nil {
				t.Error("Check with wrong digest = nil, want error")
			}
		})
	}
}

func TestCheckTamperDetection(t *testing.T) {
	orig := []byte("original artifact bytes")
	want := SHA256(orig)
	tampered := append([]byte(nil), orig...)
	tampered[0] ^= 0xff
	tests := []struct {
		name     string
		data     []byte
		expected string
		wantErr  bool
	}{
		{name: "clean", data: orig, expected: want, wantErr: false},
		{name: "one byte flipped", data: tampered, expected: want, wantErr: true},
		{name: "empty vs expected", data: nil, expected: want, wantErr: true},
		{name: "wrong expected hex", data: orig, expected: SHA256([]byte("other")), wantErr: true},
		{name: "malformed expected", data: orig, expected: "not-a-hash", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.data, tc.expected)
			if tc.wantErr && err == nil {
				t.Fatal("Check = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Check = %v, want nil", err)
			}
		})
	}
}

func TestCheckTimedRecordsMetrics(t *testing.T) {
	m := NewMetrics()
	m.SetClock(newStepClock(time.Unix(1_700_000_000, 0), time.Millisecond).Now)
	if err := CheckTimed([]byte("x"), SHA256([]byte("x")), m); err != nil {
		t.Fatalf("CheckTimed = %v, want nil", err)
	}
	if err := CheckTimed([]byte("x"), strings.Repeat("0", 64), m); err == nil {
		t.Fatal("CheckTimed with bad digest = nil, want error")
	}
	s := m.Snapshot()
	op, ok := s.Ops[OpHash]
	if !ok {
		t.Fatalf("no %q op in stats: %+v", OpHash, s.Ops)
	}
	if op.Checks != 2 || op.Pass != 1 || op.Fail != 1 {
		t.Errorf("hash op = %+v, want 2 checks / 1 pass / 1 fail", op)
	}
	if op.TotalNS == 0 {
		t.Error("hash op recorded no duration")
	}
	if op.AvgNS() <= 0 {
		t.Errorf("AvgNS = %v, want > 0", op.AvgNS())
	}
}

// --- 2. Signed task envelope ------------------------------------------------

func newTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv, KeyID(pub)
}

func TestCanonicalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "sorted keys", in: map[string]any{"b": 1, "a": 2}, want: `{"a":2,"b":1}`},
		{name: "no spaces", in: map[string]any{"a": 1, "b": "x"}, want: `{"a":1,"b":"x"}`},
		{name: "nested sorted", in: map[string]any{"z": map[string]any{"y": 1, "x": 2}}, want: `{"z":{"x":2,"y":1}}`},
		{name: "array order kept", in: map[string]any{"a": []any{3, 1, 2}}, want: `{"a":[3,1,2]}`},
		{name: "literal utf8", in: map[string]any{"a": "é"}, want: "{\"a\":\"é\"}"},
		{name: "escapes", in: map[string]any{"a": "q\"\\\n"}, want: `{"a":"q\"\\\n"}`},
		{name: "null and bool", in: map[string]any{"a": nil, "b": true}, want: `{"a":null,"b":true}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalJSON(tc.in)
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("CanonicalJSON = %s, want %s", got, tc.want)
			}
			// Encoding is stable across calls (map iteration order must not leak).
			for i := 0; i < 20; i++ {
				again, err := CanonicalJSON(tc.in)
				if err != nil || string(again) != tc.want {
					t.Fatalf("CanonicalJSON not stable: %s / %v", again, err)
				}
			}
		})
	}
}

func TestEnvelopeSignVerify(t *testing.T) {
	pub, priv, keyID := newTestKey(t)
	ring := NewKeyring()
	if err := ring.Add(keyID, pub); err != nil {
		t.Fatalf("ring.Add: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	env := NewTaskEnvelope("job-1", "task-1", SHA256([]byte("wasm")), SHA256([]byte("input")), keyID, now, 10*time.Minute)

	signed, err := SignTaskEnvelope(env, priv, keyID)
	if err != nil {
		t.Fatalf("SignTaskEnvelope: %v", err)
	}
	if err := VerifyTaskEnvelope(signed, ring, now); err != nil {
		t.Fatalf("VerifyTaskEnvelope: %v, want nil", err)
	}
	if signed.KeyID != keyID {
		t.Errorf("KeyID = %s, want %s", signed.KeyID, keyID)
	}
	// The payload must be canonical (sorted keys, no spaces).
	if !strings.Contains(string(signed.Payload), `"artifactSha256"`) {
		t.Errorf("payload missing artifactSha256: %s", signed.Payload)
	}
	if strings.Contains(string(signed.Payload), ": ") || strings.Contains(string(signed.Payload), ", ") {
		t.Errorf("payload is not canonical (spaces found): %s", signed.Payload)
	}
	dec, err := DecodeTaskEnvelope(signed)
	if err != nil {
		t.Fatalf("DecodeTaskEnvelope: %v", err)
	}
	if dec.JobID != "job-1" || dec.TaskID != "task-1" || dec.KeyID != keyID {
		t.Errorf("decoded envelope = %+v, want job-1/task-1/%s", dec, keyID)
	}
}

func TestEnvelopeVerifyFailures(t *testing.T) {
	pub, priv, keyID := newTestKey(t)
	otherPub, _, otherKeyID := newTestKey(t)
	ring := NewKeyring()
	if err := ring.Add(keyID, pub); err != nil {
		t.Fatalf("ring.Add: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	base := NewTaskEnvelope("job-1", "task-1", SHA256([]byte("wasm")), SHA256([]byte("input")), keyID, now, 10*time.Minute)
	good, err := SignTaskEnvelope(base, priv, keyID)
	if err != nil {
		t.Fatalf("SignTaskEnvelope: %v", err)
	}

	// A second signer with the same key id must not be able to forge envelopes.
	forgedPriv := ed25519.NewKeyFromSeed(make([]byte, 32))
	forgedEnv := base
	forgedEnv.KeyID = keyID
	forged, err := SignTaskEnvelope(forgedEnv, forgedPriv, keyID)
	if err != nil {
		t.Fatalf("SignTaskEnvelope(forged): %v", err)
	}
	_ = otherPub
	_ = otherKeyID

	tests := []struct {
		name    string
		mutate  func(s SignedTaskEnvelope) SignedTaskEnvelope
		ring    *Keyring
		now     time.Time
		wantErr error
	}{
		{name: "valid", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope { return s }, ring: ring, now: now, wantErr: nil},
		{name: "wrong signature", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope {
			s.Signature = append([]byte(nil), s.Signature...)
			s.Signature[0] ^= 0xff
			return s
		}, ring: ring, now: now, wantErr: ErrBadSignature},
		{name: "forged by other key", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope { return forged }, ring: ring, now: now, wantErr: ErrBadSignature},
		{name: "tampered payload", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope {
			var m map[string]any
			_ = json.Unmarshal(s.Payload, &m)
			m["taskId"] = "task-evil"
			s.Payload, _ = json.Marshal(m)
			return s
		}, ring: ring, now: now, wantErr: ErrBadSignature},
		{name: "unknown key id", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope {
			s.KeyID = "ffffffffffffffff"
			return s
		}, ring: ring, now: now, wantErr: ErrUnknownKey},
		{name: "empty payload", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope {
			s.Payload = nil
			return s
		}, ring: ring, now: now, wantErr: ErrInvalidEnv},
		{name: "empty signature", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope {
			s.Signature = nil
			return s
		}, ring: ring, now: now, wantErr: ErrInvalidEnv},
		{name: "bad algorithm", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope {
			s.Algorithm = "rsa"
			return s
		}, ring: ring, now: now, wantErr: ErrInvalidEnv},
		{name: "expired", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope { return s }, ring: ring, now: now.Add(11 * time.Minute), wantErr: ErrExpired},
		{name: "issued in the future", mutate: func(s SignedTaskEnvelope) SignedTaskEnvelope { return s }, ring: ring, now: now.Add(-10 * time.Minute), wantErr: ErrNotYetValid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.mutate(good)
			err := VerifyTaskEnvelope(s, tc.ring, tc.now)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("VerifyTaskEnvelope = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("VerifyTaskEnvelope = nil, want error")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("VerifyTaskEnvelope = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestKeyringRotationAndRevocation(t *testing.T) {
	pub1, _, key1 := newTestKey(t)
	pub2, _, key2 := newTestKey(t)
	ring := NewKeyring()
	if err := ring.Add(key1, pub1); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if ring.Len() != 1 {
		t.Errorf("Len = %d, want 1", ring.Len())
	}
	// Rotation: the successor key is published under its own id, the old key stays valid
	// during the overlap window.
	if err := ring.Add(key2, pub2); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if ring.Len() != 2 {
		t.Errorf("Len after rotation = %d, want 2", ring.Len())
	}
	if _, err := ring.PublicKey(key1); err != nil {
		t.Errorf("old key invalid during overlap: %v", err)
	}
	// Emergency revocation overrides the overlap.
	if err := ring.Revoke(key1); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := ring.PublicKey(key1); !errors.Is(err, ErrRevokedKey) {
		t.Errorf("PublicKey after revoke = %v, want ErrRevokedKey", err)
	}
	// Retirement: the key is valid until the overlap deadline and gone afterwards.
	if err := ring.Retire(key2, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if _, err := ring.PublicKey(key2); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("PublicKey after retire = %v, want ErrUnknownKey", err)
	}
	ids := ring.KeyIDs()
	if len(ids) != 2 || ids[0] > ids[1] {
		t.Errorf("KeyIDs = %v, want sorted pair", ids)
	}
}

func TestEnvelopeRotateKeyID(t *testing.T) {
	pub1, priv1, key1 := newTestKey(t)
	pub2, priv2, key2 := newTestKey(t)
	ring := NewKeyring()
	if err := ring.Add(key1, pub1); err != nil {
		t.Fatalf("Add: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	env := NewTaskEnvelope("job-2", "task-2", SHA256([]byte("module")), SHA256([]byte("input")), key1, now, 10*time.Minute)
	s1, err := SignTaskEnvelope(env, priv1, key1)
	if err != nil {
		t.Fatalf("SignTaskEnvelope: %v", err)
	}
	if err := VerifyTaskEnvelope(s1, ring, now); err != nil {
		t.Fatalf("verify with old key: %v", err)
	}
	// Rotate: publish the new key, sign with it, the old envelope still verifies.
	if err := ring.Add(key2, pub2); err != nil {
		t.Fatalf("Add: %v", err)
	}
	env2 := env
	env2.KeyID = key2
	s2, err := SignTaskEnvelope(env2, priv2, key2)
	if err != nil {
		t.Fatalf("SignTaskEnvelope(new): %v", err)
	}
	if err := VerifyTaskEnvelope(s2, ring, now); err != nil {
		t.Fatalf("verify with new key: %v", err)
	}
	if err := VerifyTaskEnvelope(s1, ring, now); err != nil {
		t.Fatalf("verify old envelope after rotation: %v", err)
	}
	if s1.KeyID == s2.KeyID {
		t.Error("rotated envelope kept the old key id")
	}
}

// --- 3. Re-run sampling -----------------------------------------------------

func TestShouldRerunDeterministic(t *testing.T) {
	tests := []struct {
		name    string
		taskID  string
		percent float64
		want    bool
	}{
		{name: "zero percent never reruns", taskID: "task-1", percent: 0, want: false},
		{name: "negative percent never reruns", taskID: "task-1", percent: -5, want: false},
		{name: "hundred percent always reruns", taskID: "task-1", percent: 100, want: true},
		{name: "over hundred percent always reruns", taskID: "task-1", percent: 250, want: true},
		{name: "one percent is stable", taskID: "task-1", percent: 1, want: ShouldRerun("task-1", 1)},
		{name: "empty task id is stable", taskID: "", percent: 50, want: ShouldRerun("", 50)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldRerun(tc.taskID, tc.percent)
			if got != tc.want {
				t.Errorf("ShouldRerun(%q, %v) = %v, want %v", tc.taskID, tc.percent, got, tc.want)
			}
			// Deterministic: the same input always yields the same decision.
			for i := 0; i < 50; i++ {
				if ShouldRerun(tc.taskID, tc.percent) != got {
					t.Fatal("ShouldRerun is not deterministic")
				}
			}
		})
	}
}

func TestShouldRerunDistribution(t *testing.T) {
	// A 10 % rate must select roughly 10 % of the task ids, never all or none.
	const n = 20000
	hits := 0
	for i := 0; i < n; i++ {
		if ShouldRerun("task-"+itoa(i), 10) {
			hits++
		}
	}
	ratio := float64(hits) / float64(n)
	if ratio < 0.07 || ratio > 0.13 {
		t.Errorf("10 %% sampling selected %.4f, want 0.07..0.13", ratio)
	}
	// 100 % selects everything, 0 % selects nothing.
	for i := 0; i < 100; i++ {
		if !ShouldRerun("task-"+itoa(i), 100) {
			t.Fatal("100 % sampling missed a task")
		}
		if ShouldRerun("task-"+itoa(i), 0) {
			t.Fatal("0 % sampling selected a task")
		}
	}
}

func TestShouldRerunMonotonic(t *testing.T) {
	// Raising the rate can only add tasks, never remove one.
	for i := 0; i < 500; i++ {
		id := "t" + itoa(i)
		prev := false
		for p := 0.0; p <= 100; p += 5 {
			got := ShouldRerun(id, p)
			if got && !prev {
				// allowed: a task enters the sample once
			}
			if prev && !got {
				t.Fatalf("ShouldRerun(%q, %v) dropped after being selected", id, p)
			}
			prev = got
		}
	}
}

func TestCompareResults(t *testing.T) {
	tests := []struct {
		name string
		a    []byte
		b    []byte
		want bool
	}{
		{name: "identical", a: []byte("same"), b: []byte("same"), want: true},
		{name: "different", a: []byte("a"), b: []byte("b"), want: false},
		{name: "one byte different", a: []byte("same"), b: []byte("samf"), want: false},
		{name: "empty vs empty", a: nil, b: []byte{}, want: true},
		{name: "empty vs value", a: nil, b: []byte("x"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompareResults(tc.a, tc.b); got != tc.want {
				t.Errorf("CompareResults = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompareResultHashes(t *testing.T) {
	h := SHA256([]byte("x"))
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "same", a: h, b: h, want: true},
		{name: "different", a: h, b: SHA256([]byte("y")), want: false},
		{name: "malformed", a: "zz", b: "zz", want: false},
		{name: "length differs", a: h, b: h[:10], want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompareResultHashes(tc.a, tc.b); got != tc.want {
				t.Errorf("CompareResultHashes = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- 4. Canary tasks --------------------------------------------------------

func TestCanaryRegisterAndCheck(t *testing.T) {
	reg := NewCanaryRegistry(NewMetrics())
	answer := []byte("the known answer")
	input := []byte("the canary input")
	if _, err := reg.RegisterWithAnswer("canary-1", "task-c1", input, answer); err != nil {
		t.Fatalf("RegisterWithAnswer: %v", err)
	}
	if reg.Len() != 1 {
		t.Errorf("Len = %d, want 1", reg.Len())
	}
	tests := []struct {
		name   string
		id     string
		answer []byte
		want   bool
	}{
		{name: "correct answer", id: "canary-1", answer: answer, want: true},
		{name: "wrong answer", id: "canary-1", answer: []byte("wrong"), want: false},
		{name: "empty answer", id: "canary-1", answer: nil, want: false},
		{name: "unknown canary", id: "canary-x", answer: answer, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := reg.CheckCanary(tc.id, tc.answer)
			if res.Pass != tc.want {
				t.Errorf("CheckCanary pass = %v, want %v (reason %q)", res.Pass, tc.want, res.Reason)
			}
			if !tc.want && res.Reason == "" {
				t.Error("failing canary has no reason")
			}
		})
	}
}

func TestCanaryRotateFixture(t *testing.T) {
	reg := NewCanaryRegistry(nil)
	if _, err := reg.RegisterWithAnswer("c", "t", []byte("in"), []byte("old answer")); err != nil {
		t.Fatalf("RegisterWithAnswer: %v", err)
	}
	if reg.CheckCanary("c", []byte("old answer")).Pass != true {
		t.Fatal("canary failed before rotation")
	}
	c, err := reg.Rotate("c", []byte("new answer"))
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if c.Rotations != 1 {
		t.Errorf("Rotations = %d, want 1", c.Rotations)
	}
	if reg.CheckCanary("c", []byte("old answer")).Pass != false {
		t.Error("old answer still accepted after rotation")
	}
	if reg.CheckCanary("c", []byte("new answer")).Pass != true {
		t.Error("new answer rejected after rotation")
	}
	if _, err := reg.Rotate("nope", []byte("x")); !errors.Is(err, ErrUnknownCanary) {
		t.Errorf("Rotate(unknown) = %v, want ErrUnknownCanary", err)
	}
}

func TestCanaryTaskHidesExpectedAnswer(t *testing.T) {
	reg := NewCanaryRegistry(nil)
	answer := []byte("secret answer")
	if _, err := reg.RegisterWithAnswer("c", "task-c", []byte(`{"x":1}`), answer); err != nil {
		t.Fatalf("RegisterWithAnswer: %v", err)
	}
	body, err := reg.CanaryTask("c")
	if err != nil {
		t.Fatalf("CanaryTask: %v", err)
	}
	text := string(body)
	if strings.Contains(text, SHA256(answer)) {
		t.Error("canary body leaks the expected hash")
	}
	if strings.Contains(text, "canary") {
		t.Error("canary body carries a canary marker")
	}
}

func TestCanaryMetrics(t *testing.T) {
	m := NewMetrics()
	m.SetClock(newStepClock(time.Unix(1_700_000_000, 0), time.Millisecond).Now)
	reg := NewCanaryRegistry(m)
	if _, err := reg.RegisterWithAnswer("c", "t", nil, []byte("a")); err != nil {
		t.Fatalf("RegisterWithAnswer: %v", err)
	}
	reg.CheckCanary("c", []byte("a"))
	reg.CheckCanary("c", []byte("b"))
	op := m.Snapshot().Ops[OpCanary]
	if op.Checks != 2 || op.Pass != 1 || op.Fail != 1 {
		t.Errorf("canary op = %+v, want 2 checks / 1 pass / 1 fail", op)
	}
}

// --- 5. Reputation ----------------------------------------------------------

func TestReputationThresholds(t *testing.T) {
	tests := []struct {
		name       string
		events     []EventKind
		wantScore  float64
		wantStatus Status
		wantBanned bool
	}{
		{name: "one pass", events: []EventKind{EventPass}, wantScore: 1.0, wantStatus: StatusGood, wantBanned: false},
		{name: "one fail", events: []EventKind{EventFail}, wantScore: 0.9, wantStatus: StatusGood, wantBanned: false},
		{name: "two fails still good", events: []EventKind{EventFail, EventFail}, wantScore: 0.8, wantStatus: StatusGood, wantBanned: false},
		{name: "three fails warn", events: []EventKind{EventFail, EventFail, EventFail}, wantScore: 0.7, wantStatus: StatusWarn, wantBanned: false},
		{name: "five fails warn", events: []EventKind{EventFail, EventFail, EventFail, EventFail, EventFail}, wantScore: 0.5, wantStatus: StatusWarn, wantBanned: false},
		{name: "six fails bar", events: []EventKind{EventFail, EventFail, EventFail, EventFail, EventFail, EventFail}, wantScore: 0.4, wantStatus: StatusBar, wantBanned: false},
		{name: "one timeout", events: []EventKind{EventTimeout}, wantScore: 0.95, wantStatus: StatusGood, wantBanned: false},
		{name: "one canary fail warns", events: []EventKind{EventCanaryFail}, wantScore: 0.75, wantStatus: StatusWarn, wantBanned: false},
		{name: "three canary fails ban", events: []EventKind{EventCanaryFail, EventCanaryFail, EventCanaryFail}, wantScore: 0.25, wantStatus: StatusBan, wantBanned: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh tracker per case: the score must not carry over between cases.
			rep := NewReputationWithConfig(DefaultReputationConfig(), nil)
			for _, e := range tc.events {
				if _, err := rep.Observe("worker-1", e, "task-1", ""); err != nil {
					t.Fatalf("Observe(%s): %v", e, err)
				}
			}
			got, err := rep.Worker("worker-1")
			if err != nil {
				t.Fatalf("Worker: %v", err)
			}
			if abs(got.Score-tc.wantScore) > 1e-9 {
				t.Errorf("score = %v, want %v", got.Score, tc.wantScore)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %v, want %v", got.Status, tc.wantStatus)
			}
			if got.Banned != tc.wantBanned {
				t.Errorf("banned = %v, want %v", got.Banned, tc.wantBanned)
			}
			if got.TotalEvents != len(tc.events) {
				t.Errorf("total events = %d, want %d", got.TotalEvents, len(tc.events))
			}
		})
	}
}

func TestReputationBanBlocksFurtherEvents(t *testing.T) {
	rep := NewReputation(nil)
	for i := 0; i < 3; i++ {
		if _, err := rep.Observe("w", EventCanaryFail, "", ""); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	if _, err := rep.Observe("w", EventPass, "", ""); !errors.Is(err, ErrBannedWorker) {
		t.Errorf("Observe on banned worker = %v, want ErrBannedWorker", err)
	}
	if rep.Eligible("w") {
		t.Error("banned worker is still eligible")
	}
}

func TestReputationUnban(t *testing.T) {
	rep := NewReputation(nil)
	for i := 0; i < 3; i++ {
		_, _ = rep.Observe("w", EventCanaryFail, "", "")
	}
	w, err := rep.Unban("w")
	if err != nil {
		t.Fatalf("Unban: %v", err)
	}
	if w.Banned {
		t.Error("worker still banned after admin review")
	}
	if w.Status == StatusBan {
		t.Errorf("status = %v, want not ban", w.Status)
	}
}

func TestReputationSnapshotRestore(t *testing.T) {
	rep := NewReputation(nil)
	_, _ = rep.Observe("w1", EventFail, "t1", "d1")
	_, _ = rep.Observe("w2", EventCanaryFail, "t2", "d2")
	snap := rep.Snapshot()
	if len(snap.Workers) != 2 {
		t.Fatalf("snapshot workers = %d, want 2", len(snap.Workers))
	}
	restored := NewReputation(nil)
	restored.Restore(snap)
	if restored.Len() != 2 {
		t.Fatalf("restored workers = %d, want 2", restored.Len())
	}
	s1, err := rep.Worker("w1")
	if err != nil {
		t.Fatalf("Worker: %v", err)
	}
	s2, err := restored.Worker("w1")
	if err != nil {
		t.Fatalf("restored Worker: %v", err)
	}
	if abs(s1.Score-s2.Score) > 1e-9 || s1.Status != s2.Status {
		t.Errorf("restored state differs: %+v vs %+v", s1, s2)
	}
	if len(s2.Evidence) != 1 {
		t.Errorf("restored evidence = %d, want 1", len(s2.Evidence))
	}
}

func TestReputationConfigurableThresholds(t *testing.T) {
	cfg := DefaultReputationConfig()
	cfg.WarnBelow = 0.95
	cfg.BarBelow = 0.9
	rep := NewReputationWithConfig(cfg, nil)
	if _, err := rep.Observe("w", EventFail, "", ""); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	got, _ := rep.Worker("w")
	if got.Status != StatusWarn {
		t.Errorf("status = %v, want warn with configured threshold", got.Status)
	}
	if got.Score != 0.9 {
		t.Errorf("score = %v, want 0.9", got.Score)
	}
}

func TestReputationUnknownWorker(t *testing.T) {
	rep := NewReputation(nil)
	if _, err := rep.Score("nope"); !errors.Is(err, ErrUnknownWorker) {
		t.Errorf("Score(unknown) = %v, want ErrUnknownWorker", err)
	}
	if _, err := rep.StatusOf("nope"); !errors.Is(err, ErrUnknownWorker) {
		t.Errorf("StatusOf(unknown) = %v, want ErrUnknownWorker", err)
	}
}

// --- 6. Metrics -------------------------------------------------------------

func TestStatsCounters(t *testing.T) {
	ResetStats()
	defer ResetStats()
	DefaultMetrics().SetClock(newStepClock(time.Unix(1_700_000_000, 0), time.Millisecond).Now)
	if err := CheckTimed([]byte("a"), SHA256([]byte("a")), nil); err != nil {
		t.Fatalf("CheckTimed: %v", err)
	}
	if err := CheckTimed([]byte("a"), strings.Repeat("0", 64), nil); err == nil {
		t.Fatal("CheckTimed = nil, want error")
	}
	s := Stats()
	if s.TotalChecks != 2 || s.TotalPass != 1 || s.TotalFail != 1 {
		t.Errorf("stats = %+v, want 2 checks / 1 pass / 1 fail", s)
	}
	if s.TotalNS == 0 {
		t.Error("stats recorded no duration")
	}
	if _, ok := s.Ops[OpHash]; !ok {
		t.Errorf("stats has no %q op", OpHash)
	}
}

func TestStatsOpStatsAvg(t *testing.T) {
	m := NewMetrics()
	m.Record("x", 10*time.Millisecond, true)
	m.Record("x", 30*time.Millisecond, false)
	op := m.Snapshot().Ops["x"]
	if op.AvgNS() != 20*1e6 {
		t.Errorf("AvgNS = %v, want 2e7", op.AvgNS())
	}
	if op.MaxNS != 30*1e6 {
		t.Errorf("MaxNS = %v, want 3e7", op.MaxNS)
	}
	if op.LastNS != 30*1e6 {
		t.Errorf("LastNS = %v, want 3e7", op.LastNS)
	}
	if op.MinNS != 10*1e6 {
		t.Errorf("MinNS = %v, want 1e7", op.MinNS)
	}
	if (OpStats{}).AvgNS() != 0 {
		t.Error("AvgNS of empty stats != 0")
	}
}

func TestMetricsConcurrent(t *testing.T) {
	m := NewMetrics()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				m.Record("c", time.Millisecond, true)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if got := m.Snapshot().Ops["c"].Checks; got != 800 {
		t.Errorf("checks = %d, want 800", got)
	}
}

// --- 7. Verifier facade -----------------------------------------------------

func TestVerifierFullPath(t *testing.T) {
	pub, priv, keyID := newTestKey(t)
	v := NewVerifier(NewMetrics())
	if err := v.Keyring().Add(keyID, pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	clock := newStepClock(time.Unix(1_700_000_000, 0), time.Millisecond)
	v.SetClock(clock.Now)

	artifact := []byte("artifact bytes")
	env := NewTaskEnvelope("job-1", "task-1", SHA256(artifact), SHA256([]byte("in")), keyID, clock.base, 10*time.Minute)
	signed, err := v.SignEnvelope(env, priv, keyID)
	if err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	if _, err := v.Canaries().RegisterWithAnswer("c1", "task-c", []byte("in"), []byte("known")); err != nil {
		t.Fatalf("RegisterWithAnswer: %v", err)
	}

	res := v.Verify(context.Background(), VerifyRequest{
		TaskID:       "task-1",
		WorkerID:     "worker-1",
		Artifact:     artifact,
		ExpectedSHA:  SHA256(artifact),
		RerunPercent: 100,
		RerunKey:     []byte("test sampling secret"),
		Envelope:     signed,
		CanaryID:     "c1",
		CanaryAnswer: []byte("known"),
	})
	if res.Verdict != VerdictAccepted {
		t.Errorf("verdict = %q, want accepted (reason %q)", res.Verdict, res.Reason)
	}
	if !res.HashOK || !res.EnvelopeOK || !res.CanaryOK {
		t.Errorf("result = %+v, want all checks ok", res)
	}
	if !res.Rerun {
		t.Error("100 %% re-run did not select the task")
	}
	if res.DurationNS <= 0 {
		t.Error("duration not recorded")
	}
}

func TestVerifierRejectsTamperedArtifact(t *testing.T) {
	v := NewVerifier(nil)
	res := v.Verify(context.Background(), VerifyRequest{
		TaskID:      "task-1",
		Artifact:    []byte("tampered"),
		ExpectedSHA: SHA256([]byte("clean")),
	})
	if res.Verdict != VerdictRejected {
		t.Errorf("verdict = %q, want rejected", res.Verdict)
	}
	if res.HashOK {
		t.Error("hash check passed a tampered artifact")
	}
}

func TestVerifierDoesNotAcceptMissingExpectedHash(t *testing.T) {
	v := NewVerifier(nil)
	cfg := v.Config()
	cfg.EnvelopeEnabled = false
	v.SetConfig(cfg)
	res := v.Verify(context.Background(), VerifyRequest{Artifact: []byte("artifact")})
	if res.Verdict != VerdictUnverified {
		t.Fatalf("missing expected hash verdict = %q, want %q", res.Verdict, VerdictUnverified)
	}
	if res.HashOK {
		t.Fatal("missing expected hash reported HashOK")
	}
}

func TestVerifierRejectsBadEnvelope(t *testing.T) {
	pub, _, keyID := newTestKey(t)
	v := NewVerifier(nil)
	if err := v.Keyring().Add(keyID, pub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	v.SetClock(func() time.Time { return now })
	signed, err := SignTaskEnvelope(NewTaskEnvelope("j", "t", SHA256([]byte("module")), SHA256([]byte("input")), keyID, now, time.Minute), mustPriv(t), keyID)
	if err != nil {
		t.Fatalf("SignTaskEnvelope: %v", err)
	}

	signed.Signature[0] ^= 0xff
	res := v.Verify(context.Background(), VerifyRequest{TaskID: "t", Envelope: signed})
	if res.Verdict != VerdictRejected {
		t.Errorf("verdict = %q, want rejected", res.Verdict)
	}
	if res.EnvelopeOK {
		t.Error("envelope check passed a broken signature")
	}
}

func TestVerifyTaskEnvelopeRejectsMissingRequiredFields(t *testing.T) {
	pub, priv, keyID := newTestKey(t)
	ring := NewKeyring()
	if err := ring.Add(keyID, pub); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	_, err := SignTaskEnvelope(TaskEnvelope{
		Version: EnvelopeVersion, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), KeyID: keyID,
	}, priv, keyID)
	if err == nil {
		t.Fatal("SignTaskEnvelope accepted an invalid envelope")
	}
}

func TestNewTaskEnvelopeHasAtLeastOneSecondTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	env := NewTaskEnvelope("job", "task", SHA256(nil), SHA256(nil), "key", now, time.Millisecond)
	if env.ExpiresAt-env.IssuedAt < 1 {
		t.Fatalf("envelope ttl = %d seconds, want at least one", env.ExpiresAt-env.IssuedAt)
	}
}

func TestVerifierConfigSwitches(t *testing.T) {
	v := NewVerifier(nil)
	cfg := v.Config()
	cfg.HashCheckEnabled = false
	v.SetConfig(cfg)
	if err := v.CheckArtifact([]byte("tampered"), SHA256([]byte("clean"))); err != nil {
		t.Errorf("hash check ran while disabled: %v", err)
	}

	cfg.HashCheckEnabled = true
	v.SetConfig(cfg)
	if err := v.CheckArtifact([]byte("tampered"), SHA256([]byte("clean"))); err == nil {
		t.Error("hash check did not run while enabled")
	}
}

func TestVerifierRejectsBarredReputation(t *testing.T) {
	v := NewVerifier(nil)
	cfg := v.Config()
	cfg.EnvelopeEnabled = false
	v.SetConfig(cfg)
	for i := 0; i < 6; i++ {
		if _, err := v.Reputation().Observe("barred", EventFail, "", "test"); err != nil {
			t.Fatal(err)
		}
	}
	res := v.Verify(context.Background(), VerifyRequest{WorkerID: "barred"})
	if res.Reputation != StatusBar || res.Verdict != VerdictRejected {
		t.Fatalf("result = %+v, want barred/rejected", res)
	}
}

func TestVerifierRerunCeiling(t *testing.T) {
	v := NewVerifier(nil)
	cfg := v.Config()
	// The measure switched off by the admin never runs (WWPP-R37.1b).
	cfg.RerunEnabled = false
	v.SetConfig(cfg)
	if v.ShouldRerun("task-1", 100) {
		t.Error("re-run ran although the measure is switched off")
	}
	// The admin ceiling caps the developer requested rate.
	cfg.RerunEnabled = true
	cfg.RerunMaxPercent = 0
	v.SetConfig(cfg)
	if v.ShouldRerun("task-1", 100) {
		t.Error("re-run ran although the admin ceiling is 0")
	}
	cfg.RerunMaxPercent = 50
	v.SetConfig(cfg)
	// 50 % ceiling, 100 % requested: the effective rate is 50 %.
	if v.Config().EffectiveRerunPercent(100) != 50 {
		t.Errorf("effective rate = %v, want 50", v.Config().EffectiveRerunPercent(100))
	}
	if !v.ShouldRerun("task-1", 100) {
		t.Error("re-run did not run at a 50 %% rate")
	}
}

func TestVerifierCanaryFailureLowersReputation(t *testing.T) {
	v := NewVerifier(nil)
	if _, err := v.Canaries().RegisterWithAnswer("c", "t", nil, []byte("right")); err != nil {
		t.Fatalf("RegisterWithAnswer: %v", err)
	}
	if _, err := v.CheckCanary("worker-1", "c", []byte("wrong")); err != nil {
		t.Fatalf("CheckCanary: %v", err)
	}
	w, err := v.Reputation().Worker("worker-1")
	if err != nil {
		t.Fatalf("Worker: %v", err)
	}
	if w.CanaryFails != 1 {
		t.Errorf("canary fails = %d, want 1", w.CanaryFails)
	}
	if w.Score >= 1 {
		t.Errorf("score = %v, want below 1 after a canary failure", w.Score)
	}
}

func TestShouldRerunWithKeyIsStableAndNotPubliclyPredictable(t *testing.T) {
	key := []byte("server-secret-that-is-not-public")
	different := false
	for i := 0; i < 32; i++ {
		taskID := fmt.Sprintf("job-1:task-%d", i)
		first := ShouldRerunWithKey(key, taskID, 50)
		if first != ShouldRerunWithKey(key, taskID, 50) {
			t.Fatal("keyed re-run decision is not stable")
		}

		if first != ShouldRerun(taskID, 50) {
			different = true
		}
	}
	if !different {
		t.Fatal("keyed re-run decisions never differed from the public hash decisions")
	}
}

func TestInconclusiveReputationEventIsNeutral(t *testing.T) {
	rep := NewReputation(nil)
	before, err := rep.Observe("worker-1", EventPass, "task-1", "")
	if err != nil {
		t.Fatal(err)
	}
	after, err := rep.Observe("worker-1", EventInconclusive, "task-1", "rerun unavailable")
	if err != nil {
		t.Fatal(err)
	}
	if after.Score != before.Score || after.Fails != before.Fails || after.Passes != before.Passes {
		t.Fatalf("inconclusive event changed reputation: before=%+v after=%+v", before, after)
	}
	if after.TotalEvents != before.TotalEvents+1 {
		t.Fatalf("inconclusive event was not recorded: before=%+v after=%+v", before, after)
	}
}

func TestLegacyInterfacesStillWork(t *testing.T) {
	// The exported API that other packages use must keep working.
	var _ Sampler = HashSampler{}
	var _ Sampler = NewRerunSampler(nil)
	var _ CanaryInjector = NewCanaryRegistry(nil)
	var _ Reputation = NewReputationService(nil)

	signer, err := NewSigner()
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	env, err := signer.Sign(map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !VerifyEnvelope(env, signer.Public) {
		t.Error("VerifyEnvelope failed on a self-signed envelope")
	}
	other, _ := NewSigner()
	if VerifyEnvelope(env, other.Public) {
		t.Error("VerifyEnvelope accepted a foreign public key")
	}
	if err := Check([]byte("x"), SHA256([]byte("x"))); err != nil {
		t.Errorf("Check = %v, want nil", err)
	}
	if !CompareResults([]byte("x"), []byte("x")) {
		t.Error("CompareResults failed on equal bytes")
	}

	// The legacy Reputation seam works through the adapter.
	svc := NewReputationService(NewReputation(nil))
	if err := svc.Observe(context.Background(), "w1", true); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	score, err := svc.Score(context.Background(), "w1")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score != 1 {
		t.Errorf("score = %v, want 1 after a pass", score)
	}
}

// --- benchmarks -------------------------------------------------------------

func BenchmarkSha256_1MB(b *testing.B) {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SHA256(data)
	}
}

func BenchmarkEd25519Verify(b *testing.B) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	msg := []byte("ccgsc.pools.task-envelope.v1 payload for the benchmark")
	sig := ed25519.Sign(priv, msg)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !ed25519.Verify(pub, msg, sig) {
			b.Fatal("verify failed")
		}
	}
}

func BenchmarkEd25519Sign(b *testing.B) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	msg := []byte("ccgsc.pools.task-envelope.v1 payload for the benchmark")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ed25519.Sign(priv, msg)
	}
}

func BenchmarkCanonicalJSON(b *testing.B) {
	env := TaskEnvelope{
		Version: 1, JobID: "job-1", TaskID: "task-1",
		ArtifactSHA256: SHA256([]byte("a")), InputSHA256: SHA256([]byte("b")),
		IssuedAt: 1_700_000_000, ExpiresAt: 1_700_000_900, KeyID: "abcdef0123456789",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := CanonicalJSON(env); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkShouldRerun(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ShouldRerun("task-1234", 1)
	}
}

func BenchmarkCheckArtifact(b *testing.B) {
	data := make([]byte, 4096)
	want := SHA256(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Check(data, want); err != nil {
			b.Fatal(err)
		}
	}
}

// --- helpers ----------------------------------------------------------------

func mustPriv(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return priv
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// Review-01 regression coverage, Codex, 2026-11-17.
func TestReviewMissingEnvelopeFailsClosed(t *testing.T) {
	for _, payload := range []json.RawMessage{nil, {}, []byte(`null`)} {
		v := NewVerifier(nil)
		res := v.Verify(context.Background(), VerifyRequest{Envelope: SignedTaskEnvelope{Payload: payload}})
		if res.Verdict != VerdictRejected || res.EnvelopeOK || res.Reason == "" {
			t.Fatalf("missing envelope accepted: %+v", res)
		}
	}
	v := NewVerifier(nil)
	cfg := v.Config()
	cfg.EnvelopeEnabled = false
	v.SetConfig(cfg)
	if res := v.Verify(context.Background(), VerifyRequest{}); res.Verdict != VerdictUnverified {
		t.Fatal(res)
	}
}
func TestReviewCanaryEnvelopeBindsArtifact(t *testing.T) {
	pub, priv, id := newTestKey(t)
	v := NewVerifier(nil)
	if err := v.Keyring().Add(id, pub); err != nil {
		t.Fatal(err)
	}
	c := Canary{ID: "fixture", TaskID: "task", Input: []byte(`{"n":7}`), ExpectedSHA: SHA256([]byte("answer")), ArtifactSHA256: SHA256([]byte("canary module"))}
	if err := v.Canaries().Register(c); err != nil {
		t.Fatal(err)
	}
	body, signed, err := v.InjectWithEnvelope(context.Background(), c.ID, "job", priv, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = v.VerifyEnvelope(signed); err != nil {
		t.Fatal(err)
	}
	var env TaskEnvelope
	if err = json.Unmarshal(signed.Payload, &env); err != nil {
		t.Fatal(err)
	}
	if env.ArtifactSHA256 != c.ArtifactSHA256 || env.InputSHA256 != SHA256(c.Input) {
		t.Fatalf("wrong binding: %+v", env)
	}
	if bytes.Contains(body, []byte(c.ExpectedSHA)) {
		t.Fatal("answer leaked")
	}
	c.ArtifactSHA256 = ""
	c.ID = "missing"
	if err = v.Canaries().Register(c); err != nil {
		t.Fatal(err)
	}
	if _, _, err = v.InjectWithEnvelope(context.Background(), c.ID, "job", priv, id); !errors.Is(err, ErrInvalidEnv) {
		t.Fatalf("missing artifact must fail: %v", err)
	}
}
func TestReviewCanonicalControlEscapes(t *testing.T) {
	got, err := CanonicalJSON(map[string]string{"v": "\b\f\x00\n\r\t"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"v":"\b\f\u0000\n\r\t"}` {
		t.Fatalf("JS-compatible escapes: %s", got)
	}
}
func TestReviewRetirementUsesCallerClock(t *testing.T) {
	pub, priv, id := newTestKey(t)
	ring := NewKeyring()
	if err := ring.Add(id, pub); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_600_000_000, 0)
	if err := ring.Retire(id, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	env, err := SignTaskEnvelope(NewTaskEnvelope("job", "task", SHA256(nil), SHA256(nil), id, now, time.Hour), priv, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyTaskEnvelope(env, ring, now); err != nil {
		t.Fatal(err)
	}
	if err = VerifyTaskEnvelope(env, ring, now.Add(time.Minute)); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("retirement boundary: %v", err)
	}
}
func TestReviewReputationBoundAndUnban(t *testing.T) {
	r := NewReputation(nil)
	for i := 0; i < MaxEvidence+20; i++ {
		if _, err := r.Observe("worker", EventPass, fmt.Sprint(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	w, err := r.Worker("worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Evidence) != MaxEvidence || w.TotalEvents != MaxEvidence+20 || w.Evidence[0].TaskID != "20" {
		t.Fatalf("bound/counters: %+v", w)
	}
	w.Evidence[0].TaskID = "mutated"
	snap := r.Snapshot()
	if snap.Workers[0].Evidence[0].TaskID != "20" {
		t.Fatal("snapshot aliases returned evidence")
	}
	for i := 0; i < 3; i++ {
		if _, err = r.Observe("worker", EventCanaryFail, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	w, err = r.Unban("worker")
	if err != nil || w.Banned || w.Status == StatusBar || w.CanaryFails != 0 {
		t.Fatalf("unban: %+v %v", w, err)
	}
	w, err = r.Observe("worker", EventCanaryFail, "", "")
	if err != nil || w.Banned {
		t.Fatalf("old failures caused immediate reban: %+v %v", w, err)
	}
	snap.Workers[0].Evidence = make([]Event, MaxEvidence+50)
	r.Restore(snap)
	w, _ = r.Worker("worker")
	if len(w.Evidence) != MaxEvidence {
		t.Fatal("restore bypassed evidence bound")
	}
}
