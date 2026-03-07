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

	"novig-take-home/internal/core"
	"novig-take-home/internal/domain"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	port := getEnvInt("CORE_PORT", 8080)
	dsn := core.InMemoryDSN(getEnv("CORE_DB_NAME", "core_events"))

	store, err := core.NewEventStore(dsn)
	if err != nil {
		logger.Error("failed to init event store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	svc, err := core.NewService(store, core.ServiceConfig{
		Breaker:         domain.DefaultBreakerConfig(),
		TickInterval:    time.Second,
		WindowDuration:  30 * time.Second,
		DefaultMarketID: getEnv("DEFAULT_MARKET_ID", "market-news-001"),
		Logger:          logger,
	})
	if err != nil {
		logger.Error("failed to init core service", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go svc.Start(ctx)

	srv := &http.Server{
		Addr:    ":" + strconv.Itoa(port),
		Handler: core.NewHandler(svc, logger),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("core server started", "port", port, "dsn", dsn)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("core server failed", "error", err)
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
