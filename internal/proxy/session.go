package proxy

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sachhg/pgpilot/internal/backend"
	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/detect"
	"github.com/sachhg/pgpilot/internal/protocol"
	"github.com/sachhg/pgpilot/internal/scram"
)

const (
	// sslNotSupported is the single byte a server sends to refuse an SSLRequest
	// or GSSENCRequest, telling the client to continue in cleartext.
	sslNotSupported = 'N'
	scramMechanism  = "SCRAM-SHA-256"
	resetTimeout    = 5 * time.Second
)

// session authenticates one client, acquires a pooled backend for its
// (user, database), and relays protocol messages between them, returning the
// backend to the pool when the client disconnects cleanly.
type session struct {
	client  net.Conn
	cfg     *config.Config
	manager *backend.Manager
	log     *slog.Logger
	tracker *protocol.TxTracker
}

// serve runs the session to completion.
func (s *session) serve(ctx context.Context) error {
	defer func() { _ = s.client.Close() }()

	pkt, err := s.negotiateStartup()
	if err != nil {
		return fmt.Errorf("startup negotiation: %w", err)
	}
	if pkt.isCancelRequest() {
		// Cancel requests cannot yet be mapped to a pooled backend; ignore.
		s.log.Debug("ignoring unsupported cancel request")
		return nil
	}

	params, err := parseStartupParams(pkt)
	if err != nil {
		return s.reject("08P01", "invalid startup packet")
	}
	user := params["user"]
	if user == "" {
		return s.reject("08P01", "startup packet has no user")
	}
	database := params["database"]
	if database == "" {
		database = user
	}

	u, ok := s.cfg.User(user)
	if !ok {
		return s.reject("28P01", fmt.Sprintf("password authentication failed for user %q", user))
	}

	clientBackend := pgproto3.NewBackend(s.client, s.client)
	if err := s.authenticateClient(clientBackend, u.Password); err != nil {
		return err
	}

	be, err := s.manager.Acquire(ctx, user, database)
	if err != nil {
		return s.reject("53300", "could not acquire a backend connection")
	}
	if err := s.completeStartup(clientBackend, be); err != nil {
		s.manager.Discard(user, database, be)
		return fmt.Errorf("complete startup: %w", err)
	}

	if s.cfg.Pool.Mode == config.ModeTransaction {
		return s.serveTransaction(ctx, user, database, be)
	}

	// Session mode: hold the backend for the whole session.
	clean := false
	defer func() { s.finishBackend(user, database, be, clean) }()
	clean = s.relayPooled(ctx, be)
	return nil
}

// serveTransaction relays in transaction-pooling mode: a backend is acquired for
// a transaction and returned to the pool once the transaction ends, so idle
// clients do not hold backends. A session that uses a feature transaction
// pooling cannot share safely — a prepared statement, temp table, LISTEN, or
// session GUC, or the extended query protocol — is pinned to its backend for the
// rest of its life.
func (s *session) serveTransaction(ctx context.Context, user, database string, initial *backend.Conn) error {
	held := initial
	pinned := false
	clean := false

	watchDone := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		select {
		case <-ctx.Done():
			_ = s.client.Close()
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		watcher.Wait()
		if held != nil {
			s.finishBackend(user, database, held, clean)
		}
	}()

	for {
		msgType, full, err := readMessage(s.client)
		if err != nil {
			return nil // client closed, shutdown, or read error ends the session
		}
		if msgType == protocol.MsgTerminate {
			clean = true
			return nil
		}

		if held == nil {
			held, err = s.manager.Acquire(ctx, user, database)
			if err != nil {
				return s.reject("53300", "could not acquire a backend connection")
			}
		}

		if msgType != protocol.MsgQuery {
			// The extended query protocol spans several messages before a
			// response; rather than track that state machine, pin the session by
			// handing off to the continuous relay for the rest of its life.
			if _, err := held.NetConn().Write(full); err != nil {
				s.manager.Discard(user, database, held)
				held = nil
				return nil
			}
			ok := s.relayPooled(ctx, held)
			s.finishBackend(user, database, held, ok)
			held = nil
			return nil
		}

		if !pinned {
			if feat, breaks := detect.BreaksTxPooling(queryString(full)); breaks {
				pinned = true
				s.log.Debug("pinning session to its backend", "feature", string(feat))
			}
		}

		if _, err := held.NetConn().Write(full); err != nil {
			s.manager.Discard(user, database, held)
			held = nil
			return nil
		}
		status, err := relayResponse(s.client, held.NetConn())
		if err != nil {
			s.manager.Discard(user, database, held)
			held = nil
			return nil
		}
		if status == protocol.StatusIdle && !pinned {
			s.releaseBetweenTransactions(user, database, held)
			held = nil
		}
	}
}

