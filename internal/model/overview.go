package model

import "time"

// ActivityBucket counts lifecycle events of one hour.
type ActivityBucket struct {
	Start   time.Time `json:"start"`
	Starts  int       `json:"starts"`
	Crashes int       `json:"crashes"`
	OOMs    int       `json:"ooms"`
}

// Activity is the hourly lifecycle-event histogram behind the Overview chart.
type Activity struct {
	// Buckets are consecutive hours, oldest first; the last one is the current (partial) hour.
	Buckets []ActivityBucket `json:"buckets"`
	// PreviousCrashes is crash + OOM events in the window just before the first bucket (same length).
	PreviousCrashes int `json:"previous_crashes"`
}

// Change is a rollout of a new pod-template revision of a workload.
type Change struct {
	WorkloadID       string    `json:"workload_id"`
	Kind             string    `json:"kind"`
	Name             string    `json:"name"`
	Namespace        string    `json:"namespace"`
	SourceName       string    `json:"source_name"`
	Revision         string    `json:"revision"`
	PreviousRevision string    `json:"previous_revision"`
	Image            string    `json:"image"`
	PreviousImage    string    `json:"previous_image"`
	At               time.Time `json:"at"`
}
