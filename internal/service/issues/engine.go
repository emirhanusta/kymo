// Package issues derives and maintains the persistent list of workload
// problems ("issues") from the recorded container, event and Kubernetes-event
// data, and hands opened / resolved / reminder events to a notifier.
package issues

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// Config tunes the engine. Zero values fall back to the documented defaults.
type Config struct {
	// Interval between evaluations (default 30s).
	Interval time.Duration
	// DegradedAfter is how long a workload must stay partially ready before a
	// "degraded" issue opens (default 3m).
	DegradedAfter time.Duration
	// ResolveAfter is how long a condition must stay false before its issue is
	// resolved (default 5m).
	ResolveAfter time.Duration
	// ReopenWindow is how long after resolving a returning problem reopens the
	// same issue (as a flap) instead of opening a new one (default 15m).
	ReopenWindow time.Duration
	// IncludeSystem also evaluates system workloads (default false).
	IncludeSystem bool
	// Retention is how long resolved issues are kept (default 90 days).
	Retention time.Duration
}

// Defaults for Config.
const (
	DefaultInterval      = 30 * time.Second
	DefaultDegradedAfter = 3 * time.Minute
	DefaultResolveAfter  = 5 * time.Minute
	DefaultReopenWindow  = 15 * time.Minute
	DefaultRetention     = 90 * 24 * time.Hour

	eventWindow   = 24 * time.Hour
	k8sWindow     = 30 * time.Minute
	cleanupPeriod = time.Hour
	maxRows       = 20000
)

func (c Config) withDefaults() Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.Interval, DefaultInterval)
	def(&c.DegradedAfter, DefaultDegradedAfter)
	def(&c.ResolveAfter, DefaultResolveAfter)
	def(&c.ReopenWindow, DefaultReopenWindow)
	def(&c.Retention, DefaultRetention)
	return c
}

// SourceRegistry tells which sources are currently registered (the recorder
// service implements it).
type SourceRegistry interface {
	Source(name string) (model.SourceSpec, bool)
}

// Notifier receives issue lifecycle events. Implementations must not block.
type Notifier interface {
	Emit(ctx context.Context, ev model.IssueEvent)
	// Remind is called after every evaluation with the open issues so that
	// channels with a repeat interval can re-send them.
	Remind(ctx context.Context, open []model.Issue)
}

// Deps are the engine's collaborators.
type Deps struct {
	Workloads repository.WorkloadRepository
	Events    repository.EventRepository
	K8sEvents repository.K8sEventRepository
	CronJobs  repository.CronJobRepository
	Issues    repository.IssueRepository
	Sources   SourceRegistry
	// Notifier may be nil (issues are tracked, nobody is told).
	Notifier Notifier
	Log      *zap.Logger
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Engine evaluates all workloads periodically and maintains persistent issues:
// one open issue per (workload, type).
type Engine struct {
	cfg Config
	d   Deps

	// falseSince tracks, per open issue, when its condition first stopped
	// holding. In-memory only: a restart simply restarts the resolve timer.
	falseSince map[int64]time.Time
	// degradedSince tracks since when a workload has been partially ready.
	degradedSince map[string]time.Time
}

// New builds an Engine.
func New(cfg Config, d Deps) *Engine {
	if d.Log == nil {
		d.Log = zap.NewNop()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{
		cfg: cfg.withDefaults(), d: d,
		falseSince: map[int64]time.Time{}, degradedSince: map[string]time.Time{},
	}
}

// Run evaluates every Interval until ctx is cancelled. The first evaluation
// happens one interval after start so collectors can sync first; issues that
// are already open in the database are kept (and not announced again).
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(e.cfg.Interval)
	defer tick.Stop()
	lastCleanup := e.d.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := e.Evaluate(ctx); err != nil && ctx.Err() == nil {
			e.d.Log.Error("issue evaluation failed", zap.Error(err))
		}
		if now := e.d.Now(); now.Sub(lastCleanup) >= cleanupPeriod {
			lastCleanup = now
			if n, err := e.d.Issues.DeleteResolvedBefore(ctx, now.Add(-e.cfg.Retention)); err != nil {
				e.d.Log.Warn("issue cleanup failed", zap.Error(err))
			} else if n > 0 {
				e.d.Log.Info("old resolved issues deleted", zap.Int64("count", n))
			}
		}
	}
}

