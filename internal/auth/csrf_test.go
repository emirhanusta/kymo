package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/emirhanusta/kymo/internal/model"
)

func csrfRouter(public string, origins []string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS(origins), CSRF(public, origins))
	h := func(c *gin.Context) { c.String(200, "ok") }
	r.GET("/api/v1/x", h)
	r.POST("/api/v1/x", h)
	r.DELETE("/api/v1/x", h)
	r.POST("/other", h)
	return r
}

func do(r *gin.Engine, method, path, host string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if host != "" {
		req.Host = host
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCSRFRule(t *testing.T) {
	r := csrfRouter("https://kymo.example.com", []string{"https://dash.example.com"})
	h := func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	cases := []struct {
		name   string
		method string
		path   string
		hdr    map[string]string
		want   int
	}{
		{"GET needs nothing", "GET", "/api/v1/x", nil, 200},
		{"POST without header", "POST", "/api/v1/x", nil, 403},
		{"DELETE without header", "DELETE", "/api/v1/x", nil, 403},
		{"POST with header, no origin", "POST", "/api/v1/x", h(CSRFHeader, "1"), 200},
		{"same-origin Origin", "POST", "/api/v1/x", h(CSRFHeader, "1", "Origin", "http://app.local"), 200},
		{"cross-site Origin", "POST", "/api/v1/x", h(CSRFHeader, "1", "Origin", "https://evil.com"), 403},
		{"null Origin", "POST", "/api/v1/x", h(CSRFHeader, "1", "Origin", "null"), 403},
		{"allowed origin", "POST", "/api/v1/x", h(CSRFHeader, "1", "Origin", "https://dash.example.com"), 200},
		{"public_url origin", "POST", "/api/v1/x", h(CSRFHeader, "1", "Origin", "https://kymo.example.com"), 200},
		{"Referer fallback ok", "POST", "/api/v1/x", h(CSRFHeader, "1", "Referer", "http://app.local/page"), 200},
		{"Referer fallback evil", "POST", "/api/v1/x", h(CSRFHeader, "1", "Referer", "https://evil.com/page"), 403},
		{"Origin wins over Referer", "POST", "/api/v1/x", h(CSRFHeader, "1", "Origin", "https://evil.com", "Referer", "http://app.local/"), 403},
		{"bearer exempt from header", "POST", "/api/v1/x", h("Authorization", "Bearer kymo_x"), 200},
		{"non-api path ignored", "POST", "/other", nil, 200},
	}
	for _, c := range cases {
		if w := do(r, c.method, c.path, "app.local", c.hdr); w.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
	// forwarded host
	if w := do(r, "POST", "/api/v1/x", "pod:8080", h(CSRFHeader, "1", "Origin", "https://public.example.org", "X-Forwarded-Host", "public.example.org")); w.Code != 200 {
		t.Errorf("forwarded host: %d", w.Code)
	}
}

func TestCORS(t *testing.T) {
	r := csrfRouter("", []string{"https://dash.example.com"})
	w := do(r, "GET", "/api/v1/x", "app.local", map[string]string{"Origin": "https://dash.example.com"})
	if w.Header().Get("Access-Control-Allow-Origin") != "https://dash.example.com" || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("allowed origin headers: %v", w.Header())
	}
	w = do(r, "GET", "/api/v1/x", "app.local", map[string]string{"Origin": "https://evil.com"})
	if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("foreign origin must get no CORS headers: %v", w.Header())
	}
	w = do(r, "GET", "/api/v1/x", "app.local", nil)
	if w.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Error("wildcard CORS must be gone")
	}
	w = do(r, "OPTIONS", "/api/v1/x", "app.local", map[string]string{"Origin": "https://dash.example.com", "Access-Control-Request-Method": "POST"})
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Errorf("preflight: %d %v", w.Code, w.Header())
	}
}

func TestRequireMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	roles := map[string]model.Role{"viewer": model.RoleViewer, "operator": model.RoleOperator, "admin": model.RoleAdmin, "owner": model.RoleOwner}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if who := c.GetHeader("X-Test-Role"); who != "" {
			c.Set(identityKey, &Identity{User: &model.User{ID: 1, Email: "a@b.c"}, Role: roles[who], Via: ViaSession})
		}
	})
	for name, min := range roles {
		r.GET("/"+name, Require(min), func(c *gin.Context) { c.String(200, "ok") })
	}
	order := []string{"viewer", "operator", "admin", "owner"}
	for ri, route := range order {
		for ci, caller := range order {
			w := do2(r, "/"+route, caller)
			want := 200
			if ci < ri {
				want = 403
			}
			if w.Code != want {
				t.Errorf("%s calling %s: %d want %d", caller, route, w.Code, want)
			}
		}
		w := do2(r, "/"+route, "")
		if w.Code != 401 {
			t.Errorf("anonymous on %s: %d", route, w.Code)
		}
	}
	w := do2(r, "/admin", "viewer")
	if w.Body.String() == "" || !strings.Contains(w.Body.String(), `"required_role":"admin"`) || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
		t.Errorf("403 body: %s", w.Body.String())
	}
	// Unknown role grants nothing.
	r2 := gin.New()
	r2.Use(func(c *gin.Context) { c.Set(identityKey, &Identity{User: &model.User{}, Role: model.Role("")}) })
	r2.GET("/", Require(model.RoleViewer), func(c *gin.Context) { c.String(200, "ok") })
	if w := do(r2, "GET", "/", "", nil); w.Code != 403 {
		t.Errorf("empty role: %d", w.Code)
	}
}

func do2(r *gin.Engine, path, role string) *httptest.ResponseRecorder {
	hdr := map[string]string{}
	if role != "" {
		hdr["X-Test-Role"] = role
	}
	return do(r, http.MethodGet, path, "", hdr)
}
