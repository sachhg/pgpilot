// Command pgpilot is a transparent, LSN-fencing PostgreSQL connection router.
//
// At this phase it is an authenticating connection pooler: it verifies each
// client with SCRAM-SHA-256 and hands it a pooled connection to a single
// primary. Read/write routing and fencing arrive in later phases. See the
// roadmap in README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sachhg/pgpilot/internal/backend"
	"github.com/sachhg/pgpilot/internal/config"
	"github.com/sachhg/pgpilot/internal/proxy"
)

// version is the build version, overridden via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pgpilot:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pgpilot", flag.ContinueOnError)
	configPath := fs.String("config", "pgpilot.json", "path to the JSON config file")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, or error")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Printf("pgpilot %s\n", version)
		return nil
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("invalid -log-level %q: %w", *logLevel, err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	users := make(map[string]string, len(cfg.Users))
	for _, u := range cfg.Users {
		users[u.Name] = u.Password
	}
	mgr := backend.NewManager(cfg.Primary, users, backend.PoolConfig{
		MaxSize:        cfg.Pool.MaxSize,
		MaxWaiters:     cfg.Pool.MaxWaiters,
		AcquireTimeout: cfg.Pool.AcquireTimeout.Std(),
		IdleTimeout:    cfg.Pool.IdleTimeout.Std(),
	})
	defer mgr.Close()

	srv := proxy.New(proxy.Config{
		ListenAddr: cfg.Listen,
		Users:      cfg,
		Manager:    mgr,
		Logger:     logger,
	})
	addr, err := srv.Listen()
	if err != nil {
		return err
	}
	logger.Info("pgpilot listening", "addr", addr.String(), "primary", cfg.Primary, "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx); err != nil {
		return err
	}
	logger.Info("pgpilot stopped")
	return nil
}
