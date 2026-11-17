// Package model contains the core domain types for the KYMO service.
// All persistence layers and application services operate on these types.
package model

import "time"

// Workload kind constants identify the Kubernetes controller type that owns a Pod.
const (
	WorkloadDeployment  = "Deployment"
	WorkloadDaemonSet   = "DaemonSet"
	WorkloadStatefulSet = "StatefulSet"
	WorkloadJob         = "Job"
	WorkloadCronJob     = "CronJob"
	WorkloadPod         = "Pod"
	WorkloadUnknown     = "unknown"
)

// Runtime identifies the container orchestration technology that manages a container.
type Runtime string

const (
	// RuntimeKubernetes indicates a container managed by a Kubernetes cluster.
	RuntimeKubernetes Runtime = "kubernetes"
)

// ContainerStatus represents the last-known operational state of a container.
type ContainerStatus string

const (
	// StatusRunning means the container process is actively executing.
	StatusRunning ContainerStatus = "running"
	// StatusStopped means the container exited cleanly (exit code 0).
	StatusStopped ContainerStatus = "stopped"
	// StatusCrashing means the container is restarting due to non-zero exits.
	StatusCrashing ContainerStatus = "crashing"
	// StatusPaused means the container process has been suspended.
	StatusPaused ContainerStatus = "paused"
	// StatusUnknown means the current state cannot be determined.
	StatusUnknown ContainerStatus = "unknown"
)

// EventType classifies a container lifecycle transition.
type EventType string

const (
	// EventTypeStart is emitted when a container process begins.
	EventTypeStart EventType = "start"
	// EventTypeStop is emitted when a container is stopped gracefully.
	EventTypeStop EventType = "stop"
	// EventTypeDie is emitted when a container process exits for any reason.
	EventTypeDie EventType = "die"
	// EventTypeCrash is emitted when a container exits with a non-zero code.
	EventTypeCrash EventType = "crash"
	// EventTypeOOM is emitted when the kernel kills a container for exceeding its memory limit.
	EventTypeOOM EventType = "oom"
	// EventTypePause is emitted when a container's cgroup is frozen.
	EventTypePause EventType = "pause"
	// EventTypeResume is emitted when a previously paused container is unfrozen.
	EventTypeResume EventType = "resume"
)

