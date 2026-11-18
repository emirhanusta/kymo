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

// overviewScope resolves ?source=. A named cluster (registered or removed, so
// recorded history stays readable) is used as is; without one the scope is the
// currently registered clusters, so removed clusters never count as live data.
func (h *Handler) overviewScope(c *gin.Context) (repository.OverviewScope, bool) {
	if src := c.Query("source"); src != "" {
		return repository.OverviewScope{Source: src}, true
	}
	list, err := h.svc.ListSources(c.Request.Context())
	if err != nil {
		h.log.Error("overview: list sources failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return repository.OverviewScope{}, false
	}
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
	}
	return repository.OverviewScope{Sources: names}, true
}

// GetOverviewActivity returns hourly start / crash / OOM counts of the last
// `hours` hours (default 24, max 168) and the crash+OOM count of the window before.
//
// Query parameters: source (optional), hours (optional).
//
// GET /api/v1/overview/activity
func (h *Handler) GetOverviewActivity(c *gin.Context) {
	hours := 24
	if v := c.Query("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 168 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "hours must be between 1 and 168"})
			return
		}
		hours = n
	}
	scope, ok := h.overviewScope(c)
	if !ok {
		return
	}
	act, err := h.svc.GetOverviewActivity(c.Request.Context(), repository.ActivityQuery{
		OverviewScope: scope, Now: time.Now(), Hours: hours,
	})
	if err != nil {
		h.log.Error("overview activity failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if act == nil {
		act = &model.Activity{}
	}
	if act.Buckets == nil {
		act.Buckets = []model.ActivityBucket{}
	}
	c.JSON(http.StatusOK, act)
}

// GetOverviewChanges returns the latest rollouts of new workload revisions.
//
// Query parameters: source (optional), limit (optional, default 6, max 50).
//
// GET /api/v1/overview/changes
func (h *Handler) GetOverviewChanges(c *gin.Context) {
	limit := 6
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	scope, ok := h.overviewScope(c)
	if !ok {
		return
	}
	out, err := h.svc.GetOverviewChanges(c.Request.Context(), repository.ChangesQuery{OverviewScope: scope, Limit: limit})
	if err != nil {
		h.log.Error("overview changes failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if out == nil {
		out = []*model.Change{}
	}
	c.JSON(http.StatusOK, out)
}
