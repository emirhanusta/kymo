package model

import "time"

// Log-capture target origins.
const (
	// TargetOriginUI marks a target created through the API / UI.
	TargetOriginUI = "ui"
	// TargetOriginConfig marks a target mirrored from config.yaml (read-only).
	TargetOriginConfig = "config"
)

// Error levels of a captured log entry.
const (
	ErrorLevelError = "error"
	ErrorLevelFatal = "fatal"
)

// LogCaptureTarget opts a namespace (WorkloadID == "") or one workload of a
// source into error-log capture.
type LogCaptureTarget struct {
	ID         int64     `json:"id"`
	SourceName string    `json:"source_name"`
	Namespace  string    `json:"namespace"`
	WorkloadID string    `json:"workload_id"`
	Origin     string    `json:"origin"`
	CreatedAt  time.Time `json:"created_at"`
}

// ErrorSignature is a group of similar error log lines of one workload.
type ErrorSignature struct {
	ID         string    `json:"id"`
	SourceName string    `json:"source_name"`
	WorkloadID string    `json:"workload_id"`
	Namespace  string    `json:"namespace"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Level      string    `json:"level"`
	Template   string    `json:"template"`
	Sample     string    `json:"sample"`
	Count      int64     `json:"count"`
	TotalCount int64     `json:"total_count"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	IsNew      bool      `json:"is_new"`
	Pods       int       `json:"pods"`
	Buckets    []int64   `json:"buckets"`
}

// ErrorOccurrence is one stored (masked) error entry of a signature.
type ErrorOccurrence struct {
	Time        time.Time `json:"time"`
	ContainerID string    `json:"container_id"`
	Pod         string    `json:"pod"`
	Container   string    `json:"container"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
}

// ErrorBucketDelta is the number of occurrences of a signature within one
// minute, with the pods that produced them.
type ErrorBucketDelta struct {
	Bucket time.Time
	Count  int
	Pods   []string
}

// ErrorSignatureDelta is everything the collector aggregated for one
// signature since the last flush.
type ErrorSignatureDelta struct {
	ID          string
	SourceName  string
	WorkloadID  string
	Namespace   string
	Kind        string
	Name        string
	Level       string
	Template    string
	Sample      string
	FirstSeen   time.Time
	LastSeen    time.Time
	Total       int
	Buckets     []ErrorBucketDelta
	Occurrences []ErrorOccurrence
}

// ErrorBatch is one flush of aggregated error log entries.
type ErrorBatch struct {
	Signatures []ErrorSignatureDelta
}

// WorkloadErrorRate is the number of captured error entries of a workload in a
// window, with its most frequent signature (used by the issue engine).
type WorkloadErrorRate struct {
	WorkloadID  string
	SourceName  string
	Namespace   string
	Kind        string
	Name        string
	Count       int64
	TopTemplate string
	TopCount    int64
}

// LogCaptureSourceStatus is the live capture state of one source.
type LogCaptureSourceStatus struct {
	SourceName   string `json:"source_name"`
	Streams      int    `json:"streams"`
	MaxStreams   int    `json:"max_streams"`
	DroppedLines int64  `json:"dropped_lines"`
	LastError    string `json:"last_error"`
}

// LogCaptureStatus is the response of GET /log-capture/status.
type LogCaptureStatus struct {
	Enabled bool                     `json:"enabled"`
	Sources []LogCaptureSourceStatus `json:"sources"`
}

// AnnotationCaptureErrorLogs is the pod (template) annotation that opts a
// workload into error-log capture when set to "true".
const AnnotationCaptureErrorLogs = "kymo.io/capture-error-logs"

// MatchesTarget reports whether any target of the source covers the workload
// (a namespace target has an empty WorkloadID).
func MatchesTarget(targets []LogCaptureTarget, source, namespace, workloadID string) bool {
	for _, t := range targets {
		if t.SourceName != source || t.Namespace != namespace {
			continue
		}
		if t.WorkloadID == "" || t.WorkloadID == workloadID {
			return true
		}
	}
	return false
}
