// Path: internal/p2p/mode.go | Author: GLM (Z.ai) for the owner | 2026-11-25
// What/why: F11 admin P2P mode and ICE configuration rules (owner grill
// section 16, G-F11-2, BINDING): both pools get ONE admin-controlled mode,
// 'stun' (self-hosted STUN-only, coturn configured at deploy) or 'lan'
// (host candidates only). No public STUN, no TURN. The code default is off:
// only an admin decision turns a pool on ("default per admin").
//
// Mirrors web/pages/js/platform/p2p/rules.js (P2P_MODES, stunUrlAllowed,
// sanitizeIceServers); the two sides must stay in lockstep, and the rules.js
// tests pin the same values.
//
// This package is platform infrastructure: no workload code (CR-53).
package p2p

import (
	"fmt"
	"net/url"
	"strings"
)

// Mode is the pools admin P2P mode of one pool.
type Mode string

const (
	// ModeOff: no discovery, no dialing, no answering. The default.
	ModeOff Mode = "off"
	// ModeLan: host candidates only; the ICE configuration is empty.
	ModeLan Mode = "lan"
	// ModeStun: self-hosted STUN only; still no TURN and no public providers.
	ModeStun Mode = "stun"
)

// ParseMode accepts exactly the admin enum values, case-insensitive.
func ParseMode(raw string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "off":
		return ModeOff, nil
	case "lan":
		return ModeLan, nil
	case "stun":
		return ModeStun, nil
	default:
		return "", fmt.Errorf("invalid p2p mode %q", raw)
	}
}

// Config is the admin configuration of one pool.
type Config struct {
	Mode     Mode     `json:"mode"`
	StunURLs []string `json:"stunUrls,omitempty"`
}

// Validate fails closed: STUN URLs are only allowed (and required) in stun
// mode, and every URL must pass ValidateStunURL.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeOff, ModeLan:
		if len(c.StunURLs) > 0 {
			return fmt.Errorf("stunUrls are only valid in stun mode")
		}
		return nil
	case ModeStun:
		if len(c.StunURLs) == 0 {
			return fmt.Errorf("stun mode requires at least one self-hosted stun URL")
		}
		for _, raw := range c.StunURLs {
			if err := ValidateStunURL(raw); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("invalid p2p mode %q", string(c.Mode))
	}
}

// ICEServer is one browser iceServers entry. STUN never carries credentials,
// and this type cannot express any: no secret may reach the browser (hard
// rule; G-F10-2). A turn: URL has no representation here at all.
type ICEServer struct {
	URLs []string `json:"urls"`
}

// ICEConfig returns the iceServers payload for the discovery endpoint.
// 'lan' and 'off' answer nil: LAN-only means host candidates only, and off
// never dials at all. 'stun' answers one entry with the validated URLs.
func (c Config) ICEConfig() []ICEServer {
	if c.Mode != ModeStun {
		return nil
	}
	urls := make([]string, 0, len(c.StunURLs))
	for _, raw := range c.StunURLs {
		if err := ValidateStunURL(raw); err == nil {
			urls = append(urls, strings.TrimSpace(raw))
		}
	}
	if len(urls) == 0 {
		return nil
	}
	return []ICEServer{{URLs: urls}}
}

// publicICEHosts lists well-known public STUN/TURN providers (G-F11-2: no
// public STUN). Defense in depth only: the binding control is that the URLs
// live in pools admin server configuration, never in a job option
// (internal/pool applyOptions refuses options.p2p), a peer message or a page.
var publicICEHosts = map[string]bool{
	"stun.l.google.com": true, "stun1.l.google.com": true, "stun2.l.google.com": true,
	"stun3.l.google.com": true, "stun4.l.google.com": true, "stun.cloudflare.com": true,
	"global.stun.twilio.com": true, "stun.sipgate.net": true, "stun.nextcloud.com": true,
	"stun.stunprotocol.org": true, "stun.sipnet.ru": true,
}

// ValidateStunURL accepts exactly one self-hosted stun:/stuns: URL of the
// RFC 7064 shape (scheme ':' host[:port], optional '//' tolerated). It
// refuses userinfo, paths, queries, fragments, commas and whitespace, and
// every well-known public provider host. No turn:/turns: URL can pass.
func ValidateStunURL(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" || strings.ContainsAny(value, ",@") {
		return fmt.Errorf("invalid stun url %q", raw)
	}
	rest := value
	switch {
	case strings.HasPrefix(rest, "stun://"):
		rest = rest[len("stun://"):]
	case strings.HasPrefix(rest, "stun:"):
		rest = rest[len("stun:"):]
	case strings.HasPrefix(rest, "stuns://"):
		rest = rest[len("stuns://"):]
	case strings.HasPrefix(rest, "stuns:"):
		rest = rest[len("stuns:"):]
	default:
		return fmt.Errorf("invalid stun url %q: scheme must be stun: or stuns:", raw)
	}
	if rest == "" || strings.ContainsAny(rest, " /?#") {
		return fmt.Errorf("invalid stun url %q: host path or query not allowed", raw)
	}
	host := rest
	if at := strings.LastIndex(host, ":"); at >= 0 {
		host = host[:at]
	}
	host = strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(host), "]"), "[")
	if host == "" || strings.Contains(host, "@") || publicICEHosts[host] {
		return fmt.Errorf("invalid stun url %q: host refused", raw)
	}
	if _, err := url.Parse(value); err != nil {
		return fmt.Errorf("invalid stun url %q: %v", raw, err)
	}
	return nil
}
