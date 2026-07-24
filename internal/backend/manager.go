package backend

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sachhg/pgpilot/internal/pool"
)

// PoolConfig sizes each per-(user, database, address) pool.
type PoolConfig struct {
	MaxSize        int
	MaxWaiters     int
	AcquireTimeout time.Duration
	IdleTimeout    time.Duration
}

// Manager owns one connection pool per (user, database, backend address), each
// holding SCRAM-authenticated connections. It is safe for concurrent use.
type Manager struct {
	primary string
	users   map[string]string // user name -> password
	poolCfg PoolConfig

	mu    sync.Mutex
	pools map[poolKey]*pool.Pool
}

type poolKey struct {
	user     string
	database string
	addr     string
}

// NewManager creates a Manager whose primary-only methods route to primary and
// which authenticates with the given user passwords (keyed by user name).
func NewManager(primary string, users map[string]string, cfg PoolConfig) *Manager {
	return &Manager{
		primary: primary,
		users:   users,
		poolCfg: cfg,
		pools:   make(map[poolKey]*pool.Pool),
	}
}

// Acquire returns a pooled connection to the primary for (user, database).
func (m *Manager) Acquire(ctx context.Context, user, database string) (*Conn, error) {
	return m.AcquireAt(ctx, user, database, m.primary)
}

// Release returns a primary connection to its pool.
func (m *Manager) Release(user, database string, c *Conn) {
	m.ReleaseAt(user, database, m.primary, c)
}

// Discard closes a primary connection instead of reusing it.
func (m *Manager) Discard(user, database string, c *Conn) {
	m.DiscardAt(user, database, m.primary, c)
}

// AcquireAt returns a pooled connection to a specific backend address, used by
// the router to reach a chosen replica.
func (m *Manager) AcquireAt(ctx context.Context, user, database, addr string) (*Conn, error) {
	p, err := m.poolFor(user, database, addr)
	if err != nil {
		return nil, err
	}
	c, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return c.(*Conn), nil
}

// ReleaseAt returns a connection to the pool for a specific backend address.
func (m *Manager) ReleaseAt(user, database, addr string, c *Conn) {
	if p := m.lookup(user, database, addr); p != nil {
		p.Release(c)
		return
	}
	_ = c.Close()
}

// DiscardAt closes a connection to a specific backend address.
func (m *Manager) DiscardAt(user, database, addr string, c *Conn) {
	if p := m.lookup(user, database, addr); p != nil {
		p.Discard(c)
		return
	}
	_ = c.Close()
}

// Close closes every pool.
func (m *Manager) Close() {
	m.mu.Lock()
	pools := make([]*pool.Pool, 0, len(m.pools))
	for _, p := range m.pools {
		pools = append(pools, p)
	}
	m.pools = make(map[poolKey]*pool.Pool)
	m.mu.Unlock()
	for _, p := range pools {
		_ = p.Close()
	}
}

func (m *Manager) poolFor(user, database, addr string) (*pool.Pool, error) {
	password, ok := m.users[user]
	if !ok {
		return nil, fmt.Errorf("backend: unknown user %q", user)
	}
	key := poolKey{user: user, database: database, addr: addr}

	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[key]; ok {
		return p, nil
	}
	p, err := pool.New(pool.Config{
		MaxSize:        m.poolCfg.MaxSize,
		MaxWaiters:     m.poolCfg.MaxWaiters,
		AcquireTimeout: m.poolCfg.AcquireTimeout,
		IdleTimeout:    m.poolCfg.IdleTimeout,
		New: func(ctx context.Context) (pool.Conn, error) {
			return Dial(ctx, addr, user, password, database)
		},
		Ping: func(ctx context.Context, c pool.Conn) error {
			return c.(*Conn).Ping(ctx)
		},
	})
	if err != nil {
		return nil, err
	}
	m.pools[key] = p
	return p, nil
}

func (m *Manager) lookup(user, database, addr string) *pool.Pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pools[poolKey{user: user, database: database, addr: addr}]
}
