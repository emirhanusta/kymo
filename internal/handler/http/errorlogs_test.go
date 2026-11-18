package httphandler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/service/errorlogs"
)

type fakeErrAPI struct {
	targets   []*model.LogCaptureTarget
	created   errorlogs.TargetInput
	createErr error
	deleteErr error
	setErr    error
	setCalls  []bool
	state     *errorlogs.WorkloadCapture

	q       repository.ErrorQuery
	window  time.Duration
	sigs    []*model.ErrorSignature
	before  time.Time
	limit   int
	sigErr  error
	occList []*model.ErrorOccurrence
	gotWL   string
}

func (f *fakeErrAPI) ListTargets(context.Context) ([]*model.LogCaptureTarget, error) {
	return f.targets, nil
}
func (f *fakeErrAPI) CreateTarget(_ context.Context, in errorlogs.TargetInput) (*model.LogCaptureTarget, error) {
	f.created = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &model.LogCaptureTarget{ID: 5, SourceName: in.SourceName, Namespace: in.Namespace, WorkloadID: in.WorkloadID, Origin: "ui"}, nil
}
func (f *fakeErrAPI) DeleteTarget(context.Context, int64) error { return f.deleteErr }
func (f *fakeErrAPI) WorkloadCapture(_ context.Context, id string) (*errorlogs.WorkloadCapture, error) {
	f.gotWL = id
	if _, _, _, _, ok := model.SplitWorkloadID(id); !ok {
		return nil, errorlogs.ErrBadWorkload
	}
	if f.state != nil {
		return f.state, nil
	}
	return &errorlogs.WorkloadCapture{GlobalEnabled: true}, nil
}
func (f *fakeErrAPI) SetWorkloadCapture(_ context.Context, id string, on bool) (*errorlogs.WorkloadCapture, error) {
	f.setCalls = append(f.setCalls, on)
	if f.setErr != nil {
		return nil, f.setErr
	}
	return &errorlogs.WorkloadCapture{Enabled: on, GlobalEnabled: true}, nil
}
func (f *fakeErrAPI) Status() model.LogCaptureStatus {
	return model.LogCaptureStatus{Enabled: true, Sources: []model.LogCaptureSourceStatus{{SourceName: "s", Streams: 2, MaxStreams: 50}}}
}
func (f *fakeErrAPI) ListSignatures(_ context.Context, q repository.ErrorQuery, w time.Duration) ([]*model.ErrorSignature, error) {
	f.q, f.window = q, w
	return f.sigs, nil
}
func (f *fakeErrAPI) GetSignature(_ context.Context, id, wl string) (*model.ErrorSignature, error) {
	f.gotWL = wl
	if f.sigErr != nil {
		return nil, f.sigErr
	}
	return &model.ErrorSignature{ID: id, Buckets: make([]int64, 24)}, nil
}
func (f *fakeErrAPI) ListOccurrences(_ context.Context, _ string, before time.Time, limit int) ([]*model.ErrorOccurrence, error) {
	f.before, f.limit = before, limit
	return f.occList, nil
}

func errRouter(f *fakeErrAPI) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.UseRawPath = true
	r.UnescapePathValues = true
	New(nil, nil, nil, nil, zap.NewNop(), r, WithErrorLogs(f), WithAuth(disabledAuth()))
	return r
}

