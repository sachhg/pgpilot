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

var queries = []string{
	"SELECT 1;",
	"SELECT count(*) FROM accounts;",
	"SELECT email FROM accounts ORDER BY id;",
	"SELECT current_database();",
	"SHOW server_version_num;",
	"SELECT 1 AS a; SELECT 2 AS b;",
	"BEGIN; SELECT count(*) FROM accounts; COMMIT;",
}

func TestProxy_SessionMode_MatchesDirect(t *testing.T) {
	compose := requireCluster(t)
	port := startProxy(t, config.ModeSession, 5)
	compareQueries(t, compose, port)
}

func TestProxy_TransactionMode(t *testing.T) {
	compose := requireCluster(t)
	// A small pool forces sequential sessions to reuse (multiplex) backends.
	port := startProxy(t, config.ModeTransaction, 2)

	// Correctness: identical to direct.
	compareQueries(t, compose, port)

	// Pinning: a session-level SET must persist across statements in the same
	// session, which only works if pgpilot pins the session to its backend.
	direct, err := runPsqlSession(compose, "127.0.0.1", 5432,
		"SET application_name TO 'pinned'", "SHOW application_name")
	if err != nil {
		t.Fatalf("direct SET/SHOW: %v", err)
	}
	via, err := runPsqlSession(compose, "host.docker.internal", port,
		"SET application_name TO 'pinned'", "SHOW application_name")
	if err != nil {
		t.Fatalf("proxy SET/SHOW: %v", err)
	}
	if via != direct || lastLine(via) != "pinned" {
		t.Errorf("session GUC not pinned: via=%q direct=%q; SHOW should return pinned", via, direct)
	}

	// Pinning: a temp table must survive across statements in the same session.
	got, err := runPsqlSession(compose, "host.docker.internal", port,
		"CREATE TEMP TABLE tt (x int)", "INSERT INTO tt VALUES (1), (2)", "SELECT count(*) FROM tt")
	if err != nil {
		t.Fatalf("temp table session: %v", err)
	}
	if lastLine(got) != "2" {
		t.Errorf("temp table not pinned: got %q, want a final count of 2", got)
	}

	// Multiplexing: many sequential sessions share the size-2 pool.
	for i := 0; i < 6; i++ {
		if out, err := runPsql(compose, "host.docker.internal", port, "SELECT 1;"); err != nil || out != "1" {
			t.Fatalf("multiplex session %d: out=%q err=%v", i, out, err)
		}
	}
}

func compareQueries(t *testing.T, compose string, proxyPort int) {
	t.Helper()
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
}

// startProxy launches an in-process proxy in the given pool mode and returns its
// port. Cleanup is registered on t.
func startProxy(t *testing.T, mode string, maxSize int) int {
	t.Helper()
	cfg := &config.Config{
		Listen:  "0.0.0.0:0",
		Primary: primaryHostPort,
		Users:   []config.User{{Name: pgUser, Password: pgPass}},
		Pool:    config.Pool{Mode: mode},
	}
	mgr := backend.NewManager(cfg.Primary, map[string]string{pgUser: pgPass},
		backend.PoolConfig{MaxSize: maxSize, AcquireTimeout: 5 * time.Second, IdleTimeout: time.Minute})
	t.Cleanup(mgr.Close)

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
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("proxy Serve did not return after cancel")
		}
	})
	return addr.(*net.TCPAddr).Port
}

func requireCluster(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; skipping proxy integration test")
	}
	compose := composeFile(t)
	if _, err := runPsql(compose, "127.0.0.1", 5432, "SELECT 1;"); err != nil {
		t.Fatalf("primary not reachable; run `make up` first: %v", err)
	}
	return compose
}

// runPsql runs a single query from inside the primary container against host:port.
func runPsql(compose, host string, port int, sql string) (string, error) {
	return runPsqlSession(compose, host, port, sql)
}

// runPsqlSession runs one psql session, each sql a separate -c command, returning
// the trimmed tuples-only output.
func runPsqlSession(compose, host string, port int, sqls ...string) (string, error) {
	args := []string{
		"compose", "-f", compose, "exec", "-T", "-e", "PGPASSWORD=" + pgPass, "primary",
		"psql", "-h", host, "-p", strconv.Itoa(port), "-U", pgUser, "-d", pgDB, "-tAX",
	}
	for _, s := range sqls {
		args = append(args, "-c", s)
	}
	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// lastLine returns the text after the final newline (psql prints a command tag
// like "SET" before the result of the next statement).
func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// waitFor polls cond until it is true or the deadline passes.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// runPsqlOn runs a query on a specific cluster container's own Postgres (used to
// pause replication on a replica or seed the primary directly).
func runPsqlOn(compose, service, sql string) (string, error) {
	cmd := exec.Command("docker", "compose", "-f", compose, "exec", "-T",
		"-e", "PGPASSWORD="+pgPass, service,
		"psql", "-h", "127.0.0.1", "-p", "5432", "-U", pgUser, "-d", pgDB, "-tAX", "-c", sql)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// composeFile locates docker-compose.yml by walking up from the working directory.
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