// Evaluate runs one evaluation pass. A failed data read aborts the pass
// without changing any issue (a transient error must not resolve everything).
func (e *Engine) Evaluate(ctx context.Context) error {
	now := e.d.Now()

	active, err := e.d.Issues.ListActive(ctx, now.Add(-e.cfg.ReopenWindow))
	if err != nil {
		return fmt.Errorf("list active issues: %w", err)
	}
	open := map[key]*model.Issue{}
	recent := map[key]*model.Issue{}
	for _, i := range active {
		k := issueKey(i)
		if i.Status == model.IssueOpen {
			open[k] = i
		} else if _, ok := recent[k]; !ok { // newest resolved first
			recent[k] = i
		}
	}

	conds, err := e.collect(ctx, now, open)
	if err != nil {
		return err
	}

	keys := make([]key, 0, len(conds))
	for k := range conds {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].workload != keys[b].workload {
			return keys[a].workload < keys[b].workload
		}
		return keys[a].typ < keys[b].typ
	})

	for _, k := range keys {
		c := conds[k]
		switch {
		case open[k] != nil:
			i := open[k]
			delete(e.falseSince, i.ID)
			i.Severity, i.Title, i.Detail, i.LastSeen = c.Severity, c.Title, c.Detail, now
			if err := e.d.Issues.UpdateOpen(ctx, i); err != nil && !errors.Is(err, repository.ErrNotFound) {
				e.d.Log.Warn("issue update failed", zap.Int64("id", i.ID), zap.Error(err))
			}
		case recent[k] != nil:
			// Back within the reopen window: same row, counted as a flap, no
			// new "opened" notification.
			i := recent[k]
			i.Severity, i.Title, i.Detail, i.LastSeen = c.Severity, c.Title, c.Detail, now
			if err := e.d.Issues.Reopen(ctx, i); err != nil {
				e.d.Log.Warn("issue reopen failed", zap.Int64("id", i.ID), zap.Error(err))
				continue
			}
			delete(e.falseSince, i.ID)
			open[k] = i
		default:
			first := now
			if !c.Since.IsZero() && c.Since.Before(now) {
				first = c.Since
			}
			i := &model.Issue{
				SourceName: c.SourceName, WorkloadID: c.WorkloadID, Namespace: c.Namespace, Kind: c.Kind, Name: c.Name,
				Type: c.Type, Severity: c.Severity, Title: c.Title, Detail: c.Detail,
				Status: model.IssueOpen, FirstSeen: first, LastSeen: now,
			}
			if err := e.d.Issues.Create(ctx, i); err != nil {
				if !errors.Is(err, repository.ErrConflict) {
					e.d.Log.Warn("issue create failed", zap.String("workload", c.WorkloadID), zap.Error(err))
				}
				continue
			}
			open[k] = i
			e.emit(ctx, model.EventOpened, *i)
		}
	}

	// Open issues whose condition no longer holds.
	live := map[int64]bool{}
	for k, i := range open {
		if _, still := conds[k]; still {
			live[i.ID] = true
			continue
		}
		if spec, ok := e.d.Sources.Source(i.SourceName); ok && !spec.Enabled {
			// A paused source's data is frozen: keep its issues as they are.
			live[i.ID] = true
			delete(e.falseSince, i.ID)
			continue
		}
		since, ok := e.falseSince[i.ID]
		if !ok {
			e.falseSince[i.ID] = now
			live[i.ID] = true
			continue
		}
		if now.Sub(since) < e.cfg.ResolveAfter {
			live[i.ID] = true
			continue
		}
		if err := e.d.Issues.Resolve(ctx, i.ID, now); err != nil {
			if !errors.Is(err, repository.ErrNotFound) {
				e.d.Log.Warn("issue resolve failed", zap.Int64("id", i.ID), zap.Error(err))
			}
			delete(e.falseSince, i.ID)
			continue
		}
		delete(e.falseSince, i.ID)
		resolved := *i
		resolved.Status, resolved.ResolvedAt = model.IssueResolved, &now
		delete(open, k)
		e.emit(ctx, model.EventResolved, resolved)
	}
	for id := range e.falseSince {
		if !live[id] {
			delete(e.falseSince, id)
		}
	}

	if e.d.Notifier != nil && len(open) > 0 {
		list := make([]model.Issue, 0, len(open))
		for _, i := range open {
			list = append(list, *i)
		}
		sort.Slice(list, func(a, b int) bool { return list[a].ID < list[b].ID })
		e.d.Notifier.Remind(ctx, list)
	}
	return nil
}

