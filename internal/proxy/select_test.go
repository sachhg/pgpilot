package proxy

import (
	"testing"

	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/registry"
	"github.com/sachhg/pgpilot/internal/router"
)

func newSelectSession(mode string) *session {
	return &session{
		cfg:    &config.Config{Primary: "primary:5432", Fencing: config.Fencing{Mode: mode, BoundedMs: 100}},
		policy: router.NewRoundRobin(),
	}
}

func replicaStatus(addr string, healthy bool) registry.Status {
	return registry.Status{Addr: addr, Role: registry.RoleReplica, Healthy: healthy}
}

func TestSelectReadTarget_ChoosesEligibleReplica(t *testing.T) {
	s := newSelectSession(config.FenceRelaxed)
	snap := []registry.Status{
		{Addr: "primary:5432", Role: registry.RolePrimary, Healthy: true},
		replicaStatus("r1:5432", true),
		replicaStatus("r2:5432", true),
	}
	addr, routed := s.selectReadTarget(snap, 0, "", nil)
	if !routed {
		t.Fatalf("expected a routed replica, got primary %q", addr)
	}
	if addr != "r1:5432" && addr != "r2:5432" {
		t.Errorf("chose %q, want a replica", addr)
	}
}

func TestSelectReadTarget_ExcludeFallsThroughToPrimary(t *testing.T) {
	s := newSelectSession(config.FenceRelaxed)
	snap := []registry.Status{replicaStatus("r1:5432", true), replicaStatus("r2:5432", true)}

	// Excluding one replica still leaves the other.
	if addr, routed := s.selectReadTarget(snap, 0, "", map[string]bool{"r1:5432": true}); !routed || addr != "r2:5432" {
		t.Errorf("with r1 excluded, chose %q routed=%v, want r2 routed", addr, routed)
	}
	// Excluding both replicas falls back to the primary.
	both := map[string]bool{"r1:5432": true, "r2:5432": true}
	if addr, routed := s.selectReadTarget(snap, 0, "", both); routed || addr != "primary:5432" {
		t.Errorf("with both excluded, chose %q routed=%v, want primary not routed", addr, routed)
	}
}

func TestSelectReadTarget_UnhealthySkipped(t *testing.T) {
	s := newSelectSession(config.FenceRelaxed)
	snap := []registry.Status{replicaStatus("r1:5432", false), replicaStatus("r2:5432", true)}
	if addr, routed := s.selectReadTarget(snap, 0, "", nil); !routed || addr != "r2:5432" {
		t.Errorf("chose %q routed=%v, want the healthy r2", addr, routed)
	}

	allDown := []registry.Status{replicaStatus("r1:5432", false), replicaStatus("r2:5432", false)}
	if addr, routed := s.selectReadTarget(allDown, 0, "", nil); routed || addr != "primary:5432" {
		t.Errorf("with no healthy replica, chose %q routed=%v, want primary", addr, routed)
	}
}

func TestSelectReadTarget_StrictFenceExcludesBehindReplica(t *testing.T) {
	s := newSelectSession(config.FenceStrict)
	// r1 has replayed to LSN 100, r2 only to 40; fence is 50.
	snap := []registry.Status{
		{Addr: "r1:5432", Role: registry.RoleReplica, Healthy: true, LSN: 100},
		{Addr: "r2:5432", Role: registry.RoleReplica, Healthy: true, LSN: 40},
	}
	addr, routed := s.selectReadTarget(snap, 50, "", nil)
	if !routed || addr != "r1:5432" {
		t.Errorf("strict fence chose %q routed=%v, want r1 (r2 is behind the fence)", addr, routed)
	}
}
