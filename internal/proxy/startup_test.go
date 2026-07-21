package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// protocolVersion3 is the PostgreSQL 3.0 protocol version a real StartupMessage
// carries in place of a special request code.
const protocolVersion3 = 196608

// buildStartup builds a length-prefixed startup-phase packet with the given
// code (a protocol version or special request code) and payload.
func buildStartup(code uint32, payload []byte) []byte {
	total := 8 + len(payload)
	b := make([]byte, total)
	binary.BigEndian.PutUint32(b[0:4], uint32(total))
	binary.BigEndian.PutUint32(b[4:8], code)
	copy(b[8:], payload)
	return b
}

func TestReadStartupPacket_SSLRequest(t *testing.T) {
	in := buildStartup(sslRequestCode, nil)
	pkt, err := readStartupPacket(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("readStartupPacket: %v", err)
	}
	if !pkt.isSSLRequest() {
		t.Errorf("isSSLRequest = false, want true (code=%d)", pkt.code)
	}
	if pkt.isGSSEncRequest() {
		t.Errorf("isGSSEncRequest = true, want false")
	}
	if !bytes.Equal(pkt.raw, in) {
		t.Errorf("raw = %x, want %x", pkt.raw, in)
	}
}

func TestReadStartupPacket_GSSEncRequest(t *testing.T) {
	pkt, err := readStartupPacket(bytes.NewReader(buildStartup(gssEncRequestCode, nil)))
	if err != nil {
		t.Fatalf("readStartupPacket: %v", err)
	}
	if !pkt.isGSSEncRequest() {
		t.Errorf("isGSSEncRequest = false, want true")
	}
}

func TestReadStartupPacket_StartupMessage(t *testing.T) {
	payload := []byte("user\x00pgpilot\x00database\x00pgpilot\x00\x00")
	in := buildStartup(protocolVersion3, payload)
	pkt, err := readStartupPacket(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("readStartupPacket: %v", err)
	}
	if pkt.isSSLRequest() || pkt.isGSSEncRequest() {
		t.Errorf("classified as an encryption request; want plain startup (code=%d)", pkt.code)
	}
	if pkt.code != protocolVersion3 {
		t.Errorf("code = %d, want %d", pkt.code, protocolVersion3)
	}
	if !bytes.Equal(pkt.raw, in) {
		t.Errorf("raw not preserved byte-for-byte")
	}
}

func TestReadStartupPacket_LengthTooSmall(t *testing.T) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], 4) // shorter than the 8-byte minimum
	if _, err := readStartupPacket(bytes.NewReader(b[:])); err == nil {
		t.Fatal("expected an error for an undersized packet")
	}
}

func TestReadStartupPacket_LengthTooLarge(t *testing.T) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], maxStartupPacketLength+1)
	if _, err := readStartupPacket(bytes.NewReader(b[:])); err == nil {
		t.Fatal("expected an error for an oversized packet")
	}
}

func TestReadStartupPacket_Truncated(t *testing.T) {
	// A full packet whose body is cut short: the length prefix promises more
	// bytes than the reader can supply, so the body read stops mid-way.
	full := buildStartup(protocolVersion3, make([]byte, 20))
	truncated := full[:len(full)-5]
	_, err := readStartupPacket(bytes.NewReader(truncated))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}
