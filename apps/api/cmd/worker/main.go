package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	runtimediagnostics "github.com/keeptrue/sforum/apps/api/app/Support/RuntimeDiagnostics"
	"github.com/keeptrue/sforum/apps/api/bootstrap"
	"github.com/keeptrue/sforum/apps/api/config"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

func main() {
	if platformversion.PrintIfRequested(os.Stdout, os.Args[1:]) {
		return
	}
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runWorker(ctx, cfg, logger); err != nil {
		logger.Error("worker stopped with failure", "error", err)
		os.Exit(1)
	}
	logger.Info("worker stopped")
}

type workerLifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
	Failures() <-chan error
	Close()
}

func runWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	worker, err := bootstrap.NewWorker(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("worker bootstrap: %w", err)
	}
	profiler, err := runtimediagnostics.StartPprof(ctx, cfg.WorkerPprofEnabled, cfg.WorkerPprofAddr, logger)
	if err != nil {
		worker.Close()
		return fmt.Errorf("worker runtime diagnostics: %w", err)
	}
	defer profiler.Close()
	return runWorkerLifecycle(ctx, cfg, logger, worker)
}

func runWorkerLifecycle(ctx context.Context, cfg config.Config, logger *slog.Logger, worker workerLifecycle) error {
	if ctx == nil || worker == nil {
		return errors.New("worker lifecycle is not configured")
	}
	defer worker.Close()
	if logger != nil {
		build := platformversion.Get()
		logger.Info("starting worker", "env", cfg.AppEnv, "locale", cfg.AppLocale, "version", build.Version, "commit", build.Commit)
	}
	if err := worker.Start(ctx); err != nil {
		return fmt.Errorf("worker start: %w", err)
	}

	var terminalErr error
	failures := worker.Failures()
waitForStop:
	for {
		select {
		case <-ctx.Done():
			break waitForStop
		case err, ok := <-failures:
			if !ok {
				// Safe Mode/embed normally return nil. Treat a closed active channel as
				// disabled rather than spinning; coordinator exits with an error value.
				failures = nil
				continue
			}
			if err == nil {
				continue
			}
			terminalErr = fmt.Errorf("plugin runtime coordinator terminal failure: %w", err)
			if logger != nil {
				logger.Error("worker plugin runtime authorization lost; stopping River", "error", err)
			}
			break waitForStop
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.WorkerShutdownTimeout)
	stopErr := worker.Stop(shutdownCtx)
	cancel()
	// os.Exit does not run defers. Close explicitly before returning the error;
	// the deferred call remains as an idempotence guard for every early return.
	worker.Close()
	if stopErr != nil {
		stopErr = fmt.Errorf("worker shutdown: %w", stopErr)
	}
	return errors.Join(terminalErr, stopErr)
}
