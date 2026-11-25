package pool

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/processapp"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/threadapp"
	"ccgsc/poolscore/internal/verify"
)

var ErrEgressUnavailable = errors.New("app egress service unavailable")

type AppEgressGrant struct {
	ConsentID     string   `json:"consentId"`
	AppID         string   `json:"appId"`
	Owner         string   `json:"owner"`
	Pool          string   `json:"pool"`
	Endpoints     []string `json:"endpoints"`
	PolicyVersion uint64   `json:"policyVersion"`
	Token         string   `json:"token"`
	ExpiresAt     int64    `json:"expiresAt"`
	RunnerGrant   string   `json:"runnerGrant"`
}
type appEgressContext struct {
	job    Job
	lease  Lease
	policy appegress.Policy
}

func (e *Engine) ApproveAppEgress(ctx context.Context, p, owner, app string, quota int, policy *appegress.Policy) error {
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		switch p {
		case Thread:
			return threadapp.ApproveTx(tx, owner, app, quota, e.Now().UTC(), policy)
		case Process:
			return processapp.ApproveTx(tx, owner, app, quota, e.Now().UTC(), policy)
		default:
			return ErrInvalid
		}
	})
	if errors.Is(err, appegress.ErrInvalid) || errors.Is(err, threadapp.ErrInvalid) || errors.Is(err, processapp.ErrInvalid) {
		return ErrInvalid
	}
	if errors.Is(err, threadapp.ErrBound) || errors.Is(err, processapp.ErrBound) || errors.Is(err, threadapp.ErrFull) || errors.Is(err, processapp.ErrFull) {
		return ErrConflict
	}
	return err
}
func (e *Engine) AppEgressPolicy(ctx context.Context, p, owner, app string) (appegress.Policy, error) {
	return appegress.Read(ctx, e.Store, p, owner, app)
}
func (e *Engine) AppEgressList(ctx context.Context) ([]appegress.PublicPolicy, error) {
	return appegress.List(ctx, e.Store)
}
func (e *Engine) EgressSigner(ctx context.Context) (*appegress.Signer, error) {
	e.egressSignerMu.Lock()
	defer e.egressSignerMu.Unlock()
	if e.egressSigner != nil {
		return e.egressSigner, nil
	}
	s, err := appegress.NewSigner(ctx, e.Store)
	if err != nil {
		return nil, err
	}
	e.egressSigner = s
	return s, nil
}
func (e *Engine) SetAppEgressReady(ready bool) { e.egressReady.Store(ready) }
func poolName(p string) string {
	if p == Thread {
		return "thread"
	}
	if p == Process {
		return "process"
	}
	return ""
}
func poolID(p string) string {
	if p == "thread" {
		return Thread
	}
	if p == "process" {
		return Process
	}
	return ""
}
func egressLeaseID(l Lease) string {
	// A developer never receives the CCGSC settlement bearer. This is an opaque,
	// stable reference to the particular task generation.
	return verify.SHA256([]byte(fmt.Sprintf("ccgsc:egress:v1\x00%s\x00%s\x00%d\x00%s", l.JobID, l.TaskID, l.Generation, l.Token)))
}
func readEgressContext(r store.Reader, p, workerID, workerOwner, jobID, taskID string, now time.Time) (appEgressContext, error) {
	var w Worker
	if err := r.Get(store.Workers, workerID, &w); err != nil {
		return appEgressContext{}, err
	}
	if w.PoolID != p || (workerOwner != "" && w.Owner != workerOwner) {
		return appEgressContext{}, ErrForbidden
	}
	var j Job
	if err := r.Get(store.Jobs, jobID, &j); err != nil {
		return appEgressContext{}, err
	}
	if j.PoolID != p || terminal(j) {
		return appEgressContext{}, ErrConflict
	}
	if j.AppID == "" {
		return appEgressContext{job: j}, nil
	}
	policy, err := appegress.ReadTx(r, p, j.Owner, j.AppID)
	if err != nil {
		return appEgressContext{}, err
	}
	if len(policy.Endpoints) == 0 {
		return appEgressContext{job: j, policy: policy}, nil
	}
	if !appEgressConsented(w, policy) || w.AppID != j.AppID || w.AppOwner != j.Owner {
		return appEgressContext{}, ErrForbidden
	}
	// The worker row mirrors the durable physical-tab fence but is written
	// separately. A grant is issued only while the fence itself still names this
	// job's pool, owner and app, so a released or rebound tab gets nothing.
	tab, err := appegress.TabIdentity(w.HostID, w.BrowserTabID)
	if err != nil {
		return appEgressContext{}, ErrForbidden
	}
	fence, err := appegress.ReadTabTx(r, tab)
	if err != nil {
		return appEgressContext{}, err
	}
	if fence.AppID != j.AppID || fence.Owner != j.Owner || fence.Pool != j.PoolID {
		return appEgressContext{}, ErrForbidden
	}
	for _, t := range j.Tasks {
		l := t.Lease
		if l != nil && l.TaskID == taskID && l.WorkerID == workerID && l.ExpiresAt.After(now) && t.State == "leased" {
			return appEgressContext{job: j, lease: *l, policy: policy}, nil
		}
	}
	return appEgressContext{}, ErrConflict
}
func (e *Engine) IssueAppEgress(ctx context.Context, p, owner, worker string, l Lease) (*AppEgressGrant, error) {
	var v appEgressContext
	now := e.Now().UTC()
	err := e.Store.View(ctx, func(r store.Reader) error {
		var er error
		v, er = readEgressContext(r, p, worker, owner, l.JobID, l.TaskID, now)
		return er
	})
	if err != nil {
		return nil, err
	}
	if len(v.policy.Endpoints) == 0 {
		return nil, nil
	}
	if !e.egressReady.Load() || v.lease.Token != l.Token || v.lease.Generation != l.Generation {
		return nil, ErrForbidden
	}
	signer, err := e.EgressSigner(ctx)
	if err != nil {
		return nil, err
	}
	id := egressLeaseID(v.lease)
	token, c, err := signer.IssueBound(v.job.Owner, v.job.AppID, worker, id, poolName(p), "worker", now, v.lease.ExpiresAt, v.job.JobID, v.lease.TaskID, v.policy.Version)
	if err != nil {
		return nil, ErrInvalid
	}
	if v.policy.TokenServiceURL != "" {
		service, _, err := signer.IssueBound(v.job.Owner, v.job.AppID, worker, id, poolName(p), "service", now, v.lease.ExpiresAt, v.job.JobID, v.lease.TaskID, v.policy.Version)
		if err != nil {
			return nil, ErrInvalid
		}
		q := appegress.ServiceRequest{AppID: v.job.AppID, WorkerID: worker, LeaseID: id, Pool: poolName(p), IssuedAt: c.IssuedAt, ExpiresAt: c.ExpiresAt}
		want := appegress.Expect{App: v.job.AppID, Owner: v.job.Owner, Pool: poolName(p), Worker: worker, Lease: id, Job: v.job.JobID, Task: v.lease.TaskID, PolicyVersion: v.policy.Version}
		response, err := appegress.FetchDeveloperToken(ctx, signer, v.policy.TokenServiceURL, service, want, q, now)
		if err != nil {
			return nil, ErrEgressUnavailable
		}
		if response.ExpiresAt <= e.Now().UTC().Unix() {
			return nil, ErrEgressUnavailable
		}
		token = response.Token
		c.ExpiresAt = response.ExpiresAt
	}
	grant, _, err := signer.IssueBound(v.job.Owner, v.job.AppID, worker, id, poolName(p), "runner", now, v.lease.ExpiresAt, v.job.JobID, v.lease.TaskID, v.policy.Version)
	if err != nil {
		return nil, ErrInvalid
	}
	return &AppEgressGrant{ConsentID: v.policy.ConsentID, AppID: v.job.AppID, Owner: v.job.Owner, Pool: p, Endpoints: v.policy.Endpoints, PolicyVersion: v.policy.Version, Token: token, ExpiresAt: c.ExpiresAt, RunnerGrant: grant}, nil
}

