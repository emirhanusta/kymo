package docker

import (
	"testing"
	"time"

	apitypes "github.com/docker/docker/api/types"

	"github.com/emirhanusta/kymo/internal/model"
)

func TestComposeWorkload(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		cname    string
		wantKind string
		wantName string
	}{
		{"compose label", map[string]string{composeLabel: "shop"}, "shop-web-1", model.WorkloadCompose, "shop"},
		{"no label", nil, "redis", model.WorkloadContainer, "redis"},
		{"leading slash", map[string]string{}, "/redis", model.WorkloadContainer, "redis"},
		{"no name", nil, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, n := composeWorkload(tt.labels, tt.cname)
			if k != tt.wantKind || n != tt.wantName {
				t.Fatalf("got (%q,%q), want (%q,%q)", k, n, tt.wantKind, tt.wantName)
			}
		})
	}
}

func TestContainerFromListItem_Workload(t *testing.T) {
	now := time.Now()
	plain := containerFromListItem(&apitypes.Container{ID: "a", Names: []string{"/nginx"}, State: "running"}, now)
	if plain.WorkloadKind != model.WorkloadContainer || plain.WorkloadName != "nginx" {
		t.Fatalf("standalone: got %q/%q", plain.WorkloadKind, plain.WorkloadName)
	}
	if id := model.WorkloadID("local", "", plain.WorkloadKind, plain.WorkloadName); id != "local::Container:nginx" {
		t.Fatalf("workload id = %q", id)
	}
	comp := containerFromListItem(&apitypes.Container{
		ID: "b", Names: []string{"/shop-web-1"}, State: "running",
		Labels: map[string]string{composeLabel: "shop"},
	}, now)
	if comp.WorkloadKind != model.WorkloadCompose || comp.WorkloadName != "shop" {
		t.Fatalf("compose: got %q/%q", comp.WorkloadKind, comp.WorkloadName)
	}
}
