package httphandler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/service"
)

type fakeK8sSvc struct {
	service.RecorderService
	q       repository.K8sEventQuery
	source  string
	since   time.Time
	calls   int
	reasons []model.K8sEventReason
}

func (f *fakeK8sSvc) ListK8sEvents(_ context.Context, q repository.K8sEventQuery) ([]*model.K8sEvent, error) {
	f.q, f.calls = q, f.calls+1
	return nil, nil
}

func (f *fakeK8sSvc) ListK8sEventReasons(_ context.Context, source string, since time.Time) ([]model.K8sEventReason, error) {
	f.source, f.since, f.calls = source, since, f.calls+1
	return f.reasons, nil
}

func k8sRouter(f *fakeK8sSvc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &Handler{svc: f, log: zap.NewNop()}
	r := gin.New()
	r.GET("/k8s-events", h.ListK8sEvents)
	r.GET("/k8s-events/reasons", h.GetK8sEventReasons)
	r.GET("/workloads/:id/k8s-events", h.GetWorkloadK8sEvents)
	return r
}

func TestK8sEventsParams(t *testing.T) {
	f := &fakeK8sSvc{}
	r := k8sRouter(f)
	w := do(r, "GET", "/k8s-events?source=s&namespace=n&type=Warning&reason=BackOff&workload_id=w&q=oom&since=2026-01-01T00:00:00Z&before=2026-01-02T00:00:00.5Z&limit=9999")
	if w.Code != 200 || w.Body.String() != "[]" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	q := f.q
	if q.Source != "s" || q.Namespace != "n" || q.Type != "Warning" || q.Reason != "BackOff" || q.WorkloadID != "w" ||
		q.Query != "oom" || q.Limit != 500 || q.Since.Year() != 2026 || q.Before.Nanosecond() != 500_000_000 {
		t.Fatalf("query: %+v", q)
	}

	do(r, "GET", "/k8s-events")
	if f.q.Limit != 100 || !f.q.Since.IsZero() {
		t.Fatalf("defaults: %+v", f.q)
	}

	w = do(r, "GET", "/workloads/src:ns:Deployment:web/k8s-events?limit=7")
	if w.Code != 200 || f.q.WorkloadID != "src:ns:Deployment:web" || f.q.Limit != 7 {
		t.Fatalf("workload: %d %+v", w.Code, f.q)
	}
}

func TestK8sEventsBadTimestamps(t *testing.T) {
	f := &fakeK8sSvc{}
	r := k8sRouter(f)
	for _, u := range []string{
		"/k8s-events?since=yesterday", "/k8s-events?before=2026-01-01",
		"/workloads/w/k8s-events?since=x", "/workloads/w/k8s-events?before=x",
		"/k8s-events/reasons?since=nope",
	} {
		if w := do(r, "GET", u); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", u, w.Code)
		}
	}
	if f.calls != 0 {
		t.Fatalf("service called %d times for bad input", f.calls)
	}
}

func TestK8sEventReasonsEndpoint(t *testing.T) {
	f := &fakeK8sSvc{reasons: []model.K8sEventReason{{Reason: "BackOff", Type: "Warning", Count: 3}}}
	r := k8sRouter(f)
	before := time.Now()
	w := do(r, "GET", "/k8s-events/reasons?source=s")
	var got []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 1 ||
		got[0]["reason"] != "BackOff" || got[0]["type"] != "Warning" || got[0]["count"] != float64(3) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if f.source != "s" || f.since.After(before.Add(-23*time.Hour)) || f.since.Before(before.Add(-25*time.Hour)) {
		t.Fatalf("default since: %v", f.since)
	}
}
