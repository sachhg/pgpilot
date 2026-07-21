package pool_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/pool"
)

type fakeConn struct {
	id      int
	closed  atomic.Bool
	pingErr error
}

func (c *fakeConn) Close() error {
	c.closed.Store(true)
	return nil
}

type factory struct {
	mu      sync.Mutex
	created int
	newErr  error
}

func (f *factory) New(_ context.Context) (pool.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.newErr != nil {
		return nil, f.newErr
	}
	f.created++
	return &fakeConn{id: f.created}, nil
}

func (f *factory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

func pingByConn(_ context.Context, c pool.Conn) error {
	return c.(*fakeConn).pingErr
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

func TestNew_InvalidConfig(t *testing.T) {
	if _, err := pool.New(pool.Config{MaxSize: 0, New: (&factory{}).New}); err == nil {
		t.Error("expected an error for MaxSize <= 0")
	}
	if _, err := pool.New(pool.Config{MaxSize: 1}); err == nil {
		t.Error("expected an error for a nil New")
	}
}

func TestAcquireRelease_ReusesConnection(t *testing.T) {
	f := &factory{}
	p, err := pool.New(pool.Config{MaxSize: 2, New: f.New})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	c1, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	p.Release(c1)
	c2, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire again: %v", err)
	}
	if c1 != c2 {
		t.Error("expected the released connection to be reused")
	}
	if f.count() != 1 {
		t.Errorf("created %d connections, want 1", f.count())
	}
}

func TestMaxSize_BackpressureTimeout(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 2, AcquireTimeout: 50 * time.Millisecond, New: f.New})
	defer p.Close()

	a, _ := p.Acquire(context.Background())
	_, _ = p.Acquire(context.Background())

	if _, err := p.Acquire(context.Background()); !errors.Is(err, pool.ErrTimeout) {
		t.Fatalf("third acquire err = %v, want ErrTimeout", err)
	}
	if s := p.Stats(); s.Open != 2 || s.InUse != 2 {
		t.Fatalf("stats = %+v, want Open=2 InUse=2", s)
	}

	p.Release(a)
	if _, err := p.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if f.count() != 2 {
		t.Errorf("created %d, want 2 (never exceed MaxSize)", f.count())
	}
}

func TestWaiter_GetsReleasedConnection(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 1, AcquireTimeout: 2 * time.Second, New: f.New})
	defer p.Close()

	c1, _ := p.Acquire(context.Background())
	got := make(chan pool.Conn, 1)
	go func() {
		c, err := p.Acquire(context.Background())
		if err != nil {
			got <- nil
			return
		}
		got <- c
	}()
	waitFor(t, func() bool { return p.Stats().Waiters == 1 })

	p.Release(c1)
	select {
	case c := <-got:
		if c != c1 {
			t.Error("waiter should receive the released connection")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter was not served")
	}
}

func TestMaxWaiters_RefusesImmediately(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 1, MaxWaiters: 1, AcquireTimeout: time.Second, New: f.New})
	defer p.Close()

	_, _ = p.Acquire(context.Background())
	go func() { _, _ = p.Acquire(context.Background()) }() // fills the single waiter slot
	waitFor(t, func() bool { return p.Stats().Waiters == 1 })

	if _, err := p.Acquire(context.Background()); !errors.Is(err, pool.ErrExhausted) {
		t.Fatalf("err = %v, want ErrExhausted", err)
	}
}

func TestPing_DiscardsUnhealthyConnection(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 1, New: f.New, Ping: pingByConn})
	defer p.Close()

	c1, _ := p.Acquire(context.Background())
	c1.(*fakeConn).pingErr = errors.New("dead")
	p.Release(c1)

	c2, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if c2 == c1 {
		t.Error("unhealthy connection should not be reused")
	}
	if !c1.(*fakeConn).closed.Load() {
		t.Error("unhealthy connection should be closed")
	}
	if f.count() != 2 {
		t.Errorf("created %d, want 2 (replacement opened)", f.count())
	}
}

func TestIdleTimeout_ReapsIdleConnections(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 2, IdleTimeout: 40 * time.Millisecond, New: f.New})
	defer p.Close()

	c1, _ := p.Acquire(context.Background())
	c2, _ := p.Acquire(context.Background())
	p.Release(c1)
	p.Release(c2)

	waitFor(t, func() bool { return p.Stats().Open == 0 })
	if !c1.(*fakeConn).closed.Load() || !c2.(*fakeConn).closed.Load() {
		t.Error("idle connections should be closed by the reaper")
	}
}

func TestClose_ClosesIdleAndRejects(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 2, New: f.New})

	c1, _ := p.Acquire(context.Background())
	p.Release(c1)
	_ = p.Close()

	if !c1.(*fakeConn).closed.Load() {
		t.Error("idle connection should be closed on Close")
	}
	if _, err := p.Acquire(context.Background()); !errors.Is(err, pool.ErrClosed) {
		t.Fatalf("acquire after close = %v, want ErrClosed", err)
	}
}

func TestClose_WakesWaiters(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 1, AcquireTimeout: 5 * time.Second, New: f.New})

	_, _ = p.Acquire(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := p.Acquire(context.Background())
		errc <- err
	}()
	waitFor(t, func() bool { return p.Stats().Waiters == 1 })

	_ = p.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, pool.ErrClosed) {
			t.Fatalf("waiter err = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the waiter")
	}
}

func TestAcquire_ContextCancel(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 1, New: f.New})
	defer p.Close()

	_, _ = p.Acquire(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := p.Acquire(ctx)
		errc <- err
	}()
	waitFor(t, func() bool { return p.Stats().Waiters == 1 })

	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation not honored")
	}
}

func TestNew_DialErrorDoesNotLeakSlot(t *testing.T) {
	f := &factory{newErr: errors.New("dial failed")}
	p, _ := pool.New(pool.Config{MaxSize: 2, New: f.New})
	defer p.Close()

	if _, err := p.Acquire(context.Background()); err == nil || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("err = %v, want dial failed", err)
	}
	if s := p.Stats(); s.Open != 0 {
		t.Errorf("open = %d after a failed dial, want 0", s.Open)
	}
}

func TestDiscard_FreesSlot(t *testing.T) {
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: 1, New: f.New})
	defer p.Close()

	c1, _ := p.Acquire(context.Background())
	p.Discard(c1)
	if !c1.(*fakeConn).closed.Load() {
		t.Error("discarded connection should be closed")
	}
	if s := p.Stats(); s.Open != 0 {
		t.Errorf("open = %d after discard, want 0", s.Open)
	}
	// The freed slot allows a new connection.
	if _, err := p.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after discard: %v", err)
	}
	if f.count() != 2 {
		t.Errorf("created %d, want 2", f.count())
	}
}

func TestConcurrent_NeverExceedsMaxSize(t *testing.T) {
	const maxSize = 4
	f := &factory{}
	p, _ := pool.New(pool.Config{MaxSize: maxSize, AcquireTimeout: 5 * time.Second, New: f.New})
	defer p.Close()

	var inUse atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c, err := p.Acquire(context.Background())
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				if n := inUse.Add(1); n > maxSize {
					t.Errorf("in-use %d exceeds MaxSize %d", n, maxSize)
				}
				inUse.Add(-1)
				p.Release(c)
			}
		}()
	}
	wg.Wait()

	if s := p.Stats(); s.Open > maxSize {
		t.Errorf("open %d exceeds MaxSize %d", s.Open, maxSize)
	}
	if f.count() > maxSize {
		t.Errorf("created %d exceeds MaxSize %d", f.count(), maxSize)
	}
}
