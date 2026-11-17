package timescale

import (
	"context"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

func saveSample(t *testing.T, db *DB, s model.ResourceSample) {
	t.Helper()
	if s.SourceName == "" {
		s.SourceName = "src1"
	}
	out, err := db.Resources().Save(context.Background(), &s)
	if err != nil || out.ID == 0 {
		t.Fatalf("save sample: id=%d err=%v", out.ID, err)
	}
}

func TestResourceSaveAndFindRaw(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("c1"))
	mustUpsert(t, db, newContainer("c2"))

	// Inserted out of order to prove ORDER BY sampled_at.
	saveSample(t, db, model.ResourceSample{ContainerID: "c1", CPUPercent: 30, MemBytes: 300, MemPercent: 3, NetRxBytes: 3, NetTxBytes: 33, BlockReadBytes: 4, BlockWriteBytes: 5, SampledAt: at(20 * time.Second)})
	saveSample(t, db, model.ResourceSample{ContainerID: "c1", CPUPercent: 10, MemBytes: 100, MemPercent: 1, SampledAt: at(0)})
	saveSample(t, db, model.ResourceSample{ContainerID: "c1", CPUPercent: 20, MemBytes: 200, MemPercent: 2, SampledAt: at(10 * time.Second)})
	saveSample(t, db, model.ResourceSample{ContainerID: "c2", CPUPercent: 99, SampledAt: at(10 * time.Second)})

	got, err := db.Resources().FindByContainerID(ctx, "c1", at(0), at(20*time.Second), 0)
	if err != nil || len(got) != 3 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	for i, want := range []float64{10, 20, 30} {
		if got[i].CPUPercent != want || got[i].ContainerID != "c1" || got[i].SourceName != "src1" || got[i].ID == 0 {
			t.Errorf("[%d] = %+v", i, got[i])
		}
	}
	last := got[2]
	if last.MemBytes != 300 || last.MemPercent != 3 || last.NetRxBytes != 3 || last.NetTxBytes != 33 ||
		last.BlockReadBytes != 4 || last.BlockWriteBytes != 5 || !last.SampledAt.Equal(at(20*time.Second)) {
		t.Errorf("round trip: %+v", last)
	}

	// Closed interval: both endpoints included; narrower window excludes.
	got, _ = db.Resources().FindByContainerID(ctx, "c1", at(10*time.Second), at(20*time.Second), 0)
	if len(got) != 2 {
		t.Errorf("closed interval returned %d, want 2", len(got))
	}
	got, _ = db.Resources().FindByContainerID(ctx, "c1", at(11*time.Second), at(19*time.Second), 0)
	if len(got) != 0 {
		t.Errorf("empty window returned %d", len(got))
	}
}

func TestResourceFindBucketed(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("c1"))
	// bucket 1 = [0,30s), bucket 2 = [30s,60s); base is hour-aligned.
	for _, s := range []model.ResourceSample{
		{CPUPercent: 10, MemBytes: 100, MemPercent: 1, NetRxBytes: 100, NetTxBytes: 10, BlockReadBytes: 1, BlockWriteBytes: 2, SampledAt: at(0)},
		{CPUPercent: 20, MemBytes: 200, MemPercent: 2, NetRxBytes: 250, NetTxBytes: 30, BlockReadBytes: 7, BlockWriteBytes: 3, SampledAt: at(10 * time.Second)},
		{CPUPercent: 30, MemBytes: 300, MemPercent: 3, NetRxBytes: 200, NetTxBytes: 20, BlockReadBytes: 5, BlockWriteBytes: 9, SampledAt: at(20 * time.Second)},
		{CPUPercent: 50, MemBytes: 1000, MemPercent: 5, NetRxBytes: 900, NetTxBytes: 90, SampledAt: at(40 * time.Second)},
	} {
		s.ContainerID = "c1"
		saveSample(t, db, s)
	}

	got, err := db.Resources().FindByContainerID(ctx, "c1", at(0), at(time.Minute), 30*time.Second)
	if err != nil || len(got) != 2 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	b1, b2 := got[0], got[1]
	if !b1.SampledAt.Equal(at(0)) || !b2.SampledAt.Equal(at(30*time.Second)) {
		t.Errorf("bucket starts = %v, %v", b1.SampledAt, b2.SampledAt)
	}
	// gauges averaged
	if !near(b1.CPUPercent, 20) || b1.MemBytes != 200 || !near(b1.MemPercent, 2) {
		t.Errorf("bucket1 gauges = %+v", b1)
	}
	// counters take the maximum
	if b1.NetRxBytes != 250 || b1.NetTxBytes != 30 || b1.BlockReadBytes != 7 || b1.BlockWriteBytes != 9 {
		t.Errorf("bucket1 counters = %+v", b1)
	}
	if b1.ContainerID != "c1" || b1.SourceName != "src1" {
		t.Errorf("bucket1 identity = %+v", b1)
	}
	if !near(b2.CPUPercent, 50) || b2.MemBytes != 1000 || b2.NetRxBytes != 900 {
		t.Errorf("bucket2 = %+v", b2)
	}

	// A single wide bucket collapses everything.
	got, _ = db.Resources().FindByContainerID(ctx, "c1", at(0), at(time.Minute), time.Minute)
	if len(got) != 1 || !near(got[0].CPUPercent, 27.5) || got[0].NetRxBytes != 900 {
		t.Errorf("wide bucket = %+v", got)
	}
}

