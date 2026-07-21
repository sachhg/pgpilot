package protocol

import (
	"bytes"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"
)

func TestRoundTrip_FrontendMessages(t *testing.T) {
	cases := []struct {
		name string
		msg  pgproto3.FrontendMessage
	}{
		{"Query", &pgproto3.Query{String: "SELECT 1"}},
		{"Parse", &pgproto3.Parse{Name: "s1", Query: "SELECT $1", ParameterOIDs: []uint32{23}}},
		{"Bind", &pgproto3.Bind{
			PreparedStatement:    "s1",
			ParameterFormatCodes: []int16{0},
			Parameters:           [][]byte{[]byte("42")},
			ResultFormatCodes:    []int16{0},
		}},
		{"Describe", &pgproto3.Describe{ObjectType: 'S', Name: "s1"}},
		{"Execute", &pgproto3.Execute{Portal: "", MaxRows: 0}},
		{"Sync", &pgproto3.Sync{}},
		{"Terminate", &pgproto3.Terminate{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := tc.msg.Encode(nil)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, err := DecodeFrontend(wire[0], wire[5:])
			if err != nil {
				t.Fatalf("DecodeFrontend: %v", err)
			}
			reenc, err := decoded.Encode(nil)
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			if !bytes.Equal(wire, reenc) {
				t.Errorf("round-trip mismatch:\n in = %x\nout = %x", wire, reenc)
			}
		})
	}
}

func TestRoundTrip_BackendMessages(t *testing.T) {
	cases := []struct {
		name string
		msg  pgproto3.BackendMessage
	}{
		{"ReadyForQuery", &pgproto3.ReadyForQuery{TxStatus: 'I'}},
		{"CommandComplete", &pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")}},
		{"ErrorResponse", &pgproto3.ErrorResponse{Severity: "ERROR", Code: "42P01", Message: "relation does not exist"}},
		{"RowDescription", &pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
			{Name: []byte("id"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1, Format: 0},
		}}},
		{"DataRow", &pgproto3.DataRow{Values: [][]byte{[]byte("1"), []byte("alice")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := tc.msg.Encode(nil)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, err := DecodeBackend(wire[0], wire[5:])
			if err != nil {
				t.Fatalf("DecodeBackend: %v", err)
			}
			reenc, err := decoded.Encode(nil)
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			if !bytes.Equal(wire, reenc) {
				t.Errorf("round-trip mismatch:\n in = %x\nout = %x", wire, reenc)
			}
		})
	}
}

func TestDecode_MalformedDoesNotPanic(t *testing.T) {
	// pgproto3's decoders can panic on truncated or empty bodies; DecodeFrontend
	// and DecodeBackend must turn that into an error instead. Regression for a
	// crash surfaced by FuzzDecodeFrontend (an empty Query body). Reaching the
	// end of this test without panicking is the assertion.
	bodies := [][]byte{nil, {}, {0x00}, {0xff, 0xff, 0xff}}
	for _, ty := range []byte{MsgQuery, MsgParse, MsgBind, MsgDescribe, MsgExecute, MsgSync, MsgTerminate} {
		for _, body := range bodies {
			_, _ = DecodeFrontend(ty, body)
		}
	}
	for _, ty := range []byte{MsgReadyForQuery, MsgCommandComplete, MsgErrorResponse, MsgRowDescription, MsgDataRow} {
		for _, body := range bodies {
			_, _ = DecodeBackend(ty, body)
		}
	}
}

func TestDecode_UnsupportedType(t *testing.T) {
	if _, err := DecodeFrontend(MsgReadyForQuery, nil); err == nil {
		t.Error("expected an error for an unsupported frontend type")
	}
	if _, err := DecodeBackend(MsgQuery, nil); err == nil {
		t.Error("expected an error for an unsupported backend type")
	}
}
