package httphandler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/emirhanusta/kymo/internal/model"
)

// routeRoles is the access policy of EVERY registered route. A new route makes
// TestEveryRouteHasAnExplicitRole fail until it is listed here, so nothing can
// be added unprotected by accident. "public" = no authentication.
var routeRoles = map[string]string{
	// public
	"GET /api/v1/healthz":            "public",
	"GET /api/v1/auth/config":        "public",
	"POST /api/v1/auth/login":        "public",
	"POST /api/v1/auth/logout":       "public",
	"GET /api/v1/auth/oidc/login":    "public",
	"GET /api/v1/auth/oidc/callback": "public",
	"GET /api/v1/auth/links/:token":  "public",
	"POST /api/v1/auth/links/:token": "public",

	// any signed-in user / token
	"GET /api/v1/auth/me":       "viewer",
	"PATCH /api/v1/me":          "viewer",
	"POST /api/v1/me/password":  "viewer",
	"GET /api/v1/users":         "viewer",
	"GET /api/v1/tokens":        "viewer",
	"POST /api/v1/tokens":       "viewer",
	"DELETE /api/v1/tokens/:id": "viewer",

	// admin / owner
	"GET /api/v1/users/invites":                 "admin",
	"POST /api/v1/users":                        "admin",
	"POST /api/v1/users/invites":                "admin",
	"DELETE /api/v1/users/invites/:id":          "admin",
	"PATCH /api/v1/users/:id":                   "admin",
	"POST /api/v1/users/:id/reset-link":         "admin",
	"POST /api/v1/users/:id/transfer-ownership": "owner",
	"GET /api/v1/audit":                         "admin",

	// sources
	"GET /api/v1/meta":                     "viewer",
	"GET /api/v1/sources":                  "viewer",
	"POST /api/v1/sources":                 "admin",
	"GET /api/v1/sources/removed":          "viewer",
	"DELETE /api/v1/sources/removed/:name": "admin",
	"POST /api/v1/sources/test":            "admin",
	"POST /api/v1/sources/reload":          "admin",
	"POST /api/v1/sources/contexts":        "admin",
	"PUT /api/v1/sources/:name":            "admin",
	"DELETE /api/v1/sources/:name":         "admin",

	// containers
	"GET /api/v1/containers":                   "viewer",
	"GET /api/v1/containers/:id":               "viewer",
	"GET /api/v1/containers/:id/score":         "viewer",
	"GET /api/v1/containers/:id/score/history": "viewer",
	"GET /api/v1/containers/:id/events":        "viewer",
	"GET /api/v1/containers/:id/resources":     "viewer",
	"GET /api/v1/containers/:id/logs":          "viewer",
	"GET /api/v1/containers/:id/crash-logs":    "viewer",
	"POST /api/v1/containers/:id/restart":      "operator",
	"DELETE /api/v1/containers/:id":            "operator",
	"POST /api/v1/containers/:id/restore":      "operator",
	"DELETE /api/v1/containers/:id/permanent":  "admin",
	"GET /api/v1/archive":                      "viewer",
	"DELETE /api/v1/archive":                   "admin",
	"GET /api/v1/ignored":                      "viewer",
	"DELETE /api/v1/ignored":                   "admin",

	// kubernetes
	"GET /api/v1/namespaces":                            "viewer",
	"GET /api/v1/deployments":                           "viewer",
	"GET /api/v1/deployments/:namespace/:name":          "viewer",
	"POST /api/v1/deployments/:namespace/:name/scale":   "operator",
	"POST /api/v1/deployments/:namespace/:name/restart": "operator",
	"GET /api/v1/pods/:namespace/:name":                 "viewer",
	"GET /api/v1/cronjobs":                              "viewer",
	"GET /api/v1/cronjobs/:namespace/:name/executions":  "viewer",

	// workloads
	"GET /api/v1/workloads":                   "viewer",
	"GET /api/v1/workloads/:id":               "viewer",
	"GET /api/v1/workloads/:id/events":        "viewer",
	"GET /api/v1/workloads/:id/resources":     "viewer",
	"GET /api/v1/workloads/:id/crash-logs":    "viewer",
	"GET /api/v1/workloads/:id/revisions":     "viewer",
	"GET /api/v1/workloads/:id/k8s-events":    "viewer",
	"GET /api/v1/workloads/:id/score/history": "viewer",
	"GET /api/v1/workloads/:id/issues":        "viewer",
	"GET /api/v1/workloads/:id/errors":        "viewer",
	"GET /api/v1/workloads/:id/log-capture":   "viewer",
	"PUT /api/v1/workloads/:id/log-capture":   "operator",
	"GET /api/v1/k8s-events":                  "viewer",
	"GET /api/v1/k8s-events/reasons":          "viewer",
	"GET /api/v1/events/recent":               "viewer",
	"GET /api/v1/overview/activity":           "viewer",
	"GET /api/v1/overview/changes":            "viewer",

	// issues & notifications
	"GET /api/v1/issues":                           "viewer",
	"GET /api/v1/issues/summary":                   "viewer",
	"GET /api/v1/issues/:id":                       "viewer",
	"GET /api/v1/notifications/channels":           "viewer",
	"POST /api/v1/notifications/channels":          "admin",
	"PUT /api/v1/notifications/channels/:id":       "admin",
	"DELETE /api/v1/notifications/channels/:id":    "admin",
	"POST /api/v1/notifications/channels/:id/test": "admin",
	"GET /api/v1/notifications/deliveries":         "viewer",

	// error log capture
	"GET /api/v1/log-capture/targets":        "viewer",
	"POST /api/v1/log-capture/targets":       "operator",
	"DELETE /api/v1/log-capture/targets/:id": "operator",
	"GET /api/v1/log-capture/status":         "viewer",
	"GET /api/v1/errors":                     "viewer",
	"GET /api/v1/errors/:id":                 "viewer",
	"GET /api/v1/errors/:id/occurrences":     "viewer",
}

