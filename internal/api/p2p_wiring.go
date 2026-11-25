package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/p2p"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
)

func (s *Server) mountP2P(mux *http.ServeMux) {
	s.p2pOnce.Do(func() {
		var peers p2p.PeerStore
		var configs p2p.ConfigStore
		if s.P2PDatabase != nil {
			pgPeers, pgConfigs := p2p.NewPGXStores(s.P2PDatabase)
			if err := pgPeers.EnsureSchema(context.Background()); err != nil {
				s.Log.Error("p2p.schema", "error", err)
				return
			}
			peers, configs = pgPeers, pgConfigs
		} else {
			peers, configs = p2p.NewMemStore(), p2p.NewMemConfigStore()
		}
		s.p2pMux = P2PMux(p2p.NewHub(peers, configs, time.Now))
		SetP2PAuthenticator(s.p2pPeer, s.p2pAdmin)
	})
	if s.p2pMux != nil {
		mux.Handle("/thread-pool/v1/p2p/", s.p2pMux)
		mux.Handle("/process-pool/v1/p2p/", s.p2pMux)
	}
}

func (s *Server) p2pPeer(ctx context.Context, r *http.Request, poolName string) (p2p.Peer, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if raw == "" {
		return p2p.Peer{}, auth.ErrUnauthorized
	}
	poolID := "thread"
	p2pPool := p2p.PoolThread
	if poolName == p2p.PoolProcess {
		poolID, p2pPool = "process", p2p.PoolProcess
	}
	signer, err := s.Engine.EgressSigner(ctx)
	if err != nil {
		return p2p.Peer{}, err
	}
	unscoped, err := signer.Verify(raw, "worker", time.Now())
	if err != nil {
		return p2p.Peer{}, err
	}
	claims, err := signer.VerifyScoped(raw, appegress.Expect{
		Purpose: "worker", App: unscoped.Audience, Owner: unscoped.Owner,
		Pool: poolID, Worker: unscoped.Subject, Lease: unscoped.Lease,
	}, time.Now())
	if err != nil {
		return p2p.Peer{}, err
	}
	var worker pool.Worker
	err = s.Engine.Store.View(ctx, func(reader store.Reader) error {
		if err := reader.Get(store.Workers, claims.Subject, &worker); err != nil {
			return err
		}
		return nil
	})
	if err != nil || worker.PoolID != poolID || worker.Owner != claims.Owner ||
		worker.HostID == "" || worker.BrowserTabID == "" {
		return p2p.Peer{}, errors.New("worker binding")
	}
	tabID, err := appegress.TabIdentity(worker.HostID, worker.BrowserTabID)
	if err != nil {
		return p2p.Peer{}, err
	}
	var fence appegress.TabFence
	err = s.Engine.Store.View(ctx, func(reader store.Reader) error {
		var e error
		fence, e = appegress.ReadTabTx(reader, tabID)
		return e
	})
	if err != nil || !fence.Permits(p2pPool, claims.Owner, claims.Audience) ||
		fence.AppID == "" {
		return p2p.Peer{}, errors.New("tab fence binding")
	}
	return p2p.Peer{
		Pool: p2pPool, AppID: claims.Audience, OwnerID: claims.Owner,
		LeaseID: claims.Lease, WorkerID: claims.Subject,
		PeerID: strings.TrimSpace(r.Header.Get("X-P2P-Peer")),
	}, nil
}

func (s *Server) p2pAdmin(ctx context.Context, r *http.Request) error {
	if cookie, err := r.Cookie("ccgsc_admin_session"); err == nil && cookie.Value != "" {
		session, err := s.Auth.ValidateSession(ctx, cookie.Value)
		if err != nil || (!contains(session.Roles, "admin") && !contains(session.Roles, "director")) {
			return auth.ErrUnauthorized
		}
		if r.Method != http.MethodGet && r.Header.Get("X-CSRF-Token") != session.CSRFToken {
			return auth.ErrUnauthorized
		}
		return nil
	}
	raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if raw == "" {
		return auth.ErrUnauthorized
	}
	claims, err := s.Auth.Validate(ctx, raw)
	scope := "config:read"
	if r.Method != http.MethodGet {
		scope = "config:write"
	}
	if err != nil || (!claims.HasRole("admin") && !claims.HasRole("director")) || !claims.Allows(scope) {
		return auth.ErrUnauthorized
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
