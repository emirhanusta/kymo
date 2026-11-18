package service

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/emirhanusta/kymo/internal/kube"
	"github.com/emirhanusta/kymo/internal/model"
)

type managementService struct {
	mu         sync.RWMutex
	k8sClients map[string]kubernetes.Interface // keyed by source name
	k8sOrder   []string
	log        *zap.Logger
	cronSpecs  *cronSpecCache
}

// NewManagementService constructs an empty ManagementService. Clients are
// added with RegisterSource as sources are configured or created at runtime.
func NewManagementService(log *zap.Logger) ManagementService {
	return &managementService{
		k8sClients: make(map[string]kubernetes.Interface),
		log:        log,
		cronSpecs:  newCronSpecCache(cronSpecTTL),
	}
}

// RegisterSource builds and stores the management client for spec, replacing
// any previous client with the same name.
func (s *managementService) RegisterSource(spec model.SourceSpec) error {
	s.UnregisterSource(spec.Name)
	switch spec.Type {
	case model.SourceTypeKubernetes:
		cli, _, err := kube.Clientset(spec.KubeconfigPath, spec.Kubeconfig, spec.KubeContext)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.k8sClients[spec.Name] = cli
		s.k8sOrder = append(s.k8sOrder, spec.Name)
		s.mu.Unlock()
	default:
		return fmt.Errorf("unknown source type %q", spec.Type)
	}
	return nil
}

// UnregisterSource drops (and closes) the client for a source, if any.
func (s *managementService) UnregisterSource(name string) {
	s.cronSpecs.drop(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.k8sClients[name]; ok {
		delete(s.k8sClients, name)
		s.k8sOrder = without(s.k8sOrder, name)
	}
}

// ResolveK8sSource returns the source a Kubernetes call would target: the
// given name if it has a client, else the first registered cluster.
func (s *managementService) ResolveK8sSource(sourceName string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sourceName != "" {
		if _, ok := s.k8sClients[sourceName]; ok {
			return sourceName, nil
		}
		return "", fmt.Errorf("no kubernetes client configured for source %q", sourceName)
	}
	if len(s.k8sOrder) > 0 {
		return s.k8sOrder[0], nil
	}
	return "", fmt.Errorf("kubernetes client not available")
}

// getK8sClient returns the Kubernetes client for the given source name, or
// the first registered cluster when sourceName is empty.
func (s *managementService) getK8sClient(sourceName string) (kubernetes.Interface, error) {
	name, err := s.ResolveK8sSource(sourceName)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.k8sClients[name], nil
}

// ---------------------------------------------------------------------------
// Kubernetes operations
// ---------------------------------------------------------------------------

func (s *managementService) DeletePod(ctx context.Context, sourceName, namespace, podName string) error {
	k8s, err := s.getK8sClient(sourceName)
	if err != nil {
		return err
	}
	return k8s.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
}

func (s *managementService) GetPodLogs(ctx context.Context, sourceName, namespace, podName, containerName string, tail int, follow bool) (io.ReadCloser, error) {
	k8s, err := s.getK8sClient(sourceName)
	if err != nil {
		return nil, err
	}
	tailLines := int64(tail)
	return k8s.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		TailLines: &tailLines,
		Follow:    follow,
		Container: containerName,
	}).Stream(ctx)
}

func (s *managementService) ScaleDeployment(ctx context.Context, sourceName, namespace, name string, replicas int32) error {
	k8s, err := s.getK8sClient(sourceName)
	if err != nil {
		return err
	}
	dep, err := k8s.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	dep.Spec.Replicas = &replicas
	_, err = k8s.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

func (s *managementService) RestartDeployment(ctx context.Context, sourceName, namespace, name string) error {
	k8s, err := s.getK8sClient(sourceName)
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339),
	)
	_, err = k8s.AppsV1().Deployments(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	return err
}

func (s *managementService) ListNamespaces(ctx context.Context, sourceName string) ([]string, error) {
	s.mu.RLock()
	none := len(s.k8sClients) == 0
	s.mu.RUnlock()
	if none {
		return []string{"local"}, nil
	}
	k8s, err := s.getK8sClient(sourceName)
	if err != nil {
		return []string{"local"}, nil
	}
	list, err := k8s.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	namespaces := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		namespaces = append(namespaces, ns.Name)
	}
	return namespaces, nil
}

func (s *managementService) ListDeployments(ctx context.Context, sourceName, namespace string) ([]DeploymentInfo, error) {
	resolved, err := s.ResolveK8sSource(sourceName)
	if err != nil {
		return nil, err
	}
	k8s, err := s.getK8sClient(resolved)
	if err != nil {
		return nil, err
	}
	list, err := k8s.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	result := make([]DeploymentInfo, 0, len(list.Items))
	for _, d := range list.Items {
		image := ""
		if len(d.Spec.Template.Spec.Containers) > 0 {
			image = d.Spec.Template.Spec.Containers[0].Image
		}
		replicas := int32(0)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		result = append(result, DeploymentInfo{
			Name:          d.Name,
			Namespace:     d.Namespace,
			Replicas:      replicas,
			ReadyReplicas: d.Status.ReadyReplicas,
			Image:         image,
			CreatedAt:     d.CreationTimestamp.UTC().Format(time.RFC3339),
			SourceName:    resolved,
		})
	}
	return result, nil
}
