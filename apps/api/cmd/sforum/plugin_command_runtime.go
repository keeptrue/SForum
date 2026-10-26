package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	postgres "github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
)

type postgresPluginCommandConsole struct {
	pool       *pgxpool.Pool
	store      *extensions.PostgresStore
	manager    *extensionsruntime.Manager
	gateway    *hostapi.Gateway
	auditor    audit.Writer
	catalog    *extensionsruntime.PluginCommandRegistry
	extensions map[string]extensions.Extension
	safeMode   bool
}

func openPostgresPluginCommandConsole(ctx context.Context, opts pluginCommandOptions) (pluginCommandConsole, error) {
	databaseURL := strings.TrimSpace(opts.DatabaseURL)
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		return nil, errors.New("plugin command database url is empty: set DATABASE_URL or pass --database-url")
	}
	pool, err := postgres.NewPool(ctx, databaseURL, 4)
	if err != nil {
		return nil, fmt.Errorf("connect plugin command database: %w", err)
	}
	fail := func(cause error) (pluginCommandConsole, error) {
		pool.Close()
		return nil, cause
	}
	store := extensions.NewPostgresStore(pool)
	items, err := store.List(ctx)
	if err != nil {
		return fail(fmt.Errorf("list plugin command extensions: %w", err))
	}
	safeMode := opts.SafeMode || pluginCommandSafeModeFromEnv()
	catalog := extensionsruntime.NewPluginCommandRegistry()
	extensionByID := make(map[string]extensions.Extension)
	for _, item := range items {
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled ||
			item.Manifest.Backend.ProtocolVersion != 2 || len(item.Manifest.Commands) == 0 {
			continue
		}
		if err := catalog.ReplaceRuntime(item, "catalog:"+item.PackageDigest); err != nil {
			return fail(fmt.Errorf("build plugin command namespace for %s: %w", item.ID, err))
		}
		extensionByID[item.ID] = item
	}
	auditor := audit.NewPostgresWriter(pool)
	trust := extensions.NewExecutableTrustService(store, extensions.NewPostgresExecutableTrustStore(pool)).WithAuditor(auditor)
	service := extensions.NewServiceWithOptions(
		store, pluginCommandEnv("EXTENSION_ROOT", "../../storage/extensions"),
		pluginCommandEnv("BUILTIN_EXTENSION_ROOT", "../../extensions/builtin"), nil,
		extensions.WithSafeMode(safeMode), extensions.WithExecutableTrust(trust, true), extensions.WithAuditor(auditor),
	)
	hostService := hostapi.New(hostapi.Config{Settings: service, Auditor: auditor})
	hostService.BindCapabilitySource(service)
	gateway := hostapi.NewGateway(hostService)
	if err := gateway.Start(); err != nil {
		return fail(fmt.Errorf("start plugin command Host API gateway: %w", err))
	}
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Settings: service, HostAPI: gateway, Trust: trust,
		DatabaseLeases: extensionsruntime.NewPostgresExtensionDatabaseRegistry(pool, nil),
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	extensions.WithRuntimeManager(manager)(service)
	return &postgresPluginCommandConsole{
		pool: pool, store: store, manager: manager, gateway: gateway, auditor: auditor,
		catalog: catalog, extensions: extensionByID, safeMode: safeMode,
	}, nil
}

func (c *postgresPluginCommandConsole) List(context.Context) ([]pluginCommandDescriptor, error) {
	if c == nil || c.catalog == nil {
		return nil, extensionsruntime.ErrPluginCommandRegistryInvalid
	}
	snapshot := c.catalog.Snapshot()
	commands := make([]pluginCommandDescriptor, 0, len(snapshot.Commands))
	for _, contract := range snapshot.Commands {
		commands = append(commands, pluginCommandDescriptorFromContract(contract, c.safeMode))
	}
	return commands, nil
}

func (c *postgresPluginCommandConsole) Run(
	ctx context.Context,
	commandID string,
	input map[string]any,
) (result pluginCommandRunResult, resultErr error) {
	if c == nil || c.catalog == nil || c.manager == nil {
		return result, extensionsruntime.ErrPluginCommandRegistryInvalid
	}
	contract, err := c.catalog.Resolve(commandID, c.safeMode)
	if err != nil {
		return result, err
	}
	extension, ok := c.extensions[contract.ExtensionID]
	if !ok {
		return result, extensionsruntime.ErrPluginCommandNotFound
	}
	metadata := map[string]any{
		"commandId": contract.ID, "contractVersion": contract.ContractVersion,
		"extensionId": contract.ExtensionID, "extensionVersion": contract.ExtensionVersion,
		"artifactDigest": contract.ArtifactDigest, "safeMode": c.safeMode,
		"authority": "host_operator_cli", "status": "attempted",
	}
	if err := c.auditor.Append(ctx, audit.Event{Action: audit.ActionExtensionPluginCommand, Metadata: metadata}); err != nil {
		return result, fmt.Errorf("record plugin command attempt: %w", err)
	}
	defer func() {
		status := "succeeded"
		if resultErr != nil {
			status = "failed"
		}
		completion := make(map[string]any, len(metadata))
		for key, value := range metadata {
			completion[key] = value
		}
		completion["status"] = status
		auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.auditor.Append(auditCtx, audit.Event{Action: audit.ActionExtensionPluginCommand, Metadata: completion})
	}()
	if err := c.manager.Start(ctx, extension); err != nil {
		return result, fmt.Errorf("start exact plugin command runtime: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.manager.Stop(stopCtx, extension); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("stop plugin command runtime: %w", err)
		}
	}()
	executed, err := c.manager.ExecutePluginCommand(ctx, contract.ID, input, c.safeMode)
	if err != nil {
		return result, err
	}
	return pluginCommandRunResult{
		CommandID: executed.Contract.ID, ContractVersion: executed.Contract.ContractVersion,
		ExtensionID: executed.Contract.ExtensionID, ExtensionVersion: executed.Contract.ExtensionVersion,
		ArtifactDigest: executed.Contract.ArtifactDigest, Output: executed.Output,
	}, nil
}

func (c *postgresPluginCommandConsole) Close(ctx context.Context) {
	if c == nil {
		return
	}
	if c.manager != nil {
		c.manager.Close(ctx)
	}
	if c.gateway != nil {
		_ = c.gateway.Close()
	}
	if c.pool != nil {
		c.pool.Close()
	}
}

func pluginCommandSafeModeFromEnv() bool {
	value := strings.TrimSpace(os.Getenv("SFORUM_SAFE_MODE"))
	enabled, err := strconv.ParseBool(value)
	return err == nil && enabled
}

func pluginCommandEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
