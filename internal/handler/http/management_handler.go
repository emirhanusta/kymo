package httphandler

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/service"
)

// ---------------------------------------------------------------------------
// Container lookup helpers
// ---------------------------------------------------------------------------

// lookupContainer loads the container named by the :id path parameter and
// writes a 404/500 response (returning nil) when it cannot be resolved.
func (h *Handler) lookupContainer(c *gin.Context) *model.Container {
	id := c.Param("id")
	container, err := h.svc.GetContainer(c.Request.Context(), id)
	if err != nil {
		h.log.Error("container lookup failed", zap.String("id", id), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to look up container"})
		return nil
	}
	if container == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "container not found"})
		return nil
	}
	return container
}

// sourceFor returns the explicit ?source= override or the container's own source.
func sourceFor(c *gin.Context, container *model.Container) string {
	if s := c.Query("source"); s != "" {
		return s
	}
	return container.SourceName
}

// RestartContainer deletes a Kubernetes pod
// (its controller will recreate it). Runtime and source are taken from the
// stored container record; ?source= overrides the source.
//
//	POST /api/v1/containers/:id/restart
func (h *Handler) RestartContainer(c *gin.Context) {
	container := h.lookupContainer(c)
	if container == nil {
		return
	}
	source := sourceFor(c, container)
	if h.guardReadOnly(c, source) {
		return
	}

	switch container.Runtime {
	case model.RuntimeKubernetes:
		ns, pod, _, parseErr := parseK8sID(container.ID)
		if parseErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": parseErr.Error()})
			return
		}
		if err := h.mgmt.DeletePod(c.Request.Context(), source, ns, pod); err != nil {
			h.log.Error("delete pod failed", zap.String("id", container.ID), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
			return
		}
		h.audit(c, "pod.restart", "pod", container.ID, map[string]any{"source": source})
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "pod deleted; its controller will recreate it"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "unsupported runtime"})
	}
}

// GetContainerLogs streams or returns container logs.
//
// Query parameters:
//   - tail   (optional, default 100, max 5000): number of lines to return.
//   - follow (optional, default false): when true, stream via SSE; otherwise return plain text.
//   - source (optional): override the container's own source.
//
// GET /api/v1/containers/:id/logs
func (h *Handler) GetContainerLogs(c *gin.Context) {
	tail := 100
	if v := c.Query("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = min(n, 5000)
		}
	}
	follow := c.Query("follow") == "true"

	container := h.lookupContainer(c)
	if container == nil {
		return
	}
	source := sourceFor(c, container)

	switch container.Runtime {
	case model.RuntimeKubernetes:
		ns, pod, ctr, parseErr := parseK8sID(container.ID)
		if parseErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": parseErr.Error()})
			return
		}
		reader, err := h.mgmt.GetPodLogs(c.Request.Context(), source, ns, pod, ctr, tail, follow)
		if err != nil {
			h.logsError(c, container.ID, pod, err)
			return
		}
		if follow {
			h.sseK8sLogs(c, reader)
		} else {
			h.plainK8sLogs(c, reader)
		}

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported runtime"})
	}
}

// logsError writes the response for a failed pod-logs request: 410 pod_gone
// when Kubernetes reports the pod/container as not found (with the number of
// stored crash-log snapshots), otherwise 502 logs_unavailable.
func (h *Handler) logsError(c *gin.Context, containerID, pod string, err error) {
	if apierrors.IsNotFound(err) {
		n := 0
		if logs, lerr := h.svc.GetContainerCrashLogs(c.Request.Context(), containerID, 50); lerr != nil {
			h.log.Warn("count crash logs failed", zap.String("id", containerID), zap.Error(lerr))
		} else {
			n = len(logs)
		}
		c.JSON(http.StatusGone, gin.H{
			"error":      pod + " no longer exists in the cluster",
			"code":       "pod_gone",
			"crash_logs": n,
		})
		return
	}
	h.log.Error("get pod logs failed", zap.String("id", containerID), zap.Error(err))
	c.JSON(http.StatusBadGateway, gin.H{
		"error": "could not fetch logs from the cluster: " + logsErrMessage(err),
		"code":  "logs_unavailable",
	})
}

