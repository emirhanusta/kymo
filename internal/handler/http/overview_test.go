package httphandler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/service"
)

type fakeOverviewSvc struct {
	service.RecorderService
	act     *model.Activity
	changes []*model.Change
	aq      repository.ActivityQuery
	cq      repository.ChangesQuery
}

func (f *fakeOverviewSvc) ListSources(context.Context) ([]service.SourceStatus, error) {
	return []service.SourceStatus{{Name: "a"}, {Name: "b"}}, nil
}

func (f *fakeOverviewSvc) GetOverviewActivity(_ context.Context, q repository.ActivityQuery) (*model.Activity, error) {
	f.aq = q
	return f.act, nil
}

func (f *fakeOverviewSvc) GetOverviewChanges(_ context.Context, q repository.ChangesQuery) ([]*model.Change, error) {
	f.cq = q
	return f.changes, nil
}

func overviewRouter(f *fakeOverviewSvc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &Handler{svc: f, log: zap.NewNop()}
	r := gin.New()
	r.GET("/overview/activity", h.GetOverviewActivity)
	r.GET("/overview/changes", h.GetOverviewChanges)
	return r
}

func TestOverviewActivityScopeAndShape(t *testing.T) {
	f := &fakeOverviewSvc{act: &model.Activity{
		Buckets:         []model.ActivityBucket{{Start: time.Unix(0, 0).UTC(), Starts: 2, Crashes: 1}},
		PreviousCrashes: 4,
	}}
	r := overviewRouter(f)

	// No source: restricted to the registered clusters (removed history is excluded).
	w := do(r, "GET", "/overview/activity")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if f.aq.Source != "" || len(f.aq.Sources) != 2 || f.aq.Hours != 24 {
		t.Fatalf("query: %+v", f.aq)
	}
	var got struct {
		Buckets []struct {
			Start   string `json:"start"`
			Starts  int    `json:"starts"`
			Crashes int    `json:"crashes"`
			OOMs    int    `json:"ooms"`
		} `json:"buckets"`
		Previous int `json:"previous_crashes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Buckets) != 1 || got.Buckets[0].Starts != 2 || got.Previous != 4 {
		t.Fatalf("body: %s (%v)", w.Body, err)
	}

	// Explicit source (also a removed cluster) is passed through untouched.
	do(r, "GET", "/overview/activity?source=gone&hours=12")
	if f.aq.Source != "gone" || f.aq.Sources != nil || f.aq.Hours != 12 {
		t.Fatalf("query: %+v", f.aq)
	}

	for _, bad := range []string{"0", "169", "x"} {
		if w := do(r, "GET", "/overview/activity?hours="+bad); w.Code != 400 {
			t.Fatalf("hours=%s: %d", bad, w.Code)
		}
	}

	// nil activity / nil buckets still render a JSON array.
	f.act = &model.Activity{}
	if w := do(r, "GET", "/overview/activity"); w.Code != 200 || !contains(w.Body.String(), `"buckets":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestOverviewChanges(t *testing.T) {
	f := &fakeOverviewSvc{}
	r := overviewRouter(f)
	w := do(r, "GET", "/overview/changes")
	if w.Code != 200 || w.Body.String() != "[]" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if f.cq.Limit != 6 || len(f.cq.Sources) != 2 {
		t.Fatalf("query: %+v", f.cq)
	}
	f.changes = []*model.Change{{WorkloadID: "w", Name: "web", Revision: "b", PreviousRevision: "a", Image: "x:2", PreviousImage: "x:1"}}
	w = do(r, "GET", "/overview/changes?source=a&limit=3")
	if f.cq.Source != "a" || f.cq.Limit != 3 || !contains(w.Body.String(), `"previous_revision":"a"`) || !contains(w.Body.String(), `"previous_image":"x:1"`) {
		t.Fatalf("%+v %s", f.cq, w.Body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
