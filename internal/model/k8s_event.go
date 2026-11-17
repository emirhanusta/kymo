package model

import "time"

// K8sEvent is a persisted Kubernetes Event (core/v1). Cluster events expire
// after about an hour, so KYMO records the interesting ones.
type K8sEvent struct {
	ID         int64  `json:"id"`
	SourceName string `json:"source_name"`
	// UID is the Event object's UID; (SourceName, UID, FirstSeen) is unique.
	UID       string `json:"uid"`
	Namespace string `json:"namespace"`
	// Kind and Name identify the involved object.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// WorkloadID is the owning workload (see WorkloadID), "" when unknown.
	WorkloadID string    `json:"workload_id"`
	Type       string    `json:"type"` // "Normal" or "Warning"
	Reason     string    `json:"reason"`
	Message    string    `json:"message"`
	Count      int       `json:"count"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	Component  string    `json:"component"`
}

// K8sEventReason is one row of the reason aggregation.
type K8sEventReason struct {
	Reason string `json:"reason"`
	Type   string `json:"type"`
	Count  int    `json:"count"`
}
