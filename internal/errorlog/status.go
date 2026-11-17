package errorlog

import "github.com/emirhanusta/kymo/internal/model"

// StatusProvider is implemented by a source's collector while it captures logs.
type StatusProvider interface {
	// LogCaptureStatus reports the live state of the source.
	LogCaptureStatus() model.LogCaptureSourceStatus
	// WorkloadCapture reports the containers currently streamed for a
	// workload and whether one of its pods carries the opt-in annotation.
	WorkloadCapture(workloadID string) (streams int, annotated bool)
}
