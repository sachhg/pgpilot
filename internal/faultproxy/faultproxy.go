// Package faultproxy is a test-only TCP forwarder that injects faults between a
// client and a fixed backend: added latency, severing live connections, and
// blackholing new ones. Pointing pgpilot's backends at fault proxies lets the
// fault-injection tests kill a replica, slow it, or cut it mid-query
// deterministically, with no external tool.
package faultproxy

import (
	"io"
	"net"
	"sync"
	"time"
)

// Proxy forwards TCP between clients and a single backend address, under fault
// controls that tests toggle at runtime. It is safe for concurrent use.
type Proxy struct {
	backend string
	ln      net.Listener

	mu        sync.Mutex
	latency   time.Duration
	blackhole bool
	live      map[net.Conn]struct{}
	closed    bool

	wg sync.WaitGroup
}

// New starts a proxy forwarding to backend, listening on an ephemeral loopback
// port. Close it when done.
func New(backend string) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{backend: backend, ln: ln, live: make(map[net.Conn]struct{})}
	p.wg.Add(1)
	go p.acceptLoop()
	return p, nil
}

// Addr is the proxy's listen address, to point a client at.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// SetLatency adds d of delay to each chunk forwarded from the backend to the
// client, simulating a slow backend. Zero disables it.
func (p *Proxy) SetLatency(d time.Duration) {
	p.mu.Lock()
	p.latency = d
	p.mu.Unlock()
}

// Blackhole makes the proxy refuse (immediately close) new connections when on,
// simulating a backend that is down. Existing connections are unaffected.
func (p *Proxy) Blackhole(on bool) {
	p.mu.Lock()
	p.blackhole = on
	p.mu.Unlock()
}

// Sever closes every live connection, simulating a backend that drops in-flight
// work. New connections are still accepted (unless also blackholed).
func (p *Proxy) Sever() {
	p.mu.Lock()
	conns := make([]net.Conn, 0, len(p.live))
	for c := range p.live {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// Close stops the proxy and severs all connections.
func (p *Proxy) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	err := p.ln.Close()
	p.Sever()
	p.wg.Wait()
	return err
}

func (p *Proxy) acceptLoop() {
	defer p.wg.Done()
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return // listener closed
		}
		p.mu.Lock()
		blackhole := p.blackhole
		p.mu.Unlock()
		if blackhole {
			_ = client.Close()
			continue
		}
		p.wg.Add(1)
		go p.handle(client)
	}
}

func (p *Proxy) handle(client net.Conn) {
	defer p.wg.Done()
	backend, err := net.Dial("tcp", p.backend)
	if err != nil {
		_ = client.Close()
		return
	}
	p.track(client)
	p.track(backend)

	done := make(chan struct{}, 2)
	go func() { p.pipe(backend, client, false); done <- struct{}{} }() // client -> backend
	go func() { p.pipe(client, backend, true); done <- struct{}{} }()  // backend -> client
	<-done
	// One side ended; close both so the other pipe unblocks, then wait for it.
	_ = client.Close()
	_ = backend.Close()
	<-done
	p.untrack(client)
	p.untrack(backend)
}

// pipe copies src to dst, optionally delaying each chunk to inject latency.
func (p *Proxy) pipe(dst io.Writer, src io.Reader, delay bool) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if delay {
				p.mu.Lock()
				d := p.latency
				p.mu.Unlock()
				if d > 0 {
					time.Sleep(d)
				}
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Proxy) track(c net.Conn) {
	p.mu.Lock()
	if !p.closed {
		p.live[c] = struct{}{}
	}
	p.mu.Unlock()
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.live, c)
	p.mu.Unlock()
}
