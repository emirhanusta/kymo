package model

import "time"

// SourceHistory summarises everything recorded for one source_name.
type SourceHistory struct {
	Name        string    `json:"name"`
	Workloads   int       `json:"workloads"`
	Containers  int       `json:"containers"`
	CronJobRuns int       `json:"cronjob_runs"`
	K8sEvents   int       `json:"k8s_events"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}
