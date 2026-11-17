package docker

import (
	"math"
	"testing"
	"time"

	apitypes "github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
)

func TestStatsToSample_CPUAndMemory(t *testing.T) {
	var s containertypes.StatsResponse
	s.Read = time.Now()
	s.CPUStats.CPUUsage.TotalUsage = 2_000_000_000
	s.PreCPUStats.CPUUsage.TotalUsage = 1_000_000_000
	s.CPUStats.SystemUsage = 20_000_000_000
	s.PreCPUStats.SystemUsage = 10_000_000_000
	s.CPUStats.OnlineCPUs = 4
	s.MemoryStats.Usage = 300 << 20
	s.MemoryStats.Limit = 1000 << 20
	s.MemoryStats.Stats = map[string]uint64{"inactive_file": 100 << 20} // cgroup v2

	got := statsToSample("abc", &s)

	// 1e9 / 1e10 * 4 cores * 100 = 40%
	if math.Abs(got.CPUPercent-40) > 1e-9 {
		t.Errorf("cpu = %v, want 40", got.CPUPercent)
	}
	if got.MemBytes != 200<<20 {
		t.Errorf("mem = %d, want %d (usage minus inactive_file)", got.MemBytes, 200<<20)
	}
	if math.Abs(got.MemPercent-20) > 1e-9 {
		t.Errorf("mem%% = %v, want 20", got.MemPercent)
	}
}

func TestStatsToSample_NoPreviousReading(t *testing.T) {
	var s containertypes.StatsResponse
	s.CPUStats.CPUUsage.TotalUsage = 5_000_000_000
	s.CPUStats.SystemUsage = 0 // no system delta available
	s.CPUStats.OnlineCPUs = 2
	if got := statsToSample("abc", &s); got.CPUPercent != 0 {
		t.Errorf("cpu without a system delta should be 0, got %v", got.CPUPercent)
	}
}

func TestListItemStatus(t *testing.T) {
	cases := map[string]string{
		"Exited (0) 5 minutes ago": "stopped",
		"Exited (3) 1 second ago":  "crashing",
	}
	for status, want := range cases {
		ct := &apitypes.Container{State: "exited", Status: status}
		if got := string(listItemStatus(ct)); got != want {
			t.Errorf("%q -> %s, want %s", status, got, want)
		}
	}
}
