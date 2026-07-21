// Package pool provides a bounded, health-checked pool of backend connections.
//
// A Pool caps the number of open connections, hands idle connections back out
// after an optional health check, reaps connections that sit idle too long, and
// applies backpressure — a bounded wait with a timeout, or an immediate refusal
// once a waiter limit is reached — instead of queueing acquirers without limit.
//
// Each connection returned by Acquire must be returned exactly once with either
// Release (reuse it) or Discard (throw it away, e.g. after a protocol error).
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Conn is a backend connection managed by a Pool. Close must be safe to call
// from the pool's own goroutines.
type Conn interface {
	Close() error
}

// Errors returned by Acquire.
var (
	// ErrClosed is returned by Acquire after the pool has been closed.
	ErrClosed = errors.New("pool: closed")
	// ErrTimeout is returned when Acquire waits longer than AcquireTimeout.
	ErrTimeout = errors.New("pool: acquire timed out")
	// ErrExhausted is returned when the pool is at capacity and the waiter
	// limit (MaxWaiters) has been reached.
	ErrExhausted = errors.New("pool: exhausted")
)

// Config configures a Pool.
type Config struct {
	// MaxSize is the maximum number of open connections. Must be > 0.
	MaxSize int
	// MaxWaiters caps the goroutines that may wait for a connection once MaxSize
	// is reached; further Acquire calls fail immediately with ErrExhausted. Zero
	// means unlimited waiters (each still bounded by AcquireTimeout).
	MaxWaiters int
	// AcquireTimeout bounds how long Acquire waits for a connection. Zero waits
	// until the caller's context is done.
	AcquireTimeout time.Duration
	// IdleTimeout closes connections that sit idle in the pool longer than this.
	// Zero disables idle reaping.
	IdleTimeout time.Duration
	// New opens a fresh backend connection. Required.
	New func(ctx context.Context) (Conn, error)
	// Ping checks that a reused connection is still usable before Acquire hands
	// it out; a non-nil error discards the connection and a replacement is used.
	// Nil skips the check.
	Ping func(ctx context.Context, c Conn) error
}

// Pool is a bounded pool of backend connections. The zero value is not usable;
// construct one with New.
type Pool struct {
	cfg Config

	mu      sync.Mutex
	idle    []idleConn
	open    int         // total open: idle + handed out
	waiters []chan Conn // FIFO queue of goroutines waiting for a connection
	closed  bool

	stopReaper chan struct{}
	reaperDone chan struct{}
}

type idleConn struct {
	conn      Conn
	idleSince time.Time
}

// New constructs a Pool from cfg.
func New(cfg Config) (*Pool, error) {
	if cfg.MaxSize <= 0 {
		return nil, fmt.Errorf("pool: MaxSize must be > 0, got %d", cfg.MaxSize)
	}
	if cfg.New == nil {
		return nil, errors.New("pool: New is required")
	}
	p := &Pool{cfg: cfg}
	if cfg.IdleTimeout > 0 {
		p.stopReaper = make(chan struct{})
		p.reaperDone = make(chan struct{})
		go p.reap()
	}
	return p, nil
}

// Acquire returns a healthy connection, creating one if the pool is below
// MaxSize, or waiting for one to be released otherwise. It respects
// AcquireTimeout and ctx cancellation, and never opens more than MaxSize
// connections.
func (p *Pool) Acquire(ctx context.Context) (Conn, error) {
	var deadline time.Time
	if p.cfg.AcquireTimeout > 0 {
		deadline = time.Now().Add(p.cfg.AcquireTimeout)
	}
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrClosed
		}

		// 1. Reuse an idle connection if one is available.
		if n := len(p.idle); n > 0 {
			ic := p.idle[n-1]
			p.idle = p.idle[:n-1]
			if p.cfg.IdleTimeout > 0 && time.Since(ic.idleSince) >= p.cfg.IdleTimeout {
				p.open--
				p.mu.Unlock()
				_ = ic.conn.Close()
				continue
			}
			p.mu.Unlock()
			if c, ok := p.checkReused(ctx, ic.conn); ok {
				return c, nil
			}
			continue
		}

		// 2. Open a new connection if under capacity.
		if p.open < p.cfg.MaxSize {
			p.open++
			p.mu.Unlock()
			c, err := p.cfg.New(ctx)
			if err != nil {
				p.mu.Lock()
				p.open--
				p.mu.Unlock()
				return nil, err
			}
			return c, nil
		}

		// 3. At capacity: wait for a released connection (backpressure).
		if p.cfg.MaxWaiters > 0 && len(p.waiters) >= p.cfg.MaxWaiters {
			p.mu.Unlock()
			return nil, ErrExhausted
		}
		w := make(chan Conn, 1)
		p.waiters = append(p.waiters, w)
		p.mu.Unlock()

		c, err := p.waitFor(ctx, w, deadline)
		if err != nil {
			return nil, err
		}
		if hc, ok := p.checkReused(ctx, c); ok {
			return hc, nil
		}
		// The released connection failed its health check and was discarded;
		// loop to open a fresh one in its place.
	}
}

