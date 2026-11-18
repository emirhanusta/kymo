package auth

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
)

const identityKey = "kymo.identity"

// Middleware resolves the caller and stores the Identity in the context. It
// never rejects; routes decide with Require.
func (s *Service) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := s.Resolve(c.Request)
		switch {
		case err == nil:
			if id != nil {
				c.Set(identityKey, id)
			}
		case errors.Is(err, ErrUnauthenticated):
			// leave unauthenticated
		default:
			s.Log.Error("resolving identity failed", zap.Error(err))
			Abort(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.Next()
	}
}

// IdentityOf returns the caller stored by Middleware (nil when anonymous).
func IdentityOf(c *gin.Context) *Identity {
	v, ok := c.Get(identityKey)
	if !ok {
		return nil
	}
	id, _ := v.(*Identity)
	return id
}

// PasswordChangeRequired is the error code of requests blocked until the
// signed-in user replaces an initial / forced password.
const PasswordChangeRequired = "password_change_required"

// Require returns a route guard demanding at least min. 401 for anonymous
// callers, 403 {code: password_change_required} while the account must change
// its password first, 403 {code: forbidden, required_role} for insufficient roles.
func Require(min model.Role) gin.HandlerFunc { return require(min, true) }

// RequireAllowPending is Require for the few routes a user whose password must
// be changed may still call (me, change password, profile).
func RequireAllowPending(min model.Role) gin.HandlerFunc { return require(min, false) }

func require(min model.Role, gatePending bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := IdentityOf(c)
		if id == nil {
			Abort(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		if gatePending && id.User != nil && id.User.MustChangePassword && id.Via != ViaDisabled {
			Abort(c, http.StatusForbidden, PasswordChangeRequired, "you must change your password before continuing")
			return
		}
		if !id.Role.AtLeast(min) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":         "your role (" + string(id.Role) + ") cannot do this; " + string(min) + " is required",
				"code":          "forbidden",
				"required_role": string(min),
			})
			return
		}
		c.Next()
	}
}
