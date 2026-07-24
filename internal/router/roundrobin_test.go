package router

import (
	"sync"
	"testing"
)

func addrs(names ...string) []Candidate {
	c := make([]Candidate, len(names))
	for i, n := range names {
		c[i] = Candidate{Addr: n}
	}
	return c
}

func TestRoundRobin_RotatesEvenly(t *testing.T) {
	p := NewRoundRobin()
	cands := addrs("a", "b", "c")
	counts := map[string]int{}
	for i := 0; i < 30; i++ {
		counts[p.Choose(cands, "")]++
	}
	for _, a := range []string{"a", "b", "c"} {
		if counts[a] != 10 {
			t.Errorf("addr %q served %d reads, want 10 (counts=%v)", a, counts[a], counts)
		}
	}
}

func TestRoundRobin_Order(t *testing.T) {
	p := NewRoundRobin()
	cands := addrs("a", "b")
	want := []string{"a", "b", "a", "b"}
	for i, w := range want {
		if got := p.Choose(cands, ""); got != w {
			t.Errorf("choice %d = %q, want %q", i, got, w)
		}
	}
}

func TestRoundRobin_SingleCandidate(t *testing.T) {
	p := NewRoundRobin()
	cands := addrs("only")
	for i := 0; i < 5; i++ {
		if got := p.Choose(cands, ""); got != "only" {
			t.Fatalf("choice %d = %q, want only", i, got)
		}
	}
}

func TestRoundRobin_ConcurrentIsRace(t *testing.T) {
	p := NewRoundRobin()
	cands := addrs("a", "b", "c", "d")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				p.Release(p.Choose(cands, ""), "", 0)
			}
		}()
	}
	wg.Wait()
}
