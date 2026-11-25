// Path: internal/p2p/pg.go | Author: GLM (Z.ai) for the owner | 2026-11-25
// What/why: the PostgreSQL PeerStore and admin ConfigStore of F11 signaling.
// PostgreSQL multi-server safety is the hard rule: EVERY state the relay
// needs lives in PostgreSQL, and the G-F11-1 same-app rule is enforced by the
// INSERT ... SELECT ... WHERE EXISTS statement itself, so two server
// instances racing the same target peer can never relay across apps even if
// an in-process check were bypassed. No in-memory registration state exists.
//
// The schema is created idempotently by EnsureSchema (CREATE TABLE IF NOT
// EXISTS) so the owner's migration harness can also adopt it (see
// docs/F11_P2P_DESIGN_20261125.md, section 4).
package p2p

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Schema is the idempotent DDL of the three signaling tables.
const Schema = `
CREATE TABLE IF NOT EXISTS p2p_peers (
  peer_id    text PRIMARY KEY,
  pool       text NOT NULL,
  app_id     text NOT NULL,
  owner_id   text NOT NULL,
  lease_id   text NOT NULL,
  worker_id  text NOT NULL DEFAULT '',
  mode       text NOT NULL DEFAULT 'off',
  seen_at    timestamptz NOT NULL,
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS p2p_peers_app ON p2p_peers (pool, app_id, expires_at);

CREATE TABLE IF NOT EXISTS p2p_messages (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  pool       text NOT NULL,
  from_peer  text NOT NULL,
  from_app   text NOT NULL,
  to_peer    text NOT NULL,
  envelope   jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS p2p_messages_to ON p2p_messages (to_peer, id);

CREATE TABLE IF NOT EXISTS p2p_config (
  pool      text PRIMARY KEY,
  mode      text NOT NULL DEFAULT 'off',
  stun_urls text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL
);
`

// PGStore implements PeerStore over database/sql (lib/pq or pgx stdlib).
type PGStore struct {
	db *sql.DB
}

// NewPGStore wraps an open database/sql pool.
func NewPGStore(db *sql.DB) *PGStore { return &PGStore{db: db} }

// EnsureSchema creates the signaling tables if they are missing.
func (s *PGStore) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, Schema)
	return err
}

// Register implements PeerStore: an upsert that refuses to hand a live peer
// id to a different lease (ErrConflict), so a peer id stays bound to its tab.
func (s *PGStore) Register(ctx context.Context, p Peer) error {
	if !ValidPool(p.Pool) {
		return ErrInvalid
	}
	tag, err := s.db.ExecContext(ctx, `
INSERT INTO p2p_peers (peer_id, pool, app_id, owner_id, lease_id, worker_id, mode, seen_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (peer_id) DO UPDATE
  SET pool = EXCLUDED.pool, app_id = EXCLUDED.app_id, owner_id = EXCLUDED.owner_id,
      lease_id = EXCLUDED.lease_id, worker_id = EXCLUDED.worker_id, mode = EXCLUDED.mode,
      seen_at = EXCLUDED.seen_at, expires_at = EXCLUDED.expires_at
  WHERE p2p_peers.lease_id = EXCLUDED.lease_id OR p2p_peers.expires_at <= EXCLUDED.seen_at`,
		p.PeerID, p.Pool, p.AppID, p.OwnerID, p.LeaseID, p.WorkerID, string(p.Mode), p.SeenAt, p.ExpiresAt)
	if err != nil {
		return err
	}
	if rows, _ := tag.RowsAffected(); rows == 0 {
		return ErrConflict
	}
	return nil
}

// Unregister implements PeerStore.
func (s *PGStore) Unregister(ctx context.Context, peerID, leaseID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM p2p_peers WHERE peer_id = $1 AND lease_id = $2`, peerID, leaseID)
	return err
}

// Get implements PeerStore.
func (s *PGStore) Get(ctx context.Context, peerID string) (Peer, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT peer_id, pool, app_id, owner_id, lease_id, worker_id, mode, seen_at, expires_at
FROM p2p_peers WHERE peer_id = $1`, peerID)
	var p Peer
	var mode string
	err := row.Scan(&p.PeerID, &p.Pool, &p.AppID, &p.OwnerID, &p.LeaseID, &p.WorkerID, &mode, &p.SeenAt, &p.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Peer{}, ErrNotFound
	}
	if err != nil {
		return Peer{}, err
	}
	p.Mode = Mode(mode)
	return p, nil
}

