package model

import (
	"strings"
	"time"
)

// WorkloadID builds the stable identifier of a workload:
// "source:namespace:kind:name". Kubernetes names and KYMO source names cannot
// contain ':', so the ID is unambiguous. It returns "" when name is empty
// (e.g. a standalone Docker container that belongs to no workload).
func WorkloadID(source, namespace, kind, name string) string {
	if name == "" || kind == "" || kind == WorkloadUnknown {
		return ""
	}
	return strings.Join([]string{source, namespace, kind, name}, ":")
}

// WorkloadSummary aggregates every recorded container instance of a workload
// (all pods of a Deployment across rollouts, all containers of a Compose
// project, …). History and stability survive pod replacement because they are
// keyed by the workload, not by the pod.
type WorkloadSummary struct {
	ID         string  `json:"id"`
	SourceName string  `json:"source_name"`
	Runtime    Runtime `json:"runtime"`
	Namespace  string  `json:"namespace,omitempty"`
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	IsSystem   bool    `json:"is_system"`

	// Containers are the distinct container names in the workload's pods.
	Containers []string `json:"containers"`
	// Images are the distinct images of currently active instances.
	Images []string `json:"images"`

	// Instances counts pods (Kubernetes) or containers (Compose) ever recorded;
	// ActiveInstances those not stopped; RunningInstances those running.
	Instances        int `json:"instances"`
	ActiveInstances  int `json:"active_instances"`
	RunningInstances int `json:"running_instances"`

	// Status is the worst status among active instances: crashing > running >
	// stopped (when nothing is active).
	Status ContainerStatus `json:"status"`

	// Lifetime counters summed over every instance. RestartCount excludes each
	// container's first boot, so a rollout's fresh pods do not count as restarts.
	StartCount   int `json:"start_count"`
	RestartCount int `json:"restart_count"`
	CrashCount   int `json:"crash_count"`
	OOMCount     int `json:"oom_count"`

	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`

	// Score is the latest workload stability score (nil until computed).
	Score *float64 `json:"score"`
}

// WorkloadScore is a stability score computed over all instances of a workload.
type WorkloadScore struct {
	ID                  int64     `json:"id"`
	WorkloadID          string    `json:"workload_id"`
	SourceName          string    `json:"source_name"`
	Score               float64   `json:"score"`
	RestartPenalty      float64   `json:"restart_penalty"`
	CrashPenalty        float64   `json:"crash_penalty"`
	OOMPenalty          float64   `json:"oom_penalty"`
	VolatilityPenalty   float64   `json:"volatility_penalty"`
	AvailabilityPenalty float64   `json:"availability_penalty"`
	Restarts            int       `json:"restarts"`
	Crashes             int       `json:"crashes"`
	OOMs                int       `json:"ooms"`
	CalculatedAt        time.Time `json:"calculated_at"`
}
