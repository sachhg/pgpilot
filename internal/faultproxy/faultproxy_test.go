package faultproxy

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"
)

// echoServer starts a loopback TCP echo server and returns its address.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _, _ = io.Copy(c, c); _ = c.Close() }(c)
		}
	}()
	return ln.Addr().String()
}

func dialLine(t *testing.T, addr, msg string) (net.Conn, string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := c.Write([]byte(msg + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return c, line
}

func TestProxy_Forwards(t *testing.T) {
	p, err := New(echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	c, got := dialLine(t, p.Addr(), "hello")
	defer func() { _ = c.Close() }()
	if got != "hello\n" {
		t.Errorf("echo through proxy = %q, want %q", got, "hello\n")
	}
}

func TestProxy_Blackhole(t *testing.T) {
	p, err := New(echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	p.Blackhole(true)
	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("dial (blackhole accepts then closes): %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	// A blackholed connection is closed immediately: the read sees EOF.
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("blackholed read err = %v, want EOF", err)
	}

	// Turning it off restores forwarding.
	p.Blackhole(false)
	c2, got := dialLine(t, p.Addr(), "back")
	defer func() { _ = c2.Close() }()
	if got != "back\n" {
		t.Errorf("after un-blackhole, echo = %q, want %q", got, "back\n")
	}
}

func TestProxy_Sever(t *testing.T) {
	p, err := New(echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	c, got := dialLine(t, p.Addr(), "live")
	defer func() { _ = c.Close() }()
	if got != "live\n" {
		t.Fatalf("setup echo = %q", got)
	}
	p.Sever()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	// The severed connection is closed: a further read ends.
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("read after Sever succeeded, want a closed connection")
	}
}

func TestProxy_Latency(t *testing.T) {
	p, err := New(echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	p.SetLatency(120 * time.Millisecond)
	start := time.Now()
	c, got := dialLine(t, p.Addr(), "slow")
	defer func() { _ = c.Close() }()
	if got != "slow\n" {
		t.Fatalf("echo = %q", got)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("round trip took %v, want >= ~120ms of injected latency", elapsed)
	}
}
