//go:build integration

package backend

import (
	"context"
	"testing"
	"time"
)

const primaryAddr = "127.0.0.1:55432"

// TestDial_AuthenticatesAgainstPrimary proves the SCRAM-SHA-256 client is
// correct against PostgreSQL's reference implementation: a successful Dial means
// the real server accepted our proof. Requires `make up`.
func TestDial_AuthenticatesAgainstPrimary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Dial(ctx, primaryAddr, "pgpilot", "pgpilot", "pgpilot")
	if err != nil {
		t.Fatalf("Dial: %v (is `make up` running?)", err)
	}
	defer func() { _ = c.Close() }()

	if c.Params()["server_version"] == "" {
		t.Error("no server_version ParameterStatus captured during startup")
	}
	if err := c.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestDial_WrongPasswordRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := Dial(ctx, primaryAddr, "pgpilot", "not-the-password", "pgpilot"); err == nil {
		t.Fatal("Dial succeeded with a wrong password")
	}
}
