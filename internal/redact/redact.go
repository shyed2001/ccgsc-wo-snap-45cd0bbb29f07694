// Path: internal/redact/redact.go | Author: GLM (z.ai) | 2026-11-21
// What/why: T101 - one redaction helper for every error, log and String()
// path that could carry a credential: PostgreSQL DSNs (URL and keyword/value
// form), URLs or addresses with userinfo (Redis addresses), and free text
// that may embed a secret value (API keys, passwords). Stdlib only, so every
// binary of the module can use it without new dependencies.
package redact

import (
	"net/url"
	"regexp"
	"strings"
)

// Marker replaces every redacted value. It names the kind of value it
// replaces so a log reader can tell a redacted password from a redacted
// address without seeing any part of the secret itself.
const Marker = "[REDACTED]"

// urlMarker is the URL-safe spelling of the marker: brackets are not legal
// userinfo characters and would come back percent-encoded (%5B...%5D).
const urlMarker = "redacted"

// dsnPasswordRe matches the password keyword of a PostgreSQL keyword/value
// connection string, including the quoted-value forms pgx accepts. The word
// boundary keeps sslpassword or passfile from matching.
var dsnPasswordRe = regexp.MustCompile(`(?i)\b(password)\s*=\s*('(?:[^']|'')*'|"[^"]*"|[^\s]*)`)

// userinfoRe matches the userinfo part of a scheme-less credential-bearing
// address (user:pass@host or :pass@host). Used only as a best effort when
// net/url refuses to parse the string.
var userinfoRe = regexp.MustCompile(`//[^@/?#]*@`)

// Password returns the display form of a password: the marker, never the
// value. An empty password is shown as an empty quoted string, because there
// is nothing to redact.
func Password(p string) string {
	if p == "" {
		return `""`
	}
	return Marker
}

// Text replaces every occurrence of the given secrets in s with the marker.
// Empty secrets are skipped: an empty pattern would match everywhere. The
// function is safe to call with no secrets and returns s unchanged.
func Text(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, Marker)
	}
	return s
}

// wrapped carries an error whose display text has its secrets replaced. The
// original error stays reachable through Unwrap, so errors.Is and errors.As
// keep working on the wrapped chain.
type wrapped struct {
	err error
	msg string
}

func (w *wrapped) Error() string { return w.msg }
func (w *wrapped) Unwrap() error { return w.err }

// Wrap returns an error that displays err's text with every occurrence of the
// secrets replaced by the marker. It returns nil when err is nil.
func Wrap(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return &wrapped{err: err, msg: Text(err.Error(), secrets...)}
}

// URL redacts the password of the userinfo and of any password query
// parameter in a URL-shaped string, keeping the username for diagnosis. A
// string net/url refuses to parse gets the best-effort userinfo removal of
// userinfoRe instead of being returned with its secret intact.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return userinfoRe.ReplaceAllString(raw, "//"+urlMarker+"@")
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), urlMarker)
		}
	}
	q := u.Query()
	changed := false
	for key, values := range q {
		if strings.EqualFold(key, "password") && len(values) > 0 {
			q[key] = []string{urlMarker}
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// DSN redacts a PostgreSQL connection string of either accepted form: a URL
// DSN (userinfo and password query parameter) or a keyword/value DSN
// (password=... with single-quoted, double-quoted or bare values).
func DSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.Contains(dsn, "://") {
		return URL(dsn)
	}
	return dsnPasswordRe.ReplaceAllString(dsn, "$1="+Marker)
}

// DSNSecrets returns the password values a DSN carries, in the form they are
// most likely to appear in error text: the decoded password of a URL DSN and
// the raw (still quoted, still escaped) value of a keyword/value DSN. Empty
// and marker values are never returned. Used by RedactDSNError.
func DSNSecrets(dsn string) []string {
	if dsn == "" {
		return nil
	}
	var secrets []string
	add := func(v string) {
		if v != "" && v != Marker && v != urlMarker {
			secrets = append(secrets, v)
		}
	}
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			if u.User != nil {
				if pw, ok := u.User.Password(); ok {
					add(pw)
				}
			}
			for key, values := range u.Query() {
				if strings.EqualFold(key, "password") {
					for _, v := range values {
						add(v)
					}
				}
			}
		}
		return secrets
	}
	for _, m := range dsnPasswordRe.FindAllStringSubmatch(dsn, -1) {
		v := m[2]
		if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
			v = v[1 : len(v)-1]
		}
		add(v)
	}
	return secrets
}

// RedactDSNError wraps err so that neither the whole DSN text (echoed by
// pgx parse errors) nor any of its password values survives in the displayed
// message. The original error stays reachable through Unwrap.
func RedactDSNError(err error, dsn string) error {
	if err == nil {
		return nil
	}
	msg := Text(err.Error(), DSNSecrets(dsn)...)
	if dsn != "" && strings.Contains(msg, dsn) {
		msg = strings.ReplaceAll(msg, dsn, DSN(dsn))
	}
	return &wrapped{err: err, msg: msg}
}
