package httphandler

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// ListWorkloads returns one summary per workload, aggregated over every
// recorded instance (pods across rollouts, Compose containers).
//
// Query parameters: source, namespace, kind, q (name/namespace substring),
// status (running|crashing|stopped|unknown), system (true to include system
// workloads; default false).
//
// GET /api/v1/workloads
func (h *Handler) ListWorkloads(c *gin.Context) {
	list, err := h.svc.ListWorkloads(c.Request.Context(), repository.WorkloadFilter{
		Source:    c.Query("source"),
		Namespace: c.Query("namespace"),
		Kind:      c.Query("kind"),
		Query:     c.Query("q"),
	})
	if err != nil {
		h.log.Error("list workloads failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	status := model.ContainerStatus(c.Query("status"))
	includeSystem := c.Query("system") == "true"
	out := make([]*model.WorkloadSummary, 0, len(list))
	for _, w := range list {
		if (status != "" && w.Status != status) || (!includeSystem && w.IsSystem) {
			continue
		}
		out = append(out, w)
	}
	c.JSON(http.StatusOK, out)
}

// GetWorkload returns a workload summary, its latest score and every recorded
// instance (newest first).
//
// GET /api/v1/workloads/:id
func (h *Handler) GetWorkload(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	w, err := h.svc.GetWorkload(ctx, id)
	if err != nil {
		h.log.Error("get workload failed", zap.String("id", id), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if w == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "workload not found"})
		return
	}
	members, err := h.svc.ListContainers(ctx, repository.ContainerFilter{WorkloadID: id})
	if err != nil {
		h.log.Error("list workload members failed", zap.String("id", id), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	sort.SliceStable(members, func(i, j int) bool { return members[i].FirstSeenAt.After(members[j].FirstSeenAt) })
	if members == nil {
		members = []*model.Container{}
	}
	score, err := h.svc.GetWorkloadScore(ctx, id)
	if err != nil {
		h.log.Warn("get workload score failed", zap.String("id", id), zap.Error(err))
	}
	c.JSON(http.StatusOK, gin.H{"workload": w, "score": score, "instances": members})
}

// GetWorkloadEvents returns lifecycle events of every instance of a workload,
// newest first, with container names.
//
// Query parameters: limit (default 50, max 200), type, before (RFC3339).
//
// GET /api/v1/workloads/:id/events
func (h *Handler) GetWorkloadEvents(c *gin.Context) {
	limit := 50
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		limit = min(v, 200)
	}
	q := repository.EventQuery{Limit: limit, WorkloadID: c.Param("id"), Type: model.EventType(c.Query("type"))}
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
		h.log.Error("get workload events failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if events == nil {
		events = []*model.LifecycleEvent{}
	}
	c.JSON(http.StatusOK, events)
}

// GetWorkloadResources returns CPU and memory summed over all instances.
//
// Query parameters: from, to (RFC3339; default last hour).
//
// GET /api/v1/workloads/:id/resources
func (h *Handler) GetWorkloadResources(c *gin.Context) {
	to := time.Now().UTC()
	from := to.Add(-time.Hour)
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
	samples, err := h.svc.GetWorkloadResources(c.Request.Context(), c.Param("id"), from, to)
	if err != nil {
		h.log.Error("get workload resources failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if samples == nil {
		samples = []*model.ResourceSample{}
	}
	c.JSON(http.StatusOK, samples)
}

// GetWorkloadScoreHistory returns recent workload scores, newest first.
//
// Query parameters: n (default 60).
//
// GET /api/v1/workloads/:id/score/history
func (h *Handler) GetWorkloadScoreHistory(c *gin.Context) {
	n, _ := strconv.Atoi(c.Query("n"))
	history, err := h.svc.GetWorkloadScoreHistory(c.Request.Context(), c.Param("id"), n)
	if err != nil {
		h.log.Error("get workload score history failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if history == nil {
		history = []*model.WorkloadScore{}
	}
	c.JSON(http.StatusOK, history)
}
