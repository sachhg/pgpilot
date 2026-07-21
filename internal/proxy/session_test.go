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
