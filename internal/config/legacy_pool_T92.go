// Path: internal/config/legacy_pool_T92.go | Author: Codex | 2026-11-20
// What/why: restrict the optional legacy bridge to an HTTP loopback origin.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func ValidateLegacyPoolURL(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(value, "#") ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return fmt.Errorf("legacyPoolURL must be an HTTP loopback origin")
	}
	switch u.Hostname() {
	case "127.0.0.1", "::1", "localhost":
	default:
		return fmt.Errorf("legacyPoolURL host must be 127.0.0.1, ::1 or localhost")
	}
	if strings.HasSuffix(u.Host, ":") {
		return fmt.Errorf("legacyPoolURL port must not be empty")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("legacyPoolURL port must be between 1 and 65535")
		}
	}
	// Reject unbracketed IPv6 and other ambiguous authority spellings.
	if u.Hostname() == "::1" && !strings.HasPrefix(u.Host, "[::1]") {
		return fmt.Errorf("legacyPoolURL IPv6 host must be bracketed")
	}
	return nil
}