func (e *Engine) emit(ctx context.Context, kind model.IssueEventKind, i model.Issue) {
	if e.d.Notifier != nil {
		e.d.Notifier.Emit(ctx, model.IssueEvent{Kind: kind, Issue: i})
	}
}

// systemNamespaces mirrors the collector's list; it is only used for Kubernetes
// events whose workload has no recorded containers.
var systemNamespaces = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true,
	"cilium": true, "calico-system": true, "cattle-system": true,
}

// collect returns every condition that holds right now, keyed by workload and type.
func (e *Engine) collect(ctx context.Context, now time.Time, open map[key]*model.Issue) (map[key]condition, error) {
	workloads, err := e.d.Workloads.List(ctx, repository.WorkloadFilter{})
	if err != nil {
		return nil, fmt.Errorf("list workloads: %w", err)
	}
	// Registered, enabled sources only; system workloads unless asked for.
	wanted := map[string]*model.WorkloadSummary{}
	known := map[string]bool{}
	registered := map[string]bool{}
	isRegistered := func(name string) bool {
		v, seen := registered[name]
		if !seen {
			spec, ok := e.d.Sources.Source(name)
			v = ok && spec.Enabled
			registered[name] = v
		}
		return v
	}
	for _, w := range workloads {
		if !isRegistered(w.SourceName) {
			continue
		}
		known[w.ID] = true
		if w.IsSystem && !e.cfg.IncludeSystem {
			continue
		}
		wanted[w.ID] = w
	}

	oomEv, err := e.recentEvents(ctx, now)
	if err != nil {
		return nil, err
	}

	out := map[key]condition{}
	add := func(c condition) { out[c.key()] = c }

	newDegraded := map[string]time.Time{}
	for _, w := range workloads {
		if wanted[w.ID] == nil || isJobKind(w.Kind) {
			continue
		}
		c, degraded := workloadCondition(w, oomEv[w.ID])
		if c == nil {
			continue
		}
		if degraded {
			since, ok := e.degradedSince[w.ID]
			if !ok {
				since = now
			}
			newDegraded[w.ID] = since
			// An already open issue stays (the gate only delays opening).
			if open[c.key()] == nil && now.Sub(since) < e.cfg.DegradedAfter {
				continue
			}
		}
		add(*c)
	}
	e.degradedSince = newDegraded

	if err := e.cronJobConditions(ctx, workloads, wanted, add); err != nil {
		return nil, err
	}

	// Kubernetes Warning events from the last 30 minutes.
	evs, err := e.d.K8sEvents.List(ctx, repository.K8sEventQuery{
		Type: "Warning", Since: now.Add(-k8sWindow), Limit: maxRows,
	})
	if err != nil {
		return nil, fmt.Errorf("list k8s events: %w", err)
	}
	byWorkload := map[string][]*model.K8sEvent{}
	for _, ev := range evs {
		if ev.WorkloadID != "" {
			byWorkload[ev.WorkloadID] = append(byWorkload[ev.WorkloadID], ev)
		}
	}
	for id, list := range byWorkload {
		var base condition
		if w := wanted[id]; w != nil {
			if w.ActiveInstances == 0 && !isJobKind(w.Kind) {
				// No pods left (deleted / scaled to zero): their scheduling,
				// pull or probe warnings no longer describe a live problem.
				continue
			}
			base = condition{SourceName: w.SourceName, WorkloadID: w.ID, Namespace: w.Namespace, Kind: w.Kind, Name: w.Name}
		} else if known[id] {
			continue // known but system
		} else {
			src, ns, kind, name, ok := model.SplitWorkloadID(id)
			if !ok || !isRegistered(src) || (systemNamespaces[ns] && !e.cfg.IncludeSystem) {
				continue
			}
			base = condition{SourceName: src, WorkloadID: id, Namespace: ns, Kind: kind, Name: name}
		}
		for _, c := range k8sConditions(base, list) {
			add(c)
		}
	}
	return out, nil
}

