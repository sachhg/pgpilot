package protocol

import (
	"bytes"
	"io"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
)

func FuzzDecodeFrontend(f *testing.F) {
	q, _ := (&pgproto3.Query{String: "SELECT 1"}).Encode(nil)
	f.Add(byte(MsgQuery), q[5:])
	p, _ := (&pgproto3.Parse{Query: "SELECT $1"}).Encode(nil)
	f.Add(byte(MsgParse), p[5:])
	f.Add(byte('?'), []byte{0, 1, 2, 3})
	f.Add(byte(MsgQuery), []byte{}) // empty body: pgproto3 panics without a guard

	f.Fuzz(func(t *testing.T, msgType byte, body []byte) {
		msg, err := DecodeFrontend(msgType, body)
		if err == nil && msg != nil {
			if _, err := msg.Encode(nil); err != nil {
				t.Fatalf("re-encode after a successful decode failed: %v", err)
			}
		}
	})
}

func FuzzDecodeBackend(f *testing.F) {
	z, _ := (&pgproto3.ReadyForQuery{TxStatus: 'I'}).Encode(nil)
	f.Add(byte(MsgReadyForQuery), z[5:])
	d, _ := (&pgproto3.DataRow{Values: [][]byte{[]byte("x")}}).Encode(nil)
	f.Add(byte(MsgDataRow), d[5:])
	f.Add(byte('?'), []byte{0, 1, 2, 3})

	f.Fuzz(func(t *testing.T, msgType byte, body []byte) {
		msg, err := DecodeBackend(msgType, body)
		if err == nil && msg != nil {
			_, _ = msg.Encode(nil)
		}
	})
}

func FuzzRelay(f *testing.F) {
	f.Add(framed(MsgQuery, []byte("SELECT 1\x00")))
	f.Add(append(framed(MsgReadyForQuery, []byte{'I'}), framed(MsgDataRow, []byte{0, 0, 0, 0})...))
	f.Add([]byte{MsgQuery, 0xff, 0xff, 0xff, 0xff}) // bogus multi-GB length header

	f.Fuzz(func(t *testing.T, data []byte) {
		_ = Relay(io.Discard, bytes.NewReader(data), func(byte, []byte) error { return nil })
	})
}
