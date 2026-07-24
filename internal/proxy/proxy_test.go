package proxy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sachhg/pgpilot/internal/scram"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// framedMessage builds a length-prefixed protocol message (type, length, body).
func framedMessage(msgType byte, body []byte) []byte {
	out := make([]byte, 5+len(body))
	out[0] = msgType
	binary.BigEndian.PutUint32(out[1:5], uint32(4+len(body)))
	copy(out[5:], body)
	return out
}

// scramClientHandshake plays a SCRAM-SHA-256 client against pgpilot's server side
// over conn, expecting to reach AuthenticationOk.
func scramClientHandshake(conn net.Conn, password string) error {
	fe := pgproto3.NewFrontend(conn, conn)

	msg, err := fe.Receive()
	if err != nil {
		return err
	}
	if _, ok := msg.(*pgproto3.AuthenticationSASL); !ok {
		return fmt.Errorf("expected AuthenticationSASL, got %T", msg)
	}
	client, err := scram.NewClient(password)
	if err != nil {
		return err
	}
	fe.Send(&pgproto3.SASLInitialResponse{AuthMechanism: scramMechanism, Data: []byte(client.FirstMessage())})
	if err := fe.Flush(); err != nil {
		return err
	}

	msg, err = fe.Receive()
	if err != nil {
		return err
	}
	cont, ok := msg.(*pgproto3.AuthenticationSASLContinue)
	if !ok {
		return fmt.Errorf("expected AuthenticationSASLContinue, got %T", msg)
	}
	final, err := client.HandleServerFirst(string(cont.Data))
	if err != nil {
		return err
	}
	fe.Send(&pgproto3.SASLResponse{Data: []byte(final)})
	if err := fe.Flush(); err != nil {
		return err
	}

	msg, err = fe.Receive()
	if err != nil {
		return err
	}
	fin, ok := msg.(*pgproto3.AuthenticationSASLFinal)
	if !ok {
		return fmt.Errorf("expected AuthenticationSASLFinal, got %T", msg)
	}
	if err := client.HandleServerFinal(string(fin.Data)); err != nil {
		return err
	}

	msg, err = fe.Receive()
	if err != nil {
		return err
	}
	if _, ok := msg.(*pgproto3.AuthenticationOk); !ok {
		return fmt.Errorf("expected AuthenticationOk, got %T", msg)
	}
	return nil
}

func TestAuthenticateClient_Succeeds(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer func() { _ = clientEnd.Close() }()
	defer func() { _ = serverEnd.Close() }()
	_ = clientEnd.SetDeadline(time.Now().Add(3 * time.Second))
	_ = serverEnd.SetDeadline(time.Now().Add(3 * time.Second))

	sess := &session{client: serverEnd, log: discardLogger()}
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- sess.authenticateClient(pgproto3.NewBackend(serverEnd, serverEnd), "pencil")
	}()

	if err := scramClientHandshake(clientEnd, "pencil"); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-srvErr; err != nil {
		t.Fatalf("authenticateClient: %v", err)
	}
}

func TestAuthenticateClient_WrongPassword(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer func() { _ = clientEnd.Close() }()
	defer func() { _ = serverEnd.Close() }()
	_ = clientEnd.SetDeadline(time.Now().Add(3 * time.Second))
	_ = serverEnd.SetDeadline(time.Now().Add(3 * time.Second))

	sess := &session{client: serverEnd, log: discardLogger()}
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- sess.authenticateClient(pgproto3.NewBackend(serverEnd, serverEnd), "correct")
	}()

	_ = scramClientHandshake(clientEnd, "wrong")
	if err := <-srvErr; err == nil {
		t.Fatal("authenticateClient accepted a wrong password")
	}
}

func TestRelayFrontend_InterceptsTerminate(t *testing.T) {
	query := framedMessage('Q', []byte("SELECT 1\x00"))
	term := framedMessage('X', nil)
	src := bytes.NewReader(append(append([]byte{}, query...), term...))

	var dst bytes.Buffer
	terminated, err := relayFrontend(&dst, src)
	if err != nil {
		t.Fatalf("relayFrontend: %v", err)
	}
	if !terminated {
		t.Error("terminated = false, want true")
	}
	if !bytes.Equal(dst.Bytes(), query) {
		t.Errorf("forwarded %x, want only the query %x (Terminate must not be forwarded)", dst.Bytes(), query)
	}
}

func TestRelayFrontend_CloseWithoutTerminate(t *testing.T) {
	query := framedMessage('Q', []byte("x\x00"))
	var dst bytes.Buffer
	terminated, err := relayFrontend(&dst, bytes.NewReader(query))
	if err != nil {
		t.Fatalf("relayFrontend: %v", err)
	}
	if terminated {
		t.Error("terminated = true on EOF without Terminate, want false")
	}
	if !bytes.Equal(dst.Bytes(), query) {
		t.Error("query was not forwarded")
	}
}

func TestParseStartupParams(t *testing.T) {
	sm := &pgproto3.StartupMessage{
		ProtocolVersion: 196608,
		Parameters:      map[string]string{"user": "alice", "database": "shop"},
	}
	wire, err := sm.Encode(nil)
	if err != nil {
		t.Fatal(err)
	}
	params, err := parseStartupParams(startupPacket{raw: wire, code: 196608})
	if err != nil {
		t.Fatalf("parseStartupParams: %v", err)
	}
	if params["user"] != "alice" || params["database"] != "shop" {
		t.Errorf("params = %v", params)
	}
}
