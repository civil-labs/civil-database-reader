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
	// Create context, logger, config, and db pool first
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

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

	dbPool, err := pgxpool.NewWithConfig(ctx, dbPoolConfig)
	if err != nil {
		slog.Error("failed to initialize database pool", slog.Any("error", err))
		os.Exit(1)
	}
	defer dbPool.Close()

	// Quick connection test
	ctxTimeout, cancelTimeout := context.WithTimeout(ctx, 2*time.Second)
	defer cancelTimeout()
	if err := dbPool.Ping(ctxTimeout); err != nil {
		logger.Warn("could not ping database", slog.Any("error", err))
	} else {
		logger.Info("ping test to PostgreSQL/PostGIS succeeded")
	}

	// Initialize the handlers and http server
	srv := &ParcelServer{db: dbPool}
	mux := http.NewServeMux()

	path, handler := parcelsv1connect.NewParcelsServiceHandler(srv)
	mux.Handle(path, handler)

	// Pass the fully qualified name of your service so the health check
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

	// Use h2c so we can serve HTTP/2 without TLS.
	p.SetUnencryptedHTTP2(true)
	httpSrv := http.Server{
		Addr:      listenPort,
		Handler:   mux,
		Protocols: p,
	}

	// Graceful Shutdown
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
		<-quit // Block until a signal is received

		logger.Info("received shutdown signal. stopping ConnectRPC server gracefully...")

		// Create a timeout context. If active requests take longer than 15 seconds
		// to finish, forcefully drop them so the container can be killed
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()

		// Trigger the HTTP shutdown
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed or timed out", slog.Any("error", err))
		}

		// Closes the database pool or other resources
		// only after the server has stopped accepting/processing requests.
		logger.Info("closing database connections...")
		dbPool.Close()

		// cancel() // If a global context cancel is added, invoke it here
	}()

	// Start Serving
	logger.Info("starting connect server", slog.Int("port", int(config.Port)))

	err = httpSrv.ListenAndServe()

	// Catch the exit. Ignore ErrServerClosed, as that means the graceful shutdown worked
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("Server crashed", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("graceful shutdown complete. exiting")
}
