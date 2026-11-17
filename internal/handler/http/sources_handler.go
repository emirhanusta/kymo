package httphandler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/service"
)

// Version is the build version reported by /meta (set via -ldflags).
var Version = "dev"

// Meta reports server capabilities the UI needs.
//
//	GET /api/v1/meta
func (h *Handler) Meta(c *gin.Context) {
	meta := gin.H{
		"version":               Version,
		"secret_key_configured": h.sources != nil && h.sources.SecretConfigured(),
		"config_file":           "",
		"config_sync":           nil,
	}
	if h.sources != nil {
		meta["config_file"] = h.sources.ConfigFile()
		if sync := h.sources.LastConfigSync(); sync != nil {
			meta["config_sync"] = sync
		}
	}
	c.JSON(http.StatusOK, meta)
}

// ReloadSources re-reads config.yaml now and applies its sources.
//
//	POST /api/v1/sources/reload
func (h *Handler) ReloadSources(c *gin.Context) {
	res, err := h.sources.ReloadConfig(c.Request.Context())
	var ce *service.ConfigError
	switch {
	case errors.Is(err, service.ErrNoConfigFile):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.As(err, &ce):
		c.JSON(http.StatusBadRequest, gin.H{"error": ce.Error()})
	case err != nil:
		h.log.Error("reload sources failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusOK, res)
	}
}

// sourceError maps SourceManager errors to HTTP responses.
func (h *Handler) sourceError(c *gin.Context, op string, err error) {
	var ve *service.ValidationError
	switch {
	case errors.As(err, &ve):
		c.JSON(http.StatusBadRequest, gin.H{"error": ve.Msg})
	case errors.Is(err, service.ErrSourceExists):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrSourceLocked):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrSourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		h.log.Error(op+" failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// sourceStatus returns the live status of one source.
func (h *Handler) sourceStatus(c *gin.Context, name string) (service.SourceStatus, bool) {
	all, err := h.svc.ListSources(c.Request.Context())
	if err != nil {
		return service.SourceStatus{}, false
	}
	for _, s := range all {
		if s.Name == name {
			return s, true
		}
	}
	return service.SourceStatus{}, false
}

// TestSource checks connectivity for a source definition without saving it.
//
//	POST /api/v1/sources/test
func (h *Handler) TestSource(c *gin.Context) {
	var in service.SourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.sources.Test(c.Request.Context(), in))
}

// SourceContexts lists the contexts of a kubeconfig.
//
//	POST /api/v1/sources/contexts   body: {kubeconfig_path?, kubeconfig?}
func (h *Handler) SourceContexts(c *gin.Context) {
	var body struct {
		KubeconfigPath string `json:"kubeconfig_path"`
		Kubeconfig     string `json:"kubeconfig"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	contexts, current, err := h.sources.Contexts(body.KubeconfigPath, body.Kubeconfig)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contexts": contexts, "current": current})
}

// CreateSource stores and starts a new source.
//
//	POST /api/v1/sources
func (h *Handler) CreateSource(c *gin.Context) {
	var in service.SourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	spec, err := h.sources.Create(c.Request.Context(), in)
	if err != nil {
		h.sourceError(c, "create source", err)
		return
	}
	st, _ := h.sourceStatus(c, spec.Name)
	c.JSON(http.StatusCreated, st)
}

// UpdateSource replaces a user source's settings and restarts it.
//
//	PUT /api/v1/sources/:name
func (h *Handler) UpdateSource(c *gin.Context) {
	var in service.SourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	spec, err := h.sources.Update(c.Request.Context(), c.Param("name"), in)
	if err != nil {
		h.sourceError(c, "update source", err)
		return
	}
	st, _ := h.sourceStatus(c, spec.Name)
	c.JSON(http.StatusOK, st)
}

// DeleteSource stops and removes a user source. ?purge=true also deletes its
// recorded history.
//
//	DELETE /api/v1/sources/:name
func (h *Handler) DeleteSource(c *gin.Context) {
	name := c.Param("name")
	purge := c.Query("purge") == "true"
	if err := h.sources.Delete(c.Request.Context(), name, purge); err != nil {
		h.sourceError(c, "delete source", err)
		return
	}
	msg := "Source removed; its history is kept"
	if purge {
		msg = "Source and its history deleted"
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": msg})
}

// ListRemovedSources lists recorded history of sources that no longer exist.
//
//	GET /api/v1/sources/removed
func (h *Handler) ListRemovedSources(c *gin.Context) {
	out, err := h.sources.RemovedSources(c.Request.Context())
	if err != nil {
		h.sourceError(c, "list removed sources", err)
		return
	}
	if out == nil {
		out = []model.SourceHistory{}
	}
	c.JSON(http.StatusOK, out)
}

// PurgeRemovedSource deletes the history of a removed source. Requires ?confirm=true.
//
//	DELETE /api/v1/sources/removed/:name
func (h *Handler) PurgeRemovedSource(c *gin.Context) {
	if c.Query("confirm") != "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirm=true is required to purge a removed source's history"})
		return
	}
	name := c.Param("name")
	n, err := h.sources.PurgeRemovedSource(c.Request.Context(), name)
	switch {
	case errors.Is(err, service.ErrSourceRegistered):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrNoHistory):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case err != nil:
		h.sourceError(c, "purge removed source", err)
	default:
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "History of removed source " + name + " deleted", "deleted_containers": n})
	}
}

// guardReadOnly writes a 403 and returns true when sourceName is read-only.
func (h *Handler) guardReadOnly(c *gin.Context, sourceName string) bool {
	if sourceName != "" && h.svc.IsReadOnly(sourceName) {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "source " + sourceName + " is read-only; management actions are disabled",
		})
		return true
	}
	return false
}
