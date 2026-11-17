package kubernetes

import (
	"context"
	"regexp"
	"strings"
	"sync"

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

// maxOwnerCacheEntries bounds the owner-resolution cache; it is cleared
// entirely when exceeded (owners are cheap to re-resolve).
const maxOwnerCacheEntries = 5000

// ownerResolver resolves the controller that owns a ReplicaSet or Job. Results
// are cached by namespace/name because the owner of a given ReplicaSet/Job
// never changes. It is safe for concurrent use.
type ownerResolver struct {
	mu    sync.Mutex
	cache map[string]string // "rs/ns/name" or "job/ns/name" -> owner name ("" = none)
}

func newOwnerResolver() *ownerResolver {
	return &ownerResolver{cache: make(map[string]string)}
}

func (r *ownerResolver) get(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.cache[key]
	return v, ok
}

func (r *ownerResolver) put(key, val string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= maxOwnerCacheEntries {
		r.cache = make(map[string]string)
	}
	r.cache[key] = val
}

// deploymentFor returns the Deployment owning the named ReplicaSet, or "".
// Successful API answers are cached; on API failure it falls back to stripping
// the pod-template-hash suffix from the ReplicaSet name (uncached).
func (r *ownerResolver) deploymentFor(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, rsName string) string {
	key := "rs/" + pod.Namespace + "/" + rsName
	if v, ok := r.get(key); ok {
		return v
	}
	rs, err := client.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, rsName, metav1.GetOptions{})
	if err == nil {
		dep := ""
		if len(rs.OwnerReferences) > 0 && rs.OwnerReferences[0].Kind == "Deployment" {
			dep = rs.OwnerReferences[0].Name
		}
		r.put(key, dep)
		return dep
	}
	hash := pod.Labels["pod-template-hash"]
	if hash != "" && strings.HasSuffix(rsName, "-"+hash) {
		if dep := strings.TrimSuffix(rsName, "-"+hash); dep != "" {
			return dep
		}
	}
	return ""
}

// cronJobFor returns the CronJob owning the named Job, or "". On API failure it
// falls back to the CronJob naming convention "<cronjob>-<digits>".
func (r *ownerResolver) cronJobFor(ctx context.Context, client kubernetes.Interface, namespace, jobName string) string {
	key := "job/" + namespace + "/" + jobName
	if v, ok := r.get(key); ok {
		return v
	}
	job, err := client.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err == nil {
		cj := ""
		if len(job.OwnerReferences) > 0 && job.OwnerReferences[0].Kind == "CronJob" {
			cj = job.OwnerReferences[0].Name
		}
		r.put(key, cj)
		return cj
	}
	if m := cronJobJobName.FindStringSubmatch(jobName); m != nil {
		return m[1]
	}
	return ""
}

var cronJobJobName = regexp.MustCompile(`^(.+)-\d+$`)

// DetectWorkload resolves the logical workload type and name for a pod by walking
// its owner reference chain via the Kubernetes API. It uses a throwaway (empty)
// owner cache; the Collector uses a long-lived resolver instead. API calls are
// best-effort; on error heuristics based on labels and names are used.
func DetectWorkload(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) WorkloadInfo {
	return newOwnerResolver().detect(ctx, client, pod)
}

func (r *ownerResolver) detect(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) WorkloadInfo {
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
		if dep := r.deploymentFor(ctx, client, pod, owner.Name); dep != "" {
			info.Kind = model.WorkloadDeployment
			info.Name = dep
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
		if cj := r.cronJobFor(ctx, client, pod.Namespace, owner.Name); cj != "" {
			info.Kind = model.WorkloadCronJob
			info.Name = cj
			info.CronJobName = cj
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
