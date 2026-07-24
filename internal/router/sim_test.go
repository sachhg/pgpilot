package router

import (
	"container/heap"
	"math"
	"math/rand"
	"sort"
	"time"
)

// This file holds a small, fully deterministic discrete-event simulator used to
// compare policies on a synthetic mixed workload without a database. Each
// replica is modeled as a single FIFO server whose service time for a read is
// the query shape's base cost times the replica's slowness; requests arrive on a
// seeded schedule with a seeded mix of shapes. A policy's in-flight view stays
// exact because Choose and Release fire in simulated-time order.

// shape is a query shape: a fingerprint and its base service cost in ms on a
// unit-speed replica.
type shape struct {
	fp   string
	cost float64
}

// simReplica is a backend in the simulation.
type simReplica struct {
	addr     string
	slowness float64 // service-time multiplier
	lag      float64 // replication lag in seconds
}

// request is one arrival: when it arrives and what shape it is.
type request struct {
	arrival float64
	shape   shape
}

// workload is a deterministic stream of requests over a fixed replica set.
type workload struct {
	replicas []simReplica
	requests []request
}

// stats summarizes a simulation run. Latencies are in milliseconds.
type stats struct {
	makespan   float64
	mean       float64
	p50        float64
	p95        float64
	p99        float64
	perReplica map[string]int
}

// genWorkload builds a deterministic workload: three replicas (one 3x slow), a
// heavy-tailed shape mix (mostly cheap, a few very expensive), and exponential
// inter-arrival times, all driven by a seeded PRNG.
func genWorkload(seed int64, n int) workload {
	rng := rand.New(rand.NewSource(seed))
	replicas := []simReplica{
		{addr: "r1", slowness: 1.0, lag: 0},
		{addr: "r2", slowness: 1.0, lag: 0},
		{addr: "r3", slowness: 3.0, lag: 0}, // a slower box
	}
	shapes := []shape{
		{fp: "cheap", cost: 1.0},
		{fp: "mid", cost: 12.0},
		{fp: "heavy", cost: 90.0},
	}
	// Cumulative weights: ~72% cheap, ~22% mid, ~6% heavy.
	weights := []float64{0.72, 0.94, 1.0}

	// Arrival rate (requests per ms) tuned so the two fast replicas run near
	// ~75% utilization: busy enough to queue and separate the policies, stable
	// enough that the tail reflects placement quality rather than a runaway
	// queue. Mean service is ~8.8ms, so two fast replicas clear ~0.23 req/ms.
	const rate = 0.16
	reqs := make([]request, n)
	t := 0.0
	for i := range reqs {
		t += rng.ExpFloat64() / rate
		u := rng.Float64()
		s := shapes[len(shapes)-1]
		for j, w := range weights {
			if u <= w {
				s = shapes[j]
				break
			}
		}
		reqs[i] = request{arrival: t, shape: s}
	}
	return workload{replicas: replicas, requests: reqs}
}

// releaseEvent is a scheduled completion.
type releaseEvent struct {
	at      float64
	addr    string
	fp      string
	latency float64 // ms
}

type releaseHeap []releaseEvent

func (h releaseHeap) Len() int            { return len(h) }
func (h releaseHeap) Less(i, j int) bool  { return h[i].at < h[j].at }
func (h releaseHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *releaseHeap) Push(x interface{}) { *h = append(*h, x.(releaseEvent)) }
func (h *releaseHeap) Pop() interface{} {
	old := *h
	n := len(old)
	ev := old[n-1]
	*h = old[:n-1]
	return ev
}

// simulate runs the workload through policy and returns latency statistics. It
// processes arrivals and releases in simulated-time order (releases first on a
// tie) so the policy's in-flight counts mirror real queue depth.
func simulate(policy Policy, w workload) stats {
	cands := make([]Candidate, len(w.replicas))
	slow := make(map[string]float64, len(w.replicas))
	for i, r := range w.replicas {
		cands[i] = Candidate{Addr: r.addr, LagSeconds: r.lag}
		slow[r.addr] = r.slowness
	}

	freeAt := make(map[string]float64, len(w.replicas))
	perReplica := make(map[string]int, len(w.replicas))
	latencies := make([]float64, 0, len(w.requests))
	pending := &releaseHeap{}
	heap.Init(pending)

	ai := 0
	makespan := 0.0
	for ai < len(w.requests) || pending.Len() > 0 {
		nextArrival := math.Inf(1)
		if ai < len(w.requests) {
			nextArrival = w.requests[ai].arrival
		}
		nextRelease := math.Inf(1)
		if pending.Len() > 0 {
			nextRelease = (*pending)[0].at
		}

		if nextRelease <= nextArrival {
			ev := heap.Pop(pending).(releaseEvent)
			policy.Release(ev.addr, ev.fp, time.Duration(ev.latency*float64(time.Millisecond)))
			continue
		}

		req := w.requests[ai]
		ai++
		addr := policy.Choose(cands, req.shape.fp)
		svc := req.shape.cost * slow[addr]
		start := req.arrival
		if freeAt[addr] > start {
			start = freeAt[addr]
		}
		finish := start + svc
		freeAt[addr] = finish
		if finish > makespan {
			makespan = finish
		}
		lat := finish - req.arrival
		latencies = append(latencies, lat)
		perReplica[addr]++
		heap.Push(pending, releaseEvent{at: finish, addr: addr, fp: req.shape.fp, latency: lat})
	}

	return summarize(latencies, makespan, perReplica)
}

func summarize(latencies []float64, makespan float64, perReplica map[string]int) stats {
	sort.Float64s(latencies)
	sum := 0.0
	for _, l := range latencies {
		sum += l
	}
	n := len(latencies)
	return stats{
		makespan:   makespan,
		mean:       sum / float64(n),
		p50:        percentile(latencies, 0.50),
		p95:        percentile(latencies, 0.95),
		p99:        percentile(latencies, 0.99),
		perReplica: perReplica,
	}
}

// percentile returns the p-quantile (0..1) of a sorted slice via nearest-rank.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
