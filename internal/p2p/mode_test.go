// F11, 2026-11-25: red->green tests for the G-F11-2 mode and ICE rules.
// RED on base 063bfac5: the internal/p2p package does not exist.
package p2p

import (
	"encoding/json"
	"testing"
)

func TestParseModeAcceptsOnlyAdminEnum(t *testing.T) {
	for raw, want := range map[string]Mode{"off": ModeOff, "lan": ModeLan, "stun": ModeStun,
		" STUN ": ModeStun, "Lan": ModeLan} {
		got, err := ParseMode(raw)
		if err != nil || got != want {
			t.Fatalf("ParseMode(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "turn", "near", "on", "same-lan"} {
		if _, err := ParseMode(raw); err == nil {
			t.Fatalf("ParseMode(%q) must fail", raw)
		}
	}
}

func TestValidateStunURLSelfHostedOnly(t *testing.T) {
	for _, good := range []string{"stun:stun.corp.internal:3478", "stuns://stun.corp.internal",
		"stun:10.0.0.8", "  stun:coturn.vps.example.net:3478  "} {
		if err := ValidateStunURL(good); err != nil {
			t.Fatalf("ValidateStunURL(%q) = %v; want accepted", good, err)
		}
	}
	for _, bad := range []string{"turn:turn.corp.internal:3478", "turns:x", "",
		"stun:stun.l.google.com:19302", "stun:stun.cloudflare.com", "stun:global.stun.twilio.com",
		"stun:user@stun.corp.internal", "stun:host/path", "stun:host?q=1", "stun:,#",
		"http://stun.corp.internal", "stun:", "stun"} {
		if err := ValidateStunURL(bad); err == nil {
			t.Fatalf("ValidateStunURL(%q) must be refused", bad)
		}
	}
}

func TestConfigValidateAndICEConfig(t *testing.T) {
	off := Config{Mode: ModeOff}
	if err := off.Validate(); err != nil {
		t.Fatalf("off config must validate: %v", err)
	}
	if got := off.ICEConfig(); got != nil {
		t.Fatalf("off mode must have an empty ICE config, got %+v", got)
	}
	lan := Config{Mode: ModeLan}
	if err := lan.Validate(); err != nil {
		t.Fatalf("lan config must validate: %v", err)
	}
	if got := lan.ICEConfig(); got != nil {
		t.Fatalf("LAN-ONLY mode must carry NO STUN server, got %+v", got)
	}
	lanWithStun := Config{Mode: ModeLan, StunURLs: []string{"stun:stun.corp.internal"}}
	if err := lanWithStun.Validate(); err == nil {
		t.Fatalf("lan mode with stun urls must be refused")
	}
	stun := Config{Mode: ModeStun, StunURLs: []string{"stun:stun.corp.internal:3478", "turn:x"}}
	if err := stun.Validate(); err == nil {
		t.Fatalf("stun config with a turn url must be refused")
	}
	stunOK := Config{Mode: ModeStun, StunURLs: []string{"stun:stun.corp.internal:3478"}}
	if err := stunOK.Validate(); err != nil {
		t.Fatalf("valid stun config refused: %v", err)
	}
	ice := stunOK.ICEConfig()
	if len(ice) != 1 || len(ice[0].URLs) != 1 || ice[0].URLs[0] != "stun:stun.corp.internal:3478" {
		t.Fatalf("stun ICE config wrong: %+v", ice)
	}
	blob, err := json.Marshal(ice)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, has := decoded[0]["credential"]; has {
		t.Fatalf("ICE server payload must never carry credentials (no secrets in the browser)")
	}
	if _, has := decoded[0]["username"]; has {
		t.Fatalf("ICE server payload must never carry usernames")
	}
}

func TestValidateEnvelopeCapsAndShapes(t *testing.T) {
	good := Envelope{V: 1, Type: TypeOffer, CallID: "call-abcdef12", SDP: "v=0...", Digest: digest64()}
	if err := ValidateEnvelope(&good); err != nil {
		t.Fatalf("valid offer refused: %v", err)
	}
	big := good
	big.SDP = string(make([]byte, MaxSDPBytes+1))
	if err := ValidateEnvelope(&big); err == nil {
		t.Fatalf("oversized sdp must be refused")
	}
	noDigest := good
	noDigest.Digest = ""
	if err := ValidateEnvelope(&noDigest); err == nil {
		t.Fatalf("offer without a sha256 digest must be refused")
	}
	badDigest := good
	badDigest.Digest = "../secret"
	if err := ValidateEnvelope(&badDigest); err == nil {
		t.Fatalf("non-hex digest must be refused")
	}
	bye := Envelope{V: 1, Type: TypeBye, CallID: "call-abcdef12"}
	if err := ValidateEnvelope(&bye); err != nil {
		t.Fatalf("bye refused: %v", err)
	}
	evil := Envelope{V: 1, Type: "relay-result", CallID: "call-abcdef12"}
	if err := ValidateEnvelope(&evil); err == nil {
		t.Fatalf("unknown envelope type must be refused: results and receipts stay server-only")
	}
	wrongV := good
	wrongV.V = 2
	if err := ValidateEnvelope(&wrongV); err == nil {
		t.Fatalf("wrong version must be refused")
	}
}

func digest64() string {
	const hexAlphabet = "0123456789abcdef"
	b := make([]byte, 64)
	for i := range b {
		b[i] = hexAlphabet[i%16]
	}
	return string(b)
}
