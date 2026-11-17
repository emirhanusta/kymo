package timescale

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// ---------------------------------------------------------------------------
// CronJobs
// ---------------------------------------------------------------------------

func cronExec(src, ns, cj, job, pod string, sampled time.Duration) *model.CronJobExecution {
	return &model.CronJobExecution{SourceName: src, Namespace: ns, CronJobName: cj, JobName: job, PodName: pod, SampledAt: at(sampled)}
}

func cronJobNames(es []*model.CronJobExecution) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.JobName
	}
	return out
}

func TestCronJobSaveRoundTripAndDedupe(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	r := db.CronJobs()

	started, done := at(time.Minute), at(2*time.Minute)
	dur, ok := 60.5, true
	full := cronExec("s1", "prod", "backup", "backup-1", "backup-1-abc", 3*time.Minute)
	full.StartedAt, full.CompletedAt, full.DurationSeconds, full.Succeeded, full.ExitCode = &started, &done, &dur, &ok, 0
	saved, err := r.Save(ctx, full)
	if err != nil || saved.ID == 0 {
		t.Fatalf("save: %+v %v", saved, err)
	}

	// Nullable columns left nil.
	if _, err := r.Save(ctx, cronExec("s1", "prod", "nulls", "nulls-1", "p", 3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, err := r.FindByCronJob(ctx, "s1", "prod", "backup", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	e := got[0]
	if e.ID != saved.ID || e.JobName != "backup-1" || e.PodName != "backup-1-abc" || e.SourceName != "s1" ||
		e.StartedAt == nil || !e.StartedAt.Equal(started) || e.CompletedAt == nil || !e.CompletedAt.Equal(done) ||
		e.DurationSeconds == nil || *e.DurationSeconds != 60.5 || e.Succeeded == nil || !*e.Succeeded {
		t.Errorf("round trip = %+v", e)
	}
	n, _ := r.FindByCronJob(ctx, "s1", "prod", "nulls", 10)
	if len(n) != 1 || n[0].StartedAt != nil || n[0].CompletedAt != nil || n[0].DurationSeconds != nil || n[0].Succeeded != nil {
		t.Errorf("null columns = %+v", n)
	}

	// Duplicate (source, namespace, job, pod, sampled_at): no error, no new row.
	dup := cronExec("s1", "prod", "backup", "backup-1", "backup-1-abc", 3*time.Minute)
	if out, err := r.Save(ctx, dup); err != nil || out == nil {
		t.Fatalf("duplicate save: %v %v", out, err)
	}
	if c := count(t, db, "cronjob_executions", "job_name = 'backup-1'"); c != 1 {
		t.Errorf("rows after duplicate = %d, want 1", c)
	}
	// A different sampled_at is a new row.
	if _, err := r.Save(ctx, cronExec("s1", "prod", "backup", "backup-1", "backup-1-abc", 4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c := count(t, db, "cronjob_executions", "job_name = 'backup-1'"); c != 2 {
		t.Errorf("rows = %d, want 2", c)
	}
}

func TestCronJobFinders(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	r := db.CronJobs()
	for _, e := range []*model.CronJobExecution{
		cronExec("s1", "prod", "backup", "b-1", "p", 1*time.Minute),
		cronExec("s1", "prod", "backup", "b-2", "p", 2*time.Minute),
		cronExec("s1", "prod", "backup", "b-3", "p", 3*time.Minute),
		cronExec("s1", "prod", "report", "r-1", "p", 5*time.Minute),
		cronExec("s1", "dev", "backup", "dev-b-1", "p", 9*time.Minute),
		cronExec("s2", "prod", "cleanup", "s2-c-1", "p", 4*time.Minute),
		cronExec("s2", "prod", "cleanup", "s2-c-2", "p", 6*time.Minute),
		// Same namespace/name as s1's backup, in another cluster: must not be merged.
		cronExec("s2", "prod", "backup", "s2-b-1", "p", 7*time.Minute),
	} {
		if _, err := r.Save(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("FindByCronJob newest first with limit", func(t *testing.T) {
		got, err := r.FindByCronJob(ctx, "s1", "prod", "backup", 2)
		if err != nil || !sameStrings(cronJobNames(got), []string{"b-3", "b-2"}) {
			t.Errorf("got %v err=%v", cronJobNames(got), err)
		}
	})
	t.Run("FindByCronJob filters namespace and source", func(t *testing.T) {
		got, _ := r.FindByCronJob(ctx, "s1", "dev", "backup", 10)
		if !sameStrings(cronJobNames(got), []string{"dev-b-1"}) {
			t.Errorf("namespace filter: %v", cronJobNames(got))
		}
		got, _ = r.FindByCronJob(ctx, "s2", "prod", "report", 10)
		if len(got) != 0 {
			t.Errorf("source filter: %v", cronJobNames(got))
		}
		got, _ = r.FindByCronJob(ctx, "", "prod", "cleanup", 10) // empty source = all sources
		if !sameStrings(cronJobNames(got), []string{"s2-c-2", "s2-c-1"}) {
			t.Errorf("all sources: %v", cronJobNames(got))
		}
	})
	t.Run("FindLatestPerCronJob", func(t *testing.T) {
		got, err := r.FindLatestPerCronJob(ctx, "", "")
		// one row per (source, namespace, cronjob), ordered by source, namespace, cronjob_name
		if err != nil || !sameStrings(cronJobNames(got), []string{"dev-b-1", "b-3", "r-1", "s2-b-1", "s2-c-2"}) {
			t.Errorf("all = %v err=%v", cronJobNames(got), err)
		}
		got, _ = r.FindLatestPerCronJob(ctx, "s1", "")
		if !sameStrings(cronJobNames(got), []string{"dev-b-1", "b-3", "r-1"}) {
			t.Errorf("source s1 = %v", cronJobNames(got))
		}
		got, _ = r.FindLatestPerCronJob(ctx, "", "prod")
		if !sameStrings(cronJobNames(got), []string{"b-3", "r-1", "s2-b-1", "s2-c-2"}) {
			t.Errorf("namespace prod = %v", cronJobNames(got))
		}
		got, _ = r.FindLatestPerCronJob(ctx, "s2", "prod")
		if !sameStrings(cronJobNames(got), []string{"s2-b-1", "s2-c-2"}) {
			t.Errorf("s2/prod = %v", cronJobNames(got))
		}
	})
	t.Run("DeleteBySource", func(t *testing.T) {
		n, err := r.DeleteBySource(ctx, "s2")
		if err != nil || n != 3 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		if c := count(t, db, "cronjob_executions", ""); c != 5 {
			t.Errorf("remaining = %d, want 5", c)
		}
		if n, err := r.DeleteBySource(ctx, "none"); err != nil || n != 0 {
			t.Errorf("unknown source: %d %v", n, err)
		}
	})
}

// ---------------------------------------------------------------------------
// Sources
// ---------------------------------------------------------------------------

func TestSourceCRUD(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	r := db.Sources()

	if list, err := r.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v %v", list, err)
	}
	if _, err := r.Get(ctx, "nope"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}

	enc := []byte{0x00, 0x01, 0xff, 'k', 'u', 'b', 'e', 0x00, 0x80}
	in := &model.SourceSpec{Name: "prod", Type: model.SourceTypeKubernetes, Enabled: true, ReadOnly: true,
		KubeconfigPath: "/etc/kube", KubeContext: "ctx-1", KubeconfigEnc: enc, PollInterval: 90 * time.Second}
	if err := r.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, &model.SourceSpec{Name: "dev", Type: model.SourceTypeKubernetes, Enabled: false}); err != nil {
		t.Fatal(err)
	}

	got, err := r.Get(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "prod" || got.Type != "kubernetes" || got.Origin != model.SourceOriginUser || !got.Enabled || !got.ReadOnly ||
		got.KubeconfigPath != "/etc/kube" || got.KubeContext != "ctx-1" || got.PollInterval != 90*time.Second {
		t.Errorf("got = %+v", got)
	}
	if !bytes.Equal(got.KubeconfigEnc, enc) {
		t.Errorf("kubeconfig_enc = %x, want %x", got.KubeconfigEnc, enc)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("timestamps = %v %v", got.CreatedAt, got.UpdatedAt)
	}
	dev, _ := r.Get(ctx, "dev")
	if dev.Enabled || len(dev.KubeconfigEnc) != 0 || dev.PollInterval != 0 {
		t.Errorf("dev = %+v", dev)
	}

	list, err := r.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "prod" || list[1].Name != "dev" { // created_at order
		t.Errorf("list = %v err=%v", list, err)
	}

	// Duplicate name -> ErrConflict
	if err := r.Create(ctx, &model.SourceSpec{Name: "prod", Type: model.SourceTypeKubernetes}); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("duplicate create = %v, want ErrConflict", err)
	}

	// Update replaces everything but name/type/created_at.
	time.Sleep(10 * time.Millisecond)
	upd := &model.SourceSpec{Name: "prod", Type: "docker", Enabled: false, ReadOnly: false, KubeconfigPath: "/new",
		KubeContext: "ctx-2", KubeconfigEnc: []byte("new-bytes"), PollInterval: 5 * time.Second}
	if err := r.Update(ctx, upd); err != nil {
		t.Fatal(err)
	}
	after, _ := r.Get(ctx, "prod")
	if after.Type != "kubernetes" {
		t.Errorf("type changed to %q", after.Type)
	}
	if after.Enabled || after.ReadOnly || after.KubeconfigPath != "/new" || after.KubeContext != "ctx-2" ||
		string(after.KubeconfigEnc) != "new-bytes" || after.PollInterval != 5*time.Second {
		t.Errorf("after update = %+v", after)
	}
	if !after.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("created_at changed")
	}
	if !after.UpdatedAt.After(got.UpdatedAt) {
		t.Errorf("updated_at not advanced: %v -> %v", got.UpdatedAt, after.UpdatedAt)
	}
	// Clearing the encrypted kubeconfig works.
	upd.KubeconfigEnc = nil
	if err := r.Update(ctx, upd); err != nil {
		t.Fatal(err)
	}
	if cleared, _ := r.Get(ctx, "prod"); len(cleared.KubeconfigEnc) != 0 {
		t.Errorf("kubeconfig_enc not cleared: %x", cleared.KubeconfigEnc)
	}

	if err := r.Update(ctx, &model.SourceSpec{Name: "ghost"}); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("update missing = %v, want ErrNotFound", err)
	}

	if err := r.Delete(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "prod"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("after delete Get = %v", err)
	}
	if err := r.Delete(ctx, "prod"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Crash logs
// ---------------------------------------------------------------------------

func TestCrashLogSaveAndDedupe(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("c1"))
	r := db.CrashLogs()

	captured := at(5 * time.Minute)
	orig := &model.CrashLog{ContainerID: "c1", SourceName: "src1", OccurredAt: at(time.Minute), Reason: "Error", ExitCode: 2,
		Previous: true, Lines: "line1\nline2", LineCount: 2, Truncated: true, CapturedAt: captured}
	if err := r.Save(ctx, orig); err != nil {
		t.Fatal(err)
	}

	got, err := r.FindByContainer(ctx, "c1", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	l := got[0]
	if l.ID == 0 || l.ContainerID != "c1" || l.SourceName != "src1" || !l.OccurredAt.Equal(at(time.Minute)) || l.Reason != "Error" ||
		l.ExitCode != 2 || !l.Previous || l.Lines != "line1\nline2" || l.LineCount != 2 || !l.Truncated || !l.CapturedAt.Equal(captured) {
		t.Errorf("round trip = %+v", l)
	}

	// Same (container, occurred_at): untouched, no error.
	dup := *orig
	dup.Reason, dup.Lines = "Changed", "other"
	if err := r.Save(ctx, &dup); err != nil {
		t.Fatalf("duplicate save: %v", err)
	}
	got, _ = r.FindByContainer(ctx, "c1", 10)
	if len(got) != 1 || got[0].Reason != "Error" || got[0].Lines != "line1\nline2" {
		t.Errorf("duplicate overwrote the original: %+v", got)
	}

	// Zero CapturedAt defaults to now; NUL bytes are stripped.
	if err := r.Save(ctx, &model.CrashLog{ContainerID: "c1", OccurredAt: at(2 * time.Minute), Lines: "a\x00b"}); err != nil {
		t.Fatalf("NUL in lines: %v", err)
	}
	got, _ = r.FindByContainer(ctx, "c1", 10)
	if len(got) != 2 || got[0].Lines != "ab" || time.Since(got[0].CapturedAt) > time.Minute {
		t.Errorf("defaults: %+v", got[0])
	}

	// Unknown container violates the FK.
	if err := r.Save(ctx, &model.CrashLog{ContainerID: "ghost", OccurredAt: at(0), Lines: "x"}); err == nil {
		t.Error("expected FK error for unknown container")
	}
}

func TestCrashLogFinders(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	r := db.CrashLogs()
	a, b, o := newContainer("ns/pa/app"), newContainer("ns/pb/app"), newContainer("ns/po/app")
	o.WorkloadName = "other"
	mustUpsert(t, db, a)
	mustUpsert(t, db, b)
	mustUpsert(t, db, o)
	save := func(cid string, d time.Duration, lines string) {
		if err := r.Save(ctx, &model.CrashLog{ContainerID: cid, SourceName: "src1", OccurredAt: at(d), Lines: lines}); err != nil {
			t.Fatal(err)
		}
	}
	save(a.ID, 1*time.Minute, "a1")
	save(a.ID, 10*time.Minute, "a2")
	save(a.ID, 20*time.Minute, "a3")
	save(b.ID, 15*time.Minute, "b1")
	save(o.ID, 30*time.Minute, "o1")
	lines := func(ls []*model.CrashLog) string {
		var s []string
		for _, l := range ls {
			s = append(s, l.Lines)
		}
		return strings.Join(s, ",")
	}

	t.Run("FindByContainer newest first with limit", func(t *testing.T) {
		got, err := r.FindByContainer(ctx, a.ID, 2)
		if err != nil || lines(got) != "a3,a2" {
			t.Errorf("got %q err=%v", lines(got), err)
		}
		if none, err := r.FindByContainer(ctx, "nope", 5); err != nil || none == nil || len(none) != 0 {
			t.Errorf("unknown container: %v %v", none, err)
		}
	})
	t.Run("FindByWorkload spans instances newest first", func(t *testing.T) {
		wid := model.WorkloadID("src1", "ns", "Deployment", "web")
		got, err := r.FindByWorkload(ctx, wid, 10)
		if err != nil || lines(got) != "a3,b1,a2,a1" {
			t.Errorf("got %q err=%v", lines(got), err)
		}
		got, _ = r.FindByWorkload(ctx, wid, 2)
		if lines(got) != "a3,b1" {
			t.Errorf("limit: %q", lines(got))
		}
		if got, _ := r.FindByWorkload(ctx, "nope", 5); len(got) != 0 {
			t.Errorf("unknown workload: %q", lines(got))
		}
	})
	t.Run("FindByContainerAt tolerance", func(t *testing.T) {
		tests := []struct {
			name string
			at   time.Duration
			tol  time.Duration
			want string // "" = nil
		}{
			{"exact", 10 * time.Minute, 0, "a2"},
			{"within", 10*time.Minute + 20*time.Second, 30 * time.Second, "a2"},
			{"before within", 10*time.Minute - 20*time.Second, 30 * time.Second, "a2"},
			{"boundary inclusive", 10*time.Minute + 30*time.Second, 30 * time.Second, "a2"},
			{"just outside", 10*time.Minute + 31*time.Second, 30 * time.Second, ""},
			{"far away", 5 * time.Minute, 30 * time.Second, ""},
		}
		for _, tc := range tests {
			got, err := r.FindByContainerAt(ctx, a.ID, at(tc.at), tc.tol)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if tc.want == "" {
				if got != nil {
					t.Errorf("%s: got %q, want nil", tc.name, got.Lines)
				}
				continue
			}
			if got == nil || got.Lines != tc.want {
				t.Errorf("%s: got %v, want %s", tc.name, got, tc.want)
			}
		}
		// Both a2 (+10m) and a3 (+20m) are in range: the nearer one wins.
		if got, _ := r.FindByContainerAt(ctx, a.ID, at(17*time.Minute), 10*time.Minute); got == nil || got.Lines != "a3" {
			t.Errorf("closest (a3): %v", got)
		}
		if got, _ := r.FindByContainerAt(ctx, a.ID, at(12*time.Minute), 10*time.Minute); got == nil || got.Lines != "a2" {
			t.Errorf("closest (a2): %v", got)
		}
		// Other container's logs are never returned.
		if got, _ := r.FindByContainerAt(ctx, b.ID, at(10*time.Minute), time.Minute); got != nil {
			t.Errorf("leaked another container's log: %v", got)
		}
	})
}
