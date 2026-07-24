//go:build integration

package registry

import (
	"context"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/backend"
)

func clusterDialer() Dialer {
	return func(ctx context.Context, addr string) (Conn, error) {
		conn, err := backend.Dial(ctx, addr, "pgpilot", "pgpilot", "pgpilot")
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// TestRegistry_AgainstCluster polls the real primary and two replicas and checks
// the roles and lag metrics the registry derives. Requires `make up`.
func TestRegistry_AgainstCluster(t *testing.T) {
	reg := New(Config{Interval: 200 * time.Millisecond, Dialer: clusterDialer()})
	ctx, cancel := context.WithCancel(context.Background())
	reg.Start(ctx, []Backend{
		{Name: "primary", Addr: "127.0.0.1:55432"},
		{Name: "replica1", Addr: "127.0.0.1:55433"},
		{Name: "replica2", Addr: "127.0.0.1:55434"},
	})
	defer func() { cancel(); reg.Wait() }()

	waitFor(t, func() bool {
		for _, s := range reg.Snapshot() {
			if !s.Healthy {
				return false
			}
		}
		return len(reg.Snapshot()) == 3
	})

	for _, s := range reg.Snapshot() {
		t.Logf("%s: role=%s healthy=%v lag_bytes=%d lag_seconds=%.3f",
			s.Name, s.Role, s.Healthy, s.LagBytes, s.LagSeconds)
		switch s.Name {
		case "primary":
			if s.Role != RolePrimary {
				t.Errorf("primary role = %s, want primary", s.Role)
			}
		default:
			if s.Role != RoleReplica {
				t.Errorf("%s role = %s, want replica", s.Name, s.Role)
			}
			if s.LagBytes < 0 {
				t.Errorf("%s lag_bytes = %d, want >= 0", s.Name, s.LagBytes)
			}
		}
	}
}

// TestRegistry_DeadBackendTripsBreaker points the poller at an address nothing
// is listening on; the circuit breaker must trip.
func TestRegistry_DeadBackendTripsBreaker(t *testing.T) {
	reg := New(Config{
		Interval:         50 * time.Millisecond,
		FailureThreshold: 2,
		BaseBackoff:      10 * time.Millisecond,
		MaxBackoff:       100 * time.Millisecond,
		Dialer:           clusterDialer(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	reg.Start(ctx, []Backend{{Name: "dead", Addr: "127.0.0.1:1"}})
	defer func() { cancel(); reg.Wait() }()

	waitFor(t, func() bool {
		s := reg.Snapshot()
		return len(s) == 1 && !s[0].Healthy && s[0].LastErr != ""
	})
}
