package p2p

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGXStore is the production relay store. It uses the same pgx pool as the
// generic pools store, so registrations and relays share one PostgreSQL
// deployment without a second database connection.
type PGXStore struct{ pool *pgxpool.Pool }

type PGXConfigStore struct{ pool *pgxpool.Pool }

func NewPGXStores(pool *pgxpool.Pool) (*PGXStore, *PGXConfigStore) {
	return &PGXStore{pool: pool}, &PGXConfigStore{pool: pool}
}

func (s *PGXStore) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, Schema)
	return err
}

func (s *PGXStore) Register(ctx context.Context, p Peer) error {
	tag, err := s.pool.Exec(ctx, `
INSERT INTO p2p_peers (peer_id, pool, app_id, owner_id, lease_id, worker_id, mode, seen_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (peer_id) DO UPDATE
 SET pool=EXCLUDED.pool, app_id=EXCLUDED.app_id, owner_id=EXCLUDED.owner_id,
     lease_id=EXCLUDED.lease_id, worker_id=EXCLUDED.worker_id, mode=EXCLUDED.mode,
     seen_at=EXCLUDED.seen_at, expires_at=EXCLUDED.expires_at
 WHERE p2p_peers.lease_id=EXCLUDED.lease_id OR p2p_peers.expires_at <= EXCLUDED.seen_at`,
		p.PeerID, p.Pool, p.AppID, p.OwnerID, p.LeaseID, p.WorkerID, string(p.Mode), p.SeenAt, p.ExpiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s *PGXStore) Unregister(ctx context.Context, peerID, leaseID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM p2p_peers WHERE peer_id=$1 AND lease_id=$2`, peerID, leaseID)
	return err
}

func (s *PGXStore) Get(ctx context.Context, peerID string) (Peer, error) {
	var p Peer
	var mode string
	err := s.pool.QueryRow(ctx, `
SELECT peer_id,pool,app_id,owner_id,lease_id,worker_id,mode,seen_at,expires_at
FROM p2p_peers WHERE peer_id=$1`, peerID).Scan(
		&p.PeerID, &p.Pool, &p.AppID, &p.OwnerID, &p.LeaseID, &p.WorkerID,
		&mode, &p.SeenAt, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Peer{}, ErrNotFound
	}
	if err != nil {
		return Peer{}, err
	}
	p.Mode = Mode(mode)
	return p, nil
}

func (s *PGXStore) SameAppPeers(ctx context.Context, pool, appID, excludePeer string, limit int) ([]Peer, error) {
	rows, err := s.pool.Query(ctx, `
SELECT peer_id,app_id,mode FROM p2p_peers
WHERE pool=$1 AND app_id=$2 AND peer_id<>$3 AND expires_at>now()
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

func (s *PGXStore) Push(ctx context.Context, from Peer, toPeer string, env Envelope, now time.Time) error {
	body, err := marshalEnvelope(env)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
INSERT INTO p2p_messages (pool,from_peer,from_app,to_peer,envelope,created_at,expires_at)
SELECT $1,$2,$3,$4,$5,$6,$7
WHERE EXISTS (SELECT 1 FROM p2p_peers t
  WHERE t.peer_id=$4 AND t.pool=$1 AND t.app_id=$3 AND t.expires_at>$6)
AND EXISTS (SELECT 1 FROM p2p_peers f
  WHERE f.peer_id=$2 AND f.pool=$1 AND f.app_id=$3 AND f.expires_at>$6)`,
		from.Pool, from.PeerID, from.AppID, toPeer, string(body), now,
		now.Add(MessageTTLSeconds*time.Second))
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var app string
	err = s.pool.QueryRow(ctx, `SELECT app_id FROM p2p_peers WHERE peer_id=$1 AND pool=$2`, toPeer, from.Pool).Scan(&app)
	if errors.Is(err, pgx.ErrNoRows) || app != from.AppID {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	return ErrNotFound
}

func (s *PGXStore) Poll(ctx context.Context, peerID string, after int64, limit int, now time.Time) ([]StoredMessage, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
SELECT id,from_peer,from_app,envelope FROM p2p_messages
WHERE to_peer=$1 AND id>$2 AND expires_at>$3 ORDER BY id LIMIT $4`,
		peerID, after, now, limit)
	if err != nil {
		return nil, 0, err
	}
	var out []StoredMessage
	var ids []int64
	maxID := after
	for rows.Next() {
		var m StoredMessage
		var body []byte
		if err := rows.Scan(&m.ID, &m.FromPeer, &m.FromApp, &body); err != nil {
			rows.Close()
			return nil, 0, err
		}
		env, err := unmarshalEnvelope(body)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		m.Envelope = env
		out = append(out, m)
		ids = append(ids, m.ID)
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `DELETE FROM p2p_messages WHERE id=$1`, id); err != nil {
			return nil, 0, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE p2p_peers SET seen_at=$2,expires_at=$3 WHERE peer_id=$1`,
		peerID, now, now.Add(peerTTL)); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return out, maxID, nil
}

func (s *PGXStore) Sweep(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
WITH gone AS (DELETE FROM p2p_peers WHERE expires_at<=$1 RETURNING peer_id),
dropped AS (DELETE FROM p2p_messages WHERE expires_at<=$1 RETURNING id)
SELECT (SELECT count(*) FROM gone)+(SELECT count(*) FROM dropped)`, now).Scan(&n)
	return n, err
}

func (s *PGXConfigStore) LoadConfig(ctx context.Context, pool string) (Config, error) {
	var mode, joined string
	err := s.pool.QueryRow(ctx, `SELECT mode,stun_urls FROM p2p_config WHERE pool=$1`, pool).
		Scan(&mode, &joined)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{Mode: ModeOff}, nil
	}
	if err != nil {
		return Config{}, err
	}
	cfg := Config{Mode: Mode(mode), StunURLs: splitStunListPGX(joined)}
	if cfg.Mode == "" {
		cfg.Mode = ModeOff
	}
	return cfg, nil
}

func (s *PGXConfigStore) SaveConfig(ctx context.Context, pool string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO p2p_config(pool,mode,stun_urls,updated_at) VALUES($1,$2,$3,$4)
ON CONFLICT(pool) DO UPDATE SET mode=EXCLUDED.mode,stun_urls=EXCLUDED.stun_urls,updated_at=EXCLUDED.updated_at`,
		pool, string(cfg.Mode), strings.Join(cfg.StunURLs, ","), time.Now())
	return err
}

func splitStunListPGX(joined string) []string {
	if strings.TrimSpace(joined) == "" {
		return nil
	}
	parts := strings.Split(joined, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}
