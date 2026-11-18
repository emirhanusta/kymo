package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// cronSpecTTL is how long a source's CronJob list is reused before the
// cluster is asked again.
const cronSpecTTL = 60 * time.Second

// CronJobSpec is the live definition of a Kubernetes CronJob (KYMO does not
// record these; they are read on demand and cached briefly).
type CronJobSpec struct {
	SourceName string `json:"source_name"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	// Timezone is spec.timeZone ("" = the controller's zone, normally UTC).
	Timezone           string     `json:"timezone"`
	Suspend            bool       `json:"suspend"`
	LastScheduleTime   *time.Time `json:"last_schedule_time"`
	LastSuccessfulTime *time.Time `json:"last_successful_time"`
	// NextRun is computed from Schedule and Timezone; nil when the CronJob is
	// suspended or the schedule cannot be parsed.
	NextRun *time.Time `json:"next_run"`
}

// NextCronRun returns the first run after now for a standard 5-field cron
// expression (or a descriptor such as @hourly) in the given IANA timezone
// ("" = UTC). It returns false when the schedule or timezone is invalid.
func NextCronRun(schedule, timezone string, now time.Time) (time.Time, bool) {
	expr := strings.TrimSpace(schedule)
	if expr == "" {
		return time.Time{}, false
	}
	if tz := strings.TrimSpace(timezone); tz != "" && !strings.HasPrefix(expr, "CRON_TZ=") && !strings.HasPrefix(expr, "TZ=") {
		expr = "CRON_TZ=" + tz + " " + expr
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, false
	}
	// Without CRON_TZ, robfig/cron uses the location of the time passed in;
	// Kubernetes evaluates such schedules in the controller's zone (UTC), not
	// in the zone of the machine KYMO runs on.
	next := sched.Next(now.UTC())
	if next.IsZero() {
		return time.Time{}, false
	}
	return next.UTC(), true
}

// cronSpecCache holds the last CronJob list per source for a short TTL.
type cronSpecCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]cronSpecEntry
}

type cronSpecEntry struct {
	at    time.Time
	specs []CronJobSpec
}

func newCronSpecCache(ttl time.Duration) *cronSpecCache {
	return &cronSpecCache{ttl: ttl, now: time.Now, entries: make(map[string]cronSpecEntry)}
}

func (c *cronSpecCache) get(source string) ([]CronJobSpec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[source]
	if !ok || c.now().Sub(e.at) >= c.ttl {
		return nil, false
	}
	return e.specs, true
}

func (c *cronSpecCache) put(source string, specs []CronJobSpec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[source] = cronSpecEntry{at: c.now(), specs: specs}
}

func (c *cronSpecCache) drop(source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, source)
}

// ListCronJobSpecs returns the CronJobs of a cluster (all namespaces) with
// their schedule and computed next run. The cluster is queried at most once per
// cronSpecTTL; next_run is recomputed on every call so it never goes stale.
func (s *managementService) ListCronJobSpecs(ctx context.Context, sourceName string) ([]CronJobSpec, error) {
	resolved, err := s.ResolveK8sSource(sourceName)
	if err != nil {
		return nil, err
	}
	specs, ok := s.cronSpecs.get(resolved)
	if !ok {
		k8s, err := s.getK8sClient(resolved)
		if err != nil {
			return nil, err
		}
		list, err := k8s.BatchV1().CronJobs(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list cronjobs: %w", err)
		}
		specs = make([]CronJobSpec, 0, len(list.Items))
		for _, cj := range list.Items {
			sp := CronJobSpec{
				SourceName: resolved, Namespace: cj.Namespace, Name: cj.Name, Schedule: cj.Spec.Schedule,
			}
			if cj.Spec.TimeZone != nil {
				sp.Timezone = *cj.Spec.TimeZone
			}
			if cj.Spec.Suspend != nil {
				sp.Suspend = *cj.Spec.Suspend
			}
			if t := cj.Status.LastScheduleTime; t != nil {
				v := t.UTC()
				sp.LastScheduleTime = &v
			}
			if t := cj.Status.LastSuccessfulTime; t != nil {
				v := t.UTC()
				sp.LastSuccessfulTime = &v
			}
			specs = append(specs, sp)
		}
		sort.Slice(specs, func(i, j int) bool {
			if specs[i].Namespace != specs[j].Namespace {
				return specs[i].Namespace < specs[j].Namespace
			}
			return specs[i].Name < specs[j].Name
		})
		s.cronSpecs.put(resolved, specs)
	}

	now := time.Now()
	out := make([]CronJobSpec, len(specs))
	copy(out, specs)
	for i := range out {
		if out[i].Suspend {
			continue
		}
		if next, ok := NextCronRun(out[i].Schedule, out[i].Timezone, now); ok {
			out[i].NextRun = &next
		}
	}
	return out, nil
}
