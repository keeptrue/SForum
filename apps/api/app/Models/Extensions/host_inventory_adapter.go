package extensions

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	runtimerollout "github.com/keeptrue/sforum/apps/api/app/Support/RuntimeRollout"
	settingslifecycle "github.com/keeptrue/sforum/apps/api/app/Support/SettingsLifecycle"
)

// ListRedactedInventory 实现 hostapi.ExtensionInventoryReader。
// 将去敏结构体转为 map[string]any，便于 Host Query 编码。
func (s *CatalogService) ListRedactedInventoryMaps(ctx context.Context) ([]map[string]any, error) {
	items, err := s.ListRedactedInventory(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, redactedInventoryItemMap(item))
	}
	return out, nil
}

// HostInventoryAdapter 把 Service 适配为 HostAPI ExtensionInventoryReader。
type HostInventoryAdapter struct {
	Service *Service
}

func (a HostInventoryAdapter) ListRedactedInventory(ctx context.Context) ([]map[string]any, error) {
	if a.Service == nil {
		return nil, ErrRuntimeUnavailable
	}
	return a.Service.ListRedactedInventoryMaps(ctx)
}

func redactedInventoryItemMap(item RedactedExtensionInventoryItem) map[string]any {
	row := map[string]any{
		"id":          item.ID,
		"name":        item.Name,
		"version":     item.Version,
		"type":        item.Type,
		"status":      item.Status,
		"source":      item.Source,
		"isSystem":    item.IsSystem,
		"installedAt": item.InstalledAt.UTC().Format(time.RFC3339Nano),
		"updatedAt":   item.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if item.PackageDigest != "" {
		row["packageDigest"] = item.PackageDigest
	}
	if len(item.Capabilities) > 0 {
		// 复制切片，避免调用方持有内部引用。
		caps := make([]any, len(item.Capabilities))
		for i, key := range item.Capabilities {
			caps[i] = key
		}
		row["capabilities"] = caps
	}
	if item.RuntimeState != "" {
		row["runtimeState"] = item.RuntimeState
	}
	if item.Protocol != "" {
		row["protocolTransport"] = item.Protocol
	}
	return row
}

// 以下方法保留历史 Service 契约；业务逻辑由 focused collaborators 持有。

func (s *Service) ListPackagesWithoutActor(ctx context.Context) ([]Extension, error) {
	return s.catalog.ListPackagesWithoutActor(ctx)
}

func (s *Service) AuthProviderSettingsConfigured(ctx context.Context, extensionID string) (bool, error) {
	return s.settings.AuthProviderSettingsConfigured(ctx, extensionID)
}

func (s *Service) ResolveInstalledDependencyGraph(ctx context.Context) (extensionmanifest.PackageGraph, error) {
	return s.lifecycle.ResolveInstalledDependencyGraph(ctx)
}

func (s *Service) PreflightActivationDependencies(ctx context.Context, id string) (extensionmanifest.PackageGraph, error) {
	return s.lifecycle.PreflightActivationDependencies(ctx, id)
}

func (s *Service) SyncExternalSources(ctx context.Context) (ExternalSyncResult, error) {
	return s.catalog.SyncExternalSources(ctx)
}

func (s *Service) ListRedactedInventoryMaps(ctx context.Context) ([]map[string]any, error) {
	return s.catalog.ListRedactedInventoryMaps(ctx)
}

func (s *Service) InstallArchive(ctx context.Context, actor identity.Actor, input ArchiveInput) (Extension, error) {
	return s.lifecycle.InstallArchive(ctx, actor, input)
}

func (s *Service) InstallOrUpgradeArchive(ctx context.Context, actor identity.Actor, input ArchiveInput) (InstallResult, error) {
	return s.lifecycle.InstallOrUpgradeArchive(ctx, actor, input)
}

func (s *Service) Uninstall(ctx context.Context, actor identity.Actor, id string, input UninstallInput) error {
	return s.lifecycle.Uninstall(ctx, actor, id, input)
}

func (s *Service) UninstallWithResult(ctx context.Context, actor identity.Actor, id string, input UninstallInput) (UninstallResult, error) {
	return s.lifecycle.UninstallWithResult(ctx, actor, id, input)
}

func (s *Service) ApplyDeclaredMigrations(ctx context.Context, actor identity.Actor, id string) ([]MigrationRecord, error) {
	return s.lifecycle.ApplyDeclaredMigrations(ctx, actor, id)
}

func (s *Service) ListMigrations(ctx context.Context, actor identity.Actor, id string) ([]MigrationRecord, error) {
	return s.lifecycle.ListMigrations(ctx, actor, id)
}

func (s *Service) LifecycleOperations(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	limit int,
) ([]LifecycleOperationSummary, error) {
	return s.lifecycle.LifecycleOperations(ctx, actor, extensionID, limit)
}

func (s *Service) LifecycleOperation(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	operationID int64,
) (LifecycleOperationDetail, error) {
	return s.lifecycle.LifecycleOperation(ctx, actor, extensionID, operationID)
}

func (s *Service) CleanupMissingArtifacts(
	ctx context.Context,
	actor identity.Actor,
	input MissingArtifactCleanupInput,
) (MissingArtifactCleanupResult, error) {
	return s.lifecycle.CleanupMissingArtifacts(ctx, actor, input)
}

func (s *Service) PluginJobContract(ctx context.Context, extensionID, jobName string) (supportjobs.PluginJobContract, error) {
	return s.catalog.PluginJobContract(ctx, extensionID, jobName)
}

func (s *Service) InspectProviderSlots(ctx context.Context, actor identity.Actor) (ProviderSlotInspection, error) {
	return s.catalog.InspectProviderSlots(ctx, actor)
}

func (s *Service) ListRedactedInventory(ctx context.Context) ([]RedactedExtensionInventoryItem, error) {
	return s.catalog.ListRedactedInventory(ctx)
}

func (s *Service) DriveRuntimeRolloutForStagedUpgrade(
	ctx context.Context,
	actor identity.Actor,
	source, target Extension,
	migrationOK bool,
	migrationErr error,
) (runtimerollout.Plan, error) {
	return s.lifecycle.DriveRuntimeRolloutForStagedUpgrade(ctx, actor, source, target, migrationOK, migrationErr)
}

func (s *Service) ExecutableTrustStatus(ctx context.Context, actor identity.Actor, extensionID string) (ExecutableTrustStatus, error) {
	return s.lifecycle.ExecutableTrustStatus(ctx, actor, extensionID)
}

func (s *Service) ExecutableTrustStatusForStaged(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
) (ExecutableTrustStatus, error) {
	return s.lifecycle.ExecutableTrustStatusForStaged(ctx, actor, extensionID)
}

func (s *Service) IssueExecutableTrustChallenge(ctx context.Context, actor identity.Actor, extensionID string) (TrustChallenge, error) {
	return s.lifecycle.IssueExecutableTrustChallenge(ctx, actor, extensionID)
}

func (s *Service) IssueExecutableTrustChallengeForStaged(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
) (TrustChallenge, error) {
	return s.lifecycle.IssueExecutableTrustChallengeForStaged(ctx, actor, extensionID)
}

func (s *Service) RevokeExecutableTrust(ctx context.Context, actor identity.Actor, extensionID string) (ExecutableTrustStatus, error) {
	return s.lifecycle.RevokeExecutableTrust(ctx, actor, extensionID)
}

func (s *Service) List(ctx context.Context, actor identity.Actor) ([]Extension, error) {
	return s.catalog.List(ctx, actor)
}

func (s *Service) Detail(ctx context.Context, actor identity.Actor, extensionID string) (Extension, error) {
	return s.catalog.Detail(ctx, actor, extensionID)
}

func (s *Service) SyncBuiltins(ctx context.Context) ([]Extension, error) {
	return s.catalog.SyncBuiltins(ctx)
}

func (s *Service) Events(ctx context.Context, actor identity.Actor, extensionID string, limit int) ([]ExtensionEvent, error) {
	return s.catalog.Events(ctx, actor, extensionID, limit)
}

func (s *Service) EventDefinitions(ctx context.Context, actor identity.Actor) ([]appevents.Definition, error) {
	return s.catalog.EventDefinitions(ctx, actor)
}

func (s *Service) EventDeliveries(ctx context.Context, actor identity.Actor, input EventDeliveryListInput) ([]ExtensionEventDelivery, error) {
	return s.catalog.EventDeliveries(ctx, actor, input)
}

func (s *Service) ContributionPoints(ctx context.Context, actor identity.Actor) ([]ContributionPointDefinition, error) {
	return s.catalog.ContributionPoints(ctx, actor)
}

func (s *Service) Contributions(ctx context.Context, actor identity.Actor) ([]EffectiveContribution, error) {
	return s.catalog.Contributions(ctx, actor)
}

func (s *Service) EffectiveContributions(ctx context.Context) ([]EffectiveContribution, error) {
	return s.catalog.EffectiveContributions(ctx)
}

func (s *Service) Navigation(ctx context.Context, actor identity.Actor) ([]ExtensionAdminNavigationItem, error) {
	return s.catalog.Navigation(ctx, actor)
}

func (s *Service) Settings(ctx context.Context, actor identity.Actor, extensionID string, locale string) (ExtensionSettings, error) {
	return s.settings.Settings(ctx, actor, extensionID, locale)
}

func (s *Service) AdminPageBootstrap(ctx context.Context, actor identity.Actor, extensionID, pagePath, locale string) (AdminPageBootstrap, error) {
	return s.catalog.AdminPageBootstrap(ctx, actor, extensionID, pagePath, locale)
}

func (s *Service) UpdateSettings(ctx context.Context, actor identity.Actor, extensionID string, input UpdateSettingsInput, locale string) (ExtensionSettings, error) {
	return s.settings.UpdateSettings(ctx, actor, extensionID, input, locale)
}

func (s *Service) ResetSettings(ctx context.Context, actor identity.Actor, extensionID string, locale string) (ExtensionSettings, error) {
	return s.settings.ResetSettings(ctx, actor, extensionID, locale)
}

func (s *Service) PublicActiveThemeSettings(ctx context.Context) (PublicActiveThemeSettings, error) {
	return s.lifecycle.PublicActiveThemeSettings(ctx)
}

func (s *Service) MatchRoute(ctx context.Context, extensionID string, method string, routePath string) (MatchedRoute, error) {
	return s.lifecycle.MatchRoute(ctx, extensionID, method, routePath)
}

func (s *Service) Enable(ctx context.Context, actor identity.Actor, id string, input EnableInput) (Extension, error) {
	return s.lifecycle.Enable(ctx, actor, id, input)
}

func (s *Service) Disable(ctx context.Context, actor identity.Actor, id string) (Extension, error) {
	return s.lifecycle.Disable(ctx, actor, id)
}

func (s *Service) DisableWithInput(ctx context.Context, actor identity.Actor, id string, input LifecycleRequestInput) (Extension, error) {
	return s.lifecycle.DisableWithInput(ctx, actor, id, input)
}

func (s *Service) VerifyExtension(ctx context.Context, actor identity.Actor, id string) (Extension, error) {
	return s.lifecycle.VerifyExtension(ctx, actor, id)
}

func (s *Service) ActivateTheme(ctx context.Context, actor identity.Actor, id string) (Extension, error) {
	return s.lifecycle.ActivateTheme(ctx, actor, id)
}

func (s *Service) ActivateThemeFromPreview(
	ctx context.Context,
	actor identity.Actor,
	id string,
	input ThemeActivationInput,
) (Extension, error) {
	return s.lifecycle.ActivateThemeFromPreview(ctx, actor, id, input)
}

func (s *Service) RecoverLifecycleOperation(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	operationID int64,
	input LifecycleRecoveryInput,
) (LifecycleOperationDetail, error) {
	return s.lifecycle.RecoverLifecycleOperation(ctx, actor, extensionID, operationID, input)
}

func (s *Service) Upgrade(ctx context.Context, actor identity.Actor, id string, input UpgradeInput) (Extension, error) {
	return s.lifecycle.Upgrade(ctx, actor, id, input)
}

func (s *Service) Rollback(ctx context.Context, actor identity.Actor, id string, input RollbackInput) (Extension, error) {
	return s.lifecycle.Rollback(ctx, actor, id, input)
}

func (s *Service) RestoreActiveThemeRegistry(ctx context.Context) error {
	return s.theme.RestoreActiveThemeRegistry(ctx)
}

func (s *Service) RestoreSafeModeThemeRegistry(ctx context.Context) error {
	return s.theme.RestoreSafeModeThemeRegistry(ctx)
}

func (s *Service) FailClosedThemeRuntime(ctx context.Context) error {
	return s.theme.FailClosedThemeRuntime(ctx)
}

func (s *Service) EnsureDefaultThemeActive(ctx context.Context) (Extension, error) {
	return s.theme.EnsureDefaultThemeActive(ctx)
}

func (s *Service) CapabilitiesFor(ctx context.Context, extensionID string) (capabilities.Set, error) {
	return s.catalog.CapabilitiesFor(ctx, extensionID)
}

func (s *Service) DeclaredJobKinds(ctx context.Context, extensionID string) ([]string, error) {
	return s.catalog.DeclaredJobKinds(ctx, extensionID)
}

func (s *Service) CapabilityCatalog(ctx context.Context, actor identity.Actor) ([]capabilities.Definition, error) {
	return s.catalog.CapabilityCatalog(ctx, actor)
}

func (s *Service) ListSettingsForRuntime(ctx context.Context, extensionID string) (map[string]string, error) {
	return s.settings.ListSettingsForRuntime(ctx, extensionID)
}

func (s *Service) ListSettings(ctx context.Context, extensionID string) (map[string]string, error) {
	return s.settings.ListSettings(ctx, extensionID)
}

func (s *Service) Restart(
	ctx context.Context,
	actor identity.Actor,
	id string,
	input RestartInput,
) (Extension, error) {
	return s.lifecycle.Restart(ctx, actor, id, input)
}

func (s *Service) ExecuteSettingsAction(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	actionID string,
	input ExecuteSettingsActionInput,
) (result SettingsActionResult, err error) {
	return s.settings.ExecuteSettingsAction(ctx, actor, extensionID, actionID, input)
}

func (s *Service) SettingsLifecycle() SettingsLifecycleRuntime {
	return s.settings.SettingsLifecycle()
}

func (s *Service) RegisterSettingsLifecycleFromManifest(extension Extension) error {
	return s.settings.RegisterSettingsLifecycleFromManifest(extension)
}

func (s *Service) ImportSettings(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	bundle settingslifecycle.ExportBundle,
	locale string,
) (ExtensionSettings, error) {
	return s.settings.ImportSettings(ctx, actor, extensionID, bundle, locale)
}

func (s *Service) ExportSettings(ctx context.Context, actor identity.Actor, extensionID string) (settingslifecycle.ExportBundle, error) {
	return s.settings.ExportSettings(ctx, actor, extensionID)
}

func (s *Service) ApplyThemeRuntimePublication(ctx context.Context, publication ThemeRuntimePublication) error {
	return s.theme.ApplyThemeRuntimePublication(ctx, publication)
}
