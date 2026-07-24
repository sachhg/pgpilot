// Package config loads pgpilot's JSON configuration: the listen address, the
// backend to route to, the users pgpilot authenticates (used both to verify
// clients and to authenticate its own pooled backend connections), and pool
// sizing. Hot-reload arrives in Phase 6.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config is the top-level pgpilot configuration.
type Config struct {
	Listen   string   `json:"listen"`
	Primary  string   `json:"primary"`
	Replicas []string `json:"replicas"`
	Users    []User   `json:"users"`
	Pool     Pool     `json:"pool"`
	Health   Health   `json:"health"`
}

// Health configures the background poller that tracks each backend's recovery
// state and replication lag and trips a circuit breaker on failure.
type Health struct {
	// Interval is how often a healthy backend is polled.
	Interval Duration `json:"interval"`
	// FailureThreshold is the number of consecutive poll failures after which a
	// backend is marked unhealthy (the breaker trips).
	FailureThreshold int `json:"failure_threshold"`
	// BaseBackoff and MaxBackoff bound the exponential backoff between poll
	// attempts while a backend is failing.
	BaseBackoff Duration `json:"base_backoff"`
	MaxBackoff  Duration `json:"max_backoff"`
}

// User is a role pgpilot authenticates. The password is used both to verify a
// connecting client (SCRAM server side) and to open pooled backend connections
// (SCRAM client side).
type User struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// Pool-mode values.
const (
	ModeSession     = "session"
	ModeTransaction = "transaction"
)

// Pool holds connection-pool sizing and the pooling mode, shared by every
// (user, database) pool.
type Pool struct {
	// Mode is "session" (a client holds a backend for its whole session) or
	// "transaction" (a backend is returned to the pool between transactions).
	Mode           string   `json:"mode"`
	MaxSize        int      `json:"max_size"`
	MaxWaiters     int      `json:"max_waiters"`
	AcquireTimeout Duration `json:"acquire_timeout"`
	IdleTimeout    Duration `json:"idle_timeout"`
}

// Duration is a time.Duration that marshals to and from a Go duration string
// (e.g. "5s", "5m") in JSON.
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// UnmarshalJSON parses a duration from a JSON string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("config: duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalJSON renders the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Load reads and validates the config file at path, applying defaults.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Pool.Mode == "" {
		c.Pool.Mode = ModeSession
	}
	if c.Pool.MaxSize == 0 {
		c.Pool.MaxSize = 10
	}
	if c.Pool.AcquireTimeout == 0 {
		c.Pool.AcquireTimeout = Duration(5 * time.Second)
	}
	if c.Pool.IdleTimeout == 0 {
		c.Pool.IdleTimeout = Duration(5 * time.Minute)
	}
	if c.Health.Interval == 0 {
		c.Health.Interval = Duration(time.Second)
	}
	if c.Health.FailureThreshold == 0 {
		c.Health.FailureThreshold = 3
	}
	if c.Health.BaseBackoff == 0 {
		c.Health.BaseBackoff = Duration(time.Second)
	}
	if c.Health.MaxBackoff == 0 {
		c.Health.MaxBackoff = Duration(30 * time.Second)
	}
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return fmt.Errorf("config: listen is required")
	}
	if c.Primary == "" {
		return fmt.Errorf("config: primary is required")
	}
	if len(c.Users) == 0 {
		return fmt.Errorf("config: at least one user is required")
	}
	seen := make(map[string]struct{}, len(c.Users))
	for _, u := range c.Users {
		if u.Name == "" {
			return fmt.Errorf("config: a user is missing a name")
		}
		if _, dup := seen[u.Name]; dup {
			return fmt.Errorf("config: duplicate user %q", u.Name)
		}
		seen[u.Name] = struct{}{}
	}
	if c.Pool.MaxSize < 1 {
		return fmt.Errorf("config: pool.max_size must be >= 1")
	}
	if c.Pool.Mode != ModeSession && c.Pool.Mode != ModeTransaction {
		return fmt.Errorf("config: pool.mode must be %q or %q, got %q", ModeSession, ModeTransaction, c.Pool.Mode)
	}
	return nil
}

// User returns the named user's configuration and whether it exists.
func (c *Config) User(name string) (User, bool) {
	for _, u := range c.Users {
		if u.Name == name {
			return u, true
		}
	}
	return User{}, false
}