func TestLogCaptureTargetEndpoints(t *testing.T) {
	f := &fakeErrAPI{targets: []*model.LogCaptureTarget{{ID: 1, SourceName: "s", Namespace: "ns", Origin: "config"}}}
	r := errRouter(f)

	w := request(r, "GET", "/api/v1/log-capture/targets", nil)
	var list []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 || list[0]["origin"] != "config" || list[0]["workload_id"] != "" {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	w = request(r, "POST", "/api/v1/log-capture/targets", map[string]any{"source_name": "s", "namespace": "ns", "workload_id": "s:ns:Deployment:w"})
	if w.Code != 201 || f.created.WorkloadID != "s:ns:Deployment:w" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	cases := []struct {
		err  error
		want int
	}{
		{&errorlogs.ValidationError{Msg: "unknown source"}, 400},
		{errorlogs.ErrDuplicate, 409},
		{errors.New("db down"), 500},
	}
	for _, c := range cases {
		f.createErr = c.err
		if w := request(r, "POST", "/api/v1/log-capture/targets", map[string]any{"source_name": "s", "namespace": "ns"}); w.Code != c.want {
			t.Errorf("create %v: %d", c.err, w.Code)
		}
	}
	if w := request(r, "POST", "/api/v1/log-capture/targets", "not an object"); w.Code != 400 {
		t.Errorf("bad body: %d", w.Code)
	}

	if w := request(r, "DELETE", "/api/v1/log-capture/targets/1", nil); w.Code != 204 {
		t.Errorf("delete: %d", w.Code)
	}
	for err, want := range map[error]int{errorlogs.ErrTargetLocked: 403, errorlogs.ErrTargetNotFound: 404} {
		f.deleteErr = err
		if w := request(r, "DELETE", "/api/v1/log-capture/targets/1", nil); w.Code != want {
			t.Errorf("delete %v: %d", err, w.Code)
		}
	}
	if w := request(r, "DELETE", "/api/v1/log-capture/targets/abc", nil); w.Code != 400 {
		t.Errorf("delete bad id: %d", w.Code)
	}

	w = request(r, "GET", "/api/v1/log-capture/status", nil)
	var st model.LogCaptureStatus
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || !st.Enabled || st.Sources[0].MaxStreams != 50 {
		t.Errorf("status: %d %s", w.Code, w.Body)
	}
}

func TestWorkloadLogCaptureEndpoints(t *testing.T) {
	f := &fakeErrAPI{}
	r := errRouter(f)
	id := url.PathEscape("src:shop:Deployment:web")

	w := request(r, "GET", "/api/v1/workloads/"+id+"/log-capture", nil)
	var body map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || f.gotWL != "src:shop:Deployment:web" ||
		body["enabled"] != false || body["via"] != nil || body["target_id"] != nil || body["global_enabled"] != true {
		t.Fatalf("get: %d %s (id %q)", w.Code, w.Body, f.gotWL)
	}
	if w := request(r, "GET", "/api/v1/workloads/garbage/log-capture", nil); w.Code != 404 {
		t.Errorf("malformed id: %d", w.Code)
	}
	if w := request(r, "PUT", "/api/v1/workloads/"+id+"/log-capture", map[string]any{"enabled": true}); w.Code != 200 || len(f.setCalls) != 1 || !f.setCalls[0] {
		t.Errorf("put: %d %s", w.Code, w.Body)
	}
	if w := request(r, "PUT", "/api/v1/workloads/"+id+"/log-capture", map[string]any{}); w.Code != 400 {
		t.Errorf("put without enabled: %d", w.Code)
	}
	f.setErr = &errorlogs.EnabledViaError{Via: "namespace"}
	w = request(r, "PUT", "/api/v1/workloads/"+id+"/log-capture", map[string]any{"enabled": false})
	if w.Code != 409 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["via"] != "namespace" || body["error"] == "" {
		t.Errorf("put 409: %d %s", w.Code, w.Body)
	}
}

func TestErrorEndpoints(t *testing.T) {
	f := &fakeErrAPI{}
	r := errRouter(f)

	w := request(r, "GET", "/api/v1/errors?source=s&namespace=n&workload_id=s:n:Deployment:w&since=7d&limit=9999", nil)
	if w.Code != 200 || w.Body.String() != "[]" || f.q.Source != "s" || f.q.Namespace != "n" || f.q.WorkloadID != "s:n:Deployment:w" ||
		f.window != 7*24*time.Hour || f.q.Limit != 500 {
		t.Fatalf("list: %d %s %+v %v", w.Code, w.Body, f.q, f.window)
	}
	request(r, "GET", "/api/v1/errors", nil)
	if f.window != 24*time.Hour || f.q.Limit != 100 {
		t.Errorf("defaults: %v %d", f.window, f.q.Limit)
	}
	for _, since := range []string{"1h", "6h", "24h", "7d"} {
		if w := request(r, "GET", "/api/v1/errors?since="+since, nil); w.Code != 200 {
			t.Errorf("since=%s: %d", since, w.Code)
		}
	}
	for _, since := range []string{"yesterday", "0h", "99d", "-1h"} {
		if w := request(r, "GET", "/api/v1/errors?since="+since, nil); w.Code != 400 {
			t.Errorf("since=%s: %d", since, w.Code)
		}
	}

	// Workload without data: 200 and []; malformed id: 404.
	id := url.PathEscape("src:shop:Deployment:web")
	if w := request(r, "GET", "/api/v1/workloads/"+id+"/errors?since=1h", nil); w.Code != 200 || w.Body.String() != "[]" || f.q.WorkloadID != "src:shop:Deployment:web" {
		t.Errorf("workload errors: %d %s %+v", w.Code, w.Body, f.q)
	}
	if w := request(r, "GET", "/api/v1/workloads/garbage/errors", nil); w.Code != 404 {
		t.Errorf("malformed workload: %d", w.Code)
	}

	f.sigs = []*model.ErrorSignature{{ID: "abc", Buckets: make([]int64, 24), Template: "t"}}
	var sigs []map[string]any
	w = request(r, "GET", "/api/v1/errors", nil)
	if json.Unmarshal(w.Body.Bytes(), &sigs) != nil || len(sigs) != 1 {
		t.Fatal(w.Body)
	}
	for _, k := range []string{"id", "source_name", "workload_id", "namespace", "kind", "name", "level", "template", "sample",
		"count", "total_count", "first_seen", "last_seen", "is_new", "pods", "buckets"} {
		if _, ok := sigs[0][k]; !ok {
			t.Errorf("signature JSON misses %q", k)
		}
	}

	if w := request(r, "GET", "/api/v1/errors/abc?workload_id=w1", nil); w.Code != 200 || f.gotWL != "w1" {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
	f.sigErr = errorlogs.ErrSignatureNotFound
	if w := request(r, "GET", "/api/v1/errors/nope", nil); w.Code != 404 {
		t.Errorf("get missing: %d", w.Code)
	}

	ts := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	f.occList = []*model.ErrorOccurrence{{Time: ts, Pod: "p", Container: "c", ContainerID: "ns/p/c", Level: "error", Message: "m"}}
	w = request(r, "GET", "/api/v1/errors/abc/occurrences?limit=7&before="+url.QueryEscape(ts.Format(time.RFC3339Nano)), nil)
	var occ []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &occ) != nil || f.limit != 7 || !f.before.Equal(ts) || occ[0]["time"] != "2026-01-02T03:04:05.123456Z" {
		t.Errorf("occurrences: %d %s before=%v limit=%d", w.Code, w.Body, f.before, f.limit)
	}
	request(r, "GET", "/api/v1/errors/abc/occurrences", nil)
	if f.limit != 50 || !f.before.IsZero() {
		t.Errorf("occurrence defaults: %d %v", f.limit, f.before)
	}
	if w := request(r, "GET", "/api/v1/errors/abc/occurrences?before=yesterday", nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad before: %d", w.Code)
	}
}

func TestErrorEndpointsUnavailableWithoutService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	New(nil, nil, nil, nil, zap.NewNop(), r, WithAuth(disabledAuth()))
	if w := request(r, "GET", "/api/v1/errors", nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without service: %d", w.Code)
	}
}
