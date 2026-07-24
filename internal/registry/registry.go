// Package registry tracks the health and replication lag of each backend. A
// background poller queries every backend for its recovery state, replayed WAL
// position, and replay timestamp, derives lag in bytes and seconds, and trips a
// per-backend circuit breaker (marking the backend unhealthy and backing off
// exponentially) when polling fails. The backend set and poll cadence can be
// reconfigured at runtime, so the config file reloads without a restart.
package registry

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// healthQuery reports, for any backend, whether it is a standby, its WAL
// position (replayed for a standby, current for the primary), and — for a
// standby — how many seconds behind the last replayed transaction it is.
const healthQuery = `SELECT pg_is_in_recovery(),
  COALESCE(CASE WHEN pg_is_in_recovery() THEN pg_last_wal_replay_lsn() ELSE pg_current_wal_lsn() END::text, '0/0'),
  CASE WHEN pg_is_in_recovery() THEN COALESCE(EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::text, '0') ELSE '0' END`

// Role is a backend's replication role.
type Role uint8

const (
	// RoleUnknown is a backend that has not been successfully polled yet.
	RoleUnknown Role = iota
	// RolePrimary accepts writes (pg_is_in_recovery() is false).
	RolePrimary
	// RoleReplica is a read-only standby.
	RoleReplica
)

func (r Role) String() string {
	switch r {
	case RolePrimary:
		return "primary"
	case RoleReplica:
		return "replica"
	default:
		return "unknown"
	}
}

// Conn is a backend connection the registry polls over.
type Conn interface {
	QueryRow(ctx context.Context, sql string) ([]string, error)
	Close() error
}

// Dialer opens a connection to a backend address.
type Dialer func(ctx context.Context, addr string) (Conn, error)

// Backend names an address to poll.
type Backend struct {
	Name string
	Addr string
}

// Config configures a Registry.
type Config struct {
	Interval         time.Duration
	FailureThreshold int
	BaseBackoff      time.Duration
	MaxBackoff       time.Duration
	Dialer           Dialer
	Logger           *slog.Logger
}

// Status is a snapshot of one backend's health.
type Status struct {
	Name       string
	Addr       string
	Role       Role
	Healthy    bool
	LagBytes   int64
	LagSeconds float64
	LastErr    string
	LastPoll   time.Time
}

// Registry polls a set of backends and exposes their health.
type Registry struct {
	cfg Config
	log *slog.Logger

	mu        sync.RWMutex
	backends  []*backendState
	parentCtx context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

type backendState struct {
	name string
	addr string

	mu         sync.Mutex
	role       Role
	lsn        uint64
	lagSeconds float64
	healthy    bool
	lastErr    string
	lastPoll   time.Time
	failures   int
}

// New builds a Registry.
func New(cfg Config) *Registry {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 3
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	return &Registry{cfg: cfg, log: cfg.Logger}
}

// Start begins polling backends. Pollers stop when ctx is cancelled; Wait blocks
// until they finish.
func (r *Registry) Start(ctx context.Context, backends []Backend) {
	r.mu.Lock()
	r.parentCtx = ctx
	r.mu.Unlock()
	r.launch(backends)
}

// Reconfigure replaces the polled backend set, stopping the current pollers and
// starting fresh ones. It is used for config reload.
func (r *Registry) Reconfigure(backends []Backend) {
	r.stopPollers()
	r.launch(backends)
}

// Wait blocks until all pollers have stopped (after the Start context is done).
func (r *Registry) Wait() {
	r.wg.Wait()
}

func (r *Registry) launch(backends []Backend) {
	r.mu.Lock()
	pctx, cancel := context.WithCancel(r.parentCtx)
	states := make([]*backendState, len(backends))
	for i, b := range backends {
		states[i] = &backendState{name: b.Name, addr: b.Addr}
	}
	r.backends = states
	r.cancel = cancel
	r.mu.Unlock()

	for _, st := range states {
		r.wg.Add(1)
		go func(st *backendState) {
			defer r.wg.Done()
			r.pollLoop(pctx, st)
		}(st)
	}
}

func (r *Registry) stopPollers() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

func (r *Registry) pollLoop(ctx context.Context, st *backendState) {
	var conn Conn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(r.pollOnce(ctx, st, &conn))
	}
}

