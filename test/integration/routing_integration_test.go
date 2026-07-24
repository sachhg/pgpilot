//go:build integration

package integration

import (
	"testing"

	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/router"
)

// TestProxy_RoundRobinSpreadsReadsAcrossReplicas proves the routing policy
// actually distributes reads over the whole replica set, end to end. Under the
// round-robin policy (least-in-flight would send every sequential, immediately
// released read to the same idle replica), a handful of reads must land on more
// than one replica. Each backend is identified by inet_server_addr(), which
// returns its distinct container IP.
func TestProxy_RoundRobinSpreadsReadsAcrossReplicas(t *testing.T) {
	compose := requireCluster(t)
	resumeReplication(t, compose)
	// Relaxed fencing keeps every healthy replica eligible, isolating the test to
	// the policy's distribution rather than the fence.
	port := startRoutingProxy(t, config.FenceRelaxed, router.NewRoundRobin())

	seen := map[string]int{}
	const reads = 6
	for i := 0; i < reads; i++ {
		addr, err := runPsql(compose, "host.docker.internal", port, "SELECT inet_server_addr()")
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if addr == "" {
			t.Fatalf("read %d returned an empty server address", i)
		}
		seen[addr]++
	}
	if len(seen) < 2 {
		t.Errorf("round-robin sent %d reads to a single backend %v; want them spread across both replicas", reads, seen)
	} else {
		t.Logf("round-robin spread %d reads across %d replicas: %v", reads, len(seen), seen)
	}
}
