package proxy

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// startupMsg is a minimal but valid StartupMessage.
func startupMsg() []byte {
	return buildStartup(protocolVersion3, []byte("user\x00pgpilot\x00\x00"))
}

func TestNegotiateStartup_RefusesSSLThenReturnsStartup(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer func() { _ = clientEnd.Close() }()
	sess := &session{client: serverEnd}

	type result struct {
		pkt startupPacket
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		pkt, err := sess.negotiateStartup()
		resCh <- result{pkt, err}
	}()

	_ = clientEnd.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := clientEnd.Write(buildStartup(sslRequestCode, nil)); err != nil {
		t.Fatalf("write SSLRequest: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(clientEnd, buf); err != nil {
		t.Fatalf("read refusal: %v", err)
	}
	if buf[0] != sslNotSupported {
		t.Fatalf("refusal byte = %q, want %q", buf[0], byte(sslNotSupported))
	}

	startup := startupMsg()
	if _, err := clientEnd.Write(startup); err != nil {
		t.Fatalf("write startup: %v", err)
	}
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("negotiateStartup: %v", r.err)
		}
		if !bytes.Equal(r.pkt.raw, startup) {
			t.Fatalf("returned packet is not the forwarded startup")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("negotiateStartup did not return")
	}
}

func TestNegotiateStartup_RefusesGSSThenSSL(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer func() { _ = clientEnd.Close() }()
	sess := &session{client: serverEnd}

	resCh := make(chan startupPacket, 1)
	errCh := make(chan error, 1)
	go func() {
		pkt, err := sess.negotiateStartup()
		if err != nil {
			errCh <- err
			return
		}
		resCh <- pkt
	}()

	_ = clientEnd.SetDeadline(time.Now().Add(2 * time.Second))
	for _, code := range []uint32{gssEncRequestCode, sslRequestCode} {
		if _, err := clientEnd.Write(buildStartup(code, nil)); err != nil {
			t.Fatalf("write request %d: %v", code, err)
		}
		buf := make([]byte, 1)
		if _, err := io.ReadFull(clientEnd, buf); err != nil {
			t.Fatalf("read refusal: %v", err)
		}
		if buf[0] != sslNotSupported {
			t.Fatalf("refusal byte = %q", buf[0])
		}
	}

	startup := startupMsg()
	if _, err := clientEnd.Write(startup); err != nil {
		t.Fatalf("write startup: %v", err)
	}
	select {
	case pkt := <-resCh:
		if !bytes.Equal(pkt.raw, startup) {
			t.Fatal("returned packet is not the forwarded startup")
		}
	case err := <-errCh:
		t.Fatalf("negotiateStartup: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestNegotiateStartup_ClientClosesEarly(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	sess := &session{client: serverEnd}
	errCh := make(chan error, 1)
	go func() {
		_, err := sess.negotiateStartup()
		errCh <- err
	}()
	_ = clientEnd.Close()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected an error when the client closes before sending a packet")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("negotiateStartup did not return after client close")
	}
}

func TestPipe_Bidirectional(t *testing.T) {
	// Two loopback connections joined by pipe(): bytes written into one side
	// come out the other, and vice versa.
	a1, a2 := tcpPair(t)
	b1, b2 := tcpPair(t)
	go func() { _ = pipe(a2, b1) }()

	if _, err := a1.Write([]byte("ping")); err != nil {
		t.Fatalf("write a1: %v", err)
	}
	assertRead(t, b2, "ping")

	if _, err := b2.Write([]byte("pong")); err != nil {
		t.Fatalf("write b2: %v", err)
	}
	assertRead(t, a1, "pong")
}

// tcpPair returns two ends of a loopback TCP connection.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = r.c.Close() })
	return client, r.c
}

func assertRead(t *testing.T, c net.Conn, want string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != want {
		t.Fatalf("read %q, want %q", buf, want)
	}
}
