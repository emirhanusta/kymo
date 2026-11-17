package issues

import (
	"context"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

type fakeErrorLogs struct {
	repository.ErrorLogRepository
	rates []model.WorkloadErrorRate
	since time.Time
}

func (f *fakeErrorLogs) WorkloadErrorRates(_ context.Context, since time.Time) ([]model.WorkloadErrorRate, error) {
	f.since = since
	return f.rates, nil
}

func TestErrorRateIssue(t *testing.T) {
	e := newEnv(t, Config{ErrorRateThreshold: 10, ResolveAfter: 5 * time.Minute})
	noisy := workload("noisy", "Deployment", model.StatusRunning, 1, 1)
	quiet := workload("quiet", "Deployment", model.StatusRunning, 1, 1)
	system := workload("sys", "Deployment", model.StatusRunning, 1, 1)
	system.IsSystem = true
	e.workloads.list = []*model.WorkloadSummary{noisy, quiet, system}
	logs := &fakeErrorLogs{rates: []model.WorkloadErrorRate{
		{WorkloadID: noisy.ID, Count: 12, TopTemplate: "dial tcp <ip>: connection refused", TopCount: 9},
		{WorkloadID: quiet.ID, Count: 9, TopTemplate: "x"},
		{WorkloadID: system.ID, Count: 100, TopTemplate: "y"},
		{WorkloadID: "prod:default:Deployment:gone", Count: 100, TopTemplate: "z"},
	}}
	e.engine.d.ErrorLogs = logs

	e.eval()
	i := e.one(noisy, model.IssueError)
	if i.Severity != model.SeverityWarning || i.Title != "Error logs ×12 in 5m" || i.Detail != "dial tcp <ip>: connection refused" {
		t.Errorf("issue = %+v", i)
	}
	e.none(quiet, model.IssueError)
	e.none(system, model.IssueError)
	if want := e.now.Add(-5 * time.Minute); logs.since.After(want) || want.Sub(logs.since) > time.Minute {
		t.Errorf("window start = %v, want about %v", logs.since, want)
	}
	if got := e.notifier.kinds(); len(got) != 1 || got[0] != model.EventOpened {
		t.Errorf("notifications = %v", got)
	}

	// Count changes update the open issue; falling below the threshold resolves it after resolve_after.
	logs.rates[0].Count = 15
	e.advance(time.Minute)
	e.eval()
	if i := e.one(noisy, model.IssueError); i.Title != "Error logs ×15 in 5m" {
		t.Errorf("updated title = %q", i.Title)
	}
	logs.rates = nil
	e.advance(time.Minute)
	e.eval()
	if e.one(noisy, model.IssueError).Status != model.IssueOpen {
		t.Fatal("resolved too early")
	}
	e.advance(6 * time.Minute)
	e.eval()
	if e.one(noisy, model.IssueError).Status != model.IssueResolved {
		t.Error("should resolve after resolve_after")
	}
}

func TestErrorRateDefaultsAndValidity(t *testing.T) {
	if got := (Config{}).withDefaults().ErrorRateThreshold; got != DefaultErrorRateThreshold {
		t.Errorf("default threshold = %d", got)
	}
	if !model.ValidIssueType("error_rate") {
		t.Error("error_rate must be a valid issue type (notification filters)")
	}
}
