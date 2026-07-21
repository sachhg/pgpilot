package protocol

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgproto3"
)

// DecodeFrontend decodes a frontend (client-sent) message body of the given
// type into a typed pgproto3 message. Only the message types pgpilot routes on
// are supported; others return an error.
func DecodeFrontend(msgType byte, body []byte) (pgproto3.FrontendMessage, error) {
	var msg pgproto3.FrontendMessage
	switch msgType {
	case MsgQuery:
		msg = &pgproto3.Query{}
	case MsgParse:
		msg = &pgproto3.Parse{}
	case MsgBind:
		msg = &pgproto3.Bind{}
	case MsgDescribe:
		msg = &pgproto3.Describe{}
	case MsgExecute:
		msg = &pgproto3.Execute{}
	case MsgSync:
		msg = &pgproto3.Sync{}
	case MsgTerminate:
		msg = &pgproto3.Terminate{}
	default:
		return nil, fmt.Errorf("protocol: unsupported frontend message type %q", msgType)
	}
	if err := decodeSafely(msg.Decode, body); err != nil {
		return nil, fmt.Errorf("protocol: decode frontend %q: %w", msgType, err)
	}
	return msg, nil
}

// DecodeBackend decodes a backend (server-sent) message body of the given type
// into a typed pgproto3 message. Only the message types pgpilot routes on are
// supported; others return an error.
func DecodeBackend(msgType byte, body []byte) (pgproto3.BackendMessage, error) {
	var msg pgproto3.BackendMessage
	switch msgType {
	case MsgReadyForQuery:
		msg = &pgproto3.ReadyForQuery{}
	case MsgCommandComplete:
		msg = &pgproto3.CommandComplete{}
	case MsgErrorResponse:
		msg = &pgproto3.ErrorResponse{}
	case MsgRowDescription:
		msg = &pgproto3.RowDescription{}
	case MsgDataRow:
		msg = &pgproto3.DataRow{}
	default:
		return nil, fmt.Errorf("protocol: unsupported backend message type %q", msgType)
	}
	if err := decodeSafely(msg.Decode, body); err != nil {
		return nil, fmt.Errorf("protocol: decode backend %q: %w", msgType, err)
	}
	return msg, nil
}

// decodeSafely runs a pgproto3 Decode and converts a panic into an error.
// pgproto3's decoders can panic on truncated or malformed bodies (for example,
// an empty Query body); pgpilot decodes bytes straight off the wire, so it must
// never let such input crash the process.
func decodeSafely(decode func([]byte) error, body []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return decode(body)
}