// Container holds the aggregated identity and lifecycle statistics for a single
// observed container. It is updated in-place via upsert so it always reflects
// the current state derived from all collected events.
type Container struct {
	// ID is the runtime-assigned unique identifier (Kubernetes UID).
	ID string `json:"id" db:"id"`

	// Runtime identifies whether this container runs under (currently always Kubernetes).
	Runtime Runtime `json:"runtime" db:"runtime"`

	// Name is the human-readable Pod/container name pair.
	Name string `json:"name" db:"name"`

	// Image is the fully-qualified image reference including tag or digest.
	Image string `json:"image" db:"image"`

	// Namespace is the Kubernetes namespace in which the container runs.
	Namespace string `json:"namespace,omitempty" db:"namespace"`

	// NodeName is the Kubernetes node (hostname) that schedules the container.
	NodeName string `json:"node_name,omitempty" db:"node_name"`

	// FirstSeenAt is the timestamp when the container was first observed by the collector.
	FirstSeenAt time.Time `json:"first_seen_at" db:"first_seen_at"`

	// LastSeenAt is the timestamp of the most recent observation or event for this container.
	LastSeenAt time.Time `json:"last_seen_at" db:"last_seen_at"`

	// StartCount is the cumulative number of times the container has been started.
	StartCount int `json:"start_count" db:"start_count"`

	// CrashCount is the cumulative number of non-zero exit events recorded for this container.
	CrashCount int `json:"crash_count" db:"crash_count"`

	// OOMCount is the cumulative number of out-of-memory kill events.
	OOMCount int `json:"oom_count" db:"oom_count"`

	// CurrentStatus reflects the most recently observed operational status.
	CurrentStatus ContainerStatus `json:"current_status" db:"current_status"`

	// SourceName is the name of the collector source that manages this container
	// (e.g. "prod-k8s"). Set from config.Source.Name.
	SourceName string `json:"source_name" db:"source_name"`

	// DeletedAt is set when a container is soft-deleted (archived). A nil value
	// means the container is active. The container is automatically restored when
	// the collector sees it again after archiving.
	DeletedAt *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`

	// Ignored is true when the container has been permanently ignored. Ignored
	// containers are excluded from all active queries and will not be re-added
	// even if the collector observes them again.
	Ignored bool `json:"ignored" db:"ignored"`

	// WorkloadKind is the Kubernetes controller kind that owns this container's pod
	// (e.g. "Deployment", "DaemonSet", "StatefulSet", "Job", "CronJob", "Pod").
	// Set to "unknown" for containers whose owner cannot be determined.
	WorkloadKind string `json:"workload_kind" db:"workload_kind"`

	// WorkloadName is the name of the Kubernetes workload that owns this pod
	// (e.g. the Deployment name for a pod owned by a ReplicaSet owned by a Deployment).
	WorkloadName string `json:"workload_name" db:"workload_name"`

	// IsSystem is true when the container runs in a known system namespace
	// (kube-system, kube-public, etc.) or has a well-known system name prefix.
	IsSystem bool `json:"is_system" db:"is_system"`

	// WorkloadID is model.WorkloadID(SourceName, Namespace, WorkloadKind,
	// WorkloadName); empty for containers that belong to no workload.
	WorkloadID string `json:"workload_id,omitempty" db:"workload_id"`

	// Revision identifies the rollout the container's pod belongs to
	// (pod-template-hash, controller-revision-hash, or the Job name). Empty
	// when unknown; once set it is never cleared.
	Revision string `json:"revision,omitempty" db:"revision"`

	// Score is the most recently computed stability score (0–100), populated by
	// read queries via a lateral join. Nil when no score has been computed yet.
	// It is never written through the containers table.
	Score *float64 `json:"score" db:"-"`
}

// CronJobExecution records a single execution of a Kubernetes CronJob.
// It is persisted in the cronjob_executions hypertable and linked to the
// CronJob by source/namespace/name rather than a foreign key.
type CronJobExecution struct {
	// ID is the auto-assigned surrogate primary key.
	ID int64 `json:"id"`

	// SourceName is the name of the collector source that observed this execution.
	SourceName string `json:"source_name"`

	// Namespace is the Kubernetes namespace of the CronJob and its Job.
	Namespace string `json:"namespace"`

	// CronJobName is the name of the parent CronJob object.
	CronJobName string `json:"cronjob_name"`

	// JobName is the name of the specific Job created for this execution.
	JobName string `json:"job_name"`

	// PodName is the name of the Pod that ran the job container.
	PodName string `json:"pod_name"`

	// StartedAt is the time the job pod started (from pod.Status.StartTime).
	StartedAt *time.Time `json:"started_at,omitempty"`

	// CompletedAt is the time the container terminated (from ContainerStateTerminated.FinishedAt).
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// DurationSeconds is the elapsed seconds between StartedAt and CompletedAt.
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`

	// Succeeded is true when the container exited with code 0.
	Succeeded *bool `json:"succeeded,omitempty"`

	// ExitCode is the container process exit code.
	ExitCode int `json:"exit_code"`

	// SampledAt is the wall-clock time when this execution record was created.
	SampledAt time.Time `json:"sampled_at"`
}

// LifecycleEvent records a single state-change transition for a container,
// such as a start, stop, crash, or OOM kill. Events are immutable once written.
type LifecycleEvent struct {
	// ID is the auto-assigned surrogate primary key.
	ID int64 `json:"id" db:"id"`

	// ContainerID is a foreign key referencing the parent Container.
	ContainerID string `json:"container_id" db:"container_id"`

	// EventType classifies the nature of the transition.
	EventType EventType `json:"event_type" db:"event_type"`

	// ExitCode is the process exit code. Zero for non-exit events (start, pause, resume).
	ExitCode int `json:"exit_code" db:"exit_code"`

	// NodeName records the host or Kubernetes node where the event occurred.
	NodeName string `json:"node_name,omitempty" db:"node_name"`

	// Metadata stores runtime-specific supplementary data as an arbitrary key-value map.
	// Examples: Kubernetes restart reason, label snapshots.
	Metadata map[string]any `json:"metadata,omitempty" db:"metadata"`

	// OccurredAt is the authoritative event timestamp reported by the runtime.
	OccurredAt time.Time `json:"occurred_at" db:"occurred_at"`

	// SourceName is the name of the collector source that produced this event.
	SourceName string `json:"source_name" db:"source_name"`

	// ContainerName and Runtime are denormalised from the containers table by
	// read queries (e.g. the recent-events feed). They are not persisted.
	ContainerName string  `json:"container_name,omitempty" db:"-"`
	Runtime       Runtime `json:"runtime,omitempty" db:"-"`
	// WorkloadID is the owning workload of the container, populated by the
	// recent-events feed.
	WorkloadID string `json:"workload_id,omitempty" db:"-"`

	// HasLogs is true when a crash-log snapshot exists for this termination.
	// Populated by read queries; not persisted on the event row.
	HasLogs bool `json:"has_logs" db:"-"`
}

