// Package protocol decodes the PostgreSQL v3 wire protocol messages that
// pgpilot needs to understand — using jackc/pgx's pgproto3 for the message
// bodies — and relays a byte stream message-by-message rather than opaquely.
// It also tracks per-session transaction status from ReadyForQuery.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// maxInspectBody bounds how large a message body may be before Relay stops
// buffering it for inspection and streams it straight through instead. It keeps
// a hostile or simply large message (a wide DataRow, a big CopyData) from
// forcing an unbounded allocation.
const maxInspectBody = 1 << 16 // 64 KiB

// Frontend (client -> server) message type bytes.
const (
	MsgQuery     = 'Q'
	MsgParse     = 'P'
	MsgBind      = 'B'
	MsgDescribe  = 'D'
	MsgExecute   = 'E'
	MsgSync      = 'S'
	MsgTerminate = 'X'
)

// Backend (server -> client) message type bytes. A type byte's meaning depends
// on direction: 'D' is Describe from a client but DataRow from a server, 'E' is
// Execute versus ErrorResponse, and so on — which is why decoding is split into
// DecodeFrontend and DecodeBackend.
const (
	MsgReadyForQuery   = 'Z'
	MsgCommandComplete = 'C'
	MsgErrorResponse   = 'E'
	MsgRowDescription  = 'T'
	MsgDataRow         = 'D'
)

// InspectFunc is called for each relayed message with its type byte and body.
// The body is nil for messages larger than the inspection limit, which are
// streamed rather than buffered. Returning an error aborts the relay.
type InspectFunc func(msgType byte, body []byte) error

// Relay reads length-prefixed protocol messages from src, writes each one to
// dst byte-for-byte, and hands it to inspect (which may be nil). It returns nil
// when src reaches a clean message boundary and closes; any other read/write
// failure is returned to the caller.
//
// Relay operates on the regular message phase, after the startup/authentication
// handshake, where every message is framed as a one-byte type, a four-byte
// length (which counts itself but not the type byte), and a body.
func Relay(dst io.Writer, src io.Reader, inspect InspectFunc) error {
	var header [5]byte
	for {
		if _, err := io.ReadFull(src, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil // clean end at a message boundary
			}
			return err
		}
		length := binary.BigEndian.Uint32(header[1:5])
		if length < 4 {
			return fmt.Errorf("protocol: message length %d below minimum of 4", length)
		}
		bodyLen := length - 4

		if _, err := dst.Write(header[:]); err != nil {
			return err
		}

		if bodyLen <= maxInspectBody {
			body := make([]byte, bodyLen)
			if _, err := io.ReadFull(src, body); err != nil {
				return err
			}
			if inspect != nil {
				if err := inspect(header[0], body); err != nil {
					return err
				}
			}
			if _, err := dst.Write(body); err != nil {
				return err
			}
			continue
		}

		if inspect != nil {
			if err := inspect(header[0], nil); err != nil {
				return err
			}
		}
		if _, err := io.CopyN(dst, src, int64(bodyLen)); err != nil {
			return err
		}
	}
}

// ParseReadyForQuery extracts the transaction status from a ReadyForQuery
// message body, which is a single indicator byte.
func ParseReadyForQuery(body []byte) (TxStatus, bool) {
	if len(body) != 1 {
		return 0, false
	}
	st := TxStatus(body[0])
	if !st.Valid() {
		return 0, false
	}
	return st, true
}
