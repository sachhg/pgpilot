// Package proxy is pgpilot's client-facing server. It accepts client
// connections, refuses TLS, authenticates each client with SCRAM-SHA-256,
// acquires a pooled backend connection for the client's (user, database), and
// relays protocol messages between them for the life of the session.
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

	"github.com/sachhg/pgpilot/internal/backend"
	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/protocol"
)

// Config configures a proxy Server.
type Config struct {
	// ListenAddr is the TCP address the proxy accepts client connections on.
	ListenAddr string
	// Users holds the credentials pgpilot verifies clients against.
	Users *config.Config
	// Manager supplies pooled, authenticated backend connections.
	Manager *backend.Manager
	// Logger receives structured logs. Nil selects slog.Default.
	Logger *slog.Logger
}

// Server is pgpilot's client-facing proxy. Construct it with New, then call
// Listen and Serve.
type Server struct {
	cfg Config
	log *slog.Logger

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
	return &Server{cfg: cfg, log: logger}
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

// Addr returns the address the proxy is listening on, or nil before Listen.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Serve accepts connections until ctx is cancelled, then drains in-flight
// sessions and returns.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln == nil {
		return errors.New("proxy: Serve called before Listen")
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		client, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.log.Warn("accept failed", "error", err)
			time.Sleep(10 * time.Millisecond)
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
		client:  client,
		cfg:     s.cfg.Users,
		manager: s.cfg.Manager,
		log:     log,
		tracker: &protocol.TxTracker{},
	}
	if err := sess.serve(ctx); err != nil {
		log.Warn("session closed", "error", err)
		return
	}
	log.Info("session closed")
}
