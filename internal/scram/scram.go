// Package scram implements SCRAM-SHA-256 (RFC 5802 / RFC 7677) for both roles:
// the client role, used to authenticate pgpilot's own connections to a backend,
// and the server role, used to verify clients connecting to pgpilot. Channel
// binding is not supported, matching PostgreSQL's plain SCRAM-SHA-256 mechanism.
package scram

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// ErrAuthFailed is returned by the server when a client's proof does not verify.
var ErrAuthFailed = errors.New("scram: authentication failed")

const (
	defaultIterations = 4096
	saltLen           = 16
	nonceLen          = 18 // random bytes, then base64-encoded into the message
	gs2Header         = "n,,"
	// channelBinding is base64("n,,"), the c= value for a no-channel-binding
	// client-final message.
	channelBinding = "biws"
)

// Client runs the SCRAM-SHA-256 client exchange.
type Client struct {
	password        string
	clientNonce     string
	clientFirstBare string
	serverSignature []byte
}

// NewClient creates a Client for the given password with a fresh client nonce.
func NewClient(password string) (*Client, error) {
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	return &Client{password: password, clientNonce: nonce}, nil
}

// FirstMessage returns the client-first message. PostgreSQL takes the username
// from the startup packet, so the SCRAM message carries an empty username.
func (c *Client) FirstMessage() string {
	c.clientFirstBare = "n=,r=" + c.clientNonce
	return gs2Header + c.clientFirstBare
}

// HandleServerFirst processes the server-first message and returns the
// client-final message.
func (c *Client) HandleServerFirst(serverFirst string) (string, error) {
	combinedNonce, salt, iterations, err := parseServerFirst(serverFirst)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(combinedNonce, c.clientNonce) {
		return "", errors.New("scram: server nonce does not extend client nonce")
	}

	salted := saltedPassword(c.password, salt, iterations)
	clientKey := hmacSum(salted, []byte("Client Key"))
	storedKey := sha256Sum(clientKey)

	clientFinalWithoutProof := "c=" + channelBinding + ",r=" + combinedNonce
	authMessage := c.clientFirstBare + "," + serverFirst + "," + clientFinalWithoutProof

	clientSignature := hmacSum(storedKey, []byte(authMessage))
	clientProof := xorBytes(clientKey, clientSignature)

	serverKey := hmacSum(salted, []byte("Server Key"))
	c.serverSignature = hmacSum(serverKey, []byte(authMessage))

	return clientFinalWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(clientProof), nil
}

// HandleServerFinal verifies the server-final message's server signature.
func (c *Client) HandleServerFinal(serverFinal string) error {
	if e, ok := field(serverFinal, "e="); ok {
		return fmt.Errorf("scram: server error: %s", e)
	}
	v, ok := field(serverFinal, "v=")
	if !ok {
		return errors.New("scram: malformed server-final message")
	}
	sig, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return fmt.Errorf("scram: bad server signature encoding: %w", err)
	}
	if subtle.ConstantTimeCompare(sig, c.serverSignature) != 1 {
		return errors.New("scram: server signature mismatch")
	}
	return nil
}

// Server runs the SCRAM-SHA-256 server exchange.
type Server struct {
	password        string
	clientNonce     string
	serverNonce     string
	salt            []byte
	iterations      int
	clientFirstBare string
	serverFirst     string
	storedKey       []byte
	serverKey       []byte
}

// NewServer creates a Server that verifies clients against the given password,
// with a fresh nonce and salt.
func NewServer(password string) (*Server, error) {
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return &Server{password: password, serverNonce: nonce, salt: salt, iterations: defaultIterations}, nil
}

