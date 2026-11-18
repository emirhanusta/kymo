package auth

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// CSRFHeader must be present (any non-empty value, "1" by convention) on
// mutating API requests that authenticate with a cookie.
const CSRFHeader = "X-KYMO-Request"

func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// OriginAllowed reports whether an Origin/Referer value may call the API:
// it must match the request host (or the proxy-forwarded host / public_url
// host) or be listed in allowedOrigins.
func OriginAllowed(r *http.Request, value, publicURL string, allowedOrigins []string) bool {
	if value == "" || value == "null" {
		return false
	}
	if o := originOf(value); o != "" {
		for _, a := range allowedOrigins {
			if strings.EqualFold(strings.TrimRight(a, "/"), o) {
				return true
			}
		}
	}
	h := hostOf(value)
	if h == "" {
		return false
	}
	if strings.EqualFold(r.Host, h) {
		return true
	}
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" && strings.EqualFold(strings.TrimSpace(strings.Split(fh, ",")[0]), h) {
		return true
	}
	return publicURL != "" && hostOf(publicURL) == h
}

// CSRF enforces the cross-site request rule for mutating /api requests:
//   - the X-KYMO-Request header must be present, and
//   - when an Origin (or, lacking one, a Referer) header is sent it must match
//     the request host, server.public_url or server.allowed_origins.
//
// Requests authenticated with an API token (Authorization: Bearer) are exempt
// from the header rule: browsers cannot attach that header cross-site without
// a CORS preflight, and tokens are not ambient credentials.
func CSRF(publicURL string, allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !mutating(c.Request.Method) || !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}
		if strings.HasPrefix(strings.ToLower(c.GetHeader("Authorization")), "bearer ") {
			c.Next()
			return
		}
		if c.GetHeader(CSRFHeader) == "" {
			Abort(c, http.StatusForbidden, "csrf", "missing "+CSRFHeader+" header")
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			origin = c.GetHeader("Referer")
		}
		if origin != "" && !OriginAllowed(c.Request, origin, publicURL, allowedOrigins) {
			Abort(c, http.StatusForbidden, "csrf", "cross-origin request rejected")
			return
		}
		c.Next()
	}
}

// CORS replaces the old wildcard policy: same-origin by default; origins in
// allowedOrigins may call the API with credentials.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowed := false
		for _, a := range allowedOrigins {
			if origin != "" && strings.EqualFold(a, origin) {
				allowed = true
			}
		}
		if origin != "" {
			c.Writer.Header().Add("Vary", "Origin")
		}
		if allowed {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, "+CSRFHeader)
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != "" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// Abort writes the standard {error, code} body and stops the chain.
func Abort(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg, "code": code})
}