// authenticateClient runs the server side of the SCRAM-SHA-256 exchange against
// the client, sending AuthenticationOk on success. On failure it sends a FATAL
// ErrorResponse and returns an error.
func (s *session) authenticateClient(be *pgproto3.Backend, password string) error {
	be.Send(&pgproto3.AuthenticationSASL{AuthMechanisms: []string{scramMechanism}})
	if err := be.Flush(); err != nil {
		return fmt.Errorf("send AuthenticationSASL: %w", err)
	}

	srv, err := scram.NewServer(password)
	if err != nil {
		return err
	}

	if err := be.SetAuthType(pgproto3.AuthTypeSASL); err != nil {
		return err
	}
	msg, err := be.Receive()
	if err != nil {
		return fmt.Errorf("receive SASL initial response: %w", err)
	}
	initial, ok := msg.(*pgproto3.SASLInitialResponse)
	if !ok {
		return fmt.Errorf("expected SASLInitialResponse, got %T", msg)
	}
	if initial.AuthMechanism != scramMechanism {
		return s.reject("28P01", "unsupported SASL mechanism")
	}
	serverFirst, err := srv.HandleClientFirst(string(initial.Data))
	if err != nil {
		return s.reject("28P01", "SCRAM negotiation failed")
	}
	be.Send(&pgproto3.AuthenticationSASLContinue{Data: []byte(serverFirst)})
	if err := be.Flush(); err != nil {
		return err
	}

	if err := be.SetAuthType(pgproto3.AuthTypeSASLContinue); err != nil {
		return err
	}
	msg, err = be.Receive()
	if err != nil {
		return fmt.Errorf("receive SASL response: %w", err)
	}
	resp, ok := msg.(*pgproto3.SASLResponse)
	if !ok {
		return fmt.Errorf("expected SASLResponse, got %T", msg)
	}
	serverFinal, err := srv.HandleClientFinal(string(resp.Data))
	if err != nil {
		return s.reject("28P01", "password authentication failed")
	}
	be.Send(&pgproto3.AuthenticationSASLFinal{Data: []byte(serverFinal)})
	be.Send(&pgproto3.AuthenticationOk{})
	if err := be.Flush(); err != nil {
		return err
	}
	return nil
}

// completeStartup finishes the client's startup after authentication: it replays
// the backend's parameters, sends a synthesized BackendKeyData, and reports the
// session ready.
func (s *session) completeStartup(be *pgproto3.Backend, conn *backend.Conn) error {
	for name, value := range conn.Params() {
		be.Send(&pgproto3.ParameterStatus{Name: name, Value: value})
	}
	pid, key, err := randomKeyData()
	if err != nil {
		return err
	}
	be.Send(&pgproto3.BackendKeyData{ProcessID: pid, SecretKey: key})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: byte(protocol.StatusIdle)})
	return be.Flush()
}

// relayPooled relays messages between the client and the backend until the
// client disconnects, and reports whether the backend is clean enough to reuse.
// The client's Terminate is intercepted, not forwarded, so the backend survives
// for the next client.
func (s *session) relayPooled(ctx context.Context, be *backend.Conn) (clean bool) {
	backendConn := be.NetConn()

	// On server shutdown, close the client and interrupt the backend read so
	// both relay directions unwind; on normal completion the watcher does
	// nothing.
	watchDone := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		select {
		case <-ctx.Done():
			_ = s.client.Close()
			_ = backendConn.SetReadDeadline(time.Now())
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		watcher.Wait()
	}()

	beDone := make(chan error, 1)
	go func() {
		err := protocol.Relay(s.client, backendConn, s.trackBackend)
		_ = s.client.Close() // unblock the frontend relay if the backend ends first
		beDone <- err
	}()

	terminated, feErr := relayFrontend(backendConn, s.client)

	select {
	case <-beDone:
		// The backend relay ended on its own (backend closed, errored, or we are
		// shutting down): the connection is not reusable.
		return false
	default:
		_ = backendConn.SetReadDeadline(time.Now()) // interrupt the idle backend read
		<-beDone
		_ = backendConn.SetReadDeadline(time.Time{})
	}

	return terminated && feErr == nil && ctx.Err() == nil
}

// finishBackend returns the backend to its pool when the session ended cleanly,
// resetting its state first; a reset failure or an unclean end discards it.
func (s *session) finishBackend(user, database string, be *backend.Conn, clean bool) {
	if !clean {
		s.manager.Discard(user, database, be)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), resetTimeout)
	defer cancel()
	if err := be.Reset(ctx); err != nil {
		s.log.Debug("discarding backend that failed to reset", "error", err)
		s.manager.Discard(user, database, be)
		return
	}
	s.manager.Release(user, database, be)
}

// releaseBetweenTransactions returns an idle backend to its pool between
// transactions in transaction mode, clearing session state with DISCARD ALL so
// the next client sees a clean connection.
func (s *session) releaseBetweenTransactions(user, database string, be *backend.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), resetTimeout)
	defer cancel()
	if err := be.DiscardAll(ctx); err != nil {
		s.log.Debug("discarding backend that failed to reset", "error", err)
		s.manager.Discard(user, database, be)
		return
	}
	s.manager.Release(user, database, be)
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

