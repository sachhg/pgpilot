// Command pgpilot is a transparent, LSN-fencing PostgreSQL connection router.
//
// At this phase it is an authenticating connection pooler that also tracks the
// health and replication lag of the primary and its replicas. Read/write
// routing and fencing arrive in later phases. See the roadmap in README.md.
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
	"github.com/sachhg/pgpilot/internal/registry"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The health poller uses the first configured user to reach each backend.
	pollUser := cfg.Users[0]
	reg := registry.New(registry.Config{
		Interval:         cfg.Health.Interval.Std(),
		FailureThreshold: cfg.Health.FailureThreshold,
		BaseBackoff:      cfg.Health.BaseBackoff.Std(),
		MaxBackoff:       cfg.Health.MaxBackoff.Std(),
		Logger:           logger,
		Dialer: func(dctx context.Context, addr string) (registry.Conn, error) {
			conn, derr := backend.Dial(dctx, addr, pollUser.Name, pollUser.Password, pollUser.Name)
			if derr != nil {
				return nil, derr
			}
			return conn, nil
		},
	})
	reg.Start(ctx, backendsFrom(cfg))
	go reloadOnHUP(ctx, logger, *configPath, reg)

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
	logger.Info("pgpilot listening",
		"addr", addr.String(), "primary", cfg.Primary, "replicas", len(cfg.Replicas), "version", version)

	if err := srv.Serve(ctx); err != nil {
		return err
	}
	reg.Wait()
	logger.Info("pgpilot stopped")
	return nil
}

// backendsFrom builds the registry's backend list from the config: the primary
// plus each replica.
func backendsFrom(cfg *config.Config) []registry.Backend {
	backends := []registry.Backend{{Name: "primary", Addr: cfg.Primary}}
	for i, addr := range cfg.Replicas {
		backends = append(backends, registry.Backend{Name: fmt.Sprintf("replica%d", i+1), Addr: addr})
	}
	return backends
}

// reloadOnHUP reloads the config on SIGHUP and reconfigures the registry's
// backend set, so replicas can be added or removed without a restart.
func reloadOnHUP(ctx context.Context, logger *slog.Logger, path string, reg *registry.Registry) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			cfg, err := config.Load(path)
			if err != nil {
				logger.Warn("config reload failed", "error", err)
				continue
			}
			reg.Reconfigure(backendsFrom(cfg))
			logger.Info("config reloaded", "replicas", len(cfg.Replicas))
		}
	}
}
