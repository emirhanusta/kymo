package httphandler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/service/notify"
)

// NotificationAPI is the part of notify.Service the handlers use.
type NotificationAPI interface {
	ListChannels(ctx context.Context) ([]notify.ChannelView, error)
	Create(ctx context.Context, in notify.ChannelInput) (*notify.ChannelView, error)
	Update(ctx context.Context, id int64, in notify.ChannelInput) (*notify.ChannelView, error)
	Delete(ctx context.Context, id int64) error
	Test(ctx context.Context, id int64) (*notify.Result, error)
	ListDeliveries(ctx context.Context, channelID int64, limit int) ([]*model.ChannelDelivery, error)
}

func (h *Handler) notifError(c *gin.Context, op string, err error) {
	var ve *notify.ValidationError
	switch {
	case errors.As(err, &ve):
		c.JSON(http.StatusBadRequest, gin.H{"error": ve.Msg})
	case errors.Is(err, notify.ErrChannelLocked):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, notify.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		h.log.Error(op+" failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}

// notifReady writes a 503 and returns false when notifications are unavailable.
func (h *Handler) notifReady(c *gin.Context) bool {
	if h.notif == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "notifications are not available"})
		return false
	}
	return true
}

func channelID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel id must be a number"})
		return 0, false
	}
	return id, true
}

// ListChannels returns every notification channel (URLs masked).
//
// GET /api/v1/notifications/channels
func (h *Handler) ListChannels(c *gin.Context) {
	if h.notif == nil {
		c.JSON(http.StatusOK, []notify.ChannelView{})
		return
	}
	list, err := h.notif.ListChannels(c.Request.Context())
	if err != nil {
		h.notifError(c, "list channels", err)
		return
	}
	if list == nil {
		list = []notify.ChannelView{}
	}
	c.JSON(http.StatusOK, list)
}

// CreateChannel stores a new channel (the URL is encrypted at rest).
//
// POST /api/v1/notifications/channels
func (h *Handler) CreateChannel(c *gin.Context) {
	if !h.notifReady(c) {
		return
	}
	var in notify.ChannelInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	v, err := h.notif.Create(c.Request.Context(), in)
	if err != nil {
		h.notifError(c, "create channel", err)
		return
	}
	c.JSON(http.StatusCreated, v)
}

// UpdateChannel changes a channel; omitted fields (incl. url, secret) are kept.
//
// PUT /api/v1/notifications/channels/:id
func (h *Handler) UpdateChannel(c *gin.Context) {
	if !h.notifReady(c) {
		return
	}
	id, ok := channelID(c)
	if !ok {
		return
	}
	var in notify.ChannelInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	v, err := h.notif.Update(c.Request.Context(), id, in)
	if err != nil {
		h.notifError(c, "update channel", err)
		return
	}
	c.JSON(http.StatusOK, v)
}

// DeleteChannel removes a channel.
//
// DELETE /api/v1/notifications/channels/:id
func (h *Handler) DeleteChannel(c *gin.Context) {
	if !h.notifReady(c) {
		return
	}
	id, ok := channelID(c)
	if !ok {
		return
	}
	if err := h.notif.Delete(c.Request.Context(), id); err != nil {
		h.notifError(c, "delete channel", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Channel deleted"})
}

// TestChannel sends a sample notification and reports the outcome.
//
// POST /api/v1/notifications/channels/:id/test -> {ok, status_code, error}
func (h *Handler) TestChannel(c *gin.Context) {
	if !h.notifReady(c) {
		return
	}
	id, ok := channelID(c)
	if !ok {
		return
	}
	res, err := h.notif.Test(c.Request.Context(), id)
	if err != nil {
		h.notifError(c, "test channel", err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// ListDeliveries returns the delivery log, newest first.
//
// Query parameters: channel_id, limit (default 50, max 500).
//
// GET /api/v1/notifications/deliveries
func (h *Handler) ListDeliveries(c *gin.Context) {
	if h.notif == nil {
		c.JSON(http.StatusOK, []*model.ChannelDelivery{})
		return
	}
	var channel int64
	if v := c.Query("channel_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id must be a number"})
			return
		}
		channel = n
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	list, err := h.notif.ListDeliveries(c.Request.Context(), channel, limit)
	if err != nil {
		h.notifError(c, "list deliveries", err)
		return
	}
	if list == nil {
		list = []*model.ChannelDelivery{}
	}
	c.JSON(http.StatusOK, list)
}
