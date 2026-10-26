package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	if err := runAPI(ctx, cfg, logger); err != nil {
		logger.Error("api stopped with failure", "error", err)
		os.Exit(1)
	}
}

const apiShutdownTimeout = 10 * time.Second

type apiLifecycle interface {
	Listen() error
	Shutdown(context.Context) error
	Failures() <-chan error
	Close()
}

type productionAPILifecycle struct {
	api    *bootstrap.API
	cfg    config.Config
	logger *slog.Logger
}

func (lifecycle *productionAPILifecycle) Listen() error {
	if lifecycle.logger != nil {
		build := platformversion.Get()
		identity := platformversion.Identity()
		lifecycle.logger.Info(
			"starting api server", "addr", lifecycle.api.Addr,
			"env", lifecycle.cfg.AppEnv, "locale", lifecycle.cfg.AppLocale,
			"version", build.Version, "commit", build.Commit,
			"coreVersion", identity.CoreVersion,
		)
		if !identity.Valid {
			// 清单兜底要靠 coreVersion 求值；标签不是严格 semver 时它无法判定，
			// 与其把所有安装判死，不如明确告警并跳过兜底。
			lifecycle.logger.Warn(
				"coreVersion is not strict semver; extension manifest compatibility is not enforced",
				"coreVersion", identity.CoreVersion,
			)
		}
	}
	// air 热重载存在新旧进程端口交接窗口；对 EADDRINUSE 做短暂重试。
	return listenWithAddrInUseRetry(lifecycle.api.App, lifecycle.api.Addr, lifecycle.logger)
}

func (lifecycle *productionAPILifecycle) Shutdown(ctx context.Context) error {
	return lifecycle.api.App.ShutdownWithContext(ctx)
}

func (lifecycle *productionAPILifecycle) Failures() <-chan error {
	return lifecycle.api.Failures()
}

func (lifecycle *productionAPILifecycle) Close() {
	lifecycle.api.Close()
}

func runAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	api, err := bootstrap.NewAPI(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("api bootstrap: %w", err)
	}
	profiler, err := runtimediagnostics.StartPprof(ctx, cfg.PprofEnabled, cfg.PprofAddr, logger)
	if err != nil {
		api.Close()
		return fmt.Errorf("api runtime diagnostics: %w", err)
	}
	defer profiler.Close()
	return runAPILifecycle(ctx, logger, &productionAPILifecycle{api: api, cfg: cfg, logger: logger})
}

func runAPILifecycle(ctx context.Context, logger *slog.Logger, api apiLifecycle) error {
	if ctx == nil || api == nil {
		return errors.New("api lifecycle is not configured")
	}
	defer api.Close()

	listenErrors := make(chan error, 1)
	go func() { listenErrors <- api.Listen() }()

	var terminalErr error
	listenStopped := false
	select {
	case <-ctx.Done():
		if logger != nil {
			logger.Info("shutting down api server", "reason", "signal")
		}
	case runtimeErr, ok := <-api.Failures():
		if !ok || runtimeErr == nil {
			runtimeErr = errors.New("api runtime supervision stopped without a terminal error")
		}
		terminalErr = fmt.Errorf("api runtime terminal failure: %w", runtimeErr)
		if logger != nil {
			logger.Error("api runtime authority lost; draining HTTP", "error", runtimeErr)
		}
	case listenErr := <-listenErrors:
		listenStopped = true
		if listenErr != nil && !errors.Is(listenErr, context.Canceled) {
			terminalErr = fmt.Errorf("api listen: %w", listenErr)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), apiShutdownTimeout)
	shutdownErr := api.Shutdown(shutdownCtx)
	if !listenStopped {
		select {
		case <-listenErrors:
		case <-shutdownCtx.Done():
			if shutdownErr == nil {
				shutdownErr = shutdownCtx.Err()
			}
		}
	}
	cancel()
	// main 的 os.Exit 不执行 defer；先显式释放 coordinator/Manager/Redis/PG。
	api.Close()
	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("api shutdown: %w", shutdownErr)
	}
	return errors.Join(terminalErr, shutdownErr)
}