// SameAppPeers implements PeerStore.
func (s *PGStore) SameAppPeers(ctx context.Context, pool, appID, excludePeer string, limit int) ([]Peer, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT peer_id, app_id, mode FROM p2p_peers
WHERE pool = $1 AND app_id = $2 AND peer_id <> $3 AND expires_at > now()
ORDER BY peer_id LIMIT $4`, pool, appID, excludePeer, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Peer{}
	for rows.Next() {
		var p Peer
		var mode string
		if err := rows.Scan(&p.PeerID, &p.AppID, &mode); err != nil {
			return nil, err
		}
		p.Mode = Mode(mode)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Push implements PeerStore. THE SAME-APP RULE LIVES IN THIS STATEMENT: the
// envelope is only inserted when BOTH the target and the sender are live
// registrations of the same pool AND the same app. RowsAffected == 0 means
// the relay was refused, which the hub maps to ErrNotFound / ErrForbidden.
func (s *PGStore) Push(ctx context.Context, from Peer, toPeer string, env Envelope, now time.Time) error {
	body, err := marshalEnvelope(env)
	if err != nil {
		return err
	}
	tag, err := s.db.ExecContext(ctx, `
INSERT INTO p2p_messages (pool, from_peer, from_app, to_peer, envelope, created_at, expires_at)
SELECT $1, $2, $3, $4, $5, $6, $7
WHERE EXISTS (
  SELECT 1 FROM p2p_peers t
  WHERE t.peer_id = $4 AND t.pool = $1 AND t.app_id = $3 AND t.expires_at > $6
) AND EXISTS (
  SELECT 1 FROM p2p_peers f
  WHERE f.peer_id = $2 AND f.pool = $1 AND f.app_id = $3 AND f.expires_at > $6
)`,
		from.Pool, from.PeerID, from.AppID, toPeer, body, now, now.Add(MessageTTLSeconds*time.Second))
	if err != nil {
		return err
	}
	if rows, _ := tag.RowsAffected(); rows == 0 {
		target, targetErr := s.peerApp(ctx, from.Pool, toPeer)
		if targetErr != nil || target != from.AppID {
			return ErrForbidden // cross-app (or vanished target): refused, never relayed
		}
		return ErrNotFound
	}
	return nil
}

// peerApp reads only the app binding of a registration (used to tell
// ErrForbidden from ErrNotFound after a refused insert).
func (s *PGStore) peerApp(ctx context.Context, pool, peerID string) (string, error) {
	var app string
	err := s.db.QueryRowContext(ctx,
		`SELECT app_id FROM p2p_peers WHERE peer_id = $1 AND pool = $2`, peerID, pool).Scan(&app)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return app, err
}

// Poll implements PeerStore: drain, consume and refresh presence in one tx.
func (s *PGStore) Poll(ctx context.Context, peerID string, after int64, limit int, now time.Time) ([]StoredMessage, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT id, from_peer, from_app, envelope FROM p2p_messages
WHERE to_peer = $1 AND id > $2 AND expires_at > $3
ORDER BY id LIMIT $4`, peerID, after, now, limit)
	if err != nil {
		return nil, 0, err
	}
	out := []StoredMessage{}
	var ids []int64
	maxID := after
	for rows.Next() {
		var message StoredMessage
		var body []byte
		if err := rows.Scan(&message.ID, &message.FromPeer, &message.FromApp, &body); err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		env, err := unmarshalEnvelope(body)
		if err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		message.Envelope = env
		out = append(out, message)
		ids = append(ids, message.ID)
		if message.ID > maxID {
			maxID = message.ID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM p2p_messages WHERE id = $1`, id); err != nil {
			return nil, 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE p2p_peers SET seen_at = $2, expires_at = $3 WHERE peer_id = $1`,
		peerID, now, now.Add(peerTTL)); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return out, maxID, nil
}

// Sweep implements PeerStore.
func (s *PGStore) Sweep(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `
WITH gone AS (
  DELETE FROM p2p_peers WHERE expires_at <= $1 RETURNING peer_id
), dropped AS (
  DELETE FROM p2p_messages WHERE expires_at <= $1 RETURNING id
)
SELECT (SELECT count(*) FROM gone) + (SELECT count(*) FROM dropped)`, now).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// PGConfigStore implements ConfigStore over the p2p_config table.
type PGConfigStore struct {
	db *sql.DB
}

// NewPGConfigStore wraps an open pool.
func NewPGConfigStore(db *sql.DB) *PGConfigStore { return &PGConfigStore{db: db} }

// LoadConfig implements ConfigStore. stun_urls is the comma-separated list
// convention the admin panel uses (rules.js documents the same shape).
func (s *PGConfigStore) LoadConfig(ctx context.Context, pool string) (Config, error) {
	var mode string
	var joined string
	err := s.db.QueryRowContext(ctx,
		`SELECT mode, stun_urls FROM p2p_config WHERE pool = $1`, pool).Scan(&mode, &joined)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{Mode: ModeOff}, nil
	}
	if err != nil {
		return Config{}, err
	}
	cfg := Config{Mode: Mode(mode), StunURLs: splitStunList(joined)}
	if cfg.Mode == "" {
		cfg.Mode = ModeOff
	}
	return cfg, nil
}

func splitStunList(joined string) []string {
	if strings.TrimSpace(joined) == "" {
		return nil
	}
	parts := strings.Split(joined, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func joinStunList(urls []string) string {
	trimmed := make([]string, 0, len(urls))
	for _, raw := range urls {
		if t := strings.TrimSpace(raw); t != "" {
			trimmed = append(trimmed, t)
		}
	}
	return strings.Join(trimmed, ",")
}

// SaveConfig implements ConfigStore.
func (s *PGConfigStore) SaveConfig(ctx context.Context, pool string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO p2p_config (pool, mode, stun_urls, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (pool) DO UPDATE
  SET mode = EXCLUDED.mode, stun_urls = EXCLUDED.stun_urls, updated_at = EXCLUDED.updated_at`,
		pool, string(cfg.Mode), joinStunList(cfg.StunURLs), time.Now())
	return err
}
