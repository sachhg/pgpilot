package metrics_test

import (
	"strings"
	"testing"

	"github.com/sachhg/pgpilot/internal/metrics"
)

type fakeSource struct {
	backends []metrics.BackendStat
	pools    []metrics.PoolStat
}

func (f *fakeSource) BackendStats() []metrics.BackendStat { return f.backends }
func (f *fakeSource) PoolStats() []metrics.PoolStat       { return f.pools }

func TestStateCollector_ReportsBackendAndPoolState(t *testing.T) {
	src := &fakeSource{
		backends: []metrics.BackendStat{
			{Addr: "10.0.0.1:5432", Role: "primary", Healthy: true, LagBytes: 0, LagSeconds: 0},
			{Addr: "10.0.0.2:5432", Role: "replica", Healthy: true, LagBytes: 4096, LagSeconds: 0.25},
			{Addr: "10.0.0.3:5432", Role: "replica", Healthy: false, LagBytes: 999999, LagSeconds: 12},
		},
		pools: []metrics.PoolStat{
			{Addr: "10.0.0.1:5432", Idle: 3, InUse: 2, Waiters: 1},
		},
	}
	m := metrics.New()
	m.EnableStateCollector(src)

	body := scrape(t, m)
	want := []string{
		`pgpilot_backend_healthy{backend="10.0.0.1:5432",role="primary"} 1`,
		`pgpilot_backend_healthy{backend="10.0.0.3:5432",role="replica"} 0`,
		`pgpilot_backend_lag_bytes{backend="10.0.0.2:5432"} 4096`,
		`pgpilot_backend_lag_seconds{backend="10.0.0.2:5432"} 0.25`,
		`pgpilot_pool_connections{backend="10.0.0.1:5432",state="idle"} 3`,
		`pgpilot_pool_connections{backend="10.0.0.1:5432",state="in_use"} 2`,
		`pgpilot_pool_waiters{backend="10.0.0.1:5432"} 1`,
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("scrape missing %q", w)
		}
	}
}

func TestStateCollector_ReflectsLiveChanges(t *testing.T) {
	src := &fakeSource{pools: []metrics.PoolStat{{Addr: "a", InUse: 1}}}
	m := metrics.New()
	m.EnableStateCollector(src)

	if !strings.Contains(scrape(t, m), `pgpilot_pool_connections{backend="a",state="in_use"} 1`) {
		t.Fatal("first scrape did not reflect initial state")
	}
	// Mutating the source is visible on the next scrape (pull, not snapshot).
	src.pools = []metrics.PoolStat{{Addr: "a", InUse: 5}}
	if !strings.Contains(scrape(t, m), `pgpilot_pool_connections{backend="a",state="in_use"} 5`) {
		t.Error("second scrape did not reflect the updated state")
	}
}