var paramRe = regexp.MustCompile(`:[A-Za-z]+`)

func routeKey(r gin.RouteInfo) string { return r.Method + " " + r.Path }

func TestEveryRouteHasAnExplicitRole(t *testing.T) {
	e := newEnv(t, nil)
	seen := map[string]bool{}
	for _, r := range e.r.Routes() {
		k := routeKey(r)
		seen[k] = true
		if _, ok := routeRoles[k]; !ok {
			t.Errorf("route %q is registered but has no entry in routeRoles: decide its role (and guard it with h.require)", k)
		}
	}
	for k := range routeRoles {
		if !seen[k] {
			t.Errorf("routeRoles lists %q but no such route is registered", k)
		}
	}
}

// TestRouteRoleEnforcement calls every route as anonymous and as each role.
func TestRouteRoleEnforcement(t *testing.T) {
	e := newEnv(t, nil)
	// Handlers have nil dependencies here; panics are recovered as 500 and are
	// irrelevant: only 401/403 matter for the access decision.
	e.r = rebuildWithRecovery(t, e)
	_, ownerCk := e.addUser("owner@example.com", model.RoleOwner)
	cookies := map[model.Role]*http.Cookie{model.RoleOwner: ownerCk}
	for _, role := range []model.Role{model.RoleAdmin, model.RoleOperator, model.RoleViewer} {
		_, cookies[role] = e.addUser(string(role)+"@example.com", role)
	}
	order := []model.Role{model.RoleViewer, model.RoleOperator, model.RoleAdmin, model.RoleOwner}

	keys := make([]string, 0, len(routeRoles))
	for k := range routeRoles {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		method, path, _ := strings.Cut(k, " ")
		url := paramRe.ReplaceAllString(path, "99999")
		req := routeRoles[k]
		var callFull func(ck *http.Cookie) *httptest.ResponseRecorder
		call := func(ck *http.Cookie) int { return callFull(ck).Code }
		callFull = func(ck *http.Cookie) *httptest.ResponseRecorder {
			opts := []reqOpt{}
			if ck != nil {
				opts = append(opts, withCookie(ck))
			}
			var body any
			if method != "GET" && method != "DELETE" {
				body = map[string]any{}
			}
			return e.do(method, url, body, opts...)
		}
		if req == "public" {
			if w := callFull(nil); errCode(w) == "unauthenticated" || errCode(w) == "forbidden" {
				t.Errorf("%s must be public, got %d %s", k, w.Code, w.Body)
			}
			continue
		}
		min := model.Role(req)
		if code := call(nil); code != 401 {
			t.Errorf("%s anonymous: %d, want 401", k, code)
		}
		for _, role := range order {
			code := call(cookies[role])
			if role.Rank() < min.Rank() {
				if code != 403 {
					t.Errorf("%s as %s: %d, want 403", k, role, code)
				}
			} else if code == 401 || code == 403 {
				// In-handler rules (e.g. PATCH on a missing user) never use 403 here,
				// so any 401/403 for an allowed role is a bug.
				t.Errorf("%s as %s: %d, want the handler to run", k, role, code)
			}
		}
	}
}

func rebuildWithRecovery(t *testing.T, e *env) *gin.Engine {
	t.Helper()
	r := gin.New()
	r.UseRawPath, r.UnescapePathValues = true, true
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) { c.AbortWithStatus(http.StatusInternalServerError) }))
	h := &Handler{log: e.svcLog(), auth: e.svc}
	h.registerRoutes(r)
	return r
}

// With auth wired but no WithAuth option, protected routes fail closed.
func TestRoutesFailClosedWithoutAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	New(nil, nil, nil, nil, e0Log(), r)
	if w := request(r, "GET", "/api/v1/containers", nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without auth: %d", w.Code)
	}
	if w := request(r, "GET", "/api/v1/healthz", nil); w.Code != http.StatusOK {
		t.Errorf("healthz: %d", w.Code)
	}
	// Unknown API paths are still 404 for anonymous callers.
	e := newEnv(t, nil)
	if w := e.do("GET", "/api/v1/nope", nil); w.Code != 404 {
		t.Errorf("unknown route: %d", w.Code)
	}
}
