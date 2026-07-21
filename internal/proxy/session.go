package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/sachhg/pgpilot/internal/protocol"
)

// sslNotSupported is the single byte a server sends to refuse an SSLRequest or
// GSSENCRequest, telling the client to continue in cleartext.
const sslNotSupported = 'N'

// session proxies one client connection: it refuses encryption negotiation,
// forwards the client's startup packet to the upstream untouched, then relays
// protocol messages in both directions, tracking transaction status as it goes.
type session struct {
	client   net.Conn
	upstream string
	dialer   *net.Dialer
	log      *slog.Logger
	tracker  *protocol.TxTracker
}

// serve runs the session to completion. When ctx is cancelled (server
// shutdown), both connections are closed, which unblocks the relay, so no
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

	if err := s.relay(s.client, up); err != nil {
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

// relay copies protocol messages in both directions until either side closes.
// Bytes are forwarded verbatim, so the relay stays transparent even for messages
// it does not interpret; backend messages are additionally decoded far enough to
// track transaction status. Each direction half-closes on EOF so both drain.
func (s *session) relay(client, up net.Conn) error {
	var wg sync.WaitGroup
	errc := make(chan error, 2)
	wg.Add(2)

	go func() { // frontend: client -> upstream
		defer wg.Done()
		err := protocol.Relay(up, client, nil)
		if hc, ok := up.(halfCloser); ok {
			_ = hc.CloseWrite()
		}
		errc <- err
	}()
	go func() { // backend: upstream -> client
		defer wg.Done()
		err := protocol.Relay(client, up, s.trackBackend)
		if hc, ok := client.(halfCloser); ok {
			_ = hc.CloseWrite()
		}
		errc <- err
	}()

	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil && !isClosedConnErr(err) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
	}
	return nil
}

// trackBackend updates the session's transaction status from ReadyForQuery.
func (s *session) trackBackend(msgType byte, body []byte) error {
	if msgType == protocol.MsgReadyForQuery {
		if st, ok := protocol.ParseReadyForQuery(body); ok && s.tracker.Update(st) {
			s.log.Debug("transaction status", "status", st.String())
		}
	}
	return nil
}

// halfCloser is implemented by *net.TCPConn: it lets one direction signal EOF
// without tearing down the other, so each side can drain fully.
type halfCloser interface {
	CloseWrite() error
}

// isClosedConnErr reports whether err is the "use of closed network connection"
// error produced when a connection is closed during shutdown.
func isClosedConnErr(err error) bool {
	return errors.Is(err, net.ErrClosed)
}
