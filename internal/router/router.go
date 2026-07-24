package router

import "fmt"

// Policy-name values accepted by New and the config file.
const (
	// PolicyRoundRobin rotates reads evenly across eligible replicas.
	PolicyRoundRobin = "round-robin"
	// PolicyLeastInFlight sends each read to the least-loaded eligible replica.
	PolicyLeastInFlight = "least-in-flight"
	// PolicyScored ranks eligible replicas by estimated completion time.
	PolicyScored = "scored"
)

// New constructs the named policy. An unknown name is an error so a typo in the
// config fails loudly at startup rather than silently falling back.
func New(name string) (Policy, error) {
	switch name {
	case PolicyRoundRobin:
		return NewRoundRobin(), nil
	case PolicyLeastInFlight:
		return NewLeastInFlight(), nil
	case PolicyScored:
		return NewScored(), nil
	default:
		return nil, fmt.Errorf("router: unknown policy %q", name)
	}
}
