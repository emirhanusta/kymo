// Package service — management operations for Kubernetes workloads. This file defines the ManagementService interface; the concrete
// implementation lives in management_impl.go.
package service

import (
	"context"
	"io"

	"github.com/emirhanusta/kymo/internal/model"
)

// ManagementService exposes control operations for Kubernetes pods and
// deployments. Methods accept the individual components of a container ID
// (namespace / pod / container).
type ManagementService interface {
	// --- Client registry ---

	// RegisterSource creates (or replaces) the runtime client for a source.
	RegisterSource(spec model.SourceSpec) error
	// UnregisterSource removes a source's client.
	UnregisterSource(name string)
	// ResolveK8sSource returns the cluster a Kubernetes call targets when given
	// sourceName ("" = first registered cluster).
	ResolveK8sSource(sourceName string) (string, error)

	// --- Kubernetes operations ---
	// sourceName selects which K8s cluster to target; pass "" to use the first
	// available client (useful when only one K8s source is configured).

	DeletePod(ctx context.Context, sourceName, namespace, podName string) error
	// GetPodLogs returns a ReadCloser over the pod log stream (plain text).
	GetPodLogs(ctx context.Context, sourceName, namespace, podName, containerName string, tail int, follow bool) (io.ReadCloser, error)
	ScaleDeployment(ctx context.Context, sourceName, namespace, name string, replicas int32) error
	RestartDeployment(ctx context.Context, sourceName, namespace, name string) error
	ListDeployments(ctx context.Context, sourceName, namespace string) ([]DeploymentInfo, error)

	// ListNamespaces returns the Kubernetes namespaces reachable via the given source.
	// Pass an empty sourceName to use any available K8s client.
	// Returns ["local"] when no Kubernetes source is registered.
	ListNamespaces(ctx context.Context, sourceName string) ([]string, error)
}

// DeploymentInfo is the JSON-serialisable summary of a Kubernetes Deployment.
type DeploymentInfo struct {
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	Replicas      int32  `json:"replicas"`
	ReadyReplicas int32  `json:"ready_replicas"`
	Image         string `json:"image"`
	CreatedAt     string `json:"created_at"`
	// SourceName is the cluster (collector source) the deployment belongs to.
	SourceName string `json:"source_name"`
}
