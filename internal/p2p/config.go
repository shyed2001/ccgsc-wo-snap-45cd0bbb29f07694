// Path: internal/p2p/config.go | Author: GLM (Z.ai) for the owner | 2026-11-25
// What/why: the admin ConfigStore contract and the envelope JSON helpers the
// stores share. The configuration lives in PostgreSQL (p2p_config, pg.go),
// so every server instance of a multi-instance deployment reads the same
// admin mode; an absent row means 'off' (the code default, G-F11-2).
package p2p

import (
	"context"
	"encoding/json"
	"sync"
)

// ConfigStore persists the pools admin P2P configuration. The API layer maps
// it onto the admin panel row tp/pp.p2p.mode (schema.js).
type ConfigStore interface {
	LoadConfig(ctx context.Context, pool string) (Config, error)
	SaveConfig(ctx context.Context, pool string, cfg Config) error
}

// MemConfigStore is an in-memory ConfigStore for tests.
type MemConfigStore struct {
	mu    sync.Mutex
	store map[string]Config
}

// NewMemConfigStore builds an empty config store.
func NewMemConfigStore() *MemConfigStore {
	return &MemConfigStore{store: map[string]Config{}}
}

// LoadConfig implements ConfigStore.
func (m *MemConfigStore) LoadConfig(_ context.Context, pool string) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg, ok := m.store[pool]; ok {
		return cfg, nil
	}
	return Config{Mode: ModeOff}, nil
}

// SaveConfig implements ConfigStore.
func (m *MemConfigStore) SaveConfig(_ context.Context, pool string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[pool] = cfg
	return nil
}

func marshalEnvelope(env Envelope) ([]byte, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxEnvelopeBytes {
		return nil, ErrInvalid
	}
	return b, nil
}

func unmarshalEnvelope(body []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Envelope{}, err
	}
	if err := ValidateEnvelope(&env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}
