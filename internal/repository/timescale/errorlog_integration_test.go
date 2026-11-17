package timescale

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

const testWL = "src1:ns:Deployment:web"

func sigDelta(id, tmpl string, total int, buckets []model.ErrorBucketDelta, occ []model.ErrorOccurrence) model.ErrorSignatureDelta {
	first, last := occ[0].Time, occ[0].Time
	for _, o := range occ {
		if o.Time.Before(first) {
			first = o.Time
		}
		if o.Time.After(last) {
			last = o.Time
		}
	}
	return model.ErrorSignatureDelta{
		ID: id, SourceName: "src1", WorkloadID: testWL, Namespace: "ns", Kind: "Deployment", Name: "web",
		Level: "error", Template: tmpl, Sample: "sample " + tmpl, FirstSeen: first, LastSeen: last,
		Total: total, Buckets: buckets, Occurrences: occ,
	}
}

func occAt(t time.Time, pod, msg string) model.ErrorOccurrence {
	return model.ErrorOccurrence{Time: t, ContainerID: "ns/" + pod + "/app", Pod: pod, Container: "app", Level: "error", Message: msg}
}

func TestErrorTargetsCRUD(t *testing.T) {
	db := reset(t)
	repo := db.ErrorLogs()
	ctx := context.Background()

	a := &model.LogCaptureTarget{SourceName: "src1", Namespace: "ns"}
	if err := repo.CreateTarget(ctx, a); err != nil || a.ID == 0 || a.Origin != model.TargetOriginUI || a.CreatedAt.IsZero() {
		t.Fatalf("create: %+v %v", a, err)
	}
	if err := repo.CreateTarget(ctx, &model.LogCaptureTarget{SourceName: "src1", Namespace: "ns"}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	w := &model.LogCaptureTarget{SourceName: "src1", Namespace: "ns", WorkloadID: testWL}
	if err := repo.CreateTarget(ctx, w); err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListTargets(ctx)
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].WorkloadID != testWL {
		t.Fatalf("list: %+v %v", list, err)
	}
	got, err := repo.GetTarget(ctx, w.ID)
	if err != nil || got.WorkloadID != testWL {
		t.Fatalf("get: %+v %v", got, err)
	}
	if err := repo.DeleteTarget(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteTarget(ctx, w.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := repo.GetTarget(ctx, w.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
}

func TestErrorTargetsSyncConfig(t *testing.T) {
	db := reset(t)
	repo := db.ErrorLogs()
	ctx := context.Background()

	ui := &model.LogCaptureTarget{SourceName: "src1", Namespace: "shop"}
	if err := repo.CreateTarget(ctx, ui); err != nil {
		t.Fatal(err)
	}
	decl := func(ns, wl string) *model.LogCaptureTarget {
		return &model.LogCaptureTarget{SourceName: "src1", Namespace: ns, WorkloadID: wl, Origin: model.TargetOriginConfig}
	}
	if err := repo.SyncConfigTargets(ctx, []*model.LogCaptureTarget{decl("shop", ""), decl("shop", "src1:shop:Deployment:worker")}); err != nil {
		t.Fatal(err)
	}
	list, _ := repo.ListTargets(ctx)
	if len(list) != 2 {
		t.Fatalf("want 2 targets, got %+v", list)
	}
	origins := map[string]string{}
	for _, x := range list {
		origins[x.WorkloadID] = x.Origin
	}
	if origins[""] != "config" || origins["src1:shop:Deployment:worker"] != "config" {
		t.Fatalf("origins: %v", origins)
	}
	if list[0].ID != ui.ID {
		t.Error("the UI target should have been promoted in place")
	}
	// Idempotent, and removing one from the file deletes it; UI targets of other scopes stay.
	other := &model.LogCaptureTarget{SourceName: "src1", Namespace: "other"}
	if err := repo.CreateTarget(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := repo.SyncConfigTargets(ctx, []*model.LogCaptureTarget{decl("shop", "")}); err != nil {
		t.Fatal(err)
	}
	list, _ = repo.ListTargets(ctx)
	if len(list) != 2 {
		t.Fatalf("want shop + other, got %+v", list)
	}
	if err := repo.SyncConfigTargets(ctx, nil); err != nil {
		t.Fatal(err)
	}
	list, _ = repo.ListTargets(ctx)
	if len(list) != 1 || list[0].ID != other.ID {
		t.Fatalf("want only the UI target, got %+v", list)
	}
}

func TestErrorSignatureUpsertCountsAndBuckets(t *testing.T) {
	db := reset(t)
	repo := db.ErrorLogs()
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Minute)
	m := func(d time.Duration) time.Time { return now.Add(-d) }

	// First flush: 3 occurrences at -30m (2 pods), 1 at -2m.
	b1 := &model.ErrorBatch{Signatures: []model.ErrorSignatureDelta{sigDelta("sig1", "boom <n>", 4,
		[]model.ErrorBucketDelta{
			{Bucket: m(30 * time.Minute), Count: 3, Pods: []string{"web-1", "web-2"}},
			{Bucket: m(2 * time.Minute), Count: 1, Pods: []string{"web-1"}},
		},
		[]model.ErrorOccurrence{occAt(m(30*time.Minute), "web-1", "m1"), occAt(m(2*time.Minute), "web-1", "m2")})}}
	if err := repo.SaveBatch(ctx, b1, 50); err != nil {
		t.Fatal(err)
	}
	// Second flush: same minute bucket as before (-2m) from a third pod, fatal level, newer sample.
	d2 := sigDelta("sig1", "boom <n>", 2,
		[]model.ErrorBucketDelta{{Bucket: m(2 * time.Minute), Count: 2, Pods: []string{"web-3", "web-1"}}},
		[]model.ErrorOccurrence{occAt(m(time.Minute), "web-3", "m3")})
	d2.Level, d2.Sample = "fatal", "newest sample"
	if err := repo.SaveBatch(ctx, &model.ErrorBatch{Signatures: []model.ErrorSignatureDelta{d2}}, 50); err != nil {
		t.Fatal(err)
	}
	other := sigDelta("sig2", "other", 1, []model.ErrorBucketDelta{{Bucket: m(10 * time.Minute), Count: 1, Pods: []string{"web-1"}}},
		[]model.ErrorOccurrence{occAt(m(10*time.Minute), "web-1", "x")})
	if err := repo.SaveBatch(ctx, &model.ErrorBatch{Signatures: []model.ErrorSignatureDelta{other}}, 50); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListSignatures(ctx, repository.ErrorQuery{WorkloadID: testWL, Since: m(24 * time.Hour), Until: now.Add(time.Minute)})
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list[0].ID != "sig1" { // newest last_seen first
		t.Fatalf("order: %s first", list[0].ID)
	}
	s := list[0]
	if s.Count != 6 || s.TotalCount != 6 || s.Pods != 3 || s.Level != "fatal" || s.Sample != "newest sample" || !s.IsNew {
		t.Errorf("sig1 = %+v", s)
	}
	if len(s.Buckets) != 24 {
		t.Fatalf("buckets: %d", len(s.Buckets))
	}
	var sum int64
	for _, n := range s.Buckets {
		sum += n
	}
	if sum != 6 || s.Buckets[23] != 6 { // 24h/24 = 1h buckets: everything is in the newest one
		t.Errorf("buckets = %v", s.Buckets)
	}

	// 48m window = 2m buckets, oldest first: the -30m minute and the newest ones separate.
	fine, err := repo.GetSignature(ctx, "sig1", now.Add(-48*time.Minute), now)
	if err != nil || fine.Count != 6 || fine.Buckets[23] != 3 || fine.Buckets[22] != 0 || fine.Buckets[9] != 3 {
		t.Errorf("fine buckets = %+v %v", fine, err)
	}

	// Short window: only the -2m / -1m data counts, the -30m bucket does not.
	short, err := repo.GetSignature(ctx, "sig1", m(10*time.Minute), now.Add(time.Minute))
	if err != nil || short.Count != 3 || short.Pods != 2 || short.TotalCount != 6 {
		t.Fatalf("short window: %+v %v", short, err)
	}
	if _, err := repo.GetSignature(ctx, "nope", time.Time{}, time.Time{}); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// is_new is relative to the window start.
	old, _ := repo.GetSignature(ctx, "sig1", m(10*time.Minute), now.Add(time.Minute))
	if old.IsNew {
		t.Error("first seen 30m ago is not new in a 10m window")
	}

	// Filters.
	if l, _ := repo.ListSignatures(ctx, repository.ErrorQuery{Source: "nope"}); len(l) != 0 {
		t.Errorf("source filter: %+v", l)
	}
	if l, _ := repo.ListSignatures(ctx, repository.ErrorQuery{Namespace: "ns", Since: m(time.Hour), Limit: 1}); len(l) != 1 {
		t.Errorf("limit: %+v", l)
	}
	// Signatures not seen in the window are skipped.
	if l, _ := repo.ListSignatures(ctx, repository.ErrorQuery{Since: m(time.Second)}); len(l) != 0 {
		t.Errorf("stale signatures listed: %+v", l)
	}
}

func TestErrorOccurrencesTrimAndPaging(t *testing.T) {
	db := reset(t)
	repo := db.ErrorLogs()
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	var occ []model.ErrorOccurrence
	for i := 0; i < 8; i++ {
		occ = append(occ, occAt(base.Add(time.Duration(i)*time.Second+123456*time.Nanosecond*1000), "web-1", fmt.Sprintf("msg %d", i)))
	}
	d := sigDelta("sigT", "trim", 8, []model.ErrorBucketDelta{{Bucket: base, Count: 8, Pods: []string{"web-1"}}}, occ)
	if err := repo.SaveBatch(ctx, &model.ErrorBatch{Signatures: []model.ErrorSignatureDelta{d}}, 5); err != nil {
		t.Fatal(err)
	}
	// A later flush adds two more; still only the newest 5 remain.
	d2 := sigDelta("sigT", "trim", 2, nil, []model.ErrorOccurrence{occAt(base.Add(20*time.Second), "web-2", "msg 20"), occAt(base.Add(21*time.Second), "web-2", "msg 21")})
	if err := repo.SaveBatch(ctx, &model.ErrorBatch{Signatures: []model.ErrorSignatureDelta{d2}}, 5); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListOccurrences(ctx, "sigT", time.Time{}, 50)
	if err != nil || len(got) != 5 {
		t.Fatalf("occurrences: %d %v", len(got), err)
	}
	want := []string{"msg 21", "msg 20", "msg 7", "msg 6", "msg 5"}
	for i, o := range got {
		if o.Message != want[i] {
			t.Errorf("occurrence %d = %q, want %q", i, o.Message, want[i])
		}
	}
	// Counts stay exact although occurrences were trimmed.
	sig, _ := repo.GetSignature(ctx, "sigT", base.Add(-time.Hour), base.Add(time.Hour))
	if sig.TotalCount != 10 || sig.Count != 8 {
		t.Errorf("counts: %+v", sig)
	}
	// Paging: strictly older than the previous page's last time, full precision.
	page1, _ := repo.ListOccurrences(ctx, "sigT", time.Time{}, 2)
	page2, _ := repo.ListOccurrences(ctx, "sigT", page1[1].Time, 2)
	if len(page2) != 2 || page2[0].Message != "msg 7" || !page2[0].Time.Before(page1[1].Time) {
		t.Errorf("page2 = %+v", page2)
	}
	if page1[0].Time.Nanosecond()%1000 != 0 {
		t.Errorf("stored time has sub-microsecond precision: %v", page1[0].Time)
	}
	if none, _ := repo.ListOccurrences(ctx, "sigT", got[len(got)-1].Time, 10); len(none) != 0 {
		t.Errorf("before the oldest kept: %+v", none)
	}
}

func TestWorkloadErrorRatesAndStale(t *testing.T) {
	db := reset(t)
	repo := db.ErrorLogs()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)

	a := sigDelta("rateA", "common", 12, []model.ErrorBucketDelta{
		{Bucket: now.Add(-time.Minute), Count: 8, Pods: []string{"p"}},
		{Bucket: now.Add(-30 * time.Minute), Count: 4, Pods: []string{"p"}}}, []model.ErrorOccurrence{occAt(now, "p", "x")})
	b := sigDelta("rateB", "rare", 3, []model.ErrorBucketDelta{{Bucket: now.Add(-2 * time.Minute), Count: 3, Pods: []string{"p"}}},
		[]model.ErrorOccurrence{occAt(now, "p", "y")})
	if err := repo.SaveBatch(ctx, &model.ErrorBatch{Signatures: []model.ErrorSignatureDelta{a, b}}, 50); err != nil {
		t.Fatal(err)
	}
	rates, err := repo.WorkloadErrorRates(ctx, now.Add(-5*time.Minute))
	if err != nil || len(rates) != 1 {
		t.Fatalf("rates: %+v %v", rates, err)
	}
	r := rates[0]
	if r.WorkloadID != testWL || r.Count != 11 || r.TopTemplate != "common" || r.TopCount != 8 || r.Kind != "Deployment" || r.Name != "web" {
		t.Errorf("rate = %+v", r)
	}
	if rates, _ = repo.WorkloadErrorRates(ctx, now.Add(time.Hour)); len(rates) != 0 {
		t.Errorf("future since: %+v", rates)
	}

	// DeleteStale cascades counts and occurrences.
	n, err := repo.DeleteStale(ctx, now.Add(time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("delete stale: %d %v", n, err)
	}
	for _, tbl := range []string{"error_signatures", "error_counts", "error_occurrences"} {
		if c := count(t, db, tbl, ""); c != 0 {
			t.Errorf("%s has %d rows left", tbl, c)
		}
	}
}

func TestErrorPurgeSourceAndRetention(t *testing.T) {
	db := reset(t)
	repo := db.ErrorLogs()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)

	mk := func(id, src string) model.ErrorSignatureDelta {
		d := sigDelta(id, id, 1, []model.ErrorBucketDelta{{Bucket: now, Count: 1, Pods: []string{"p"}}}, []model.ErrorOccurrence{occAt(now, "p", id)})
		d.SourceName = src
		return d
	}
	if err := repo.SaveBatch(ctx, &model.ErrorBatch{Signatures: []model.ErrorSignatureDelta{mk("keep", "other"), mk("gone", "src1")}}, 50); err != nil {
		t.Fatal(err)
	}
	_ = repo.CreateTarget(ctx, &model.LogCaptureTarget{SourceName: "src1", Namespace: "ns"})
	_ = repo.CreateTarget(ctx, &model.LogCaptureTarget{SourceName: "other", Namespace: "ns"})
	mustUpsert(t, db, newContainer("ns/p/app"))

	if _, err := db.Containers().PurgeSource(ctx, "src1"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "error_signatures", ""); n != 1 {
		t.Errorf("signatures after purge = %d", n)
	}
	if n := count(t, db, "error_counts", ""); n != 1 {
		t.Errorf("counts after purge = %d", n)
	}
	if n := count(t, db, "error_occurrences", ""); n != 1 {
		t.Errorf("occurrences after purge = %d", n)
	}
	targets, _ := repo.ListTargets(ctx)
	if len(targets) != 1 || targets[0].SourceName != "other" {
		t.Errorf("targets after purge: %+v", targets)
	}

	// Retention policy is re-applied without error (and rejects silly values).
	if err := repo.SetRetention(ctx, 48*time.Hour); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	if err := repo.SetRetention(ctx, 48*time.Hour); err != nil {
		t.Fatalf("set retention twice: %v", err)
	}
	var cfg string
	if err := db.pool.QueryRow(ctx, `SELECT config::text FROM timescaledb_information.jobs
WHERE proc_name = 'policy_retention' AND hypertable_name = 'error_counts'`).Scan(&cfg); err != nil {
		t.Fatalf("retention job: %v", err)
	}
	if want := "48:00:00"; !contains(cfg, want) {
		t.Errorf("retention config %s does not mention %q", cfg, want)
	}
	if err := repo.SetRetention(ctx, time.Minute); err == nil {
		t.Error("retention below 1h accepted")
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
