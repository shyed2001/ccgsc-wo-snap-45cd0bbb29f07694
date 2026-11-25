// Path: internal/store/pg/admin_config_F5.go | Codex | 2026-11-18
// T42 F5: transactional HTTP config writes and engine version references.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"ccgsc/poolscore/internal/store"
	"github.com/jackc/pgx/v5"
)

// AdminConfigWrite matches the existing admin page client. Identity is deliberately
// absent: the authenticated Store A username is resolved in PostgreSQL.
type AdminConfigWrite struct {
	Settings        json.RawMessage `json:"settings,omitempty"`
	Reason          string          `json:"reason"`
	PinName         *string         `json:"pinName,omitempty"`
	ExpectedVersion *int            `json:"expectedVersion,omitempty"`
	RollbackVersion int             `json:"rollbackVersion,omitempty"`
}

func (c *ConfigStore) AdminAuthor(ctx context.Context, username string) (int64, error) {
	var id int64
	err := c.pool.QueryRow(ctx, `SELECT id FROM admin_user WHERE username=$1 AND is_active AND store='A' AND roles && ARRAY['admin','director']::text[]`, username).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrAuthorRequired
	}
	return id, err
}

// WriteAdminConfig serializes expected-version validation, save/rollback and pin
// on the same page lock used by pruning and job attachment.
func (c *ConfigStore) WriteAdminConfig(ctx context.Context, page string, q AdminConfigWrite, author int64, validate func(json.RawMessage) error) (*ConfigVersion, error) {
	if err := validPage(page); err != nil {
		return nil, err
	}
	if strings.TrimSpace(q.Reason) == "" {
		return nil, ErrReasonRequired
	}
	if author == 0 {
		return nil, ErrAuthorRequired
	}
	if q.RollbackVersion < 0 || (q.ExpectedVersion != nil && *q.ExpectedVersion < 0) || (q.RollbackVersion > 0 && len(q.Settings) > 0) {
		return nil, ErrInvalidConfig
	}
	var saved *ConfigVersion
	err := c.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pool_config_lock_page($1)`, page); err != nil {
			return err
		}
		var latest int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version),0) FROM pool_config_version WHERE page=$1`, page).Scan(&latest); err != nil {
			return err
		}
		if q.ExpectedVersion != nil && latest != *q.ExpectedVersion {
			return ErrVersionConflict
		}
		data := q.Settings
		if q.RollbackVersion > 0 {
			previous, err := c.readVersion(ctx, tx, page, q.RollbackVersion)
			if err != nil {
				return err
			}
			data = previous.Config
		}
		if err := validate(data); err != nil {
			return err
		}
		var err error
		saved, err = c.SaveVersionTx(ctx, tx, page, data, "", author, q.Reason)
		if err != nil {
			return err
		}
		if q.PinName != nil {
			if strings.TrimSpace(*q.PinName) == "" || len(*q.PinName) > 64 {
				return ErrInvalidConfig
			}
			if _, err = tx.Exec(ctx, `SELECT pool_config_pin($1,$2,$3,$4)`, page, saved.Version, *q.PinName, author); err != nil {
				return err
			}
			saved, err = c.readVersion(ctx, tx, page, saved.Version)
		}
		return err
	})
	return saved, err
}

func (t transaction) activePolicyConfig(page string, out any) error {
	if err := validPage(page); err != nil {
		return err
	}
	// A PG engine requires the existing config schema; missing schema fails closed.
	// Read-only capability requests do not acquire write locks.
	if t.lock {
		if _, err := t.tx.Exec(t.ctx, `SELECT pool_config_lock_page($1)`, page); err != nil {
			return err
		}
	}
	var version int
	var config []byte
	err := t.tx.QueryRow(t.ctx, `SELECT version, config FROM pool_config_version WHERE page=$1 ORDER BY version DESC LIMIT 1`, page).Scan(&version, &config)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"page": page, "version": version, "config": json.RawMessage(config)})
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (t transaction) pinPolicyConfig(jobID string, value any) error {
	var ref struct {
		Page    string
		Version int
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &ref); err != nil {
		return err
	}
	var attached bool
	if err = t.tx.QueryRow(t.ctx, `SELECT pool_config_attach_job($1,$2,$3)`, jobID, ref.Page, ref.Version).Scan(&attached); err != nil {
		return err
	}
	if !attached {
		return ErrVersionNotFound
	}
	return nil
}
