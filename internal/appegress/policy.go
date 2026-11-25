package appegress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ccgsc/poolscore/internal/store"
)

const policyBucket = "app_egress_policies"
const DefaultIdle = 30 * time.Second
const IdleTimeout = "idle-timeout"
const Immediate = "immediate-when-idle"

var appName = regexp.MustCompile("^[A-Za-z][A-Za-z0-9_-]{0,63}$")
var hostname = regexp.MustCompile("^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$")

type Policy struct {
	ConsentID       string   `json:"consentId,omitempty"`
	Pool            string   `json:"pool,omitempty"`
	AppID           string   `json:"appId,omitempty"`
	Endpoints       []string `json:"endpoints"`
	TokenServiceURL string   `json:"tokenServiceUrl,omitempty"`
	Version         uint64   `json:"version"`
}

func hash(s string) string                     { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func policyKey(pool, owner, app string) string { return hash(pool + "\x00" + owner + "\x00" + app) }
func validApp(pool, owner, app string) bool {
	return (pool == "tp" || pool == "pp") && owner != "" && len(owner) <= 256 && appName.MatchString(app)
}

// Explicit, canonical public origins only. CSP host-source matching is an
// origin boundary, NOT a reliable path restriction after redirects.
func endpoint(raw string, service bool) (string, error) {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, " \t\r\n;\\*'\"%") {
		return "", ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", ErrInvalid
	}
	if u.Scheme != "https" && (service || u.Scheme != "wss") {
		return "", ErrInvalid
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || !hostname.MatchString(host) || !strings.Contains(host, ".") || strings.Contains(host, "..") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || net.ParseIP(host) != nil {
		return "", ErrInvalid
	}
	// Nondefault ports are intentionally disallowed for this first security lane.
	if u.Port() != "" && u.Port() != "443" {
		return "", ErrInvalid
	}
	if !service && u.Path != "" && u.Path != "/" {
		return "", ErrInvalid
	}
	if service {
		if u.Path == "" || u.Path == "/" || strings.Contains(u.Path, "..") {
			return "", ErrInvalid
		}
		return "https://" + host + u.EscapedPath(), nil
	}
	return u.Scheme + "://" + host, nil
}
func Normalize(p Policy) (Policy, error) {
	if len(p.Endpoints) > 16 {
		return Policy{}, ErrInvalid
	}
	result := Policy{Endpoints: make([]string, 0, len(p.Endpoints))}
	seen := map[string]bool{}
	for _, raw := range p.Endpoints {
		v, err := endpoint(raw, false)
		if err != nil {
			return Policy{}, err
		}
		if !seen[v] {
			result.Endpoints = append(result.Endpoints, v)
			seen[v] = true
		}
	}
	sort.Strings(result.Endpoints)
	if p.TokenServiceURL != "" {
		v, err := endpoint(p.TokenServiceURL, true)
		if err != nil || len(result.Endpoints) == 0 {
			return Policy{}, ErrInvalid
		}
		result.TokenServiceURL = v
	}
	return result, nil
}
func ReadTx(r store.Reader, pool, owner, app string) (Policy, error) {
	if !validApp(pool, owner, app) {
		return Policy{}, ErrInvalid
	}
	var p Policy
	err := r.Get(policyBucket, policyKey(pool, owner, app), &p)
	if errors.Is(err, store.ErrNotFound) {
		return Policy{Endpoints: []string{}}, nil
	}
	if err != nil {
		return Policy{}, err
	}
	canonical, err := Normalize(p)
	if err != nil {
		return Policy{}, err
	}
	canonical.Version = p.Version
	canonical.Pool = pool
	canonical.AppID = app
	canonical.ConsentID = hash(policyKey(pool, owner, app) + ":" + strconv.FormatUint(p.Version, 10))
	return canonical, nil
}
func WriteTx(tx store.Tx, pool, owner, app string, p Policy) (Policy, error) {
	if !validApp(pool, owner, app) {
		return Policy{}, ErrInvalid
	}
	v, err := Normalize(p)
	if err != nil {
		return Policy{}, err
	}
	old, err := ReadTx(tx, pool, owner, app)
	if err != nil {
		return Policy{}, err
	}
	v.Version = old.Version + 1
	v.Pool = pool
	v.AppID = app
	v.ConsentID = hash(policyKey(pool, owner, app) + ":" + strconv.FormatUint(v.Version, 10))
	if old.Version == 0 {
		atomic, ok := tx.(store.PutIfAbsentTx)
		if !ok {
			return Policy{}, store.ErrNotImplemented
		}
		ok, err = atomic.PutIfAbsent(policyBucket, policyKey(pool, owner, app), v)
		if err != nil {
			return Policy{}, err
		}
		if !ok {
			return Policy{}, ErrInvalid
		}
	} else if err = tx.Put(policyBucket, policyKey(pool, owner, app), v); err != nil {
		return Policy{}, err
	}
	return v, nil
}
func Read(ctx context.Context, s store.Store, pool, owner, app string) (Policy, error) {
	var p Policy
	err := s.View(ctx, func(r store.Reader) error { var err error; p, err = ReadTx(r, pool, owner, app); return err })
	return p, err
}
func (p Policy) ConnectSrc() string {
	if len(p.Endpoints) == 0 {
		return "'none'"
	}
	return strings.Join(p.Endpoints, " ")
}

type PublicPolicy struct {
	ConsentID string   `json:"consentId"`
	Pool      string   `json:"pool"`
	AppID     string   `json:"appId"`
	Endpoints []string `json:"endpoints"`
}

func List(ctx context.Context, s store.Store) ([]PublicPolicy, error) {
	out := []PublicPolicy{}
	err := s.View(ctx, func(r store.Reader) error {
		rows, err := r.List(policyBucket)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var p Policy
			if err = json.Unmarshal(row.Value, &p); err != nil {
				return err
			}
			v, err := Normalize(p)
			if err != nil {
				return err
			}
			if len(v.Endpoints) > 0 {
				out = append(out, PublicPolicy{ConsentID: hash(row.Key + ":" + strconv.FormatUint(p.Version, 10)), Pool: p.Pool, AppID: p.AppID, Endpoints: v.Endpoints})
			}
		}
		return nil
	})
	return out, err
}
