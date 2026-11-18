package httphandler

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/service/errorlogs"
)

// ErrorLogAPI is the part of errorlogs.Service the handlers use.
type ErrorLogAPI interface {
	ListTargets(ctx context.Context) ([]*model.LogCaptureTarget, error)
	CreateTarget(ctx context.Context, in errorlogs.TargetInput) (*model.LogCaptureTarget, error)
	DeleteTarget(ctx context.Context, id int64) error
	WorkloadCapture(ctx context.Context, workloadID string) (*errorlogs.WorkloadCapture, error)
	SetWorkloadCapture(ctx context.Context, workloadID string, enabled bool) (*errorlogs.WorkloadCapture, error)
	Status() model.LogCaptureStatus
	ListSignatures(ctx context.Context, q repository.ErrorQuery, window time.Duration) ([]*model.ErrorSignature, error)
	GetSignature(ctx context.Context, id, workloadID string) (*model.ErrorSignature, error)
	ListOccurrences(ctx context.Context, id string, before time.Time, limit int) ([]*model.ErrorOccurrence, error)
}

// WithErrorLogs enables the log-capture and error endpoints.
func WithErrorLogs(api ErrorLogAPI) Option { return func(h *Handler) { h.errlogs = api } }

func (h *Handler) errorLogsReady(c *gin.Context) bool {
	if h.errlogs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "error log capture is not available"})
		return false
	}
	return true
}

func (h *Handler) errorLogsError(c *gin.Context, op string, err error) {
	var (
		ve  *errorlogs.ValidationError
		via *errorlogs.EnabledViaError
	)
	switch {
	case errors.As(err, &ve):
		c.JSON(http.StatusBadRequest, gin.H{"error": ve.Msg})
	case errors.As(err, &via):
		c.JSON(http.StatusConflict, gin.H{"error": via.Error(), "via": via.Via})
	case errors.Is(err, errorlogs.ErrDuplicate):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, errorlogs.ErrTargetLocked):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, errorlogs.ErrTargetNotFound), errors.Is(err, errorlogs.ErrSignatureNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errorlogs.ErrBadWorkload):
		c.JSON(http.StatusNotFound, gin.H{"error": "workload not found"})
	default:
		h.log.Error(op+" failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}

// ListLogCaptureTargets returns every capture target.
//
// GET /api/v1/log-capture/targets
func (h *Handler) ListLogCaptureTargets(c *gin.Context) {
	if !h.errorLogsReady(c) {
		return
	}
	list, err := h.errlogs.ListTargets(c.Request.Context())
	if err != nil {
		h.errorLogsError(c, "list log capture targets", err)
		return
	}
	if list == nil {
		list = []*model.LogCaptureTarget{}
	}
	c.JSON(http.StatusOK, list)
}

// CreateLogCaptureTarget opts a namespace or workload in.
//
// POST /api/v1/log-capture/targets {source_name, namespace, workload_id?}
func (h *Handler) CreateLogCaptureTarget(c *gin.Context) {
	if !h.errorLogsReady(c) {
		return
	}
	var in errorlogs.TargetInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	t, err := h.errlogs.CreateTarget(c.Request.Context(), in)
	if err != nil {
		h.errorLogsError(c, "create log capture target", err)
		return
	}
	h.audit(c, "logcapture.target_create", "log_capture_target", strconv.FormatInt(t.ID, 10),
		map[string]any{"source": in.SourceName, "namespace": in.Namespace, "workload_id": in.WorkloadID})
	c.JSON(http.StatusCreated, t)
}

// DeleteLogCaptureTarget removes a UI target (config.yaml targets are locked).
//
// DELETE /api/v1/log-capture/targets/:id
func (h *Handler) DeleteLogCaptureTarget(c *gin.Context) {
	if !h.errorLogsReady(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target id must be a number"})
		return
	}
	if err := h.errlogs.DeleteTarget(c.Request.Context(), id); err != nil {
		h.errorLogsError(c, "delete log capture target", err)
		return
	}
	h.audit(c, "logcapture.target_delete", "log_capture_target", strconv.FormatInt(id, 10), nil)
	c.Status(http.StatusNoContent)
}

// GetWorkloadLogCapture reports whether a workload is opted in and how.
//
// GET /api/v1/workloads/:id/log-capture
func (h *Handler) GetWorkloadLogCapture(c *gin.Context) {
	if !h.errorLogsReady(c) {
		return
	}
	st, err := h.errlogs.WorkloadCapture(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.errorLogsError(c, "get workload log capture", err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// SetWorkloadLogCapture turns the workload's own opt-in on or off.
//
// PUT /api/v1/workloads/:id/log-capture {enabled}
func (h *Handler) SetWorkloadLogCapture(c *gin.Context) {
	if !h.errorLogsReady(c) {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be {\"enabled\": true|false}"})
		return
	}
	st, err := h.errlogs.SetWorkloadCapture(c.Request.Context(), c.Param("id"), *in.Enabled)
	if err != nil {
		h.errorLogsError(c, "set workload log capture", err)
		return
	}
	h.audit(c, "logcapture.workload_toggle", "workload", c.Param("id"), map[string]any{"enabled": *in.Enabled})
	c.JSON(http.StatusOK, st)
}

// LogCaptureStatus reports the live state of capture per source.
//
// GET /api/v1/log-capture/status
func (h *Handler) LogCaptureStatus(c *gin.Context) {
	if !h.errorLogsReady(c) {
		return
	}
	c.JSON(http.StatusOK, h.errlogs.Status())
}

var windowRe = regexp.MustCompile(`^(\d{1,4})([mhd])$`)

// parseWindow reads the "since" window (1h, 6h, 24h, 7d, …; 1m to 30d, default 24h).
func parseWindow(c *gin.Context) (time.Duration, bool) {
	v := c.Query("since")
	if v == "" {
		return errorlogs.DefaultWindow, true
	}
	m := windowRe.FindStringSubmatch(v)
	if m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
		if d := time.Duration(n) * unit; n > 0 && d <= 30*24*time.Hour {
			return d, true
		}
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "since must be a window such as 1h, 6h, 24h or 7d (at most 30d)"})
	return 0, false
}

func (h *Handler) respondErrors(c *gin.Context, q repository.ErrorQuery) {
	if !h.errorLogsReady(c) {
		return
	}
	window, ok := parseWindow(c)
	if !ok {
		return
	}
	q.Limit = 100
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		q.Limit = min(v, errorlogs.MaxLimit)
	}
	list, err := h.errlogs.ListSignatures(c.Request.Context(), q, window)
	if err != nil {
		h.errorLogsError(c, "list error signatures", err)
		return
	}
	if list == nil {
		list = []*model.ErrorSignature{}
	}
	c.JSON(http.StatusOK, list)
}

