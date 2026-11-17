package httphandler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

const (
	defaultIssueLimit = 200
	maxIssueLimit     = 500
)

// Option customises the Handler built by New.
type Option func(*Handler)

// WithIssues enables the issue endpoints.
func WithIssues(r repository.IssueRepository) Option { return func(h *Handler) { h.issues = r } }

// WithNotifications enables the notification channel endpoints.
func WithNotifications(n NotificationAPI) Option { return func(h *Handler) { h.notif = n } }

// WithServerInfo sets the values /meta reports: the UI's public URL and whether
// notifications are being sent.
func WithServerInfo(publicURL string, notificationsEnabled bool) Option {
	return func(h *Handler) { h.publicURL, h.notifEnabled = publicURL, notificationsEnabled }
}

func (h *Handler) issueQuery(c *gin.Context, q repository.IssueQuery) (repository.IssueQuery, bool) {
	bad := func(msg string) (repository.IssueQuery, bool) {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return q, false
	}
	switch st := c.DefaultQuery("status", "open"); st {
	case "open", "resolved", "all":
		q.Status = st
	default:
		return bad("status must be open, resolved or all")
	}
	if sev := c.Query("severity"); sev != "" {
		if sev != string(model.SeverityCritical) && sev != string(model.SeverityWarning) {
			return bad("severity must be critical or warning")
		}
		q.Severity = sev
	}
	if t := c.Query("type"); t != "" {
		if !model.ValidIssueType(t) {
			return bad("unknown issue type " + strconv.Quote(t))
		}
		q.Type = t
	}
	if q.Source == "" {
		q.Source = c.Query("source")
	}
	q.Limit = defaultIssueLimit
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		q.Limit = min(v, maxIssueLimit)
	}
	var ok bool
	if q.Before, ok = parseTimeParam(c, "before"); !ok {
		return q, false
	}
	return q, true
}

func (h *Handler) respondIssues(c *gin.Context, q repository.IssueQuery) {
	if h.issues == nil {
		c.JSON(http.StatusOK, []*model.Issue{})
		return
	}
	q, ok := h.issueQuery(c, q)
	if !ok {
		return
	}
	list, err := h.issues.List(c.Request.Context(), q)
	if err != nil {
		h.log.Error("list issues failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if list == nil {
		list = []*model.Issue{}
	}
	c.JSON(http.StatusOK, list)
}

// ListIssues returns issues.
//
// Query parameters: status (open|resolved|all, default open), source, severity,
// type, limit (default 200, max 500) and before (RFC3339 cursor: resolved_at for
// status=resolved, first_seen for status=all). Open issues are sorted critical
// first, then newest first; resolved ones by resolved_at.
//
// GET /api/v1/issues
func (h *Handler) ListIssues(c *gin.Context) { h.respondIssues(c, repository.IssueQuery{}) }

// GetWorkloadIssues returns the issues of one workload (same filters as ListIssues).
//
// GET /api/v1/workloads/:id/issues
func (h *Handler) GetWorkloadIssues(c *gin.Context) {
	h.respondIssues(c, repository.IssueQuery{WorkloadID: c.Param("id")})
}

// GetIssue returns one issue.
//
// GET /api/v1/issues/:id
func (h *Handler) GetIssue(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "issue id must be a number"})
		return
	}
	if h.issues == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "issue not found"})
		return
	}
	i, err := h.issues.Get(c.Request.Context(), id)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "issue not found"})
	case err != nil:
		h.log.Error("get issue failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	default:
		c.JSON(http.StatusOK, i)
	}
}

// IssueSummary counts open issues for badges.
//
// GET /api/v1/issues/summary -> {open, critical, warning, by_source}
func (h *Handler) IssueSummary(c *gin.Context) {
	if h.issues == nil {
		c.JSON(http.StatusOK, model.IssueSummary{BySource: map[string]int{}})
		return
	}
	s, err := h.issues.Summary(c.Request.Context())
	if err != nil {
		h.log.Error("issue summary failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if s.BySource == nil {
		s.BySource = map[string]int{}
	}
	c.JSON(http.StatusOK, s)
}