// recentEvents loads the OOM and crash events of the last 24h per workload.
func (e *Engine) recentEvents(ctx context.Context, now time.Time) (map[string]*recentEvents, error) {
	out := map[string]*recentEvents{}
	get := func(id string) *recentEvents {
		r := out[id]
		if r == nil {
			r = &recentEvents{}
			out[id] = r
		}
		return r
	}
	since := now.Add(-eventWindow)
	ooms, err := e.d.Events.FindRecent(ctx, repository.EventQuery{Type: model.EventTypeOOM, Since: since, Limit: maxRows})
	if err != nil {
		return nil, fmt.Errorf("list oom events: %w", err)
	}
	for _, ev := range ooms {
		if ev.WorkloadID == "" {
			continue
		}
		r := get(ev.WorkloadID)
		r.oom++
		r.note(ev.OccurredAt)
	}
	crashes, err := e.d.Events.FindRecent(ctx, repository.EventQuery{Type: model.EventTypeCrash, Since: since, Limit: maxRows})
	if err != nil {
		return nil, fmt.Errorf("list crash events: %w", err)
	}
	for _, ev := range crashes {
		if ev.WorkloadID == "" {
			continue
		}
		r := get(ev.WorkloadID)
		r.crash++
		r.note(ev.OccurredAt)
		if r.lastExit == nil || ev.OccurredAt.After(r.lastExitT) {
			code := ev.ExitCode
			r.lastExit, r.lastExitT = &code, ev.OccurredAt
		}
	}
	return out, nil
}

// cronJobConditions adds job_failed for CronJobs whose latest finished run failed.
func (e *Engine) cronJobConditions(ctx context.Context, workloads []*model.WorkloadSummary,
	wanted map[string]*model.WorkloadSummary, add func(condition)) error {
	var cron []*model.WorkloadSummary
	for _, w := range workloads {
		if w.Kind == model.WorkloadCronJob && wanted[w.ID] != nil {
			cron = append(cron, w)
		}
	}
	if len(cron) == 0 {
		return nil
	}
	latest, err := e.d.CronJobs.FindLatestPerCronJob(ctx, "", "")
	if err != nil {
		return fmt.Errorf("list cronjob executions: %w", err)
	}
	type ident struct{ src, ns, name string }
	newest := map[ident]*model.CronJobExecution{}
	for _, x := range latest {
		newest[ident{x.SourceName, x.Namespace, x.CronJobName}] = x
	}
	for _, w := range cron {
		last := newest[ident{w.SourceName, w.Namespace, w.Name}]
		if last == nil {
			continue
		}
		if executionState(last) == runOK {
			continue
		}
		// The newest run failed or is still running: look at the last finished one.
		recent, err := e.d.CronJobs.FindByCronJob(ctx, w.SourceName, w.Namespace, w.Name, 20)
		if err != nil {
			return fmt.Errorf("list cronjob history: %w", err)
		}
		var finished []*model.CronJobExecution
		for _, x := range recent {
			if executionState(x) != runRunning {
				finished = append(finished, x)
			}
		}
		if len(finished) == 0 {
			continue
		}
		sort.SliceStable(finished, func(a, b int) bool { return executionTime(finished[a]).After(executionTime(finished[b])) })
		lf := finished[0]
		if executionState(lf) != runFailed {
			continue
		}
		okRuns := 0
		for _, x := range finished {
			if executionState(x) == runOK {
				okRuns++
			}
		}
		c := condition{SourceName: w.SourceName, WorkloadID: w.ID, Namespace: w.Namespace, Kind: w.Kind, Name: w.Name,
			Type: model.IssueJobFailed, Severity: model.SeverityCritical,
			Title:  fmt.Sprintf("Last run failed (exit %d)", lf.ExitCode),
			Detail: fmt.Sprintf("%d/%d recent runs succeeded", okRuns, len(finished)),
			Since:  executionTime(lf),
		}
		add(c)
	}
	return nil
}
