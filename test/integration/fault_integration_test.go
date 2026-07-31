//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/backend"
	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/faultproxy"
	"github.com/sachhg/pgpilot/internal/metrics"
	"github.com/sachhg/pgpilot/internal/proxy"
	"github.com/sachhg/pgpilot/internal/registry"
	"github.com/sachhg/pgpilot/internal/router"
)

// faultCluster is a routing proxy whose replicas are reached through fault
// proxies, so a test can blackhole, sever, or slow a replica deterministically.
type faultCluster struct {
	port     int
	replicas []*faultproxy.Proxy // one per replica, index-aligned
	metrics  *metrics.Metrics
	manager  *backend.Manager
}

// startFaultCluster wires pgpilot to the real replicas through fault proxies. The
// health poll interval is long so that, once every backend has polled healthy,
// a fault injected mid-test does not immediately flip the registry — exposing
// the routing/failover path rather than plain health-based avoidance.
func startFaultCluster(t *testing.T, mode string, policy router.Policy) faultCluster {
	t.Helper()
	var proxies []*faultproxy.Proxy
	var proxyAddrs []string
	for _, addr := range replicaHostPorts {
		fp, err := faultproxy.New(addr)
		if err != nil {
			t.Fatalf("fault proxy: %v", err)
		}
		t.Cleanup(func() { _ = fp.Close() })
		proxies = append(proxies, fp)
		proxyAddrs = append(proxyAddrs, fp.Addr())
	}

	cfg := &config.Config{
		Primary:  primaryHostPort,
		Replicas: proxyAddrs,
		Users:    []config.User{{Name: pgUser, Password: pgPass}},
		Fencing:  config.Fencing{Mode: mode, BoundedMs: 100},
	}
	mgr := backend.NewManager(cfg.Primary, map[string]string{pgUser: pgPass},
		backend.PoolConfig{MaxSize: 5, AcquireTimeout: 3 * time.Second, IdleTimeout: time.Minute})
	t.Cleanup(mgr.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	reg := registry.New(registry.Config{
		Interval: 60 * time.Second, // effectively static health during a test
		Logger:   log,
		Dialer: func(dctx context.Context, addr string) (registry.Conn, error) {
			c, err := backend.Dial(dctx, addr, pgUser, pgPass, pgUser)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	backends := []registry.Backend{{Name: "primary", Addr: cfg.Primary}}
	for i, a := range proxyAddrs {
		backends = append(backends, registry.Backend{Name: fmt.Sprintf("replica%d", i+1), Addr: a})
	}
	reg.Start(ctx, backends)

	srv := proxy.New(proxy.Config{
		ListenAddr: "0.0.0.0:0", Users: cfg, Manager: mgr,
		Registry: reg, Policy: policy, Metrics: m, Logger: log,
	})
	addr, err := srv.Listen()
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		reg.Wait()
		<-done
	})

	waitFor(t, func() bool {
		ss := reg.Snapshot()
		if len(ss) != len(backends) {
			return false
		}
		for _, s := range ss {
			if !s.Healthy {
				return false
			}
		}
		return true
	})
	return faultCluster{port: addr.(*net.TCPAddr).Port, replicas: proxies, metrics: m, manager: mgr}
}

func scrapeCounter(t *testing.T, m *metrics.Metrics, series string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, series+" ") {
			v, _ := strconv.ParseFloat(strings.Fields(line)[1], 64)
			return v
		}
	}
	return 0
}

func inUseTotal(m *backend.Manager) int {
	total := 0
	for _, s := range m.StatsByAddr() {
		total += s.InUse
	}
	return total
}

// TestProxy_FailsOverWhenReplicaDown proves the invariant: a read never reaches
// the client as an error while a healthy backend alternative exists. One replica
// is blackholed (its connections refused) but still reads healthy in the
// registry, so round-robin keeps selecting it; every such read must fail over to
// the other replica or the primary.
func TestProxy_FailsOverWhenReplicaDown(t *testing.T) {
	compose := requireCluster(t)
	fc := startFaultCluster(t, config.FenceRelaxed, router.NewRoundRobin())

	fc.replicas[1].Blackhole(true)

	before := scrapeCounter(t, fc.metrics, "pgpilot_read_failovers_total")
	for i := 0; i < 16; i++ {
		got, err := runPsql(compose, "host.docker.internal", fc.port, "SELECT 1")
		if err != nil {
			t.Fatalf("read %d dropped though a healthy backend existed: %v", i, err)
		}
		if strings.TrimSpace(got) != "1" {
			t.Fatalf("read %d = %q, want 1", i, got)
		}
	}
	if fo := scrapeCounter(t, fc.metrics, "pgpilot_read_failovers_total") - before; fo == 0 {
		t.Error("no failovers recorded; round-robin should have hit the downed replica and failed over")
	}
}

