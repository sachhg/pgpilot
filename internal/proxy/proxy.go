// Package proxy implements a transparent PostgreSQL proxy: it accepts client
// connections, refuses TLS negotiation, forwards the startup and authentication
// exchange to an upstream backend untouched, and pipes bytes in both directions
// for the life of each session. It does not yet parse or route queries; that
// arrives in later phases.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures a proxy Server.
type Config struct {
	// ListenAddr is the TCP address the proxy accepts client connections on.
	ListenAddr string
	// UpstreamAddr is the PostgreSQL backend every session is forwarded to.
	UpstreamAddr string
	// DialTimeout bounds a single upstream dial. Zero selects a default.
	DialTimeout time.Duration
	// Logger receives structured logs. Nil selects slog.Default.
	Logger *slog.Logger
}

// Server is a transparent PostgreSQL proxy. The zero value is not usable;
// construct a Server with New, call Listen, then Serve.
type Server struct {
	cfg    Config
	log    *slog.Logger
	dialer *net.Dialer

	sessions atomic.Uint64
	wg       sync.WaitGroup

	mu       sync.Mutex
	listener net.Listener
}

// New builds a Server from cfg.
func New(cfg Config) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Server{
		cfg:    cfg,
		log:    logger,
		dialer: &net.Dialer{Timeout: timeout},
	}
}

// Listen binds the configured listen address and returns the resolved address.
// It must be called once, before Serve.
func (s *Server) Listen() (net.Addr, error) {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy: listen on %q: %w", s.cfg.ListenAddr, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	return ln.Addr(), nil
}

// Addr returns the address the proxy is listening on, or nil if Listen has not
// been called.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Serve accepts connections until ctx is cancelled, then drains in-flight
// sessions and returns. Because every session is bound to ctx, no goroutine
// outlives Serve.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln == nil {
		return errors.New("proxy: Serve called before Listen")
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close() // unblock Accept
	}()

	for {
		client, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break // shutting down
			}
			s.log.Warn("accept failed", "error", err)
			time.Sleep(10 * time.Millisecond) // avoid a hot loop on transient errors
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, client)
		}()
	}

	s.wg.Wait()
	return nil
}

// handle runs one client session and logs its lifecycle.
func (s *Server) handle(ctx context.Context, client net.Conn) {
	id := s.sessions.Add(1)
	log := s.log.With("session", id, "client", client.RemoteAddr().String())
	log.Info("session opened")

	sess := &session{
		client:   client,
		upstream: s.cfg.UpstreamAddr,
		dialer:   s.dialer,
	}
	if err := sess.serve(ctx); err != nil {
		log.Warn("session closed", "error", err)
		return
	}
	log.Info("session closed")
}
