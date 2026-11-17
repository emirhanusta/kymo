package kubernetes

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/emirhanusta/kymo/internal/model"
)

// WorkloadInfo describes the logical workload that owns a Pod.
type WorkloadInfo struct {
	// Kind is the Kubernetes controller kind (e.g. model.WorkloadDeployment).
	Kind string
	// Name is the workload name (e.g. the Deployment name).
	Name string
	// CronJobName is non-empty when Kind == model.WorkloadCronJob.
	CronJobName string
	// JobName is non-empty when Kind == model.WorkloadJob or model.WorkloadCronJob.
	JobName string
	// IsSystem is true when the pod runs in a known infrastructure namespace
	// or has a well-known system component name prefix.
	IsSystem bool
}

// systemNamespaces is the set of Kubernetes namespaces that are considered
// infrastructure / system namespaces. Pods in these namespaces are flagged as
// IsSystem = true regardless of their name.
var systemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
	"cilium":          true,
	"calico-system":   true,
	"cattle-system":   true,
}

// systemPrefixes is a list of well-known system component name prefixes.
// A pod whose name starts with any of these is flagged IsSystem = true.
var systemPrefixes = []string{
	"cilium",
	"konnectivity",
	"coredns",
	"kube-proxy",
	"metrics-server",
	"calico",
	"flannel",
	"node-local-dns",
}

// DetectWorkload resolves the logical workload type and name for a pod by walking
// its owner reference chain via the Kubernetes API. All API calls are best-effort;
// on any error the function falls back to WorkloadPod with the pod's own name.
func DetectWorkload(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) WorkloadInfo {
	info := WorkloadInfo{
		Kind:     model.WorkloadPod,
		Name:     pod.Name,
		IsSystem: isSystemPod(pod),
	}

	if len(pod.OwnerReferences) == 0 {
		return info
	}

	owner := pod.OwnerReferences[0]
	switch owner.Kind {
	case "ReplicaSet":
		rs, err := client.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err == nil && len(rs.OwnerReferences) > 0 && rs.OwnerReferences[0].Kind == "Deployment" {
			info.Kind = model.WorkloadDeployment
			info.Name = rs.OwnerReferences[0].Name
		} else {
			info.Kind = "ReplicaSet"
			info.Name = owner.Name
		}
	case "DaemonSet":
		info.Kind = model.WorkloadDaemonSet
		info.Name = owner.Name
	case "StatefulSet":
		info.Kind = model.WorkloadStatefulSet
		info.Name = owner.Name
	case "Job":
		job, err := client.BatchV1().Jobs(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err == nil && len(job.OwnerReferences) > 0 && job.OwnerReferences[0].Kind == "CronJob" {
			info.Kind = model.WorkloadCronJob
			info.Name = job.OwnerReferences[0].Name
			info.CronJobName = job.OwnerReferences[0].Name
			info.JobName = owner.Name
		} else {
			info.Kind = model.WorkloadJob
			info.Name = owner.Name
			info.JobName = owner.Name
		}
	}

	return info
}

// isSystemPod returns true when the pod's namespace is a known system namespace
// or the pod name starts with a known system component prefix.
func isSystemPod(pod *corev1.Pod) bool {
	if systemNamespaces[pod.Namespace] {
		return true
	}
	for _, prefix := range systemPrefixes {
		if strings.HasPrefix(pod.Name, prefix) {
			return true
		}
	}
	return false
}
