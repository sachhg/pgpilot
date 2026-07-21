package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"runtime"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeUpstream is a stand-in PostgreSQL backend: it reads one startup packet,
// records it, then echoes everything else back.
type fakeUpstream struct {
	ln      net.Listener
	startup chan []byte
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fu := &fakeUpstream{ln: ln, startup: make(chan []byte, 4)}
	go fu.accept()
	t.Cleanup(func() { _ = ln.Close() })
	return fu
}

func (fu *fakeUpstream) addr() string { return fu.ln.Addr().String() }

func (fu *fakeUpstream) accept() {
	for {
		c, err := fu.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			pkt, err := readStartupPacket(c)
			if err != nil {
				return
			}
			select {
			case fu.startup <- pkt.raw:
			default:
			}
			_, _ = io.Copy(c, c) // echo the rest
		}(c)
	}
}

func startServer(t *testing.T, upstream string) (srv *Server, addr string, cancel context.CancelFunc, done chan struct{}) {
	t.Helper()
	srv = New(Config{ListenAddr: "127.0.0.1:0", UpstreamAddr: upstream, Logger: discardLogger()})
	a, err := srv.Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, c := context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(done)
	}()
	return srv, a.String(), c, done
}

func TestServer_RefusesSSLForwardsStartupAndPipes(t *testing.T) {
	fu := newFakeUpstream(t)
	_, addr, cancel, _ := startServer(t, fu.addr())
	defer cancel()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))

	// SSLRequest is refused with 'N'.
	if _, err := c.Write(buildStartup(sslRequestCode, nil)); err != nil {
		t.Fatalf("write SSLRequest: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read refusal: %v", err)
	}
	if buf[0] != sslNotSupported {
		t.Fatalf("refusal = %q, want %q", buf[0], byte(sslNotSupported))
	}

	// The StartupMessage is forwarded to the upstream untouched.
	startup := buildStartup(protocolVersion3, []byte("user\x00pgpilot\x00\x00"))
	if _, err := c.Write(startup); err != nil {
		t.Fatalf("write startup: %v", err)
	}
	select {
	case got := <-fu.startup:
		if !bytes.Equal(got, startup) {
			t.Fatalf("upstream received %x, want %x", got, startup)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received the startup packet")
	}

	// Bytes flow in both directions (the fake upstream echoes).
	payload := []byte("SELECT 1")
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(c, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatalf("echo = %q, want %q", echo, payload)
	}
}

func TestServer_GracefulShutdownClosesSessionsNoLeak(t *testing.T) {
	fu := newFakeUpstream(t)
	baseline := runtime.NumGoroutine()
	srv, addr, cancel, done := startServer(t, fu.addr())
	_ = srv

	var clients []net.Conn
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write(buildStartup(protocolVersion3, []byte("user\x00pgpilot\x00\x00"))); err != nil {
			t.Fatalf("write startup %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	time.Sleep(100 * time.Millisecond) // let sessions establish

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}

	// Every client connection should have been closed by the proxy.
	for i, c := range clients {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var b [1]byte
		if _, err := c.Read(b[:]); err == nil {
			t.Errorf("client %d still open after shutdown", i)
		}
		_ = c.Close()
	}

	// Session goroutines should be gone (allow slack and a moment to settle).
	if !eventually(2*time.Second, func() bool {
		return runtime.NumGoroutine() <= baseline+2
	}) {
		t.Errorf("goroutines did not settle: have %d, baseline %d", runtime.NumGoroutine(), baseline)
	}
}

func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}
