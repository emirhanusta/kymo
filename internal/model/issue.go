package model

import (
	"strings"
	"time"
)

// IssueType identifies the kind of problem an Issue reports. There is at most
// one open issue per (workload, type).
type IssueType string

// Issue types.
const (
	IssueCrashLoop    IssueType = "crash_loop"
	IssueOOM          IssueType = "oom"
	IssueDegraded     IssueType = "degraded"
	IssueJobFailed    IssueType = "job_failed"
	IssueCantSchedule IssueType = "cant_schedule"
	IssueImagePull    IssueType = "image_pull"
	IssueEvicted      IssueType = "evicted"
	IssueVolumeMount  IssueType = "volume_mount"
	IssueProbeFailing IssueType = "probe_failing"
	// IssueError is a burst of captured error log lines (opt-in log capture).
	IssueError IssueType = "error_rate"
)

// IssueTypes lists every issue type.
var IssueTypes = []IssueType{
	IssueCrashLoop, IssueOOM, IssueDegraded, IssueJobFailed,
	IssueCantSchedule, IssueImagePull, IssueEvicted, IssueVolumeMount, IssueProbeFailing, IssueError,
}

// ValidIssueType reports whether t is a known issue type.
func ValidIssueType(t string) bool {
	for _, k := range IssueTypes {
		if string(k) == t {
			return true
		}
	}
	return false
}

// IssueSeverity ranks an issue.
type IssueSeverity string

// Issue severities.
const (
	SeverityCritical IssueSeverity = "critical"
	SeverityWarning  IssueSeverity = "warning"
)

// SeverityAtLeast reports whether s is at least as severe as min.
func SeverityAtLeast(s, min IssueSeverity) bool {
	rank := func(v IssueSeverity) int {
		if v == SeverityCritical {
			return 2
		}
		return 1
	}
	return rank(s) >= rank(min)
}

// IssueStatus is the lifecycle state of an issue.
type IssueStatus string

// Issue statuses.
const (
	IssueOpen     IssueStatus = "open"
	IssueResolved IssueStatus = "resolved"
)

// Issue is a detected problem of a workload (see service/issues).
type Issue struct {
	ID         int64         `json:"id"`
	SourceName string        `json:"source_name"`
	WorkloadID string        `json:"workload_id"`
	Namespace  string        `json:"namespace"`
	Kind       string        `json:"kind"`
	Name       string        `json:"name"`
	Type       IssueType     `json:"type"`
	Severity   IssueSeverity `json:"severity"`
	Title      string        `json:"title"`
	Detail     string        `json:"detail"`
	Status     IssueStatus   `json:"status"`
	FirstSeen  time.Time     `json:"first_seen"`
	LastSeen   time.Time     `json:"last_seen"`
	ResolvedAt *time.Time    `json:"resolved_at"`
	// Flaps counts how often the issue was reopened shortly after resolving.
	Flaps int `json:"flaps"`

	// Notification bookkeeping (not exposed).
	LastNotifiedAt *time.Time `json:"-"`
	NotifyCount    int        `json:"-"`
}

// IssueSummary counts open issues for UI badges.
type IssueSummary struct {
	Open     int            `json:"open"`
	Critical int            `json:"critical"`
	Warning  int            `json:"warning"`
	BySource map[string]int `json:"by_source"`
}

// IssueEventKind is what happened to an issue.
type IssueEventKind string

// Issue event kinds (also the delivery log's event names).
const (
	EventOpened   IssueEventKind = "opened"
	EventResolved IssueEventKind = "resolved"
	EventReminder IssueEventKind = "reminder"
	EventTest     IssueEventKind = "test"
)

// IssueEvent is one notification-worthy change of an issue.
type IssueEvent struct {
	Kind  IssueEventKind
	Issue Issue
}

// SplitWorkloadID parses "source:namespace:kind:name" (see WorkloadID).
func SplitWorkloadID(id string) (source, namespace, kind, name string, ok bool) {
	p := strings.SplitN(id, ":", 4)
	if len(p) != 4 || p[3] == "" {
		return "", "", "", "", false
	}
	return p[0], p[1], p[2], p[3], true
}
