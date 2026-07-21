// Command pgpilot is a transparent, LSN-fencing PostgreSQL connection router.
//
// At this phase it is a transparent proxy: it forwards every connection to a
// single upstream primary. Routing, pooling, and fencing arrive in later
// phases. See the roadmap in README.md.
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
	"time"

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
	listen := fs.String("listen", "127.0.0.1:6432", "address to accept client connections on")
	primary := fs.String("primary", "127.0.0.1:55432", "address of the upstream PostgreSQL primary")
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
	srv := proxy.New(proxy.Config{
		ListenAddr:   *listen,
		UpstreamAddr: *primary,
		DialTimeout:  5 * time.Second,
		Logger:       logger,
	})

	addr, err := srv.Listen()
	if err != nil {
		return err
	}
	logger.Info("pgpilot listening", "addr", addr.String(), "primary", *primary, "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx); err != nil {
		return err
	}
	logger.Info("pgpilot stopped")
	return nil
}
