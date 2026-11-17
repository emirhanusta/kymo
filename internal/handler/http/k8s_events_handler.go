package httphandler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

const (
	defaultK8sEventLimit = 100
	maxK8sEventLimit     = 500
)

// parseTimeParam parses an optional RFC3339 query parameter. ok is false (and a
// 400 has been written) when the value is present but malformed.
func parseTimeParam(c *gin.Context, name string) (t time.Time, ok bool) {
	v := c.Query(name)
	if v == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": name + " must be an RFC3339 timestamp"})
		return time.Time{}, false
	}
	return t, true
}

func k8sEventLimit(c *gin.Context) int {
	limit := defaultK8sEventLimit
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		limit = min(v, maxK8sEventLimit)
	}
	return limit
}

func (h *Handler) respondK8sEvents(c *gin.Context, q repository.K8sEventQuery) {
	var ok bool
	if q.Since, ok = parseTimeParam(c, "since"); !ok {
		return
	}
	if q.Before, ok = parseTimeParam(c, "before"); !ok {
		return
	}
	q.Limit = k8sEventLimit(c)
	events, err := h.svc.ListK8sEvents(c.Request.Context(), q)
	if err != nil {
		h.log.Error("list k8s events failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if events == nil {
		events = []*model.K8sEvent{}
	}
	c.JSON(http.StatusOK, events)
}

// ListK8sEvents returns persisted Kubernetes Events, newest last_seen first.
//
// Query parameters: source, namespace, type, reason, workload_id, q (substring
// over reason/message/name), since and before (RFC3339), limit (default 100, max 500).
//
// GET /api/v1/k8s-events
func (h *Handler) ListK8sEvents(c *gin.Context) {
	h.respondK8sEvents(c, repository.K8sEventQuery{
		Source:     c.Query("source"),
		Namespace:  c.Query("namespace"),
		Type:       c.Query("type"),
		Reason:     c.Query("reason"),
		WorkloadID: c.Query("workload_id"),
		Query:      c.Query("q"),
	})
}

// GetWorkloadK8sEvents returns the Kubernetes Events of one workload.
//
// Query parameters: since, before (RFC3339), limit (default 100, max 500).
//
// GET /api/v1/workloads/:id/k8s-events
func (h *Handler) GetWorkloadK8sEvents(c *gin.Context) {
	h.respondK8sEvents(c, repository.K8sEventQuery{WorkloadID: c.Param("id")})
}

// GetK8sEventReasons aggregates event reasons.
//
// Query parameters: source, since (RFC3339; default 24h ago).
//
// GET /api/v1/k8s-events/reasons
func (h *Handler) GetK8sEventReasons(c *gin.Context) {
	since, ok := parseTimeParam(c, "since")
	if !ok {
		return
	}
	if since.IsZero() {
		since = time.Now().Add(-24 * time.Hour)
	}
	reasons, err := h.svc.ListK8sEventReasons(c.Request.Context(), c.Query("source"), since)
	if err != nil {
		h.log.Error("list k8s event reasons failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if reasons == nil {
		reasons = []model.K8sEventReason{}
	}
	c.JSON(http.StatusOK, reasons)
}
