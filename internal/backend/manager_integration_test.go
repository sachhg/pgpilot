//go:build integration

package backend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/pool"
)

func testManager(maxSize int) *Manager {
	return NewManager(primaryAddr,
		map[string]string{"pgpilot": "pgpilot"},
		PoolConfig{MaxSize: maxSize, AcquireTimeout: 3 * time.Second, IdleTimeout: time.Minute},
	)
}

func TestManager_AcquireResetReleaseReuses(t *testing.T) {
	m := testManager(2)
	defer m.Close()
	ctx := context.Background()

	c1, err := m.Acquire(ctx, "pgpilot", "pgpilot")
	if err != nil {
		t.Fatalf("acquire: %v (is `make up` running?)", err)
	}
	if err := c1.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	m.Release("pgpilot", "pgpilot", c1)

	c2, err := m.Acquire(ctx, "pgpilot", "pgpilot")
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if c1 != c2 {
		t.Error("expected the released backend connection to be reused")
	}
	m.Release("pgpilot", "pgpilot", c2)
}

func TestManager_UnknownUser(t *testing.T) {
	m := testManager(1)
	defer m.Close()
	if _, err := m.Acquire(context.Background(), "ghost", "pgpilot"); err == nil {
		t.Fatal("acquire for an unknown user should fail")
	}
}

func TestManager_RespectsMaxSize(t *testing.T) {
	m := testManager(2)
	defer m.Close()
	ctx := context.Background()

	a, err := m.Acquire(ctx, "pgpilot", "pgpilot")
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	b, err := m.Acquire(ctx, "pgpilot", "pgpilot")
	if err != nil {
		t.Fatalf("acquire b: %v", err)
	}
	if _, err := m.Acquire(ctx, "pgpilot", "pgpilot"); !errors.Is(err, pool.ErrTimeout) {
		t.Fatalf("third acquire err = %v, want ErrTimeout", err)
	}
	m.Release("pgpilot", "pgpilot", a)
	m.Release("pgpilot", "pgpilot", b)
}