// logsErrMessage returns the innermost, user-presentable message of err.
func logsErrMessage(err error) string {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		if m := st.Status().Message; m != "" {
			return m
		}
	}
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err.Error()
		}
		err = u
	}
}

// ---------------------------------------------------------------------------
// Kubernetes Namespace endpoint
// ---------------------------------------------------------------------------

// ListNamespaces returns the Kubernetes namespaces reachable via the given source.
// Falls back to ["local"] when no Kubernetes source is registered.
//
// Query parameters:
//   - source (optional): target collector source name.
//
// GET /api/v1/namespaces
func (h *Handler) ListNamespaces(c *gin.Context) {
	source := c.Query("source")
	namespaces, err := h.mgmt.ListNamespaces(c.Request.Context(), source)
	if err != nil {
		h.log.Error("list namespaces failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, namespaces)
}

// ---------------------------------------------------------------------------
// Kubernetes Deployment endpoints
// ---------------------------------------------------------------------------

// ListDeployments returns all deployments in a namespace.
//
// Query parameters:
//   - namespace (optional): omit or leave empty to list all namespaces.
//   - source    (optional): target collector source name.
//
// GET /api/v1/deployments
func (h *Handler) ListDeployments(c *gin.Context) {
	ns := c.Query("namespace")
	source := c.Query("source")
	deps, err := h.mgmt.ListDeployments(c.Request.Context(), source, ns)
	if err != nil {
		h.log.Error("list deployments failed", zap.String("namespace", ns), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if deps == nil {
		deps = []service.DeploymentInfo{}
	}
	c.JSON(http.StatusOK, deps)
}

// ScaleDeployment sets the replica count for a deployment.
//
// Body: {"replicas": 3}
//
//	POST /api/v1/deployments/:namespace/:name/scale
func (h *Handler) ScaleDeployment(c *gin.Context) {
	var body struct {
		Replicas int32 `json:"replicas"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if body.Replicas < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "replicas must be >= 0"})
		return
	}
	ns := c.Param("namespace")
	name := c.Param("name")
	source, ok := h.k8sTarget(c)
	if !ok {
		return
	}
	if err := h.mgmt.ScaleDeployment(c.Request.Context(), source, ns, name, body.Replicas); err != nil {
		h.log.Error("scale deployment failed", zap.String("ns", ns), zap.String("name", name), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	h.audit(c, "deployment.scale", "deployment", ns+"/"+name, map[string]any{"source": source, "replicas": body.Replicas})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": fmt.Sprintf("scaled to %d replicas", body.Replicas)})
}

// RestartDeployment performs a rolling restart of a deployment.
//
//	POST /api/v1/deployments/:namespace/:name/restart
func (h *Handler) RestartDeployment(c *gin.Context) {
	ns := c.Param("namespace")
	name := c.Param("name")
	source, ok := h.k8sTarget(c)
	if !ok {
		return
	}
	if err := h.mgmt.RestartDeployment(c.Request.Context(), source, ns, name); err != nil {
		h.log.Error("restart deployment failed", zap.String("ns", ns), zap.String("name", name), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	h.audit(c, "deployment.restart", "deployment", ns+"/"+name, map[string]any{"source": source})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "rolling restart triggered"})
}

// ---------------------------------------------------------------------------
// Kubernetes Pod / Deployment detail endpoints
// ---------------------------------------------------------------------------

// GetPodContainers returns all containers belonging to a Kubernetes Pod along
// with pod-level metadata (node, workload info).
//
// Path parameters:
//   - namespace: Kubernetes namespace.
//   - name:      Pod name.
//
// Query parameters:
//   - source (optional): filter by collector source name.
//
// GET /api/v1/pods/:namespace/:name
func (h *Handler) GetPodContainers(c *gin.Context) {
	namespace := c.Param("namespace")
	podName := c.Param("name")
	source := c.Query("source")

	all, err := h.svc.ListContainers(c.Request.Context(), repository.ContainerFilter{Runtime: model.RuntimeKubernetes, Source: source})
	if err != nil {
		h.log.Error("get pod containers failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	prefix := namespace + "/" + podName + "/"
	var containers []*model.Container
	for _, ct := range all {
		if strings.HasPrefix(ct.ID, prefix) {
			containers = append(containers, ct)
		}
	}

	if len(containers) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "pod not found"})
		return
	}

	first := containers[0]
	c.JSON(http.StatusOK, gin.H{
		"pod_name":      podName,
		"namespace":     namespace,
		"node_name":     first.NodeName,
		"workload_kind": first.WorkloadKind,
		"workload_name": first.WorkloadName,
		"source_name":   first.SourceName,
		"containers":    containers,
	})
}

// GetDeploymentDetail returns live deployment info from the management service
// together with the pods (and their containers) recorded by KYMO.
//
// Path parameters:
//   - namespace: Kubernetes namespace.
//   - name:      Deployment name.
//
// Query parameters:
//   - source (optional): filter by collector source name.
//
// GET /api/v1/deployments/:namespace/:name
func (h *Handler) GetDeploymentDetail(c *gin.Context) {
	namespace := c.Param("namespace")
	name := c.Param("name")
	source := c.Query("source")

	// Try to fetch live deployment info; failure is non-fatal.
	var depInfo *service.DeploymentInfo
	if deps, err := h.mgmt.ListDeployments(c.Request.Context(), source, namespace); err == nil {
		for i := range deps {
			if deps[i].Name == name {
				depInfo = &deps[i]
				break
			}
		}
	}

	// Fetch all K8s containers and filter for this deployment.
	all, err := h.svc.ListContainers(c.Request.Context(), repository.ContainerFilter{Runtime: model.RuntimeKubernetes, Source: source})
	if err != nil {
		h.log.Error("get deployment detail: list containers failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	type podEntry struct {
		PodName    string             `json:"pod_name"`
		Namespace  string             `json:"namespace"`
		NodeName   string             `json:"node_name"`
		Status     string             `json:"status"`
		Containers []*model.Container `json:"containers"`
	}

	podMap := make(map[string]*podEntry)
	for _, ct := range all {
		if ct.Namespace != namespace || ct.WorkloadName != name || ct.WorkloadKind != model.WorkloadDeployment {
			continue
		}
		parts := strings.SplitN(ct.ID, "/", 3)
		if len(parts) < 2 {
			continue
		}
		podName := parts[1]
		pe, ok := podMap[podName]
		if !ok {
			pe = &podEntry{
				PodName:   podName,
				Namespace: namespace,
				NodeName:  ct.NodeName,
				Status:    string(ct.CurrentStatus),
			}
			podMap[podName] = pe
		}
		pe.Containers = append(pe.Containers, ct)
	}

	pods := make([]*podEntry, 0, len(podMap))
	for _, pe := range podMap {
		pods = append(pods, pe)
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].PodName < pods[j].PodName })

	c.JSON(http.StatusOK, gin.H{
		"deployment": depInfo,
		"pods":       pods,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// k8sTarget resolves the cluster a deployment action targets (?source= or the
// first cluster) and rejects read-only sources. It writes the error response
// and returns ok=false on failure.
func (h *Handler) k8sTarget(c *gin.Context) (string, bool) {
	source, err := h.mgmt.ResolveK8sSource(c.Query("source"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return "", false
	}
	if h.guardReadOnly(c, source) {
		return "", false
	}
	return source, true
}

// parseK8sID splits a Kubernetes container ID "namespace/pod-name/container-name"
// into its three components.
func parseK8sID(id string) (namespace, pod, container string, err error) {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("invalid kubernetes container ID %q: expected namespace/pod/container", id)
	}
	return parts[0], parts[1], parts[2], nil
}

// sseK8sLogs streams Kubernetes pod logs (plain text) as SSE.
func (h *Handler) sseK8sLogs(c *gin.Context, reader io.ReadCloser) {
	defer reader.Close()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, _ := c.Writer.(http.Flusher)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-c.Request.Context().Done():
			return
		default:
		}
		fmt.Fprintf(c.Writer, "data: %s\n\n", scanner.Text())
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// plainK8sLogs returns Kubernetes pod logs as plain text.
func (h *Handler) plainK8sLogs(c *gin.Context, reader io.ReadCloser) {
	defer reader.Close()
	var buf bytes.Buffer
	io.Copy(&buf, reader) //nolint:errcheck
	c.Data(http.StatusOK, "text/plain; charset=utf-8", buf.Bytes())
}
