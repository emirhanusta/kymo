package kube

import (
	"strings"
	"testing"
)

func TestInClusterPathNeverFallsBackToLocalKubeconfig(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	_, err := RestConfig(InClusterPath, nil, "")
	if err == nil || !strings.Contains(err.Error(), "not running inside Kubernetes") {
		t.Fatalf("want a not-in-cluster error, got %v", err)
	}
}