// HandleClientFirst parses the client-first message and returns the server-first
// message.
func (s *Server) HandleClientFirst(clientFirst string) (string, error) {
	if strings.HasPrefix(clientFirst, "p=") {
		return "", errors.New("scram: channel binding is not supported")
	}
	// The gs2 header ends after the second comma; the rest is client-first-bare.
	c1 := strings.IndexByte(clientFirst, ',')
	if c1 < 0 {
		return "", errors.New("scram: malformed client-first message")
	}
	c2 := strings.IndexByte(clientFirst[c1+1:], ',')
	if c2 < 0 {
		return "", errors.New("scram: malformed client-first message")
	}
	s.clientFirstBare = clientFirst[c1+1+c2+1:]

	nonce, ok := field(s.clientFirstBare, "r=")
	if !ok {
		return "", errors.New("scram: client-first missing nonce")
	}
	s.clientNonce = nonce

	combined := s.clientNonce + s.serverNonce
	s.serverFirst = "r=" + combined +
		",s=" + base64.StdEncoding.EncodeToString(s.salt) +
		",i=" + strconv.Itoa(s.iterations)

	salted := saltedPassword(s.password, s.salt, s.iterations)
	s.storedKey = sha256Sum(hmacSum(salted, []byte("Client Key")))
	s.serverKey = hmacSum(salted, []byte("Server Key"))

	return s.serverFirst, nil
}

// HandleClientFinal verifies the client-final message and returns the
// server-final message. It returns ErrAuthFailed if the proof does not verify.
func (s *Server) HandleClientFinal(clientFinal string) (string, error) {
	if cb, ok := field(clientFinal, "c="); !ok || cb != channelBinding {
		return "", errors.New("scram: unexpected channel binding")
	}
	if r, ok := field(clientFinal, "r="); !ok || r != s.clientNonce+s.serverNonce {
		return "", errors.New("scram: nonce mismatch")
	}
	pEnc, ok := field(clientFinal, "p=")
	if !ok {
		return "", errors.New("scram: client-final missing proof")
	}
	clientProof, err := base64.StdEncoding.DecodeString(pEnc)
	if err != nil {
		return "", fmt.Errorf("scram: bad client proof encoding: %w", err)
	}
	if len(clientProof) != sha256.Size {
		return "", errors.New("scram: bad client proof length")
	}

	idx := strings.Index(clientFinal, ",p=")
	if idx < 0 {
		return "", errors.New("scram: malformed client-final message")
	}
	clientFinalWithoutProof := clientFinal[:idx]

	authMessage := s.clientFirstBare + "," + s.serverFirst + "," + clientFinalWithoutProof
	clientSignature := hmacSum(s.storedKey, []byte(authMessage))
	clientKey := xorBytes(clientProof, clientSignature)
	if subtle.ConstantTimeCompare(sha256Sum(clientKey), s.storedKey) != 1 {
		return "", ErrAuthFailed
	}
	serverSignature := hmacSum(s.serverKey, []byte(authMessage))
	return "v=" + base64.StdEncoding.EncodeToString(serverSignature), nil
}

func parseServerFirst(s string) (nonce string, salt []byte, iterations int, err error) {
	nonce, ok := field(s, "r=")
	if !ok {
		return "", nil, 0, errors.New("scram: server-first missing nonce")
	}
	saltEnc, ok := field(s, "s=")
	if !ok {
		return "", nil, 0, errors.New("scram: server-first missing salt")
	}
	iterStr, ok := field(s, "i=")
	if !ok {
		return "", nil, 0, errors.New("scram: server-first missing iteration count")
	}
	salt, err = base64.StdEncoding.DecodeString(saltEnc)
	if err != nil {
		return "", nil, 0, fmt.Errorf("scram: bad salt encoding: %w", err)
	}
	iterations, err = strconv.Atoi(iterStr)
	if err != nil || iterations < 1 {
		return "", nil, 0, fmt.Errorf("scram: bad iteration count %q", iterStr)
	}
	return nonce, salt, iterations, nil
}

// field returns the value of the first comma-separated attribute in msg that has
// the given "key=" prefix.
func field(msg, prefix string) (string, bool) {
	for _, part := range strings.Split(msg, ",") {
		if strings.HasPrefix(part, prefix) {
			return part[len(prefix):], true
		}
	}
	return "", false
}

func newNonce() (string, error) {
	b := make([]byte, nonceLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}

func saltedPassword(password string, salt []byte, iterations int) []byte {
	return pbkdf2.Key([]byte(password), salt, iterations, sha256.Size, sha256.New)
}

func hmacSum(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}
