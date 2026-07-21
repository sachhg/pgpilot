package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// framed builds a length-prefixed protocol message.
func framed(msgType byte, body []byte) []byte {
	out := make([]byte, 5+len(body))
	out[0] = msgType
	binary.BigEndian.PutUint32(out[1:5], uint32(4+len(body)))
	copy(out[5:], body)
	return out
}

func TestRelay_ForwardsVerbatimAndInspects(t *testing.T) {
	m1 := framed(MsgQuery, []byte("SELECT 1\x00"))
	m2 := framed(MsgReadyForQuery, []byte{'I'})
	in := append(append([]byte{}, m1...), m2...)

	var out bytes.Buffer
	type seen struct {
		typ  byte
		body []byte
	}
	var got []seen
	err := Relay(&out, bytes.NewReader(in), func(typ byte, body []byte) error {
		got = append(got, seen{typ, append([]byte{}, body...)})
		return nil
	})
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if !bytes.Equal(out.Bytes(), in) {
		t.Errorf("output not verbatim:\n in = %x\nout = %x", in, out.Bytes())
	}
	if len(got) != 2 || got[0].typ != MsgQuery || got[1].typ != MsgReadyForQuery {
		t.Fatalf("inspected messages = %+v, want Query then ReadyForQuery", got)
	}
	if !bytes.Equal(got[1].body, []byte{'I'}) {
		t.Errorf("ReadyForQuery body = %x, want 'I'", got[1].body)
	}
}

func TestRelay_CleanEOF(t *testing.T) {
	var out bytes.Buffer
	if err := Relay(&out, bytes.NewReader(nil), nil); err != nil {
		t.Fatalf("Relay on empty stream: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("wrote %d bytes for empty stream", out.Len())
	}
}

func TestRelay_Truncated(t *testing.T) {
	// Header promises a 10-byte body, but only 3 are present.
	msg := framed(MsgQuery, make([]byte, 10))[:5+3]
	err := Relay(io.Discard, bytes.NewReader(msg), nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestRelay_BadLength(t *testing.T) {
	bad := []byte{MsgQuery, 0, 0, 0, 3} // length 3 < 4
	if err := Relay(io.Discard, bytes.NewReader(bad), nil); err == nil {
		t.Fatal("expected an error for a sub-minimum length")
	}
}

func TestRelay_LargeBodyStreamed(t *testing.T) {
	big := make([]byte, maxInspectBody+1024)
	for i := range big {
		big[i] = byte(i)
	}
	in := framed(MsgDataRow, big)

	var out bytes.Buffer
	var inspectedBody []byte
	inspected := false
	err := Relay(&out, bytes.NewReader(in), func(_ byte, body []byte) error {
		inspected = true
		inspectedBody = body
		return nil
	})
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if !inspected {
		t.Fatal("inspect was not called")
	}
	if inspectedBody != nil {
		t.Errorf("large body was buffered (len=%d), want nil (streamed)", len(inspectedBody))
	}
	if !bytes.Equal(out.Bytes(), in) {
		t.Error("large message not forwarded verbatim")
	}
}

func TestRelay_InspectErrorAborts(t *testing.T) {
	sentinel := errors.New("stop")
	in := framed(MsgQuery, []byte("x\x00"))
	err := Relay(io.Discard, bytes.NewReader(in), func(byte, []byte) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}

func TestParseReadyForQuery(t *testing.T) {
	for _, st := range []TxStatus{StatusIdle, StatusInTx, StatusInFailedTx} {
		got, ok := ParseReadyForQuery([]byte{byte(st)})
		if !ok || got != st {
			t.Errorf("ParseReadyForQuery(%q) = %q, %v; want %q, true", byte(st), byte(got), ok, byte(st))
		}
	}
	if _, ok := ParseReadyForQuery([]byte{'X'}); ok {
		t.Error("accepted invalid status byte 'X'")
	}
	if _, ok := ParseReadyForQuery([]byte{'I', 'I'}); ok {
		t.Error("accepted a two-byte body")
	}
	if _, ok := ParseReadyForQuery(nil); ok {
		t.Error("accepted an empty body")
	}
}
