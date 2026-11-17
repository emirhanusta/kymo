package service

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

type managementService struct {
	dockerClients map[string]*dockerclient.Client // keyed by source name; may be empty
	dockerOrder   []string                        // source names in config order
	k8sClients    map[string]kubernetes.Interface // keyed by source name; may be empty
	k8sOrder      []string                        // source names in config order
	log           *zap.Logger
}

// NewManagementService constructs a ManagementService backed by the provided
// clients, keyed by source name. dockerOrder / k8sOrder list the source names in
// configuration order and determine the fallback client when a call does not
// name a source. Attempts to call methods for a missing runtime return an error.
func NewManagementService(
	dockerClients map[string]*dockerclient.Client, dockerOrder []string,
	k8sClients map[string]kubernetes.Interface, k8sOrder []string,
	log *zap.Logger,
) ManagementService {
	if dockerClients == nil {
		dockerClients = make(map[string]*dockerclient.Client)
	}
	if k8sClients == nil {
		k8sClients = make(map[string]kubernetes.Interface)
	}
	return &managementService{
		dockerClients: dockerClients, dockerOrder: dockerOrder,
		k8sClients: k8sClients, k8sOrder: k8sOrder,
		log: log,
	}
}

// getDockerClient returns the Docker client for the given source name, or the
// first configured one when sourceName is empty.
func (s *managementService) getDockerClient(sourceName string) (*dockerclient.Client, error) {
	if sourceName != "" {
		if cli, ok := s.dockerClients[sourceName]; ok {
			return cli, nil
		}
		return nil, fmt.Errorf("no docker client configured for source %q", sourceName)
	}
	for _, name := range s.dockerOrder {
		if cli, ok := s.dockerClients[name]; ok {
			return cli, nil
		}
	}
	return nil, fmt.Errorf("docker client not available")
}

// getK8sClient returns the Kubernetes client for the given source name.
// If sourceName is empty, the first available client is returned.
func (s *managementService) getK8sClient(sourceName string) (kubernetes.Interface, error) {
	if len(s.k8sClients) == 0 {
		return nil, fmt.Errorf("kubernetes client not available")
	}
	if sourceName != "" {
		if cli, ok := s.k8sClients[sourceName]; ok {
			return cli, nil
		}
		return nil, fmt.Errorf("no kubernetes client configured for source %q", sourceName)
	}
	// Fall back to the first configured client (deterministic, config order).
	for _, name := range s.k8sOrder {
		if cli, ok := s.k8sClients[name]; ok {
			return cli, nil
		}
	}
	return nil, fmt.Errorf("kubernetes client not available")
}

// ---------------------------------------------------------------------------
// Docker operations
// ---------------------------------------------------------------------------

func (s *managementService) StartContainer(ctx context.Context, sourceName, containerID string) error {
	cli, err := s.getDockerClient(sourceName)
	if err != nil {
		return err
	}
	return cli.ContainerStart(ctx, containerID, containertypes.StartOptions{})
}

func (s *managementService) StopContainer(ctx context.Context, sourceName, containerID string) error {
	cli, err := s.getDockerClient(sourceName)
	if err != nil {
		return err
	}
	return cli.ContainerStop(ctx, containerID, containertypes.StopOptions{})
}

func (s *managementService) RestartContainer(ctx context.Context, sourceName, containerID string) error {
	cli, err := s.getDockerClient(sourceName)
	if err != nil {
		return err
	}
	return cli.ContainerRestart(ctx, containerID, containertypes.StopOptions{})
}

func (s *managementService) KillContainer(ctx context.Context, sourceName, containerID string) error {
	cli, err := s.getDockerClient(sourceName)
	if err != nil {
		return err
	}
	return cli.ContainerKill(ctx, containerID, "SIGKILL")
}

func (s *managementService) GetContainerLogs(ctx context.Context, sourceName, containerID string, tail int, follow bool) (io.ReadCloser, bool, error) {
	cli, err := s.getDockerClient(sourceName)
	if err != nil {
		return nil, false, err
	}
	// Containers created with a TTY produce a raw (non-multiplexed) stream.
	tty := false
	if info, err := cli.ContainerInspect(ctx, containerID); err == nil && info.Config != nil {
		tty = info.Config.Tty
	}
	rc, err := cli.ContainerLogs(ctx, containerID, containertypes.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
		Follow:     follow,
	})
	return rc, tty, err
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
	if len(s.k8sClients) == 0 {
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
	k8s, err := s.getK8sClient(sourceName)
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
		})
	}
	return result, nil
}
