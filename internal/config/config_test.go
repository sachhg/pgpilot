package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pgpilot.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_Valid(t *testing.T) {
	path := writeConfig(t, `{
		"listen": "127.0.0.1:6432",
		"primary": "127.0.0.1:55432",
		"users": [{"name": "pgpilot", "password": "pw"}],
		"pool": {"max_size": 8, "max_waiters": 50, "acquire_timeout": "3s", "idle_timeout": "2m"}
	}`)
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Listen != "127.0.0.1:6432" || c.Primary != "127.0.0.1:55432" {
		t.Errorf("addresses = %q / %q", c.Listen, c.Primary)
	}
	if c.Pool.MaxSize != 8 || c.Pool.MaxWaiters != 50 {
		t.Errorf("pool sizes = %+v", c.Pool)
	}
	if c.Pool.AcquireTimeout.Std() != 3*time.Second || c.Pool.IdleTimeout.Std() != 2*time.Minute {
		t.Errorf("durations = %v / %v", c.Pool.AcquireTimeout.Std(), c.Pool.IdleTimeout.Std())
	}
	if u, ok := c.User("pgpilot"); !ok || u.Password != "pw" {
		t.Errorf("User lookup = %+v, %v", u, ok)
	}
	if _, ok := c.User("nobody"); ok {
		t.Error("unexpected user found")
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	path := writeConfig(t, `{
		"listen": "127.0.0.1:6432",
		"primary": "127.0.0.1:55432",
		"users": [{"name": "u", "password": "p"}]
	}`)
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Pool.MaxSize != 10 {
		t.Errorf("default MaxSize = %d, want 10", c.Pool.MaxSize)
	}
	if c.Pool.AcquireTimeout.Std() != 5*time.Second {
		t.Errorf("default AcquireTimeout = %v, want 5s", c.Pool.AcquireTimeout.Std())
	}
	if c.Pool.IdleTimeout.Std() != 5*time.Minute {
		t.Errorf("default IdleTimeout = %v, want 5m", c.Pool.IdleTimeout.Std())
	}
	if c.Pool.Mode != config.ModeSession {
		t.Errorf("default Mode = %q, want %q", c.Pool.Mode, config.ModeSession)
	}
}

func TestLoad_TransactionMode(t *testing.T) {
	path := writeConfig(t, `{
		"listen": "127.0.0.1:6432",
		"primary": "127.0.0.1:55432",
		"users": [{"name": "u", "password": "p"}],
		"pool": {"mode": "transaction"}
	}`)
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Pool.Mode != config.ModeTransaction {
		t.Errorf("Mode = %q, want %q", c.Pool.Mode, config.ModeTransaction)
	}
}

func TestLoad_Errors(t *testing.T) {
	cases := map[string]string{
		"missing listen":  `{"primary":"x","users":[{"name":"u","password":"p"}]}`,
		"missing primary": `{"listen":"x","users":[{"name":"u","password":"p"}]}`,
		"no users":        `{"listen":"x","primary":"y","users":[]}`,
		"duplicate user":  `{"listen":"x","primary":"y","users":[{"name":"u","password":"a"},{"name":"u","password":"b"}]}`,
		"bad duration":    `{"listen":"x","primary":"y","users":[{"name":"u","password":"p"}],"pool":{"acquire_timeout":"nope"}}`,
		"bad pool mode":   `{"listen":"x","primary":"y","users":[{"name":"u","password":"p"}],"pool":{"mode":"bogus"}}`,
		"unknown field":   `{"listen":"x","primary":"y","users":[{"name":"u","password":"p"}],"bogus":1}`,
		"bad json":        `{`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, body)
			if _, err := config.Load(path); err == nil {
				t.Errorf("Load(%s) succeeded, want an error", name)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := config.Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("Load of a missing file should fail")
	}
}
