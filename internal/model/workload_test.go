package model

import "testing"

func TestWorkloadID(t *testing.T) {
	tests := []struct {
		source, ns, kind, name, want string
	}{
		{"prod", "default", WorkloadDeployment, "api", "prod:default:Deployment:api"},
		{"local", "default", WorkloadDeployment, "", ""},
		{"local", "", WorkloadUnknown, "x", ""},
	}
	for _, tt := range tests {
		if got := WorkloadID(tt.source, tt.ns, tt.kind, tt.name); got != tt.want {
			t.Errorf("WorkloadID(%q,%q,%q,%q) = %q, want %q", tt.source, tt.ns, tt.kind, tt.name, got, tt.want)
		}
	}
}
