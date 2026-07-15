package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/openstream/openstream/internal/api"
	"github.com/openstream/openstream/internal/bus"
	"github.com/openstream/openstream/internal/config"
	"github.com/openstream/openstream/internal/realtime"
	"github.com/openstream/openstream/internal/state"
	"github.com/openstream/openstream/internal/store"
	"github.com/openstream/openstream/internal/worker"
)

// serviceSet selects which tiers a process runs (SPEC.md §2.2).
type serviceSet struct {
	api      bool
	realtime bool
	worker   bool
}

func serveCmd() *cobra.Command {
	var httpAddr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run all services in one process (dev / small deployments)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServices(cmd, httpAddr, serviceSet{api: true, realtime: true, worker: true})
		},
	}
	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "listen address override")
	return cmd
}

func apiCmd() *cobra.Command {
	var httpAddr string
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Run the REST API service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServices(cmd, httpAddr, serviceSet{api: true})
		},
	}
	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "listen address override")
	return cmd
}

func realtimeCmd() *cobra.Command {
	var httpAddr string
	cmd := &cobra.Command{
		Use:   "realtime",
		Short: "Run the realtime WebSocket service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServices(cmd, httpAddr, serviceSet{realtime: true})
		},
	}
	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "listen address override")
	return cmd
}

func workerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "worker",
		Short: "Run background workers (outbox relay, retention sweeps)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServices(cmd, "", serviceSet{worker: true})
		},
	}
}

func gatewayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gateway",
		Short: "Run the edge gateway (reserved: TLS termination/routing land here)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("the gateway tier is not yet implemented; deploy api/realtime behind your ingress (SPEC.md §2.2)")
		},
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

func runServices(cmd *cobra.Command, httpAddr string, services serviceSet) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if httpAddr != "" {
		cfg.HTTPAddr = httpAddr
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting openstream", "version", version,
		"api", services.api, "realtime", services.realtime, "worker", services.worker,
		"http_addr", cfg.HTTPAddr)

	st, err := store.New(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer st.Close()

	// Bus: NATS when reachable, in-process for single-binary deployments.
	var eventBus bus.Bus
	if cfg.NATSURL != "" && !allInOne(services) {
		eventBus, err = bus.NewNATS(cfg.NATSURL)
		if err != nil {
			return fmt.Errorf("nats required for multi-process mode: %w", err)
		}
	} else if cfg.NATSURL != "" {
		if natsBus, natsErr := bus.NewNATS(cfg.NATSURL); natsErr == nil {
			eventBus = natsBus
		} else {
			log.Warn("nats unavailable, using in-process bus", "error", natsErr)
			eventBus = bus.NewInProc()
		}
	} else {
		eventBus = bus.NewInProc()
	}
	defer func() { _ = eventBus.Close() }()

	// Ephemeral state: Redis when reachable, in-memory otherwise.
	var ephemeral state.State
	if cfg.RedisAddr != "" {
		if redisState, redisErr := state.NewRedis(ctx, cfg.RedisAddr); redisErr == nil {
			ephemeral = redisState
		} else if allInOne(services) {
			log.Warn("redis unavailable, using in-memory state", "error", redisErr)
			ephemeral = state.NewMemory()
		} else {
			return fmt.Errorf("redis required for multi-process mode: %w", redisErr)
		}
	} else {
		ephemeral = state.NewMemory()
	}
	defer func() { _ = ephemeral.Close() }()

	apiServer := &api.Server{Store: st, Bus: eventBus, State: ephemeral, Cfg: cfg, Log: log}
	var hub *realtime.Hub
	if services.realtime {
		hub = realtime.NewHub(st, eventBus, ephemeral, apiServer, log)
		hub.HeartbeatInterval = cfg.WSHeartbeatInterval
		hub.DeadTimeout = cfg.WSDeadTimeout
		hub.PresenceDebounce = cfg.PresenceDebounce
		apiServer.Realtime = hub
	}

	if services.worker {
		relay := &worker.Relay{Store: st, Bus: eventBus, Batch: cfg.OutboxBatchSize, Interval: cfg.OutboxPollInterval, Log: log}
		go relay.Run(ctx)
		sweeper := &worker.Sweeper{Store: st, Log: log}
		go sweeper.Run(ctx)
	}

	mux := http.NewServeMux()
	if services.api {
		mux.Handle("/", apiServer.Router())
	}
	if services.realtime {
		mux.HandleFunc("/connect", hub.HandleConnect)
		if !services.api {
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
			})
		}
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func allInOne(s serviceSet) bool { return s.api && s.realtime && s.worker }