// negotiateStartup answers any SSLRequest/GSSENCRequest with a refusal until the
// client sends a real startup (or cancel) packet, which it returns.
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

// reject sends a FATAL ErrorResponse to the client and returns an error.
func (s *session) reject(code, message string) error {
	e := &pgproto3.ErrorResponse{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: code, Message: message}
	if buf, err := e.Encode(nil); err == nil {
		_, _ = s.client.Write(buf)
	}
	return fmt.Errorf("proxy: rejected client: %s %s", code, message)
}

// relayFrontend forwards frontend messages from src to dst until the client
// sends Terminate (which is not forwarded, so the backend can be reused) or the
// connection ends. It reports whether the client terminated cleanly.
func relayFrontend(dst io.Writer, src io.Reader) (terminated bool, err error) {
	var header [5]byte
	for {
		if _, err := io.ReadFull(src, header[:]); err != nil {
			if errors.Is(err, io.EOF) || isClosedConnErr(err) || errors.Is(err, os.ErrDeadlineExceeded) {
				return false, nil
			}
			return false, err
		}
		if header[0] == protocol.MsgTerminate {
			return true, nil
		}
		length := binary.BigEndian.Uint32(header[1:5])
		if length < 4 {
			return false, fmt.Errorf("proxy: frontend message length %d below minimum", length)
		}
		if _, err := dst.Write(header[:]); err != nil {
			return false, err
		}
		if _, err := io.CopyN(dst, src, int64(length-4)); err != nil {
			return false, err
		}
	}
}

// maxBufferedMessage bounds a frontend message that transaction mode buffers
// whole (a query or the first extended-protocol message); larger traffic flows
// through the streaming relay instead.
const maxBufferedMessage = 64 << 20

// readMessage reads one whole length-prefixed message and returns its type byte
// and full bytes (header included), for forwarding.
func readMessage(r io.Reader) (msgType byte, full []byte, err error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:5])
	if length < 4 {
		return 0, nil, fmt.Errorf("proxy: message length %d below minimum", length)
	}
	bodyLen := length - 4
	if bodyLen > maxBufferedMessage {
		return 0, nil, fmt.Errorf("proxy: message body %d exceeds the buffered limit", bodyLen)
	}
	full = make([]byte, 5+bodyLen)
	copy(full, header[:])
	if _, err := io.ReadFull(r, full[5:]); err != nil {
		return 0, nil, err
	}
	return header[0], full, nil
}

// relayResponse forwards backend messages from src to dst up to and including the
// next ReadyForQuery, whose transaction-status byte it returns. Large message
// bodies are streamed rather than buffered.
func relayResponse(dst io.Writer, src io.Reader) (protocol.TxStatus, error) {
	var header [5]byte
	for {
		if _, err := io.ReadFull(src, header[:]); err != nil {
			return 0, err
		}
		length := binary.BigEndian.Uint32(header[1:5])
		if length < 4 {
			return 0, fmt.Errorf("proxy: backend message length %d below minimum", length)
		}
		if _, err := dst.Write(header[:]); err != nil {
			return 0, err
		}
		bodyLen := int64(length - 4)
		if header[0] == protocol.MsgReadyForQuery {
			if bodyLen < 1 {
				return 0, fmt.Errorf("proxy: empty ReadyForQuery")
			}
			var status [1]byte
			if _, err := io.ReadFull(src, status[:]); err != nil {
				return 0, err
			}
			if _, err := dst.Write(status[:]); err != nil {
				return 0, err
			}
			if bodyLen > 1 {
				if _, err := io.CopyN(dst, src, bodyLen-1); err != nil {
					return 0, err
				}
			}
			return protocol.TxStatus(status[0]), nil
		}
		if _, err := io.CopyN(dst, src, bodyLen); err != nil {
			return 0, err
		}
	}
}

// queryString extracts the SQL text from a simple Query message (type + length +
// NUL-terminated string).
func queryString(full []byte) string {
	if len(full) < 6 {
		return ""
	}
	return string(full[5 : len(full)-1])
}

// parseStartupParams decodes the StartupMessage parameters (user, database, ...).
func parseStartupParams(pkt startupPacket) (map[string]string, error) {
	var sm pgproto3.StartupMessage
	if err := sm.Decode(pkt.raw[4:]); err != nil {
		return nil, err
	}
	return sm.Parameters, nil
}

// randomKeyData generates a synthesized BackendKeyData for the client. pgpilot
// does not yet support cancellation, so these values are not mapped to a backend.
func randomKeyData() (pid, key uint32, err error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, 0, err
	}
	return binary.BigEndian.Uint32(b[0:4]), binary.BigEndian.Uint32(b[4:8]), nil
}

// isClosedConnErr reports whether err is the "use of closed network connection"
// error produced when a connection is closed during shutdown.
func isClosedConnErr(err error) bool {
	return errors.Is(err, net.ErrClosed)
}
