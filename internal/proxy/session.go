package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// sslNotSupported is the single byte a server sends to refuse an SSLRequest or
// GSSENCRequest, telling the client to continue in cleartext.
const sslNotSupported = 'N'

// session proxies one client connection: it refuses encryption negotiation,
// forwards the client's startup packet to the upstream untouched, then pipes
// bytes in both directions until either side closes.
type session struct {
	client   net.Conn
	upstream string
	dialer   *net.Dialer
}

// serve runs the session to completion. When ctx is cancelled (server
// shutdown), both connections are closed, which unblocks the copy loops, so no
// goroutine outlives serve.
func (s *session) serve(ctx context.Context) error {
	defer func() { _ = s.client.Close() }()

	pkt, err := s.negotiateStartup()
	if err != nil {
		return fmt.Errorf("startup negotiation: %w", err)
	}

	up, err := s.dialer.DialContext(ctx, "tcp", s.upstream)
	if err != nil {
		return fmt.Errorf("dial upstream %s: %w", s.upstream, err)
	}

	// Close both ends when ctx is cancelled, and stop the watcher
	// deterministically when the session ends on its own.
	ctx, cancel := context.WithCancel(ctx)
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		<-ctx.Done()
		_ = s.client.Close()
		_ = up.Close()
	}()
	defer func() {
		cancel()
		watcher.Wait()
	}()

	// Forward the client's startup packet to the primary untouched.
	if _, err := up.Write(pkt.raw); err != nil {
		return fmt.Errorf("forward startup packet: %w", err)
	}

	if err := pipe(s.client, up); err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	return nil
}

// negotiateStartup answers any SSLRequest/GSSENCRequest with a refusal until the
// client sends a real startup (or cancel) packet, which it returns unmodified
// for forwarding.
func (s *session) negotiateStartup() (startupPacket, error) {
	for {
		pkt, err := readStartupPacket(s.client)
		if err != nil {
			return startupPacket{}, err
		}
		if pkt.isSSLRequest() || pkt.isGSSEncRequest() {
			if _, err := s.client.Write([]byte{sslNotSupported}); err != nil {
				return startupPacket{}, fmt.Errorf("write ssl refusal: %w", err)
			}
			continue
		}
		return pkt, nil
	}
}

// halfCloser is implemented by *net.TCPConn: it lets one direction signal EOF
// without tearing down the other, so each side can drain fully.
type halfCloser interface {
	CloseWrite() error
}

// pipe copies bytes in both directions between a and b until both directions
// reach EOF, then returns. Closing a connection elsewhere (on shutdown)
// unblocks the copies, so pipe never leaves a goroutine running.
func pipe(a, b net.Conn) error {
	var wg sync.WaitGroup
	errc := make(chan error, 2)
	wg.Add(2)
	copyDir := func(dst, src net.Conn) {
		defer wg.Done()
		_, err := io.Copy(dst, src)
		if hc, ok := dst.(halfCloser); ok {
			_ = hc.CloseWrite()
		}
		errc <- err
	}
	go copyDir(a, b)
	go copyDir(b, a)
	wg.Wait()
	close(errc)

	for err := range errc {
		if err != nil && !errors.Is(err, io.EOF) && !isClosedConnErr(err) {
			return err
		}
	}
	return nil
}

// isClosedConnErr reports whether err is the "use of closed network connection"
// error produced when a connection is closed during shutdown.
func isClosedConnErr(err error) bool {
	return errors.Is(err, net.ErrClosed)
}
