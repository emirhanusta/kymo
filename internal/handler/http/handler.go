// Package httphandler implements the HTTP presentation layer for the KYMO
// REST API using the Gin web framework. All routes are mounted under /api/v1.
package httphandler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/auth"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/service"
)

// Pinger reports whether a dependency (the database) is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler holds all HTTP handler methods and their shared dependencies.
type Handler struct {
	svc     service.RecorderService
	mgmt    service.ManagementService
	sources *service.SourceManager
	db      Pinger
	log     *zap.Logger

	issues       repository.IssueRepository
	notif        NotificationAPI
	errlogs      ErrorLogAPI
	publicURL    string
	notifEnabled bool
	auth         *auth.Service
}

// New creates a Handler, registers all API routes on r, and returns the Handler.
// db may be nil, in which case /healthz does not check the database.
func New(svc service.RecorderService, mgmt service.ManagementService, sources *service.SourceManager, db Pinger, log *zap.Logger, r *gin.Engine, opts ...Option) *Handler {
	h := &Handler{svc: svc, mgmt: mgmt, sources: sources, db: db, log: log}
	for _, o := range opts {
		o(h)
	}
	h.registerRoutes(r)
	return h
}

// registerRoutes mounts all KYMO endpoints under /api/v1. Every route is
// guarded with an explicit minimum role (viewer for reads); the only public
// ones are /healthz and the /auth endpoints needed to sign in.
func (h *Handler) registerRoutes(r *gin.Engine) {
	v1 := r.Group("/api/v1")
	if h.auth != nil {
		v1.Use(h.auth.Middleware())
	}
	viewer, operator, admin := h.require(model.RoleViewer), h.require(model.RoleOperator), h.require(model.RoleAdmin)
	{
		v1.GET("/healthz", h.Healthz)
		h.registerAuthRoutes(v1)

		v1.GET("/meta", viewer, h.Meta)

		// Sources
		v1.GET("/sources", viewer, h.ListSources)
		v1.POST("/sources", admin, h.CreateSource)
		v1.GET("/sources/removed", viewer, h.ListRemovedSources)
		v1.DELETE("/sources/removed/:name", admin, h.PurgeRemovedSource)
		v1.POST("/sources/test", admin, h.TestSource)
		v1.POST("/sources/reload", admin, h.ReloadSources)
		v1.POST("/sources/contexts", admin, h.SourceContexts)
		v1.PUT("/sources/:name", admin, h.UpdateSource)
		v1.DELETE("/sources/:name", admin, h.DeleteSource)

		// Read-only container endpoints
		v1.GET("/containers", viewer, h.ListContainers)
		v1.GET("/containers/:id", viewer, h.GetContainer)
		v1.GET("/containers/:id/score", viewer, h.GetStabilityScore)
		v1.GET("/containers/:id/score/history", viewer, h.GetScoreHistory)
		v1.GET("/containers/:id/events", viewer, h.GetLifecycleEvents)
		v1.GET("/containers/:id/resources", viewer, h.GetResourceSamples)
		v1.GET("/containers/:id/logs", viewer, h.GetContainerLogs)
		v1.GET("/containers/:id/crash-logs", viewer, h.GetContainerCrashLogs)

		// Container management endpoints
		v1.POST("/containers/:id/restart", operator, h.RestartContainer)

		// Ignore / restore (operator); permanent deletion and purges (admin)
		v1.DELETE("/containers/:id", operator, h.DeleteContainer)
		v1.POST("/containers/:id/restore", operator, h.RestoreContainer)
		v1.DELETE("/containers/:id/permanent", admin, h.HardDeleteContainer)

		// Archive and ignored listings + bulk purge
		v1.GET("/archive", viewer, h.GetArchive)
		v1.DELETE("/archive", admin, h.PurgeArchive)
		v1.GET("/ignored", viewer, h.GetIgnored)
		v1.DELETE("/ignored", admin, h.PurgeIgnored)

		// Namespace listing endpoint (Kubernetes)
		v1.GET("/namespaces", viewer, h.ListNamespaces)

		// Deployment management endpoints (Kubernetes)
		v1.GET("/deployments", viewer, h.ListDeployments)
		v1.GET("/deployments/:namespace/:name", viewer, h.GetDeploymentDetail)
		v1.POST("/deployments/:namespace/:name/scale", operator, h.ScaleDeployment)
		v1.POST("/deployments/:namespace/:name/restart", operator, h.RestartDeployment)

		// Pod detail endpoint (Kubernetes)
		v1.GET("/pods/:namespace/:name", viewer, h.GetPodContainers)

		// CronJob tracking endpoints (Kubernetes)
		v1.GET("/cronjobs", viewer, h.ListCronJobs)
		v1.GET("/cronjobs/:namespace/:name/executions", viewer, h.GetCronJobExecutions)

		// Workloads (all instances of a Deployment / StatefulSet / … aggregated)
		v1.GET("/workloads", viewer, h.ListWorkloads)
		v1.GET("/workloads/:id", viewer, h.GetWorkload)
		v1.GET("/workloads/:id/events", viewer, h.GetWorkloadEvents)
		v1.GET("/workloads/:id/resources", viewer, h.GetWorkloadResources)
		v1.GET("/workloads/:id/crash-logs", viewer, h.GetWorkloadCrashLogs)
		v1.GET("/workloads/:id/revisions", viewer, h.GetWorkloadRevisions)
		v1.GET("/workloads/:id/k8s-events", viewer, h.GetWorkloadK8sEvents)
		v1.GET("/k8s-events", viewer, h.ListK8sEvents)
		v1.GET("/k8s-events/reasons", viewer, h.GetK8sEventReasons)
		v1.GET("/workloads/:id/score/history", viewer, h.GetWorkloadScoreHistory)

		// Issues (server-side derived problems)
		v1.GET("/issues", viewer, h.ListIssues)
		v1.GET("/issues/summary", viewer, h.IssueSummary)
		v1.GET("/issues/:id", viewer, h.GetIssue)
		v1.GET("/workloads/:id/issues", viewer, h.GetWorkloadIssues)

		// Notification channels
		v1.GET("/notifications/channels", viewer, h.ListChannels)
		v1.POST("/notifications/channels", admin, h.CreateChannel)
		v1.PUT("/notifications/channels/:id", admin, h.UpdateChannel)
		v1.DELETE("/notifications/channels/:id", admin, h.DeleteChannel)
		v1.POST("/notifications/channels/:id/test", admin, h.TestChannel)
		v1.GET("/notifications/deliveries", viewer, h.ListDeliveries)

		// Opt-in error-log capture and the captured error signatures
		v1.GET("/log-capture/targets", viewer, h.ListLogCaptureTargets)
		v1.POST("/log-capture/targets", operator, h.CreateLogCaptureTarget)
		v1.DELETE("/log-capture/targets/:id", operator, h.DeleteLogCaptureTarget)
		v1.GET("/log-capture/status", viewer, h.LogCaptureStatus)
		v1.GET("/workloads/:id/log-capture", viewer, h.GetWorkloadLogCapture)
		v1.PUT("/workloads/:id/log-capture", operator, h.SetWorkloadLogCapture)
		v1.GET("/workloads/:id/errors", viewer, h.GetWorkloadErrors)
		v1.GET("/errors", viewer, h.ListErrors)
		v1.GET("/errors/:id", viewer, h.GetError)
		v1.GET("/errors/:id/occurrences", viewer, h.ListErrorOccurrences)

		// Recent events endpoint (cross-container)
		v1.GET("/events/recent", viewer, h.GetRecentEvents)

		// Overview aggregates (viewer)
		v1.GET("/overview/activity", viewer, h.GetOverviewActivity)
		v1.GET("/overview/changes", viewer, h.GetOverviewChanges)
	}
}