// pollOnce polls a backend once and returns how long to wait before the next
// poll: the interval on success, an exponential backoff on failure.
func (r *Registry) pollOnce(ctx context.Context, st *backendState, conn *Conn) time.Duration {
	if *conn == nil {
		c, err := r.cfg.Dialer(ctx, st.addr)
		if err != nil {
			return r.recordFailure(st, fmt.Errorf("dial: %w", err))
		}
		*conn = c
	}

	row, err := (*conn).QueryRow(ctx, healthQuery)
	if err != nil {
		_ = (*conn).Close()
		*conn = nil
		return r.recordFailure(st, err)
	}
	role, lsn, lagSeconds, err := parseHealthRow(row)
	if err != nil {
		_ = (*conn).Close()
		*conn = nil
		return r.recordFailure(st, err)
	}
	r.recordSuccess(st, role, lsn, lagSeconds)
	return r.cfg.Interval
}

func (r *Registry) recordSuccess(st *backendState, role Role, lsn uint64, lagSeconds float64) {
	st.mu.Lock()
	recovered := !st.healthy
	st.role = role
	st.lsn = lsn
	st.lagSeconds = lagSeconds
	st.healthy = true
	st.failures = 0
	st.lastErr = ""
	st.lastPoll = time.Now()
	st.mu.Unlock()
	if recovered {
		r.log.Info("backend healthy", "backend", st.name, "role", role.String())
	}
	r.log.Debug("polled backend", "backend", st.name, "role", role.String(), "lag_seconds", lagSeconds)
}

func (r *Registry) recordFailure(st *backendState, err error) time.Duration {
	st.mu.Lock()
	st.failures++
	tripped := st.healthy && st.failures >= r.cfg.FailureThreshold
	if st.failures >= r.cfg.FailureThreshold {
		st.healthy = false
	}
	failures := st.failures
	st.lastErr = err.Error()
	st.lastPoll = time.Now()
	st.mu.Unlock()

	if tripped {
		r.log.Warn("backend unhealthy", "backend", st.name, "failures", failures, "error", err)
	}

	shift := failures - 1
	if shift > 30 {
		shift = 30
	}
	backoff := r.cfg.BaseBackoff << shift
	if backoff <= 0 || backoff > r.cfg.MaxBackoff {
		backoff = r.cfg.MaxBackoff
	}
	return backoff
}

// Snapshot returns the current health of every backend, with replica lag in
// bytes computed against the primary's WAL position.
func (r *Registry) Snapshot() []Status {
	r.mu.RLock()
	backends := r.backends
	r.mu.RUnlock()

	var primaryLSN uint64
	var havePrimary bool
	for _, st := range backends {
		st.mu.Lock()
		if st.role == RolePrimary && st.healthy {
			primaryLSN = st.lsn
			havePrimary = true
		}
		st.mu.Unlock()
	}

	out := make([]Status, 0, len(backends))
	for _, st := range backends {
		st.mu.Lock()
		s := Status{
			Name:       st.name,
			Addr:       st.addr,
			Role:       st.role,
			Healthy:    st.healthy,
			LagSeconds: st.lagSeconds,
			LastErr:    st.lastErr,
			LastPoll:   st.lastPoll,
		}
		if st.role == RoleReplica && havePrimary && primaryLSN > st.lsn {
			s.LagBytes = int64(primaryLSN - st.lsn)
		}
		st.mu.Unlock()
		out = append(out, s)
	}
	return out
}

func parseHealthRow(row []string) (Role, uint64, float64, error) {
	if len(row) < 3 {
		return RoleUnknown, 0, 0, fmt.Errorf("registry: health query returned %d columns, want 3", len(row))
	}
	role := RolePrimary
	if row[0] == "t" {
		role = RoleReplica
	}
	lsn, err := parseLSN(row[1])
	if err != nil {
		return RoleUnknown, 0, 0, err
	}
	lagSeconds, err := strconv.ParseFloat(row[2], 64)
	if err != nil {
		return RoleUnknown, 0, 0, fmt.Errorf("registry: bad lag seconds %q: %w", row[2], err)
	}
	return role, lsn, lagSeconds, nil
}

// parseLSN parses a pg_lsn ("hi/lo", both hex) into a 64-bit byte position.
func parseLSN(s string) (uint64, error) {
	hi, lo, ok := strings.Cut(s, "/")
	if !ok {
		return 0, fmt.Errorf("registry: bad LSN %q", s)
	}
	high, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("registry: bad LSN %q: %w", s, err)
	}
	low, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("registry: bad LSN %q: %w", s, err)
	}
	return high<<32 | low, nil
}