// checkReused health-checks a reused connection. On failure it discards the
// connection (closes it and frees its slot) and returns ok=false so the caller
// tries again.
func (p *Pool) checkReused(ctx context.Context, c Conn) (Conn, bool) {
	if p.cfg.Ping == nil {
		return c, true
	}
	if err := p.cfg.Ping(ctx, c); err != nil {
		p.mu.Lock()
		p.open--
		p.mu.Unlock()
		_ = c.Close()
		return nil, false
	}
	return c, true
}

func (p *Pool) waitFor(ctx context.Context, w chan Conn, deadline time.Time) (Conn, error) {
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case c := <-w:
		if c == nil {
			return nil, ErrClosed
		}
		return c, nil
	case <-timeout:
		return nil, p.abandonWaiter(w, ErrTimeout)
	case <-ctx.Done():
		return nil, p.abandonWaiter(w, ctx.Err())
	}
}

// abandonWaiter removes w from the waiter queue and returns err. If a releaser
// already delivered a connection to w, that connection is returned to the pool
// instead of being lost.
func (p *Pool) abandonWaiter(w chan Conn, err error) error {
	p.mu.Lock()
	for i, x := range p.waiters {
		if x == w {
			p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
			p.mu.Unlock()
			return err
		}
	}
	p.mu.Unlock()
	if c := <-w; c != nil {
		p.Release(c)
	}
	return err
}

// Release returns a connection to the pool for reuse.
func (p *Pool) Release(c Conn) {
	if c == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.open--
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	if len(p.waiters) > 0 {
		w := p.waiters[0]
		p.waiters = p.waiters[1:]
		w <- c
		p.mu.Unlock()
		return
	}
	p.idle = append(p.idle, idleConn{conn: c, idleSince: time.Now()})
	p.mu.Unlock()
}

// Discard closes a connection and frees its slot instead of returning it to the
// pool. Use it for a connection left in an unknown state, e.g. after an error.
func (p *Pool) Discard(c Conn) {
	if c == nil {
		return
	}
	p.mu.Lock()
	p.open--
	p.mu.Unlock()
	_ = c.Close()
}

// Close drains the pool: it closes every idle connection, wakes waiters with
// ErrClosed, and rejects further Acquire calls. Connections still handed out are
// closed when they are Released or Discarded.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	waiters := p.waiters
	p.waiters = nil
	p.mu.Unlock()

	if p.stopReaper != nil {
		close(p.stopReaper)
		<-p.reaperDone
	}
	for _, w := range waiters {
		w <- nil // wakes the waiter, which returns ErrClosed
	}
	for _, ic := range idle {
		_ = ic.conn.Close()
	}
	return nil
}

// Stats is a snapshot of a pool's connection counts.
type Stats struct {
	Open    int // total open connections (idle + in use)
	Idle    int // connections sitting idle in the pool
	InUse   int // connections currently handed out
	Waiters int // goroutines waiting for a connection
}

// Stats returns a snapshot of the pool's current counts.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{
		Open:    p.open,
		Idle:    len(p.idle),
		InUse:   p.open - len(p.idle),
		Waiters: len(p.waiters),
	}
}

func (p *Pool) reap() {
	defer close(p.reaperDone)
	interval := p.cfg.IdleTimeout
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopReaper:
			return
		case <-ticker.C:
			p.reapOnce()
		}
	}
}

func (p *Pool) reapOnce() {
	now := time.Now()
	var toClose []Conn
	p.mu.Lock()
	kept := make([]idleConn, 0, len(p.idle))
	for _, ic := range p.idle {
		if now.Sub(ic.idleSince) >= p.cfg.IdleTimeout {
			p.open--
			toClose = append(toClose, ic.conn)
		} else {
			kept = append(kept, ic)
		}
	}
	p.idle = kept
	p.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
}
