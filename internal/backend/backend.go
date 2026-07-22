// Package backend opens and authenticates pgpilot's own connections to a
// PostgreSQL backend using SCRAM-SHA-256, leaving them ready to relay client
// traffic. The connections are what the pool manages.
package backend

import (
	"context"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sachhg/pgpilot/internal/scram"
)

// protocolVersion3 is the PostgreSQL 3.0 protocol version.
const protocolVersion3 = 196608

const scramMechanism = "SCRAM-SHA-256"

// Conn is an authenticated backend connection, quiescent at ReadyForQuery. It
// implements pool.Conn.
type Conn struct {
	raw    net.Conn
	params map[string]string
	pid    uint32
	key    uint32
}

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.raw.Close() }

// NetConn returns the underlying connection for byte-level relaying. The backend
// is quiescent at ReadyForQuery when Dial returns, so no protocol bytes are
// buffered ahead.
func (c *Conn) NetConn() net.Conn { return c.raw }

// Params returns the ParameterStatus values the backend reported during startup.
func (c *Conn) Params() map[string]string { return c.params }

// Dial opens a TCP connection to addr and authenticates as user/password for the
// given database with SCRAM-SHA-256, returning once the backend reaches
// ReadyForQuery.
func Dial(ctx context.Context, addr, user, password, database string) (*Conn, error) {
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("backend: dial %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	c := &Conn{raw: raw, params: make(map[string]string)}
	if err := c.authenticate(user, password, database); err != nil {
		_ = raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{}) // clear the handshake deadline before relaying
	return c, nil
}

func (c *Conn) authenticate(user, password, database string) error {
	fe := pgproto3.NewFrontend(c.raw, c.raw)
	fe.Send(&pgproto3.StartupMessage{
		ProtocolVersion: protocolVersion3,
		Parameters:      map[string]string{"user": user, "database": database},
	})
	if err := fe.Flush(); err != nil {
		return fmt.Errorf("backend: send startup: %w", err)
	}

	var sc *scram.Client
	for {
		msg, err := fe.Receive()
		if err != nil {
			return fmt.Errorf("backend: receive during startup: %w", err)
		}
		switch m := msg.(type) {
		case *pgproto3.AuthenticationSASL:
			if !slices.Contains(m.AuthMechanisms, scramMechanism) {
				return fmt.Errorf("backend: server offered unsupported SASL mechanisms %v", m.AuthMechanisms)
			}
			sc, err = scram.NewClient(password)
			if err != nil {
				return err
			}
			fe.Send(&pgproto3.SASLInitialResponse{AuthMechanism: scramMechanism, Data: []byte(sc.FirstMessage())})
			if err := fe.Flush(); err != nil {
				return err
			}
		case *pgproto3.AuthenticationSASLContinue:
			if sc == nil {
				return fmt.Errorf("backend: unexpected SASL continue")
			}
			final, err := sc.HandleServerFirst(string(m.Data))
			if err != nil {
				return err
			}
			fe.Send(&pgproto3.SASLResponse{Data: []byte(final)})
			if err := fe.Flush(); err != nil {
				return err
			}
		case *pgproto3.AuthenticationSASLFinal:
			if sc == nil {
				return fmt.Errorf("backend: unexpected SASL final")
			}
			if err := sc.HandleServerFinal(string(m.Data)); err != nil {
				return err
			}
		case *pgproto3.AuthenticationOk:
			// Authentication done; keep reading to ReadyForQuery.
		case *pgproto3.ParameterStatus:
			c.params[m.Name] = m.Value
		case *pgproto3.BackendKeyData:
			c.pid, c.key = m.ProcessID, m.SecretKey
		case *pgproto3.ReadyForQuery:
			return nil
		case *pgproto3.ErrorResponse:
			return fmt.Errorf("backend: server rejected connection: %s %s", m.Code, m.Message)
		case *pgproto3.NoticeResponse:
			// ignore
		default:
			return fmt.Errorf("backend: unexpected message %T during startup", msg)
		}
	}
}

// Reset returns the connection to a clean state for reuse by another client: it
// rolls back any open transaction and discards session state (prepared
// statements, temp tables, LISTEN registrations, session GUCs), reading to
// ReadyForQuery. Callers should reuse the connection only when Reset succeeds.
//
// ROLLBACK and DISCARD ALL are sent as separate simple queries: combined into
// one multi-statement query they would run inside an implicit transaction block,
// which DISCARD ALL forbids.
func (c *Conn) Reset(ctx context.Context) error {
	fe, restore := c.frontend(ctx)
	defer restore()
	if err := runSimple(fe, "ROLLBACK"); err != nil {
		return fmt.Errorf("backend: reset rollback: %w", err)
	}
	if err := runSimple(fe, "DISCARD ALL"); err != nil {
		return fmt.Errorf("backend: reset discard: %w", err)
	}
	return nil
}

// Ping checks the connection is alive and at a clean ReadyForQuery state by
// running a no-op query. It is suitable as a pool health check.
func (c *Conn) Ping(ctx context.Context) error {
	fe, restore := c.frontend(ctx)
	defer restore()
	if err := runSimple(fe, ";"); err != nil {
		return fmt.Errorf("backend: ping: %w", err)
	}
	return nil
}

// frontend returns a pgproto3 Frontend over the connection, applying ctx's
// deadline for the duration; restore clears it.
func (c *Conn) frontend(ctx context.Context) (fe *pgproto3.Frontend, restore func()) {
	fe = pgproto3.NewFrontend(c.raw, c.raw)
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.raw.SetDeadline(deadline)
		return fe, func() { _ = c.raw.SetDeadline(time.Time{}) }
	}
	return fe, func() {}
}

// runSimple sends one simple query and reads until ReadyForQuery, returning an
// error only on ErrorResponse (notices and command tags are ignored).
func runSimple(fe *pgproto3.Frontend, sql string) error {
	fe.Send(&pgproto3.Query{String: sql})
	if err := fe.Flush(); err != nil {
		return err
	}
	for {
		msg, err := fe.Receive()
		if err != nil {
			return err
		}
		switch m := msg.(type) {
		case *pgproto3.ReadyForQuery:
			return nil
		case *pgproto3.ErrorResponse:
			return fmt.Errorf("%s %s", m.Code, m.Message)
		}
	}
}
