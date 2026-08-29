package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kilo666mj/rendercase/internal/app"
	"github.com/kilo666mj/rendercase/internal/blob"
	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration invalid", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}
	blobs := blob.Store{Root: cfg.StorageRoot, MaxBundleBytes: cfg.MaxBundleBytes, MaxFiles: cfg.MaxFiles}
	if err := blobs.Init(); err != nil {
		logger.Error("artifact storage unavailable", "error", err)
		os.Exit(1)
	}
	application, err := app.New(ctx, cfg, db, blobs, logger)
	if err != nil {
		logger.Error("application startup failed", "error", err)
		os.Exit(1)
	}
	go application.RunMaintenance(ctx)
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: application.Handler(), ReadTimeout: 5 * time.Minute, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("rendercase listening", "address", cfg.ListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server stopped", "error", err)
		os.Exit(1)
	}
}
