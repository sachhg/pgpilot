//go:build integration

package smoke

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	pgUser = "pgpilot"
	pgDB   = "pgpilot"
)

// replicas is the set of standby services declared in docker-compose.yml.
var replicas = []string{"replica1", "replica2"}

// TestSmokeReplication asserts the core Phase 1 invariant: a row written to the
// primary is replayed by, and readable from, every streaming replica. It also
// checks the cluster's shape (one writable primary, two read-only standbys that
// the primary sees actively streaming).
func TestSmokeReplication(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; skipping cluster smoke test")
	}
	compose := composeFile(t)

	// Fail fast with a helpful message if the cluster is not running.
	if _, err := psqlErr(compose, "primary", "SELECT 1;"); err != nil {
		t.Fatalf("primary not reachable; run `make up` first: %v", err)
	}

	// The primary accepts writes; the replicas are read-only standbys.
	if got := psql(t, compose, "primary", "SELECT pg_is_in_recovery();"); got != "f" {
		t.Fatalf("primary pg_is_in_recovery = %q, want f", got)
	}
	for _, r := range replicas {
		if got := psql(t, compose, r, "SELECT pg_is_in_recovery();"); got != "t" {
			t.Fatalf("%s pg_is_in_recovery = %q, want t", r, got)
		}
	}

	// The primary must see both standbys actively streaming.
	if got := psql(t, compose, "primary",
		"SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming';"); got != "2" {
		t.Fatalf("streaming standbys = %q, want 2", got)
	}

	// Write a unique probe row on the primary and record the commit LSN.
	token := fmt.Sprintf("smoke-%d", time.Now().UnixNano())
	id := psql(t, compose, "primary",
		fmt.Sprintf("INSERT INTO replication_probe (token) VALUES ('%s') RETURNING id;", token))
	if id == "" {
		t.Fatal("insert returned no id")
	}
	lsn := psql(t, compose, "primary", "SELECT pg_current_wal_lsn();")
	t.Logf("wrote probe id=%s token=%s at primary LSN=%s", id, token, lsn)

	// Every replica must replay past that LSN and then serve the row.
	for _, r := range replicas {
		waitForReplay(t, compose, r, lsn)
		got := psql(t, compose, r,
			fmt.Sprintf("SELECT token FROM replication_probe WHERE id = %s;", id))
		if got != token {
			t.Fatalf("%s served token %q, want %q", r, got, token)
		}
		t.Logf("%s replayed to the fence and served the probe row", r)
	}
}

// waitForReplay blocks until the standby has replayed at or past lsn, or fails
// the test if that does not happen within the deadline.
func waitForReplay(t *testing.T, compose, service, lsn string) {
	t.Helper()
	const timeout = 15 * time.Second
	deadline := time.Now().Add(timeout)
	for {
		got, err := psqlErr(compose, service,
			fmt.Sprintf("SELECT pg_wal_lsn_diff(pg_last_wal_replay_lsn(), '%s'::pg_lsn) >= 0;", lsn))
		if err == nil && got == "t" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not replay to LSN %s within %s (last=%q err=%v)",
				service, lsn, timeout, got, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// composeFile locates docker-compose.yml by walking up from the working
// directory, so the test works whether it is run from the repo root or the
// package directory.
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

// psql runs a single query against a service and returns its trimmed output,
// failing the test on error.
func psql(t *testing.T, compose, service, sql string) string {
	t.Helper()
	out, err := psqlErr(compose, service, sql)
	if err != nil {
		t.Fatalf("psql on %s failed: %v", service, err)
	}
	return out
}

// psqlErr runs a single query against a service via `docker compose exec` and
// returns its trimmed stdout, or an error carrying stderr.
func psqlErr(compose, service, sql string) (string, error) {
	cmd := exec.Command("docker", "compose", "-f", compose, "exec", "-T",
		service, "psql", "-U", pgUser, "-d", pgDB, "-tAXq", "-c", sql)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
