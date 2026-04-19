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

	"github.com/jackc/pgx/v5/pgxpool"

	"connectrpc.com/grpchealth"

	"github.com/civil-labs/civil-api-go/civil/mesh/parcels/v1/parcelsv1connect"
)

func main() {
	// Create app context, logger, config, and db pool first
	ctxApp, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()

	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug, // Show debug logs!
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, opts))

	config, err := LoadConfig(logger)

	if err != nil {
		logger.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("service initialized",
		slog.Int("grpc_port", int(config.Port)),
		slog.Bool("verbose", config.Verbose),
	)

	dbPoolConfig, err := BuildPoolConfig(config)

	dbPool, err := pgxpool.NewWithConfig(ctxApp, dbPoolConfig)
	if err != nil {
		slog.Error("failed to initialize database pool", slog.Any("error", err))
		os.Exit(1)
	}

	// Quick connection test
	ctxTimeout, cancelTimeout := context.WithTimeout(ctxApp, 2*time.Second)
	defer cancelTimeout()
	if err := dbPool.Ping(ctxTimeout); err != nil {
		logger.Warn("could not ping database", slog.Any("error", err))
	} else {
		logger.Info("ping test to PostgreSQL/PostGIS succeeded")
	}

	// Initialize the handlers and http server
	srv := &ParcelServer{
		db:     dbPool,
		logger: logger,
	}
	mux := http.NewServeMux()

	path, handler := parcelsv1connect.NewParcelsServiceHandler(srv)
	mux.Handle(path, handler)

	// Pass the fully qualified name of the service so the health check
	// can report on this specific service, as well as the global server status.
	checker := grpchealth.NewStaticChecker(
		parcelsv1connect.ParcelsServiceName,
	)

	healthPath, healthHandler := grpchealth.NewHandler(checker)
	mux.Handle(healthPath, healthHandler)

	// Create required port format
	listenPort := fmt.Sprintf(":%d", config.Port)

	p := new(http.Protocols)
	p.SetHTTP1(true)

	p.SetUnencryptedHTTP2(true)
	httpSrv := http.Server{
		Addr:      listenPort,
		Handler:   mux,
		Protocols: p,
	}

	shutdownSig := make(chan os.Signal, 1)
	signal.Notify(shutdownSig, os.Interrupt, syscall.SIGTERM)

	serverErr := make(chan error, 1)

	// Start the HTTP server in a background goroutine
	go func() {
		logger.Info("starting connect server", slog.Int("port", int(config.Port)))
		serverErr <- httpSrv.ListenAndServe()
	}()

	// This is inited by default to go's int zero value, zero
	var exitCode int

	// Block main() until something happens
	select {
	case err := <-serverErr:
		// The server crashed prematurely
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server crashed", slog.Any("error", err))
			exitCode = 1
		}
	case sig := <-shutdownSig:
		// Graceful shutdown signal received
		logger.Info("received shutdown signal", slog.String("signal", sig.String()))

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()

		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP graceful shutdown failed", slog.Any("error", err))
			exitCode = 1
		}
	}

	// This block runs no matter how the select statement unblocked.
	slog.Info("stopping background workers...")
	cancelApp()

	slog.Info("closing database connections...")
	dbPool.Close()

	slog.Info("teardown complete. exiting.")
	os.Exit(exitCode)
}
