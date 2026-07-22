package scram

import (
	"errors"
	"strings"
	"testing"
)

// runExchange drives a full client/server SCRAM exchange and returns any error.
func runExchange(t *testing.T, client *Client, server *Server) error {
	t.Helper()
	clientFirst := client.FirstMessage()
	serverFirst, err := server.HandleClientFirst(clientFirst)
	if err != nil {
		return err
	}
	clientFinal, err := client.HandleServerFirst(serverFirst)
	if err != nil {
		return err
	}
	serverFinal, err := server.HandleClientFinal(clientFinal)
	if err != nil {
		return err
	}
	return client.HandleServerFinal(serverFinal)
}

func TestExchange_Succeeds(t *testing.T) {
	client, err := NewClient("pencil")
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer("pencil")
	if err != nil {
		t.Fatal(err)
	}
	if err := runExchange(t, client, server); err != nil {
		t.Fatalf("valid exchange failed: %v", err)
	}
}

func TestExchange_Deterministic(t *testing.T) {
	// Fixed nonces and salt make the exchange reproducible; it must still verify
	// end to end. This locks the message construction and crypto wiring.
	client := &Client{password: "hunter2", clientNonce: "clientNONCEfixed01"}
	server := &Server{
		password:    "hunter2",
		serverNonce: "serverNONCEfixed02",
		salt:        []byte("0123456789abcdef"),
		iterations:  4096,
	}
	if err := runExchange(t, client, server); err != nil {
		t.Fatalf("deterministic exchange failed: %v", err)
	}
}

func TestExchange_WrongPassword(t *testing.T) {
	client, _ := NewClient("wrong-password")
	server, _ := NewServer("real-password")
	err := runExchange(t, client, server)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
}

func TestClient_RejectsTamperedServerSignature(t *testing.T) {
	client, _ := NewClient("pencil")
	server, _ := NewServer("pencil")

	cf := client.FirstMessage()
	sf, _ := server.HandleClientFirst(cf)
	cfin, _ := client.HandleServerFirst(sf)
	sfin, _ := server.HandleClientFinal(cfin)

	tampered := sfin[:len(sfin)-1] + string(flipLastChar(sfin))
	if err := client.HandleServerFinal(tampered); err == nil {
		t.Fatal("client accepted a tampered server signature")
	}
}

func flipLastChar(s string) byte {
	c := s[len(s)-1]
	if c == 'A' {
		return 'B'
	}
	return 'A'
}

func TestClient_ServerError(t *testing.T) {
	client, _ := NewClient("pencil")
	if err := client.HandleServerFinal("e=invalid-proof"); err == nil || !strings.Contains(err.Error(), "invalid-proof") {
		t.Fatalf("err = %v, want a server error carrying the reason", err)
	}
}

func TestServer_RejectsChannelBinding(t *testing.T) {
	server, _ := NewServer("pencil")
	if _, err := server.HandleClientFirst("p=tls-server-end-point,,n=,r=abc"); err == nil {
		t.Fatal("server accepted a channel-binding request it cannot satisfy")
	}
}

func TestServer_RejectsMalformedClientFirst(t *testing.T) {
	server, _ := NewServer("pencil")
	for _, bad := range []string{"", "n", "n,", "n,,", "n,,x=1"} {
		if _, err := server.HandleClientFirst(bad); err == nil {
			t.Errorf("server accepted malformed client-first %q", bad)
		}
	}
}

func TestClient_RejectsMalformedServerFirst(t *testing.T) {
	client, _ := NewClient("pencil")
	_ = client.FirstMessage()
	for _, bad := range []string{"", "r=only", "s=abc,i=1", "r=x,s=notbase64!!,i=1"} {
		if _, err := client.HandleServerFirst(bad); err == nil {
			t.Errorf("client accepted malformed server-first %q", bad)
		}
	}
}

func TestClient_RejectsServerNonceNotExtendingClientNonce(t *testing.T) {
	client := &Client{password: "pencil", clientNonce: "AAAA"}
	_ = client.FirstMessage()
	// server nonce that does not start with the client nonce
	if _, err := client.HandleServerFirst("r=ZZZZ,s=MDEyMzQ1Njc4OWFiY2RlZg==,i=4096"); err == nil {
		t.Fatal("client accepted a server nonce that does not extend its own")
	}
}
