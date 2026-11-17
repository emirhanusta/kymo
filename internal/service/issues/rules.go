package issues

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// condition is one problem that is true right now.
type condition struct {
	SourceName string
	WorkloadID string
	Namespace  string
	Kind       string
	Name       string
	Type       model.IssueType
	Severity   model.IssueSeverity
	Title      string
	Detail     string
	// Since is when the problem started, when known (zero otherwise).
	Since time.Time
}

type key struct {
	workload string
	typ      model.IssueType
}

func (c condition) key() key { return key{c.WorkloadID, c.Type} }

func issueKey(i *model.Issue) key { return key{i.WorkloadID, i.Type} }

func base(w *model.WorkloadSummary) condition {
	return condition{SourceName: w.SourceName, WorkloadID: w.ID, Namespace: w.Namespace, Kind: w.Kind, Name: w.Name}
}

func isJobKind(kind string) bool { return kind == model.WorkloadCronJob || kind == model.WorkloadJob }

// recentEvents summarises a workload's OOM / crash lifecycle events inside the
// 24h window.
type recentEvents struct {
	oom       int
	crash     int
	lastExit  *int
	firstAt   time.Time
	lastExitT time.Time
}

func (r *recentEvents) note(at time.Time) {
	if r.firstAt.IsZero() || at.Before(r.firstAt) {
		r.firstAt = at
	}
}

// workloadCondition applies the status rules of one long-running workload and
// returns the headline condition (or nil) plus whether it is merely degraded.
// The rules mirror the former client-side derivation: OOM events win over a
// crash loop, which wins over plain degradation.
func workloadCondition(w *model.WorkloadSummary, ev *recentEvents) (c *condition, degraded bool) {
	oom := 0
	if ev != nil {
		oom = ev.oom
	}
	crashing := w.Status == model.StatusCrashing
	notReady := w.ActiveInstances > 0 && w.RunningInstances < w.ActiveInstances
	ready := fmt.Sprintf("%d/%d pods ready", w.RunningInstances, w.ActiveInstances)

	out := base(w)
	switch {
	case oom > 0:
		out.Type, out.Severity = model.IssueOOM, model.SeverityCritical
		out.Title = fmt.Sprintf("OOMKilled ×%d in 24h", oom)
		switch {
		case crashing:
			out.Detail = "crash looping"
		case notReady:
			out.Detail = ready
		}
		out.Since = ev.firstAt
		return &out, false
	case crashing:
		out.Severity = model.SeverityCritical
		if w.OOMCount > 0 {
			// Crashing with lifetime OOM kills but none in the window is still an OOM kill.
			out.Type, out.Title = model.IssueOOM, "OOMKilled"
		} else {
			out.Type, out.Title = model.IssueCrashLoop, "Crash loop"
			if ev != nil && ev.lastExit != nil {
				out.Title = fmt.Sprintf("Crash loop · exit %d", *ev.lastExit)
			}
		}
		if notReady {
			out.Detail = ready
		}
		if ev != nil {
			out.Since = ev.firstAt
		}
		return &out, false
	case notReady:
		out.Type, out.Severity = model.IssueDegraded, model.SeverityWarning
		out.Title = fmt.Sprintf("Degraded (%d/%d ready)", w.RunningInstances, w.ActiveInstances)
		out.Detail = fmt.Sprintf("%d of %d pods running", w.RunningInstances, w.ActiveInstances)
		return &out, true
	}
	return nil, false
}

// classifyK8sWarning maps a Warning event to an issue type ("" when not reported).
func classifyK8sWarning(e *model.K8sEvent) model.IssueType {
	msg := strings.ToLower(e.Message)
	switch e.Reason {
	case "FailedScheduling":
		return model.IssueCantSchedule
	case "ErrImagePull", "ImagePullBackOff":
		return model.IssueImagePull
	case "Failed":
		if strings.Contains(msg, "pull") || strings.Contains(msg, "image") {
			return model.IssueImagePull
		}
	case "BackOff":
		if strings.Contains(msg, "pull") {
			return model.IssueImagePull
		}
	case "Evicted":
		return model.IssueEvicted
	case "FailedMount", "FailedAttachVolume":
		return model.IssueVolumeMount
	case "Unhealthy":
		return model.IssueProbeFailing
	}
	return ""
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// k8sConditions turns the Warning events of one workload into conditions, one
// per issue type.
func k8sConditions(w condition, events []*model.K8sEvent) []condition {
	byType := map[model.IssueType][]*model.K8sEvent{}
	for _, e := range events {
		if t := classifyK8sWarning(e); t != "" {
			byType[t] = append(byType[t], e)
		}
	}
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, string(t))
	}
	sort.Strings(types)

	var out []condition
	for _, ts := range types {
		t := model.IssueType(ts)
		evs := byType[t]
		newest, since, n := evs[0], evs[0].FirstSeen, 0
		for _, e := range evs {
			if e.LastSeen.After(newest.LastSeen) {
				newest = e
			}
			if e.FirstSeen.Before(since) {
				since = e.FirstSeen
			}
			n += max(1, e.Count)
		}
		c := w
		c.Type, c.Since = t, since
		c.Detail = truncate(firstLine(newest.Message), 300)
		switch t {
		case model.IssueCantSchedule:
			c.Severity, c.Title = model.SeverityCritical, "Can't schedule"
			if msg := firstLine(newest.Message); msg != "" {
				c.Title = "Can't schedule — " + truncate(msg, 140)
				c.Detail = truncate(newest.Message, 500)
			}
		case model.IssueImagePull:
			c.Severity, c.Title = model.SeverityCritical, "Image pull failing"
		case model.IssueEvicted:
			c.Severity, c.Title = model.SeverityCritical, fmt.Sprintf("Evicted ×%d", n)
		case model.IssueVolumeMount:
			c.Severity, c.Title = model.SeverityCritical, "Volume mount failing"
		default:
			c.Severity, c.Title = model.SeverityWarning, fmt.Sprintf("Probe failing ×%d", n)
		}
		out = append(out, c)
	}
	return out
}

// runState classifies one CronJob execution.
type runState int

const (
	runOK runState = iota
	runFailed
	runRunning
)

func executionState(e *model.CronJobExecution) runState {
	switch {
	case e.Succeeded != nil && *e.Succeeded:
		return runOK
	case e.CompletedAt != nil || e.Succeeded != nil || e.ExitCode != 0:
		return runFailed
	case e.StartedAt != nil:
		return runRunning
	}
	return runFailed
}

func executionTime(e *model.CronJobExecution) time.Time {
	switch {
	case e.CompletedAt != nil:
		return *e.CompletedAt
	case e.StartedAt != nil:
		return *e.StartedAt
	}
	return e.SampledAt
}
