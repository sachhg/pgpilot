package registry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu      sync.Mutex
	row     []string
	err     error
	dialErr error
	closes  int
}

func (b *fakeBackend) set(row []string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.row, b.err = row, err
}

type fakeConn struct{ b *fakeBackend }

func (c *fakeConn) QueryRow(_ context.Context, _ string) ([]string, error) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	if c.b.err != nil {
		return nil, c.b.err
	}
	return c.b.row, nil
}

func (c *fakeConn) Close() error {
	c.b.mu.Lock()
	c.b.closes++
	c.b.mu.Unlock()
	return nil
}

func testRegistry(t *testing.T, backends map[string]*fakeBackend, cfg Config) *Registry {
	t.Helper()
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.Dialer = func(_ context.Context, addr string) (Conn, error) {
		b := backends[addr]
		b.mu.Lock()
		derr := b.dialErr
		b.mu.Unlock()
		if derr != nil {
			return nil, derr
		}
		return &fakeConn{b}, nil
	}
	return New(cfg)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func statusByName(r *Registry, name string) Status {
	for _, s := range r.Snapshot() {
		if s.Name == name {
			return s
		}
	}
	return Status{}
}

func TestRegistry_DetectsRolesAndLag(t *testing.T) {
	primary := &fakeBackend{row: []string{"f", "0/5000000", "0"}}
	replica := &fakeBackend{row: []string{"t", "0/4000000", "1.5"}}
	backends := map[string]*fakeBackend{"p": primary, "r": replica}

	reg := testRegistry(t, backends, Config{Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	reg.Start(ctx, []Backend{{Name: "primary", Addr: "p"}, {Name: "replica", Addr: "r"}})
	defer func() { cancel(); reg.Wait() }()

	waitFor(t, func() bool {
		return statusByName(reg, "primary").Healthy && statusByName(reg, "replica").Healthy
	})

	p := statusByName(reg, "primary")
	if p.Role != RolePrimary {
		t.Errorf("primary role = %v, want primary", p.Role)
	}
	rep := statusByName(reg, "replica")
	if rep.Role != RoleReplica {
		t.Errorf("replica role = %v, want replica", rep.Role)
	}
	if rep.LagSeconds != 1.5 {
		t.Errorf("replica lag seconds = %v, want 1.5", rep.LagSeconds)
	}
	if want := int64(0x5000000 - 0x4000000); rep.LagBytes != want {
		t.Errorf("replica lag bytes = %d, want %d", rep.LagBytes, want)
	}
}

func TestRegistry_CircuitBreakerTripsAndRecovers(t *testing.T) {
	be := &fakeBackend{err: errors.New("connection refused")}
	backends := map[string]*fakeBackend{"b": be}

	reg := testRegistry(t, backends, Config{
		Interval:         5 * time.Millisecond,
		FailureThreshold: 3,
		BaseBackoff:      2 * time.Millisecond,
		MaxBackoff:       20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	reg.Start(ctx, []Backend{{Name: "b", Addr: "b"}})
	defer func() { cancel(); reg.Wait() }()

	// The breaker trips after the failure threshold.
	waitFor(t, func() bool {
		s := statusByName(reg, "b")
		return !s.Healthy && s.LastErr != ""
	})

	// Once the backend recovers, the breaker closes.
	be.set([]string{"f", "0/1000000", "0"}, nil)
	waitFor(t, func() bool { return statusByName(reg, "b").Healthy })
	if s := statusByName(reg, "b"); s.LastErr != "" {
		t.Errorf("lastErr = %q after recovery, want empty", s.LastErr)
	}
}

func TestRegistry_Reconfigure(t *testing.T) {
	primary := &fakeBackend{row: []string{"f", "0/1000000", "0"}}
	replica := &fakeBackend{row: []string{"t", "0/0FF0000", "0.2"}}
	backends := map[string]*fakeBackend{"p": primary, "r": replica}

	reg := testRegistry(t, backends, Config{Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	reg.Start(ctx, []Backend{{Name: "primary", Addr: "p"}})
	defer func() { cancel(); reg.Wait() }()

	waitFor(t, func() bool { return statusByName(reg, "primary").Healthy })
	if len(reg.Snapshot()) != 1 {
		t.Fatalf("snapshot has %d backends, want 1", len(reg.Snapshot()))
	}

	reg.Reconfigure([]Backend{{Name: "primary", Addr: "p"}, {Name: "replica", Addr: "r"}})
	waitFor(t, func() bool {
		return len(reg.Snapshot()) == 2 && statusByName(reg, "replica").Healthy
	})
}

func TestParseLSN(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"0/0", 0, true},
		{"0/3000060", 0x3000060, true},
		{"1/0", 0x100000000, true},
		{"FF/FFFFFFFF", 0xFFFFFFFFFF, true},
		{"nope", 0, false},
		{"0/", 0, false},
	}
	for _, c := range cases {
		got, err := parseLSN(c.in)
		if c.ok {
			if err != nil || got != c.want {
				t.Errorf("parseLSN(%q) = %#x, %v; want %#x", c.in, got, err, c.want)
			}
		} else if err == nil {
			t.Errorf("parseLSN(%q) = %#x, nil; want error", c.in, got)
		}
	}
}
