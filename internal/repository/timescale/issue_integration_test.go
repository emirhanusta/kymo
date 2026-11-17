package timescale

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

func newIssue(wid string, typ model.IssueType, sev model.IssueSeverity, first time.Duration) *model.Issue {
	return &model.Issue{
		SourceName: "src1", WorkloadID: wid, Namespace: "ns", Kind: "Deployment", Name: "web", Type: typ, Severity: sev,
		Title: "title " + string(typ), Detail: "detail", FirstSeen: at(first), LastSeen: at(first),
	}
}

func issueIDs(list []*model.Issue) []int64 {
	out := make([]int64, len(list))
	for i, x := range list {
		out[i] = x.ID
	}
	return out
}

func sameIDs(a []int64, b ...int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestIssueLifecycle(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Issues()

	i := newIssue("src1:ns:Deployment:web", model.IssueCrashLoop, model.SeverityCritical, time.Minute)
	if err := repo.Create(ctx, i); err != nil || i.ID == 0 || i.Status != model.IssueOpen {
		t.Fatalf("create: %+v %v", i, err)
	}
	// One open issue per (workload, type).
	dup := newIssue(i.WorkloadID, i.Type, model.SeverityCritical, 2*time.Minute)
	if err := repo.Create(ctx, dup); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("duplicate open: %v", err)
	}
	// Another type or workload is fine.
	if err := repo.Create(ctx, newIssue(i.WorkloadID, model.IssueOOM, model.SeverityCritical, time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, i.ID)
	if err != nil || got.Title != "title crash_loop" || got.Status != model.IssueOpen || got.ResolvedAt != nil ||
		got.Flaps != 0 || got.NotifyCount != 0 || got.LastNotifiedAt != nil || !got.FirstSeen.Equal(at(time.Minute)) {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := repo.Get(ctx, 123456); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}

	// UpdateOpen moves last_seen forward only, and changes title/severity/detail.
	got.Title, got.Detail, got.Severity, got.LastSeen = "new title", "new detail", model.SeverityWarning, at(5*time.Minute)
	if err := repo.UpdateOpen(ctx, got); err != nil {
		t.Fatal(err)
	}
	got.LastSeen = at(2 * time.Minute)
	if err := repo.UpdateOpen(ctx, got); err != nil {
		t.Fatal(err)
	}
	cur, _ := repo.Get(ctx, i.ID)
	if cur.Title != "new title" || cur.Detail != "new detail" || cur.Severity != model.SeverityWarning || !cur.LastSeen.Equal(at(5*time.Minute)) {
		t.Errorf("update: %+v", cur)
	}

	if err := repo.MarkNotified(ctx, i.ID, at(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	cur, _ = repo.Get(ctx, i.ID)
	if cur.NotifyCount != 1 || cur.LastNotifiedAt == nil || !cur.LastNotifiedAt.Equal(at(6*time.Minute)) {
		t.Errorf("notified: %+v", cur)
	}

	// Resolve, after which a new open issue of the same key is allowed.
	if err := repo.Resolve(ctx, i.ID, at(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Resolve(ctx, i.ID, at(11*time.Minute)); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("resolve twice: %v", err)
	}
	if err := repo.UpdateOpen(ctx, cur); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("update resolved: %v", err)
	}
	cur, _ = repo.Get(ctx, i.ID)
	if cur.Status != model.IssueResolved || cur.ResolvedAt == nil || !cur.ResolvedAt.Equal(at(10*time.Minute)) {
		t.Fatalf("resolved: %+v", cur)
	}
	again := newIssue(i.WorkloadID, i.Type, model.SeverityCritical, 20*time.Minute)
	if err := repo.Create(ctx, again); err != nil {
		t.Fatalf("new open after resolve: %v", err)
	}
	// ...which blocks reopening the old row.
	if err := repo.Reopen(ctx, cur); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("reopen while another is open: %v", err)
	}
	if err := repo.Resolve(ctx, again.ID, at(21*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Reopen: same row, open, resolved_at cleared, flaps + 1.
	cur.Title, cur.LastSeen = "back again", at(25*time.Minute)
	if err := repo.Reopen(ctx, cur); err != nil {
		t.Fatal(err)
	}
	re, _ := repo.Get(ctx, i.ID)
	if re.Status != model.IssueOpen || re.ResolvedAt != nil || re.Flaps != 1 || re.Title != "back again" || !re.FirstSeen.Equal(at(time.Minute)) {
		t.Errorf("reopen: %+v", re)
	}
	if err := repo.Reopen(ctx, re); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("reopen open issue: %v", err)
	}
}

func TestIssueListAndSummary(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Issues()
	mk := func(src, wid string, typ model.IssueType, sev model.IssueSeverity, first time.Duration) *model.Issue {
		i := newIssue(wid, typ, sev, first)
		i.SourceName = src
		if err := repo.Create(ctx, i); err != nil {
			t.Fatal(err)
		}
		return i
	}
	w1, w2 := "src1:ns:Deployment:a", "src2:ns:Deployment:b"
	warnOld := mk("src1", w1, model.IssueDegraded, model.SeverityWarning, 1*time.Minute)
	critOld := mk("src1", w1, model.IssueCrashLoop, model.SeverityCritical, 2*time.Minute)
	critNew := mk("src2", w2, model.IssueOOM, model.SeverityCritical, 5*time.Minute)
	warnNew := mk("src2", w2, model.IssueProbeFailing, model.SeverityWarning, 6*time.Minute)
	res1 := mk("src1", "src1:ns:Deployment:c", model.IssueEvicted, model.SeverityCritical, 3*time.Minute)
	res2 := mk("src1", "src1:ns:Deployment:d", model.IssueImagePull, model.SeverityCritical, 4*time.Minute)
	if err := repo.Resolve(ctx, res1.ID, at(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Resolve(ctx, res2.ID, at(20*time.Minute)); err != nil {
		t.Fatal(err)
	}

	list := func(q repository.IssueQuery) []int64 {
		t.Helper()
		out, err := repo.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return issueIDs(out)
	}
	// Default (open): critical first, newest first_seen first.
	if got := list(repository.IssueQuery{}); !sameIDs(got, critNew.ID, critOld.ID, warnNew.ID, warnOld.ID) {
		t.Errorf("open order: %v", got)
	}
	// Resolved by resolved_at desc, with the before cursor on resolved_at.
	if got := list(repository.IssueQuery{Status: "resolved"}); !sameIDs(got, res1.ID, res2.ID) {
		t.Errorf("resolved order: %v", got)
	}
	if got := list(repository.IssueQuery{Status: "resolved", Before: at(30 * time.Minute)}); !sameIDs(got, res2.ID) {
		t.Errorf("resolved before: %v", got)
	}
	// All by first_seen desc, cursor on first_seen.
	if got := list(repository.IssueQuery{Status: "all"}); !sameIDs(got, warnNew.ID, critNew.ID, res2.ID, res1.ID, critOld.ID, warnOld.ID) {
		t.Errorf("all order: %v", got)
	}
	if got := list(repository.IssueQuery{Status: "all", Before: at(4 * time.Minute)}); !sameIDs(got, res1.ID, critOld.ID, warnOld.ID) {
		t.Errorf("all before: %v", got)
	}
	// Filters and limit.
	if got := list(repository.IssueQuery{Source: "src2"}); !sameIDs(got, critNew.ID, warnNew.ID) {
		t.Errorf("source: %v", got)
	}
	if got := list(repository.IssueQuery{Severity: "warning"}); !sameIDs(got, warnNew.ID, warnOld.ID) {
		t.Errorf("severity: %v", got)
	}
	if got := list(repository.IssueQuery{Type: "crash_loop"}); !sameIDs(got, critOld.ID) {
		t.Errorf("type: %v", got)
	}
	if got := list(repository.IssueQuery{WorkloadID: w2}); !sameIDs(got, critNew.ID, warnNew.ID) {
		t.Errorf("workload: %v", got)
	}
	if got := list(repository.IssueQuery{Limit: 2}); !sameIDs(got, critNew.ID, critOld.ID) {
		t.Errorf("limit: %v", got)
	}
	if got := list(repository.IssueQuery{Status: "all", Source: "nope"}); len(got) != 0 {
		t.Errorf("no match: %v", got)
	}

	// ListActive: all open + resolved within the window, newest resolved first.
	act, err := repo.ListActive(ctx, at(25*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(act) != 5 {
		t.Fatalf("active: %v", issueIDs(act))
	}
	var sawResolved []int64
	for _, a := range act {
		if a.Status == model.IssueResolved {
			sawResolved = append(sawResolved, a.ID)
		}
	}
	if !sameIDs(sawResolved, res1.ID) {
		t.Errorf("resolved within window: %v", sawResolved)
	}

	sum, err := repo.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Open != 4 || sum.Critical != 2 || sum.Warning != 2 || sum.BySource["src1"] != 2 || sum.BySource["src2"] != 2 || len(sum.BySource) != 2 {
		t.Errorf("summary: %+v", sum)
	}
	empty, _ := reset(t).Issues().Summary(ctx)
	if empty.Open != 0 || empty.BySource == nil {
		t.Errorf("empty summary: %+v", empty)
	}
}

func TestIssueCleanup(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Issues()
	old := newIssue("w:old", model.IssueOOM, model.SeverityCritical, 0)
	recent := newIssue("w:recent", model.IssueOOM, model.SeverityCritical, 0)
	open := newIssue("w:open", model.IssueOOM, model.SeverityCritical, 0)
	for _, i := range []*model.Issue{old, recent, open} {
		if err := repo.Create(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	_ = repo.Resolve(ctx, old.ID, time.Now().Add(-100*24*time.Hour))
	_ = repo.Resolve(ctx, recent.ID, time.Now().Add(-time.Hour))

	n, err := repo.DeleteResolvedBefore(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	if _, err := repo.Get(ctx, old.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Error("old resolved issue should be gone")
	}
	for _, i := range []*model.Issue{recent, open} {
		if _, err := repo.Get(ctx, i.ID); err != nil {
			t.Errorf("issue %d should remain: %v", i.ID, err)
		}
	}
}
