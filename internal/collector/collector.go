// Package collector defines the common interface that all runtime-specific
// container collectors must implement, along with the shared event types
// that flow from collectors into the recorder.
package collector

import (
	"context"

	"github.com/emirhanusta/kymo/internal/model"
)

// CollectorEventKind classifies the payload carried by a CollectorEvent.
type CollectorEventKind string

const (
	// CollectorEventLifecycle indicates the event carries a LifecycleEvent payload.
	CollectorEventLifecycle CollectorEventKind = "lifecycle"
	// CollectorEventResource indicates the event carries a ResourceSample payload.
	CollectorEventResource CollectorEventKind = "resource"
	// CollectorEventCronJob indicates the event carries a CronJobExecution payload.
	CollectorEventCronJob CollectorEventKind = "cronjob"
	// CollectorEventState carries a point-in-time snapshot of a container's state
	// (e.g. emitted when a collector seeds its view of the runtime on startup).
	// It updates the container row but records no lifecycle event and does not
	// touch the start/crash/OOM counters.
	CollectorEventState CollectorEventKind = "state"
	// CollectorEventSync carries the full set of container IDs currently known to
	// the runtime for this source (SyncIDs). Containers stored for the source that
	// are absent from the set and still marked active are marked stopped.
	CollectorEventSync CollectorEventKind = "sync"
)

// CollectorEvent is the unit of data emitted by a Collector onto its Events channel.
// Only one of Lifecycle, Sample or CronJobExec will be non-nil, depending on Kind.
// Container is nil for CollectorEventSync.
type CollectorEvent struct {
	// Kind identifies whether this event carries lifecycle or resource data.
	Kind CollectorEventKind

	// SourceName is the name of the collector source that produced this event.
	// It matches the Name field from config.Source and is copied into every
	// persisted model struct (Container, LifecycleEvent, ResourceSample, StabilityScore).
	SourceName string

	// Container is the container associated with this event. Non-nil for every
	// kind except CollectorEventSync.
	Container *model.Container

	// Lifecycle holds event data when Kind == CollectorEventLifecycle; nil otherwise.
	Lifecycle *model.LifecycleEvent

	// Sample holds resource data when Kind == CollectorEventResource; nil otherwise.
	Sample *model.ResourceSample

	// CronJobExec holds CronJob execution data when Kind == CollectorEventCronJob; nil otherwise.
	CronJobExec *model.CronJobExecution

	// UpdateOnly, for CollectorEventState, means the state should only be applied
	// to an existing container row; unknown containers are not inserted.
	UpdateOnly bool

	// SyncIDs holds every container ID currently present in the runtime when
	// Kind == CollectorEventSync.
	SyncIDs []string
}

// Collector is the common interface that all runtime-specific collector implementations
// must satisfy. A Collector watches a container runtime, translates runtime events into
// the domain model, and emits them on a channel for consumption by the recorder.
type Collector interface {
	// Start initialises the connection to the runtime and launches background goroutines
	// that produce events. It must return promptly; long-running work runs in goroutines
	// that observe ctx cancellation for clean shutdown.
	Start(ctx context.Context) error

	// Stop signals the collector to finish in-flight work, release runtime connections,
	// and close the events channel. It must block until all background goroutines exit.
	Stop() error

	// Events returns a read-only channel on which the collector publishes CollectorEvents.
	// The channel is closed after Stop returns.
	Events() <-chan CollectorEvent

	// Stats fetches a single point-in-time ResourceSample for the container identified
	// by containerID. It is used for on-demand sampling outside the regular polling loop.
	Stats(ctx context.Context, containerID string) (*model.ResourceSample, error)
}
