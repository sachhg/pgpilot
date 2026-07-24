//go:build integration

package integration

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/backend"
	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/proxy"
	"github.com/sachhg/pgpilot/internal/registry"
	"github.com/sachhg/pgpilot/internal/router"
)

var replicaHostPorts = []string{"127.0.0.1:55433", "127.0.0.1:55434"}

// startRoutingProxy launches an in-process routing proxy (primary + 2 replicas)
// in the given fencing mode and returns its port once the registry has polled
// every backend healthy. A nil policy leaves the proxy to resolve its default.
func startRoutingProxy(t *testing.T, mode string, policy router.Policy) int {
	t.Helper()
	cfg := &config.Config{
		Primary:  primaryHostPort,
		Replicas: replicaHostPorts,
		Users:    []config.User{{Name: pgUser, Password: pgPass}},
		Fencing:  config.Fencing{Mode: mode, BoundedMs: 100},
	}
	mgr := backend.NewManager(cfg.Primary, map[string]string{pgUser: pgPass},
		backend.PoolConfig{MaxSize: 5, AcquireTimeout: 5 * time.Second, IdleTimeout: time.Minute})
	t.Cleanup(mgr.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New(registry.Config{
		Interval: 150 * time.Millisecond,
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
	reg.Start(ctx, []registry.Backend{
		{Name: "primary", Addr: cfg.Primary},
		{Name: "replica1", Addr: replicaHostPorts[0]},
		{Name: "replica2", Addr: replicaHostPorts[1]},
	})

	srv := proxy.New(proxy.Config{ListenAddr: "0.0.0.0:0", Users: cfg, Manager: mgr, Registry: reg, Policy: policy, Logger: log})
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
		if len(ss) != 3 {
			return false
		}
		for _, s := range ss {
			if !s.Healthy {
				return false
			}
		}
		return true
	})
	return addr.(*net.TCPAddr).Port
}

// execOn runs a statement on a specific cluster container's local Postgres.
func execOn(t *testing.T, compose, service, sql string) string {
	t.Helper()
	out, err := runPsqlOn(compose, service, sql)
	if err != nil {
		t.Fatalf("exec on %s (%q): %v", service, sql, err)
	}
	return out
}

func pauseReplication(t *testing.T, compose string) {
	for _, r := range []string{"replica1", "replica2"} {
		execOn(t, compose, r, "SELECT pg_wal_replay_pause()")
	}
}

func resumeReplication(t *testing.T, compose string) {
	for _, r := range []string{"replica1", "replica2"} {
		// Ignore errors: resume is a no-op if replay is not paused.
		_, _ = runPsqlOn(compose, r, "SELECT pg_wal_replay_resume()")
	}
}

func TestProxy_RoutesReadsToReplicas(t *testing.T) {
	compose := requireCluster(t)
	resumeReplication(t, compose)
	port := startRoutingProxy(t, config.FenceStrict, nil)

	// A read with no write fence is served by a replica.
	if got, err := runPsql(compose, "host.docker.internal", port, "SELECT pg_is_in_recovery()"); err != nil {
		t.Fatalf("read: %v", err)
	} else if got != "t" {
		t.Errorf("read pg_is_in_recovery() = %q, want t (served by a replica)", got)
	}

	// A write is served by the primary; a replica would reject it as read-only.
	if _, err := runPsql(compose, "host.docker.internal", port,
		"CREATE TABLE IF NOT EXISTS route_probe (id int)"); err != nil {
		t.Errorf("write through the proxy failed (should route to the primary): %v", err)
	}
}

// TestProxy_LSNFencing_StrictNeverStale is the phase's acceptance test: with
// replication paused, a write followed by a read in the same session must never
// observe a stale value under strict fencing — while a relaxed read is served
// the stale value from a frozen replica, showing why fencing is needed.
func TestProxy_LSNFencing_StrictNeverStale(t *testing.T) {
	compose := requireCluster(t)
	resumeReplication(t, compose)

	execOn(t, compose, "primary", "CREATE TABLE IF NOT EXISTS fence_test (id int PRIMARY KEY, v text)")
	execOn(t, compose, "primary", "INSERT INTO fence_test VALUES (1, 'old') ON CONFLICT (id) DO UPDATE SET v = 'old'")

	// Wait for both replicas to have replayed the seed value, then freeze them.
	waitFor(t, func() bool {
		for _, r := range []string{"replica1", "replica2"} {
			v, err := runPsqlOn(compose, r, "SELECT v FROM fence_test WHERE id = 1")
			if err != nil || v != "old" {
				return false
			}
		}
		return true
	})
	pauseReplication(t, compose)
	defer resumeReplication(t, compose)

	// Strict: write 'new' (fence advances on the primary) then read — the frozen
	// replicas are behind the fence, so the read falls back to the primary.
	strict := startRoutingProxy(t, config.FenceStrict, nil)
	got, err := runPsqlSession(compose, "host.docker.internal", strict,
		"UPDATE fence_test SET v = 'new' WHERE id = 1",
		"SELECT v FROM fence_test WHERE id = 1")
	if err != nil {
		t.Fatalf("strict session: %v", err)
	}
	if lastLine(got) != "new" {
		t.Errorf("strict fencing served a STALE read: got %q, want new", lastLine(got))
	}

	// Relaxed: the same read is served by a frozen replica and is stale.
	relaxed := startRoutingProxy(t, config.FenceRelaxed, nil)
	rgot, err := runPsql(compose, "host.docker.internal", relaxed, "SELECT v FROM fence_test WHERE id = 1")
	if err != nil {
		t.Fatalf("relaxed session: %v", err)
	}
	if lastLine(rgot) != "old" {
		t.Logf("relaxed read = %q (expected stale 'old' from a frozen replica)", lastLine(rgot))
	} else {
		t.Logf("relaxed read served the stale value 'old' from a frozen replica — exactly what strict fencing prevents")
	}
}