func TestResourceGetAggregated(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("c1"))
	mustUpsert(t, db, newContainer("empty"))
	saveSample(t, db, model.ResourceSample{ContainerID: "c1", CPUPercent: 10, MemBytes: 100, NetRxBytes: 5, NetTxBytes: 7, SampledAt: at(0)})
	saveSample(t, db, model.ResourceSample{ContainerID: "c1", CPUPercent: 40, MemBytes: 300, NetRxBytes: 15, NetTxBytes: 3, SampledAt: at(time.Minute)})
	saveSample(t, db, model.ResourceSample{ContainerID: "c1", CPUPercent: 99, MemBytes: 9999, SampledAt: at(time.Hour)}) // outside window

	from, to := at(0), at(2*time.Minute)
	agg, err := db.Resources().GetAggregated(ctx, "c1", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if agg.ContainerID != "c1" || !agg.WindowStart.Equal(from) || !agg.WindowEnd.Equal(to) {
		t.Errorf("identity = %+v", agg)
	}
	if !near(agg.AvgCPUPercent, 25) || !near(agg.MaxCPUPercent, 40) || agg.AvgMemBytes != 200 ||
		agg.MaxMemBytes != 300 || agg.TotalNetRxBytes != 20 || agg.TotalNetTxBytes != 10 {
		t.Errorf("aggregates = %+v", agg)
	}

	for _, id := range []string{"empty", "missing"} {
		agg, err := db.Resources().GetAggregated(ctx, id, from, to)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if agg.AvgCPUPercent != 0 || agg.MaxCPUPercent != 0 || agg.AvgMemBytes != 0 || agg.MaxMemBytes != 0 ||
			agg.TotalNetRxBytes != 0 || agg.TotalNetTxBytes != 0 || agg.ContainerID != id {
			t.Errorf("%s: want zeroes, got %+v", id, agg)
		}
	}
}

func TestScoreSaveLatestHistory(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("c1"))
	mustUpsert(t, db, newContainer("c2"))
	r := db.Scores()

	if got, err := r.FindLatest(ctx, "c1"); err != nil || got != nil {
		t.Fatalf("no scores: got %v err %v, want nil,nil", got, err)
	}
	if got, err := r.FindHistory(ctx, "c1", 5); err != nil || len(got) != 0 {
		t.Fatalf("no history: got %v err %v", got, err)
	}

	for i, sc := range []float64{70, 80, 60, 90} { // inserted out of calculated_at order for the 3rd
		cal := at(time.Duration(i) * time.Minute)
		if i == 2 {
			cal = at(-time.Minute)
		}
		in := &model.StabilityScore{ContainerID: "c1", SourceName: "src1", Score: sc, RestartPenalty: 1, CrashPenalty: 2,
			OOMPenalty: 3, VolatilityPenalty: 4, AvailabilityPenalty: 5, CalculatedAt: cal}
		out, err := r.Save(ctx, in)
		if err != nil || out.ID == 0 {
			t.Fatalf("save: %v %v", out, err)
		}
	}
	if _, err := r.Save(ctx, &model.StabilityScore{ContainerID: "c2", SourceName: "src1", Score: 1, CalculatedAt: at(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	latest, err := r.FindLatest(ctx, "c1")
	if err != nil || latest == nil {
		t.Fatal(latest, err)
	}
	if latest.Score != 90 || latest.RestartPenalty != 1 || latest.CrashPenalty != 2 || latest.OOMPenalty != 3 ||
		latest.VolatilityPenalty != 4 || latest.AvailabilityPenalty != 5 || latest.SourceName != "src1" ||
		latest.ContainerID != "c1" || !latest.CalculatedAt.Equal(at(3*time.Minute)) {
		t.Errorf("latest = %+v", latest)
	}

	hist, err := r.FindHistory(ctx, "c1", 3)
	if err != nil || len(hist) != 3 {
		t.Fatalf("len=%d err=%v", len(hist), err)
	}
	for i, want := range []float64{90, 80, 70} { // newest first, limited to 3
		if hist[i].Score != want {
			t.Errorf("hist[%d] = %v, want %v", i, hist[i].Score, want)
		}
	}
	if all, _ := r.FindHistory(ctx, "c1", 100); len(all) != 4 || all[3].Score != 60 {
		t.Errorf("full history = %d rows", len(all))
	}
}
