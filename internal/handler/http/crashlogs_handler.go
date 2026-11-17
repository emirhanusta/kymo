package httphandler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
)

// GetWorkloadCrashLogs returns the newest crash-log snapshots across every
// instance of a workload.
//
// Query parameters: limit (default 10, max 50).
//
// GET /api/v1/workloads/:id/crash-logs
func (h *Handler) GetWorkloadCrashLogs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	logs, err := h.svc.GetWorkloadCrashLogs(c.Request.Context(), c.Param("id"), limit)
	if err != nil {
		h.log.Error("get workload crash logs failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if logs == nil {
		logs = []*model.CrashLog{}
	}
	c.JSON(http.StatusOK, logs)
}

// GetContainerCrashLogs returns a container's crash-log snapshots, newest
// first, or with occurred_at the single snapshot of that termination (±2s).
//
// Query parameters: limit (default 20, max 50), occurred_at (RFC3339Nano).
//
// GET /api/v1/containers/:id/crash-logs
func (h *Handler) GetContainerCrashLogs(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")

	if v := c.Query("occurred_at"); v != "" {
		at, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "occurred_at must be an RFC3339 timestamp"})
			return
		}
		cl, err := h.svc.GetContainerCrashLogAt(ctx, id, at)
		if err != nil {
			h.log.Error("get container crash log failed", zap.String("id", id), zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		if cl == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "crash log not found"})
			return
		}
		c.JSON(http.StatusOK, cl)
		return
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	logs, err := h.svc.GetContainerCrashLogs(ctx, id, limit)
	if err != nil {
		h.log.Error("get container crash logs failed", zap.String("id", id), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if logs == nil {
		logs = []*model.CrashLog{}
	}
	c.JSON(http.StatusOK, logs)
}

// GetWorkloadRevisions returns the workload's rollout revisions, newest first.
//
// GET /api/v1/workloads/:id/revisions
func (h *Handler) GetWorkloadRevisions(c *gin.Context) {
	revs, err := h.svc.GetWorkloadRevisions(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.log.Error("get workload revisions failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if revs == nil {
		revs = []model.Revision{}
	}
	c.JSON(http.StatusOK, revs)
}