// RunnerAppPolicy returns the egress policy a runner frame may use. The grant
// only names the job, task and worker to look up; every identity field of the
// token is then compared with the stored job, lease and policy, so a grant for
// one app, lease, owner, pool or policy version can never authorize another.
func (e *Engine) RunnerAppPolicy(ctx context.Context, token string) (appegress.Policy, error) {
	if !e.egressReady.Load() {
		return appegress.Policy{}, ErrForbidden
	}
	signer, err := e.EgressSigner(ctx)
	if err != nil {
		return appegress.Policy{}, err
	}
	now := e.Now().UTC()
	c, err := signer.Verify(token, "runner", now)
	if err != nil || c.JobID == "" || c.TaskID == "" || c.PolicyVersion == 0 || !strings.HasPrefix(c.Audience, "ccgsc:runner:") {
		return appegress.Policy{}, ErrForbidden
	}
	var v appEgressContext
	err = e.Store.View(ctx, func(r store.Reader) error {
		var er error
		v, er = readEgressContext(r, poolID(c.Pool), c.Subject, "", c.JobID, c.TaskID, now)
		return er
	})
	if err != nil || len(v.policy.Endpoints) == 0 {
		return appegress.Policy{}, ErrForbidden
	}
	want := appegress.Expect{Purpose: "runner", App: v.job.AppID, Owner: v.job.Owner, Pool: poolName(v.job.PoolID), Worker: v.lease.WorkerID, Lease: egressLeaseID(v.lease), Job: v.job.JobID, Task: v.lease.TaskID, PolicyVersion: v.policy.Version}
	if _, err = signer.VerifyScoped(token, want, now); err != nil {
		return appegress.Policy{}, ErrForbidden
	}
	return v.policy, nil
}

func appEgressConsented(w Worker, p appegress.Policy) bool {
	return w.EgressConsent && p.ConsentID != "" && slices.Contains(w.EgressConsentIDs, p.ConsentID)
}
