package backend

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/pool"
)

// newIdlePool builds a pool whose dialer is never invoked (Stats does not dial),
// so tests can exercise aggregation without a database.
func newIdlePool(t *testing.T) *pool.Pool {
	t.Helper()
	p, err := pool.New(pool.Config{
		MaxSize:        4,
		AcquireTimeout: time.Second,
		IdleTimeout:    time.Minute,
		New: func(context.Context) (pool.Conn, error) {
			t.Fatal("dialer should not be called")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestStatsByAddr_Empty(t *testing.T) {
	m := NewManager("p", nil, PoolConfig{MaxSize: 1})
	if got := m.StatsByAddr(); len(got) != 0 {
		t.Errorf("empty manager StatsByAddr = %v, want none", got)
	}
}

func TestStatsByAddr_AggregatesPerAddress(t *testing.T) {
	m := NewManager("primary:5432", nil, PoolConfig{MaxSize: 1})
	// Two (user, database) pools share address A; one pool is on address B.
	// Aggregation must collapse the two A pools into a single series so the
	// metrics collector never emits a duplicate series for one address.
	m.pools[poolKey{"u1", "db", "A:5432"}] = newIdlePool(t)
	m.pools[poolKey{"u2", "db", "A:5432"}] = newIdlePool(t)
	m.pools[poolKey{"u1", "db", "B:5432"}] = newIdlePool(t)

	got := m.StatsByAddr()
	addrs := make([]string, len(got))
	for i, s := range got {
		addrs[i] = s.Addr
	}
	sort.Strings(addrs)
	want := []string{"A:5432", "B:5432"}
	if len(addrs) != len(want) || addrs[0] != want[0] || addrs[1] != want[1] {
		t.Errorf("StatsByAddr addresses = %v, want %v (one series per address)", addrs, want)
	}
}
