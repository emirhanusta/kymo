package httphandler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/service"
)

type logsFakeSvc struct {
	service.RecorderService
	crash []*model.CrashLog
}

func (f *logsFakeSvc) GetContainer(_ context.Context, id string) (*model.Container, error) {
	return &model.Container{ID: id, Runtime: model.RuntimeKubernetes}, nil
}

func (f *logsFakeSvc) GetContainerCrashLogs(context.Context, string, int) ([]*model.CrashLog, error) {
	return f.crash, nil
}

type logsFakeMgmt struct {
	service.ManagementService
	err error
}

func (f *logsFakeMgmt) GetPodLogs(context.Context, string, string, string, string, int, bool) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader("hello\n")), nil
}

func getLogs(t *testing.T, mgmt *logsFakeMgmt, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.UseRawPath, r.UnescapePathValues = true, true
	h := &Handler{
		svc:  &logsFakeSvc{crash: []*model.CrashLog{{}, {}}},
		mgmt: mgmt,
		log:  zap.NewNop(),
	}
	r.GET("/containers/:id/logs", h.GetContainerLogs)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/containers/default%2Fweb-1%2Fapp/logs"+query, nil))
	return w
}

func TestGetContainerLogs_PodGone(t *testing.T) {
	nf := apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "web-1")
	for _, q := range []string{"", "?follow=true"} {
		w := getLogs(t, &logsFakeMgmt{err: nf}, q)
		if w.Code != http.StatusGone {
			t.Fatalf("%q: status = %d, want 410", q, w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != "pod_gone" || body["crash_logs"] != float64(2) || body["error"] != "web-1 no longer exists in the cluster" {
			t.Fatalf("%q: unexpected body %v", q, body)
		}
	}
}

func TestGetContainerLogs_OtherError(t *testing.T) {
	w := getLogs(t, &logsFakeMgmt{err: errors.New("dial tcp: connection refused")}, "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "logs_unavailable" {
		t.Fatalf("unexpected body %v", body)
	}
}

func TestGetContainerLogs_OK(t *testing.T) {
	w := getLogs(t, &logsFakeMgmt{}, "")
	if w.Code != http.StatusOK || w.Body.String() != "hello\n" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}
