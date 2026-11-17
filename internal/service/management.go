// Package service — management operations for Docker containers and Kubernetes
// workloads. This file defines the ManagementService interface; the concrete
// implementation lives in management_impl.go.
package service

import (
	"context"
	"io"
)

// ManagementService exposes runtime-level control operations for containers
// and Kubernetes deployments. All methods that target Docker accept a Docker
// container ID; all methods that target Kubernetes accept the individual
// components of a container ID (namespace / pod / container).
type ManagementService interface {
	// --- Docker operations ---
	// sourceName selects which Docker daemon to target; pass "" to use the
	// first configured daemon.

	StartContainer(ctx context.Context, sourceName, containerID string) error
	StopContainer(ctx context.Context, sourceName, containerID string) error
	RestartContainer(ctx context.Context, sourceName, containerID string) error
	KillContainer(ctx context.Context, sourceName, containerID string) error
	// GetContainerLogs returns a ReadCloser over the Docker log stream.
	// tail controls how many lines to return; follow keeps the stream open.
	// tty reports whether the container was created with a TTY: when false the
	// stream uses Docker's multiplexed format and must be demuxed with stdcopy.
	GetContainerLogs(ctx context.Context, sourceName, containerID string, tail int, follow bool) (rc io.ReadCloser, tty bool, err error)

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
	// Returns ["local"] when only Docker sources are configured.
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
}
