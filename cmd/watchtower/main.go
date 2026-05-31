// Command watchtower is the single-binary Polymarket surveillance service.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Borislavv/polymarket-sharks/internal/app"
	"github.com/Borislavv/polymarket-sharks/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "watchtower:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))
	log.Info("app started",
		"categories", cfg.TargetCategories,
		"metrics_addr", cfg.MetricsAddr,
		"alerting_enabled", cfg.AlertingEnabled,
		"lifecycle_enabled", cfg.LifecycleEnabled)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer a.Close()

	go func() {
		<-ctx.Done()
		log.Info("shutdown received")
	}()

	if err := a.Run(ctx); err != nil {
		return fmt.Errorf("app run: %w", err)
	}
	log.Info("app stopped")
	return nil
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
