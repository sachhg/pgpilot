package metrics

import "github.com/prometheus/client_golang/prometheus"

// BackendStat is one backend's health as of the last poll. The state collector
// turns it into gauges at scrape time.
type BackendStat struct {
	Addr       string
	Role       string
	Healthy    bool
	LagBytes   int64
	LagSeconds float64
}

// PoolStat is one backend pool's occupancy, aggregated per backend address so
// each address yields exactly one series.
type PoolStat struct {
	Addr    string
	Idle    int
	InUse   int
	Waiters int
}

// StateSource supplies current backend and pool state, read fresh on every
// scrape so the gauges never go stale between events.
type StateSource interface {
	BackendStats() []BackendStat
	PoolStats() []PoolStat
}

// EnableStateCollector registers a collector that reports backend lag, health,
// and pool saturation, pulling from src each time Prometheus scrapes.
func (m *Metrics) EnableStateCollector(src StateSource) {
	m.reg.MustRegister(newStateCollector(src))
}

type stateCollector struct {
	src         StateSource
	healthy     *prometheus.Desc
	lagBytes    *prometheus.Desc
	lagSeconds  *prometheus.Desc
	poolConns   *prometheus.Desc
	poolWaiters *prometheus.Desc
}

func newStateCollector(src StateSource) *stateCollector {
	return &stateCollector{
		src: src,
		healthy: prometheus.NewDesc(namespace+"_backend_healthy",
			"Whether a backend is currently healthy (1) or tripped (0).",
			[]string{"backend", "role"}, nil),
		lagBytes: prometheus.NewDesc(namespace+"_backend_lag_bytes",
			"Replication lag behind the primary, in WAL bytes.",
			[]string{"backend"}, nil),
		lagSeconds: prometheus.NewDesc(namespace+"_backend_lag_seconds",
			"Replication lag behind the primary, in seconds.",
			[]string{"backend"}, nil),
		poolConns: prometheus.NewDesc(namespace+"_pool_connections",
			"Pooled backend connections by state (idle or in_use).",
			[]string{"backend", "state"}, nil),
		poolWaiters: prometheus.NewDesc(namespace+"_pool_waiters",
			"Clients waiting for a pooled connection to a backend.",
			[]string{"backend"}, nil),
	}
}

// Describe sends the descriptors, making this a checked collector.
func (c *stateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.healthy
	ch <- c.lagBytes
	ch <- c.lagSeconds
	ch <- c.poolConns
	ch <- c.poolWaiters
}

// Collect reads the source and emits one set of gauges per backend and pool.
func (c *stateCollector) Collect(ch chan<- prometheus.Metric) {
	for _, b := range c.src.BackendStats() {
		healthy := 0.0
		if b.Healthy {
			healthy = 1.0
		}
		ch <- prometheus.MustNewConstMetric(c.healthy, prometheus.GaugeValue, healthy, b.Addr, b.Role)
		ch <- prometheus.MustNewConstMetric(c.lagBytes, prometheus.GaugeValue, float64(b.LagBytes), b.Addr)
		ch <- prometheus.MustNewConstMetric(c.lagSeconds, prometheus.GaugeValue, b.LagSeconds, b.Addr)
	}
	for _, p := range c.src.PoolStats() {
		ch <- prometheus.MustNewConstMetric(c.poolConns, prometheus.GaugeValue, float64(p.Idle), p.Addr, "idle")
		ch <- prometheus.MustNewConstMetric(c.poolConns, prometheus.GaugeValue, float64(p.InUse), p.Addr, "in_use")
		ch <- prometheus.MustNewConstMetric(c.poolWaiters, prometheus.GaugeValue, float64(p.Waiters), p.Addr)
	}
}