// TestProxy_RecoversFromSeveredConnections severs both replicas' live
// connections (as a backend dropping in-flight work would), then keeps reading.
// The pool health-checks and re-dials the reachable proxies, so no read is lost.
func TestProxy_RecoversFromSeveredConnections(t *testing.T) {
	compose := requireCluster(t)
	fc := startFaultCluster(t, config.FenceRelaxed, router.NewLeastInFlight())

	// Warm a pooled connection to each replica.
	for i := 0; i < 4; i++ {
		if _, err := runPsql(compose, "host.docker.internal", fc.port, "SELECT 1"); err != nil {
			t.Fatalf("warmup read: %v", err)
		}
	}
	for _, p := range fc.replicas {
		p.Sever()
	}
	for i := 0; i < 12; i++ {
		if _, err := runPsql(compose, "host.docker.internal", fc.port, "SELECT 1"); err != nil {
			t.Fatalf("read %d after sever failed: %v", i, err)
		}
	}
}

// TestProxy_StrictNeverStale_WithReplicaDown is the safety invariant under
// fault: strict fencing must never serve a stale read even when a replica is
// down and the others are frozen behind the fence.
func TestProxy_StrictNeverStale_WithReplicaDown(t *testing.T) {
	compose := requireCluster(t)
	resumeReplication(t, compose)
	execOn(t, compose, "primary", "CREATE TABLE IF NOT EXISTS fault_test (id int PRIMARY KEY, v text)")
	execOn(t, compose, "primary", "INSERT INTO fault_test VALUES (1,'old') ON CONFLICT (id) DO UPDATE SET v='old'")
	waitFor(t, func() bool {
		for _, r := range []string{"replica1", "replica2"} {
			if v, err := runPsqlOn(compose, r, "SELECT v FROM fault_test WHERE id=1"); err != nil || v != "old" {
				return false
			}
		}
		return true
	})
	pauseReplication(t, compose)
	defer resumeReplication(t, compose)

	fc := startFaultCluster(t, config.FenceStrict, router.NewRoundRobin())
	fc.replicas[0].Blackhole(true) // one replica down, the other frozen behind the fence

	got, err := runPsqlSession(compose, "host.docker.internal", fc.port,
		"UPDATE fault_test SET v='new' WHERE id=1",
		"SELECT v FROM fault_test WHERE id=1")
	if err != nil {
		t.Fatalf("write+read session: %v", err)
	}
	if lastLine(got) != "new" {
		t.Errorf("strict fencing served a STALE read under fault: got %q, want new", lastLine(got))
	}
}

// TestProxy_NoConnectionOrGoroutineLeak drives many sessions through faults and
// asserts nothing leaks: every pooled connection is returned (in-use drops to
// zero), and the goroutine count settles back near its post-startup baseline.
func TestProxy_NoConnectionOrGoroutineLeak(t *testing.T) {
	compose := requireCluster(t)
	fc := startFaultCluster(t, config.FenceRelaxed, router.NewLeastInFlight())

	// Baseline after the server and pollers are running.
	time.Sleep(200 * time.Millisecond)
	baseGoroutines := runtime.NumGoroutine()

	fc.replicas[1].Blackhole(true) // force failovers through the churn
	for i := 0; i < 24; i++ {
		if _, err := runPsql(compose, "host.docker.internal", fc.port, "SELECT 1"); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	fc.replicas[1].Blackhole(false)

	// Sessions have closed; the pool should hold nothing in use.
	var inUse int
	for i := 0; i < 20; i++ {
		if inUse = inUseTotal(fc.manager); inUse == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if inUse != 0 {
		t.Errorf("connection leak: %d pooled connections still in use after all sessions closed", inUse)
	}

	// Goroutines should settle near the baseline (allow slack for pool/poller).
	var goroutines int
	for i := 0; i < 20; i++ {
		runtime.GC()
		if goroutines = runtime.NumGoroutine(); goroutines <= baseGoroutines+10 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if goroutines > baseGoroutines+10 {
		t.Errorf("goroutine leak: %d goroutines, baseline %d", goroutines, baseGoroutines)
	}
}
