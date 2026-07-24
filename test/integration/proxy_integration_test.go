//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sachhg/pgpilot/internal/backend"
	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/proxy"
)

const (
	pgUser          = "pgpilot"
	pgPass          = "pgpilot"
	pgDB            = "pgpilot"
	primaryHostPort = "127.0.0.1:55432"
)

// TestProxy_PsqlThroughMatchesDirect asserts that psql, authenticating to
// pgpilot with SCRAM-SHA-256 and served by a pooled backend, behaves identically
// to psql connected directly to the primary — validating the SCRAM server
// against real psql and the end-to-end session-pooling path. Requires `make up`.
func TestProxy_PsqlThroughMatchesDirect(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; skipping proxy integration test")
	}
	compose := composeFile(t)
	if _, err := runPsql(compose, "127.0.0.1", 5432, "SELECT 1;"); err != nil {
		t.Fatalf("primary not reachable; run `make up` first: %v", err)
	}

	cfg := &config.Config{
		Listen:  "0.0.0.0:0",
		Primary: primaryHostPort,
		Users:   []config.User{{Name: pgUser, Password: pgPass}},
	}
	mgr := backend.NewManager(cfg.Primary, map[string]string{pgUser: pgPass},
		backend.PoolConfig{MaxSize: 5, AcquireTimeout: 5 * time.Second, IdleTimeout: time.Minute})
	defer mgr.Close()

	srv := proxy.New(proxy.Config{
		ListenAddr: cfg.Listen,
		Users:      cfg,
		Manager:    mgr,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	addr, err := srv.Listen()
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(serveDone)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveDone:
		case <-time.After(3 * time.Second):
			t.Error("proxy Serve did not return after cancel")
		}
	})
	proxyPort := addr.(*net.TCPAddr).Port

	queries := []string{
		"SELECT 1;",
		"SELECT count(*) FROM accounts;",
		"SELECT email FROM accounts ORDER BY id;",
		"SELECT current_database();",
		"SHOW server_version_num;",
		"SELECT 1 AS a; SELECT 2 AS b;",
		"BEGIN; SELECT count(*) FROM accounts; COMMIT;",
	}
	for _, q := range queries {
		direct, err := runPsql(compose, "127.0.0.1", 5432, q)
		if err != nil {
			t.Fatalf("direct %q: %v", q, err)
		}
		via, err := runPsql(compose, "host.docker.internal", proxyPort, q)
		if err != nil {
			t.Fatalf("through proxy %q: %v", q, err)
		}
		if direct != via {
			t.Errorf("mismatch for %q:\n  direct = %q\n   proxy = %q", q, direct, via)
			continue
		}
		t.Logf("identical: %q -> %q", q, via)
	}

	// Several sequential sessions must reuse the pooled backend without error.
	for i := 0; i < 3; i++ {
		if _, err := runPsql(compose, "host.docker.internal", proxyPort, "SELECT 1;"); err != nil {
			t.Fatalf("reuse session %d failed: %v", i, err)
		}
	}
}

// runPsql runs a query from inside the primary container against the given host
// and port, returning psql's trimmed tuples-only output.
func runPsql(compose, host string, port int, sql string) (string, error) {
	cmd := exec.Command("docker", "compose", "-f", compose, "exec", "-T",
		"-e", "PGPASSWORD="+pgPass, "primary",
		"psql", "-h", host, "-p", strconv.Itoa(port),
		"-U", pgUser, "-d", pgDB, "-tAX", "-c", sql)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// composeFile locates docker-compose.yml by walking up from the working
// directory.
func composeFile(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	start := dir
	for {
		candidate := filepath.Join(dir, "docker-compose.yml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find docker-compose.yml at or above %s", start)
		}
		dir = parent
	}
}