// Healthz responds with 200 OK when the service and its database are healthy,
// or 503 when the database cannot be reached.
//
//	GET /api/v1/healthz
func (h *Handler) Healthz(c *gin.Context) {
	if h.db != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := h.db.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "database": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "ok"})
}

// ListSources returns the configured collector sources with live container counts.
//
//	GET /api/v1/sources
func (h *Handler) ListSources(c *gin.Context) {
	sources, err := h.svc.ListSources(c.Request.Context())
	if err != nil {
		h.log.Error("list sources failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if sources == nil {
		sources = []service.SourceStatus{}
	}
	c.JSON(http.StatusOK, sources)
}

// ListContainers returns containers known to KYMO.
//
// Query parameters:
//   - runtime (optional): filter by runtime (only "kubernetes").
//   - source  (optional): filter by collector source name (e.g. "prod-k8s").
//   - q       (optional): case-insensitive substring of name/image/namespace/workload.
//   - status  (optional): filter by current status (running, stopped, crashing, …).
//   - workload_kind / workload (optional): filter by workload kind / exact name.
//   - sort / order (paged mode): sort field (incl. "score") and "asc"/"desc".
//   - page    (optional): 1-based page number; when present the response is
//     wrapped in a page envelope: {data, total, page, limit, total_pages}.
//   - limit   (optional, default 20): page size, max 200.
//
// GET /api/v1/containers
func (h *Handler) ListContainers(c *gin.Context) {
	filter := repository.ContainerFilter{
		Runtime: model.Runtime(c.Query("runtime")),
		Source:  c.Query("source"),
		Query:   c.Query("q"),
		Status:  model.ContainerStatus(c.Query("status")),

		WorkloadKind: c.Query("workload_kind"),
		Workload:     c.Query("workload"),
	}

	// If neither pagination param is provided fall back to the flat-list response
	// so the dashboard and stability pages (which fetch all containers) are unaffected.
	pageStr := c.Query("page")
	limitStr := c.Query("limit")

	if pageStr == "" && limitStr == "" {
		containers, err := h.svc.ListContainers(c.Request.Context(), filter)
		if err != nil {
			h.log.Error("list containers failed", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		if containers == nil {
			containers = []*model.Container{}
		}
		c.JSON(http.StatusOK, containers)
		return
	}

	page := 1
	limit := 20
	if v, err := strconv.Atoi(pageStr); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 200 {
		limit = v
	}

	sortField := c.DefaultQuery("sort", "last_seen_at")
	sortDir := c.DefaultQuery("order", "desc")

	data, total, err := h.svc.ListContainersPaged(c.Request.Context(), filter, page, limit, sortField, sortDir)
	if err != nil {
		h.log.Error("list containers paged failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if data == nil {
		data = []*model.Container{}
	}

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	c.JSON(http.StatusOK, gin.H{
		"data":        data,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"total_pages": totalPages,
	})
}

// GetContainer returns the current persisted state of a single container.
//
//	GET /api/v1/containers/:id
func (h *Handler) GetContainer(c *gin.Context) {
	container, err := h.svc.GetContainer(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.log.Error("get container failed", zap.String("id", c.Param("id")), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if container == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "container not found"})
		return
	}

	c.JSON(http.StatusOK, container)
}

// GetStabilityScore returns the latest computed stability score for the container.
//
//	GET /api/v1/containers/:id/score
func (h *Handler) GetStabilityScore(c *gin.Context) {
	score, err := h.svc.GetStabilityScore(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.log.Error("get stability score failed", zap.String("id", c.Param("id")), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if score == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no score available yet"})
		return
	}

	c.JSON(http.StatusOK, score)
}

// GetScoreHistory returns the n most recent stability scores for the container,
// ordered newest-first.
//
// Query parameters:
//   - n (optional, default 10): number of score entries to return.
//
// GET /api/v1/containers/:id/score/history
func (h *Handler) GetScoreHistory(c *gin.Context) {
	n := 10
	if v := c.Query("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}

	history, err := h.svc.GetScoreHistory(c.Request.Context(), c.Param("id"), n)
	if err != nil {
		h.log.Error("get score history failed", zap.String("id", c.Param("id")), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if history == nil {
		history = []*model.StabilityScore{}
	}
	c.JSON(http.StatusOK, history)
}

// GetLifecycleEvents returns the most recent lifecycle events for the
// container, ordered by OccurredAt ascending.
//
// Query parameters:
//   - limit (optional, default 200, max 1000): number of most recent events.
//
// GET /api/v1/containers/:id/events
func (h *Handler) GetLifecycleEvents(c *gin.Context) {
	limit := 200
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		limit = min(v, 1000)
	}
	events, err := h.svc.GetLifecycleEvents(c.Request.Context(), c.Param("id"), limit)
	if err != nil {
		h.log.Error("get lifecycle events failed", zap.String("id", c.Param("id")), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if events == nil {
		events = []*model.LifecycleEvent{}
	}
	c.JSON(http.StatusOK, events)
}

// GetResourceSamples returns resource samples for the container within a time window.
//
// Query parameters:
//   - from (optional, RFC3339): window start; defaults to 1 hour ago.
//   - to   (optional, RFC3339): window end;   defaults to now.
//
// Ranges longer than a few hours are downsampled server-side.
//
// GET /api/v1/containers/:id/resources
func (h *Handler) GetResourceSamples(c *gin.Context) {
	now := time.Now().UTC()
	from := now.Add(-time.Hour)
	to := now

	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	if !from.Before(to) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from must be before to"})
		return
	}

	samples, err := h.svc.GetResourceSamples(c.Request.Context(), c.Param("id"), from, to)
	if err != nil {
		h.log.Error("get resource samples failed", zap.String("id", c.Param("id")), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if samples == nil {
		samples = []*model.ResourceSample{}
	}
	c.JSON(http.StatusOK, samples)
}

// DeleteContainer soft-deletes or ignores a container depending on the request body.
//
// Request body: {"mode": "soft"} or {"mode": "ignore"}
//
//	DELETE /api/v1/containers/:id
func (h *Handler) DeleteContainer(c *gin.Context) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || (body.Mode != "soft" && body.Mode != "ignore") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be 'soft' or 'ignore'"})
		return
	}

	id := c.Param("id")
	ctx := c.Request.Context()

	var (
		err     error
		message string
	)
	switch body.Mode {
	case "soft":
		err = h.svc.SoftDeleteContainer(ctx, id)
		message = "Container archived"
	case "ignore":
		err = h.svc.IgnoreContainer(ctx, id)
		message = "Container ignored"
	}

	if err != nil {
		h.log.Error("delete container failed", zap.String("id", id), zap.String("mode", body.Mode), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	h.audit(c, "container."+body.Mode, "container", id, nil)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": message})
}

// RestoreContainer clears deleted_at and ignored, making the container active again.
//
//	POST /api/v1/containers/:id/restore
func (h *Handler) RestoreContainer(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.RestoreContainer(c.Request.Context(), id); err != nil {
		h.log.Error("restore container failed", zap.String("id", id), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	h.audit(c, "container.restore", "container", id, nil)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Container restored"})
}

// HardDeleteContainer permanently removes the container and all related records.
// Requires ?confirm=true to prevent accidental deletion.
//
//	DELETE /api/v1/containers/:id/permanent
func (h *Handler) HardDeleteContainer(c *gin.Context) {
	if c.Query("confirm") != "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "add ?confirm=true to permanently delete this container"})
		return
	}
	id := c.Param("id")
	if err := h.svc.HardDeleteContainer(c.Request.Context(), id); err != nil {
		h.log.Error("hard delete container failed", zap.String("id", id), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	h.audit(c, "container.hard_delete", "container", id, nil)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Container permanently deleted"})
}

// GetArchive returns soft-deleted containers.
//
// Query parameters:
//   - source (optional): filter by collector source name.
//
// GET /api/v1/archive
func (h *Handler) GetArchive(c *gin.Context) {
	sourceName := c.Query("source")
	containers, err := h.svc.GetDeletedContainers(c.Request.Context(), sourceName)
	if err != nil {
		h.log.Error("get archive failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if containers == nil {
		containers = []*model.Container{}
	}
	c.JSON(http.StatusOK, containers)
}

// GetIgnored returns ignored containers.
//
// Query parameters:
//   - source (optional): filter by collector source name.
//
// GET /api/v1/ignored
func (h *Handler) GetIgnored(c *gin.Context) {
	sourceName := c.Query("source")
	containers, err := h.svc.GetIgnoredContainers(c.Request.Context(), sourceName)
	if err != nil {
		h.log.Error("get ignored failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if containers == nil {
		containers = []*model.Container{}
	}
	c.JSON(http.StatusOK, containers)
}

// PurgeArchive permanently deletes all soft-deleted containers and their history.
// Requires ?confirm=true to prevent accidental mass-deletion.
//
// Query parameters:
//   - confirm (required): must be "true".
//   - source  (optional): filter by collector source name.
//
// DELETE /api/v1/archive
func (h *Handler) PurgeArchive(c *gin.Context) {
	if c.Query("confirm") != "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "add ?confirm=true to permanently delete all archived containers"})
		return
	}
	sourceName := c.Query("source")
	count, err := h.svc.PurgeArchivedContainers(c.Request.Context(), sourceName)
	if err != nil {
		h.log.Error("purge archive failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	h.audit(c, "container.purge_archive", "source", sourceName, map[string]any{"deleted": count})
	c.JSON(http.StatusOK, gin.H{"success": true, "deleted": count, "message": "All archived containers permanently deleted"})
}

// PurgeIgnored permanently deletes all ignored containers and their history.
// Requires ?confirm=true to prevent accidental mass-deletion.
//
// Query parameters:
//   - confirm (required): must be "true".
//   - source  (optional): filter by collector source name.
//
// DELETE /api/v1/ignored
func (h *Handler) PurgeIgnored(c *gin.Context) {
	if c.Query("confirm") != "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "add ?confirm=true to permanently delete all ignored containers"})
		return
	}
	sourceName := c.Query("source")
	count, err := h.svc.PurgeIgnoredContainers(c.Request.Context(), sourceName)
	if err != nil {
		h.log.Error("purge ignored failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	h.audit(c, "container.purge_ignored", "source", sourceName, map[string]any{"deleted": count})
	c.JSON(http.StatusOK, gin.H{"success": true, "deleted": count, "message": "All ignored containers permanently deleted"})
}

// ListCronJobs returns the most recent execution for each CronJob in the given
// source and namespace.
//
// Query parameters:
//   - source    (optional): filter by collector source name.
//   - namespace (optional, default "default"): Kubernetes namespace to query.
//
// GET /api/v1/cronjobs
func (h *Handler) ListCronJobs(c *gin.Context) {
	sourceName := c.Query("source")
	namespace := c.Query("namespace")
	execs, err := h.svc.ListCronJobs(c.Request.Context(), sourceName, namespace)
	if err != nil {
		h.log.Error("list cronjobs failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if execs == nil {
		execs = []*model.CronJobExecution{}
	}
	c.JSON(http.StatusOK, execs)
}

// GetRecentEvents returns the n most recent lifecycle events across all containers.
//
// Query parameters:
//   - limit  (optional, default 20, max 200): number of events to return.
//   - source (optional): restrict to one collector source.
//   - type   (optional): restrict to one event type (start, crash, oom, …).
//   - before (optional, RFC3339): only events older than this (pagination cursor).
//
// GET /api/v1/events/recent
func (h *Handler) GetRecentEvents(c *gin.Context) {
	limit := 20
	if v := c.Query("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	q := repository.EventQuery{Limit: limit, Source: c.Query("source"), Type: model.EventType(c.Query("type"))}
	if v := c.Query("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "before must be an RFC3339 timestamp"})
			return
		}
		q.Before = t
	}
	events, err := h.svc.GetRecentEvents(c.Request.Context(), q)
	if err != nil {
		h.log.Error("get recent events failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if events == nil {
		events = []*model.LifecycleEvent{}
	}
	c.JSON(http.StatusOK, events)
}

// GetCronJobExecutions returns recent executions for a specific CronJob.
//
// Path parameters:
//   - namespace: Kubernetes namespace.
//   - name:      CronJob name.
//
// Query parameters:
//   - source (optional): filter by collector source name.
//   - limit  (optional, default 20): maximum number of executions to return.
//
// GET /api/v1/cronjobs/:namespace/:name/executions
func (h *Handler) GetCronJobExecutions(c *gin.Context) {
	sourceName := c.Query("source")
	namespace := c.Param("namespace")
	name := c.Param("name")
	limit := 20
	if v := c.Query("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	execs, err := h.svc.GetCronJobExecutions(c.Request.Context(), sourceName, namespace, name, limit)
	if err != nil {
		h.log.Error("get cronjob executions failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if execs == nil {
		execs = []*model.CronJobExecution{}
	}
	c.JSON(http.StatusOK, execs)
}