// CrashLog is a snapshot of the tail of a container instance's logs captured
// when it crashed or was OOM-killed. OccurredAt equals the termination's
// FinishedAt, i.e. the timestamp of the matching crash/OOM lifecycle events.
type CrashLog struct {
	ID          int64     `json:"id"`
	ContainerID string    `json:"container_id"`
	SourceName  string    `json:"source_name"`
	OccurredAt  time.Time `json:"occurred_at"`
	Reason      string    `json:"reason"`
	ExitCode    int       `json:"exit_code"`
	// Previous is true when the logs came from the previous container instance.
	Previous  bool   `json:"previous"`
	Lines     string `json:"lines"`
	LineCount int    `json:"line_count"`
	// Truncated is true when the byte or line limit cut the snapshot short.
	Truncated  bool      `json:"truncated"`
	CapturedAt time.Time `json:"captured_at"`
}

// ResourceSample is a point-in-time snapshot of a container's resource consumption,
// collected by polling the runtime's stats API at a configurable interval.
type ResourceSample struct {
	// ID is the auto-assigned surrogate primary key.
	ID int64 `json:"id" db:"id"`

	// ContainerID is a foreign key referencing the parent Container.
	ContainerID string `json:"container_id" db:"container_id"`

	// CPUPercent is the CPU utilisation expressed as a percentage of total available host CPU.
	CPUPercent float64 `json:"cpu_percent" db:"cpu_percent"`

	// MemBytes is the absolute resident memory usage in bytes.
	MemBytes int64 `json:"mem_bytes" db:"mem_bytes"`

	// MemPercent is memory usage as a percentage of the container's memory limit.
	MemPercent float64 `json:"mem_percent" db:"mem_percent"`

	// NetRxBytes is the cumulative bytes received over all network interfaces since container start.
	NetRxBytes int64 `json:"net_rx_bytes" db:"net_rx_bytes"`

	// NetTxBytes is the cumulative bytes transmitted over all network interfaces since container start.
	NetTxBytes int64 `json:"net_tx_bytes" db:"net_tx_bytes"`

	// BlockReadBytes is the cumulative bytes read from all block devices since container start.
	BlockReadBytes int64 `json:"block_read_bytes" db:"block_read_bytes"`

	// BlockWriteBytes is the cumulative bytes written to all block devices since container start.
	BlockWriteBytes int64 `json:"block_write_bytes" db:"block_write_bytes"`

	// SampledAt is the wall-clock timestamp at the moment this sample was collected.
	SampledAt time.Time `json:"sampled_at" db:"sampled_at"`

	// SourceName is the name of the collector source that produced this sample.
	SourceName string `json:"source_name" db:"source_name"`
}

// StabilityScore is the computed health and reliability score for a container
// over a specific look-back window. Each row captures the individual penalty
// components so score breakdowns can be explained to operators.
//
// Scoring formula:
//
//	Score = max(0, 100 − RestartPenalty − CrashPenalty − OOMPenalty
//	                    − VolatilityPenalty − AvailabilityPenalty)
type StabilityScore struct {
	// ID is the auto-assigned surrogate primary key.
	ID int64 `json:"id" db:"id"`

	// ContainerID is a foreign key referencing the scored Container.
	ContainerID string `json:"container_id" db:"container_id"`

	// Score is the overall stability score in the range [0.0, 100.0].
	// Higher values indicate a more stable container.
	Score float64 `json:"score" db:"score"`

	// RestartPenalty is the deduction applied for restart frequency above the baseline.
	RestartPenalty float64 `json:"restart_penalty" db:"restart_penalty"`

	// CrashPenalty is the deduction applied for non-zero exit events within the window.
	CrashPenalty float64 `json:"crash_penalty" db:"crash_penalty"`

	// OOMPenalty is the deduction applied for out-of-memory kill events within the window.
	OOMPenalty float64 `json:"oom_penalty" db:"oom_penalty"`

	// VolatilityPenalty is the deduction for high variance in CPU or memory usage.
	VolatilityPenalty float64 `json:"volatility_penalty" db:"volatility_penalty"`

	// AvailabilityPenalty is the deduction for observed uptime gaps within the scoring window.
	AvailabilityPenalty float64 `json:"availability_penalty" db:"availability_penalty"`

	// CalculatedAt is the timestamp when this score was computed.
	CalculatedAt time.Time `json:"calculated_at" db:"calculated_at"`

	// SourceName is the name of the collector source for the scored container.
	SourceName string `json:"source_name" db:"source_name"`
}
