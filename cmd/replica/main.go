package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"novig-take-home/internal/replica"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	replicaID := getEnv("REPLICA_ID", "replica-1")
	coreURL := getEnv("CORE_BASE_URL", "http://127.0.0.1:8080")
	port := getEnvInt("REPLICA_PORT", 8081)
	dsn := replica.InMemoryDSN(replicaID)

	store, err := replica.NewStore(dsn)
	if err != nil {
		logger.Error("failed to init replica sqlite", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	svc, err := replica.NewService(store, replica.ServiceConfig{
		ID:               replicaID,
		CoreBaseURL:      coreURL,
		Logger:           logger,
		ReconnectBackoff: 500 * time.Millisecond,
	})
	if err != nil {
		logger.Error("failed to init replica service", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go svc.Start(ctx)

	srv := &http.Server{
		Addr:    ":" + strconv.Itoa(port),
		Handler: replica.NewHandler(svc),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("replica server started", "replica_id", replicaID, "port", port, "core_url", coreURL, "dsn", dsn)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("replica server failed", "error", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if raw := os.Getenv(key); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			return parsed
		}
	}
	return fallback
}
