package httphandler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/service"
)

type specsSvc struct {
	service.RecorderService
	sources []service.SourceStatus
}

func (f *specsSvc) ListSources(context.Context) ([]service.SourceStatus, error) {
	return f.sources, nil
}

type specsMgmt struct {
	service.ManagementService
	calls []string
}

func (f *specsMgmt) ListCronJobSpecs(_ context.Context, source string) ([]service.CronJobSpec, error) {
	f.calls = append(f.calls, source)
	if source == "broken" {
		return nil, errors.New("connection refused")
	}
	return []service.CronJobSpec{{SourceName: source, Namespace: "app", Name: "nightly", Schedule: "0 2 * * *"}}, nil
}

func getSpecs(t *testing.T, query string) (*httptest.ResponseRecorder, *specsMgmt) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mgmt := &specsMgmt{}
	h := &Handler{
		svc: &specsSvc{sources: []service.SourceStatus{
			{Name: "a", Type: model.SourceTypeKubernetes, Enabled: true},
			{Name: "broken", Type: model.SourceTypeKubernetes, Enabled: true},
			{Name: "paused", Type: model.SourceTypeKubernetes, Enabled: false},
		}},
		mgmt: mgmt, log: zap.NewNop(),
	}
	r := gin.New()
	r.GET("/cronjobs/specs", h.ListCronJobSpecs)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/cronjobs/specs"+query, nil))
	return w, mgmt
}

type specsBody struct {
	Items  []service.CronJobSpec `json:"items"`
	Errors map[string]string     `json:"errors"`
}

func decodeSpecs(t *testing.T, w *httptest.ResponseRecorder) specsBody {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var b specsBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestListCronJobSpecs_PartialFailure(t *testing.T) {
	w, mgmt := getSpecs(t, "")
	b := decodeSpecs(t, w)
	if len(b.Items) != 1 || b.Items[0].SourceName != "a" {
		t.Fatalf("items = %+v", b.Items)
	}
	if b.Errors["broken"] != "connection refused" || len(b.Errors) != 1 {
		t.Fatalf("errors = %+v", b.Errors)
	}
	for _, c := range mgmt.calls {
		if c == "paused" {
			t.Fatal("disabled source must not be queried")
		}
	}
}

func TestListCronJobSpecs_SourceFilter(t *testing.T) {
	b := decodeSpecs(t, func() *httptest.ResponseRecorder { w, _ := getSpecs(t, "?source=a"); return w }())
	if len(b.Items) != 1 || len(b.Errors) != 0 {
		t.Fatalf("got %+v", b)
	}
}

func TestListCronJobSpecs_RemovedSourceIsEmpty(t *testing.T) {
	w, mgmt := getSpecs(t, "?source=removed-cluster")
	b := decodeSpecs(t, w)
	if len(mgmt.calls) != 0 || len(b.Items) != 0 || len(b.Errors) != 0 {
		t.Fatalf("calls=%v body=%+v", mgmt.calls, b)
	}
	// Arrays must serialise as [] / {} rather than null.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if string(raw["items"]) != "[]" || string(raw["errors"]) != "{}" {
		t.Fatalf("raw = %s", w.Body)
	}
}
