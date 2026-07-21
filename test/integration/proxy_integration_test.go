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

	"github.com/sachhg/pgpilot/internal/proxy"
)

const (
	pgUser = "pgpilot"
	pgDB   = "pgpilot"
	// primaryHostPort is where the dev-cluster primary is published on the host;
	// the proxy dials it as its upstream.
	primaryHostPort = "127.0.0.1:55432"
)

// TestProxy_PsqlThroughMatchesDirect asserts the Phase 2 invariant: psql routed
// through the transparent proxy behaves identically to psql connected directly
// to the primary. The proxy runs in-process on the host; psql runs inside the
// primary container and reaches the proxy back on the host via
// host.docker.internal (Docker Desktop).
func TestProxy_PsqlThroughMatchesDirect(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; skipping proxy integration test")
	}
	compose := composeFile(t)

	// Precondition: the cluster must be up.
	if _, err := runPsql(compose, "127.0.0.1", 5432, "SELECT 1;"); err != nil {
		t.Fatalf("primary not reachable; run `make up` first: %v", err)
	}

	// Start the transparent proxy in-process, forwarding to the primary.
	srv := proxy.New(proxy.Config{
		ListenAddr:   "0.0.0.0:0",
		UpstreamAddr: primaryHostPort,
		DialTimeout:  5 * time.Second,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
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
		"SELECT id, token FROM replication_probe ORDER BY id;",
		"SELECT 1 AS a; SELECT 2 AS b;", // multi-statement simple query
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
}

// runPsql runs a query from inside the primary container against the given host
// and port, returning psql's trimmed tuples-only output.
func runPsql(compose, host string, port int, sql string) (string, error) {
	cmd := exec.Command("docker", "compose", "-f", compose, "exec", "-T",
		"-e", "PGPASSWORD="+pgUser, "primary",
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
