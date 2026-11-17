package model

import "testing"

func TestWorkloadID(t *testing.T) {
	tests := []struct {
		source, ns, kind, name, want string
	}{
		{"local", "", WorkloadContainer, "redis", "local::Container:redis"},
		{"local", "", WorkloadCompose, "shop", "local::Compose:shop"},
		{"prod", "default", WorkloadDeployment, "api", "prod:default:Deployment:api"},
		{"local", "", WorkloadContainer, "", ""},
		{"local", "", WorkloadUnknown, "x", ""},
	}
	for _, tt := range tests {
		if got := WorkloadID(tt.source, tt.ns, tt.kind, tt.name); got != tt.want {
			t.Errorf("WorkloadID(%q,%q,%q,%q) = %q, want %q", tt.source, tt.ns, tt.kind, tt.name, got, tt.want)
		}
	}
}
