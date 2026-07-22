package backend

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sachhg/pgpilot/internal/pool"
)

// PoolConfig sizes each per-(user, database) pool.
type PoolConfig struct {
	MaxSize        int
	MaxWaiters     int
	AcquireTimeout time.Duration
	IdleTimeout    time.Duration
}

// Manager owns one connection pool per (user, database), each holding
// SCRAM-authenticated connections to the primary. It is safe for concurrent use.
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
}

// NewManager creates a Manager that routes to primary and authenticates with the
// given user passwords (keyed by user name).
func NewManager(primary string, users map[string]string, cfg PoolConfig) *Manager {
	return &Manager{
		primary: primary,
		users:   users,
		poolCfg: cfg,
		pools:   make(map[poolKey]*pool.Pool),
	}
}

// Acquire returns a pooled, authenticated backend connection for (user,
// database). The user must be known to the manager.
func (m *Manager) Acquire(ctx context.Context, user, database string) (*Conn, error) {
	p, err := m.poolFor(user, database)
	if err != nil {
		return nil, err
	}
	c, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return c.(*Conn), nil
}

// Release returns a connection to its (user, database) pool for reuse.
func (m *Manager) Release(user, database string, c *Conn) {
	if p := m.lookup(user, database); p != nil {
		p.Release(c)
		return
	}
	_ = c.Close()
}

// Discard closes a connection and frees its slot instead of reusing it.
func (m *Manager) Discard(user, database string, c *Conn) {
	if p := m.lookup(user, database); p != nil {
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

func (m *Manager) poolFor(user, database string) (*pool.Pool, error) {
	password, ok := m.users[user]
	if !ok {
		return nil, fmt.Errorf("backend: unknown user %q", user)
	}
	key := poolKey{user: user, database: database}

	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[key]; ok {
		return p, nil
	}
	primary := m.primary
	p, err := pool.New(pool.Config{
		MaxSize:        m.poolCfg.MaxSize,
		MaxWaiters:     m.poolCfg.MaxWaiters,
		AcquireTimeout: m.poolCfg.AcquireTimeout,
		IdleTimeout:    m.poolCfg.IdleTimeout,
		New: func(ctx context.Context) (pool.Conn, error) {
			return Dial(ctx, primary, user, password, database)
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

func (m *Manager) lookup(user, database string) *pool.Pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pools[poolKey{user: user, database: database}]
}