// ListErrors returns error signatures seen in the window, newest last_seen first.
//
// Query parameters: source, namespace, workload_id, since (1h|6h|24h|7d,
// default 24h), limit (default 100, max 500).
//
// GET /api/v1/errors
func (h *Handler) ListErrors(c *gin.Context) {
	h.respondErrors(c, repository.ErrorQuery{
		Source: c.Query("source"), Namespace: c.Query("namespace"), WorkloadID: c.Query("workload_id"),
	})
}

// GetWorkloadErrors returns the error signatures of one workload (empty when
// nothing was captured).
//
// GET /api/v1/workloads/:id/errors
func (h *Handler) GetWorkloadErrors(c *gin.Context) {
	id := c.Param("id")
	if _, _, _, _, ok := model.SplitWorkloadID(id); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "workload not found"})
		return
	}
	h.respondErrors(c, repository.ErrorQuery{WorkloadID: id})
}

// GetError returns one signature (24h window).
//
// GET /api/v1/errors/:id[?workload_id=]
func (h *Handler) GetError(c *gin.Context) {
	if !h.errorLogsReady(c) {
		return
	}
	sig, err := h.errlogs.GetSignature(c.Request.Context(), c.Param("id"), c.Query("workload_id"))
	if err != nil {
		h.errorLogsError(c, "get error signature", err)
		return
	}
	c.JSON(http.StatusOK, sig)
}

// ListErrorOccurrences returns the stored occurrences of a signature, newest
// first, strictly older than before.
//
// Query parameters: limit (default 50, max 500), before (RFC3339Nano).
//
// GET /api/v1/errors/:id/occurrences
func (h *Handler) ListErrorOccurrences(c *gin.Context) {
	if !h.errorLogsReady(c) {
		return
	}
	before, ok := parseTimeParam(c, "before")
	if !ok {
		return
	}
	limit := 50
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		limit = min(v, errorlogs.MaxLimit)
	}
	list, err := h.errlogs.ListOccurrences(c.Request.Context(), c.Param("id"), before, limit)
	if err != nil {
		h.errorLogsError(c, "list error occurrences", err)
		return
	}
	if list == nil {
		list = []*model.ErrorOccurrence{}
	}
	c.JSON(http.StatusOK, list)
}
