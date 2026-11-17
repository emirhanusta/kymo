package model

import "time"

// SourceHistory summarises everything recorded for one source_name.
type SourceHistory struct {
	Name        string    `json:"name"`
	Workloads   int       `json:"workloads"`
	Containers  int       `json:"containers"`
	CronJobRuns int       `json:"cronjob_runs"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}
