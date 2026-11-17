package model

import (
	"sort"
	"strings"
	"time"
)

// Revision summarises one rollout (pod template revision) of a workload.
type Revision struct {
	Revision    string    `json:"revision"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	Images      []string  `json:"images"`
	// Pods is the number of distinct pods recorded with this revision;
	// ActivePods those with at least one non-stopped container.
	Pods       int  `json:"pods"`
	ActivePods int  `json:"active_pods"`
	Current    bool `json:"current"`
}

// podOfContainerID returns the pod segment of "namespace/pod/container".
func podOfContainerID(id string) string {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return id
}

// BuildRevisions groups a workload's containers by revision and returns the
// revisions newest first (by first_seen_at). Containers without a revision are
// grouped under "". The current revision is the one with the latest
// first_seen_at among revisions that have active pods, or the newest one when
// none is active.
func BuildRevisions(containers []*Container) []Revision {
	type agg struct {
		rev    Revision
		pods   map[string]bool
		active map[string]bool
		images map[string]bool
	}
	byRev := map[string]*agg{}
	for _, c := range containers {
		if c == nil {
			continue
		}
		a := byRev[c.Revision]
		if a == nil {
			a = &agg{
				rev:    Revision{Revision: c.Revision, FirstSeenAt: c.FirstSeenAt, LastSeenAt: c.LastSeenAt},
				pods:   map[string]bool{},
				active: map[string]bool{},
				images: map[string]bool{},
			}
			byRev[c.Revision] = a
		}
		if c.FirstSeenAt.Before(a.rev.FirstSeenAt) {
			a.rev.FirstSeenAt = c.FirstSeenAt
		}
		if c.LastSeenAt.After(a.rev.LastSeenAt) {
			a.rev.LastSeenAt = c.LastSeenAt
		}
		pod := podOfContainerID(c.ID)
		a.pods[pod] = true
		if c.CurrentStatus != StatusStopped {
			a.active[pod] = true
		}
		if c.Image != "" {
			a.images[c.Image] = true
		}
	}

	out := make([]Revision, 0, len(byRev))
	for _, a := range byRev {
		a.rev.Pods = len(a.pods)
		a.rev.ActivePods = len(a.active)
		a.rev.Images = make([]string, 0, len(a.images))
		for img := range a.images {
			a.rev.Images = append(a.rev.Images, img)
		}
		sort.Strings(a.rev.Images)
		out = append(out, a.rev)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].FirstSeenAt.Equal(out[j].FirstSeenAt) {
			return out[i].FirstSeenAt.After(out[j].FirstSeenAt)
		}
		return out[i].Revision < out[j].Revision
	})

	cur := -1
	for i := range out { // newest first, so the first with active pods wins
		if out[i].ActivePods > 0 {
			cur = i
			break
		}
	}
	if cur < 0 && len(out) > 0 {
		cur = 0
	}
	if cur >= 0 {
		out[cur].Current = true
	}
	return out
}
