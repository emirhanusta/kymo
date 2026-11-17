package httphandler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
	"github.com/emirhanusta/kymo/internal/service"
)

type histRepo struct {
	repository.ContainerRepository
	history map[string]model.SourceHistory
}

func (f *histRepo) SourceHistory(context.Context) ([]model.SourceHistory, error) {
	out := []model.SourceHistory{}
	for _, h := range f.history {
		out = append(out, h)
	}
	return out, nil
}

func (f *histRepo) PurgeSource(_ context.Context, name string) (int64, error) {
	n := int64(f.history[name].Containers)
	delete(f.history, name)
	return n, nil
}

type nopCollector struct{}

func (nopCollector) Start(context.Context) error             { return nil }
func (nopCollector) Stop() error                             { return nil }
func (nopCollector) Events() <-chan collector.CollectorEvent { return nil }
func (nopCollector) Stats(context.Context, string) (*model.ResourceSample, error) {
	return nil, nil
}

func removedRouter(t *testing.T, hist map[string]model.SourceHistory) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := service.NewRecorderService(&histRepo{history: hist}, nil, nil, nil, nil, nil, nil, nil,
		service.ScoringConfig{Interval: time.Hour}, zap.NewNop())
	rec.AttachSource(model.SourceSpec{Name: "live", Type: model.SourceTypeKubernetes}, nopCollector{}, nil)
	box, _ := secret.New("")
	mgr := service.NewSourceManager(nil, box, rec, service.NewManagementService(zap.NewNop()),
		func(model.SourceSpec) collector.Collector { return nopCollector{} }, zap.NewNop())
	h := &Handler{svc: rec, sources: mgr, log: zap.NewNop()}
	r := gin.New()
	r.GET("/sources/removed", h.ListRemovedSources)
	r.DELETE("/sources/removed/:name", h.PurgeRemovedSource)
	return r
}

func do(r *gin.Engine, method, url string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, url, nil))
	return w
}

func TestRemovedSourcesEndpoints(t *testing.T) {
	r := removedRouter(t, map[string]model.SourceHistory{
		"live": {Name: "live", Containers: 1}, "gone": {Name: "gone", Containers: 3, Workloads: 2},
	})

	w := do(r, "GET", "/sources/removed")
	var list []model.SourceHistory
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].Name != "gone" {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	for _, tc := range []struct {
		url  string
		code int
	}{
		{"/sources/removed/gone", http.StatusBadRequest},
		{"/sources/removed/gone?confirm=yes", http.StatusBadRequest},
		{"/sources/removed/live?confirm=true", http.StatusConflict},
		{"/sources/removed/nope?confirm=true", http.StatusNotFound},
	} {
		if w := do(r, "DELETE", tc.url); w.Code != tc.code {
			t.Errorf("DELETE %s = %d, want %d (%s)", tc.url, w.Code, tc.code, w.Body)
		}
	}

	w = do(r, "DELETE", "/sources/removed/gone?confirm=true")
	var body struct {
		Success bool  `json:"success"`
		Deleted int64 `json:"deleted_containers"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || !body.Success || body.Deleted != 3 {
		t.Fatalf("purge: %d %s", w.Code, w.Body)
	}
	if w := do(r, "GET", "/sources/removed"); w.Body.String() != "[]" {
		t.Fatalf("after purge: %s", w.Body)
	}
}
