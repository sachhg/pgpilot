package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	// maxStartupPacketLength mirrors PostgreSQL's PG_MAX_STARTUP_PACKET_LENGTH:
	// startup packets larger than this are rejected before allocation.
	maxStartupPacketLength = 10000

	// These special codes occupy the protocol-version field of the first
	// message a client sends, in place of a real protocol version.
	sslRequestCode    = 80877103 // request a TLS-encrypted connection
	gssEncRequestCode = 80877104 // request a GSSAPI-encrypted connection
	cancelRequestCode = 80877102 // request cancellation of an in-flight query
)

// startupPacket is a raw, length-prefixed startup-phase message from a client.
// raw holds the entire packet including its four-byte length prefix, so it can
// be forwarded to the upstream byte-for-byte.
type startupPacket struct {
	raw  []byte
	code uint32
}

func (p startupPacket) isSSLRequest() bool    { return p.code == sslRequestCode }
func (p startupPacket) isGSSEncRequest() bool { return p.code == gssEncRequestCode }
func (p startupPacket) isCancelRequest() bool { return p.code == cancelRequestCode }

// readStartupPacket reads a single length-prefixed startup-phase message. The
// four-byte length prefix counts itself; the four bytes after it are either a
// protocol version (a real StartupMessage or CancelRequest) or one of the
// special request codes above.
func readStartupPacket(r io.Reader) (startupPacket, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return startupPacket{}, err
	}
	msgLen := binary.BigEndian.Uint32(header[:])
	if msgLen < 8 || msgLen > maxStartupPacketLength {
		return startupPacket{}, fmt.Errorf("proxy: startup packet length %d out of range [8, %d]", msgLen, maxStartupPacketLength)
	}
	raw := make([]byte, msgLen)
	copy(raw, header[:])
	if _, err := io.ReadFull(r, raw[4:]); err != nil {
		return startupPacket{}, err
	}
	return startupPacket{raw: raw, code: binary.BigEndian.Uint32(raw[4:8])}, nil
}
