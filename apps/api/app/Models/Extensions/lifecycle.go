package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

// TrustRevoker 在包 digest 变化时吊销前端信任（F2.4 升级重审批）。
type TrustRevoker interface {
	RevokeAllForExtension(ctx context.Context, extensionID string, actorUserID int64) error
}

// WithTrustRevoker 注入升级时的信任吊销器。
func WithTrustRevoker(revoker TrustRevoker) ServiceOption {
	return func(s *Service) {
		s.trustRevoker = revoker
	}
}

// InstallArchive 安装或同 id 升级上传包（兼容旧调用方）。
func (s *LifecycleService) InstallArchive(ctx context.Context, actor identity.Actor, input ArchiveInput) (Extension, error) {
	result, err := s.InstallOrUpgradeArchive(ctx, actor, input)
	if err != nil {
		return Extension{}, err
	}
	return result.Extension, nil
}

// InstallOrUpgradeArchive 返回完整升级元数据（F2.4）。
// V3 静态上传只保存不可变候选；活动 runtime、状态、信任和 provider 选择均保持不变。
func (s *LifecycleService) InstallOrUpgradeArchive(ctx context.Context, actor identity.Actor, input ArchiveInput) (InstallResult, error) {
	// 上传前先挡住无扩展管理权限的调用者；包类型只能在有界静态解析后确定。
	if !canManagePlugins(actor) && !canManageThemes(actor) {
		return InstallResult{}, identity.ErrPermissionDenied
	}
	if len(input.Data) == 0 || len(input.Data) > maxArchiveBytes {
		return InstallResult{}, ErrInvalidArchive
	}

	manifest, packageFiles, err := extensionpackage.ReadArchive(input.Data, extensionpackage.ArchiveLimits{
		Entries: maxArchiveEntries,
		Bytes:   maxArchiveBytes,
	})
	if err != nil {
		if errors.Is(err, extensionpackage.ErrInvalidArchive) {
			return InstallResult{}, ErrInvalidArchive
		}
		if errors.Is(err, extensionpackage.ErrInvalidManifest) {
			return InstallResult{}, ErrInvalidManifest
		}
		return InstallResult{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return InstallResult{}, err
	}
	// 期望身份必须在类型权限 switch 之前比对：类型不符时报“期望不匹配”，
	// 而不是让调用者看到一个像是权限问题的错误。
	if err := checkArchiveExpectations(input, manifest); err != nil {
		return InstallResult{}, err
	}
	// 上传包只会以 uploaded 身份落库，因此安装阶段不需要内置豁免。
	if err := ensureCoreCompatible(manifest, CoreCompatibilityStageInstall); err != nil {
		return InstallResult{}, err
	}
	// 架构闸门也在落盘之前：错架构的包一旦进 store，管理端就会看到一个永远
	// 起不来的扩展。判定读的是已解进内存的 backend 可执行文件头。
	if err := requireArchiveExecutablePlatform(manifest, packageFiles); err != nil {
		return InstallResult{}, err
	}
	switch manifest.Type {
	case TypePlugin:
		if !canManagePlugins(actor) {
			return InstallResult{}, identity.ErrPermissionDenied
		}
	case TypeTheme:
		if !canManageThemes(actor) {
			return InstallResult{}, identity.ErrPermissionDenied
		}
	default:
		return InstallResult{}, ErrInvalidManifest
	}
	if manifest.ID == DefaultThemeID {
		return InstallResult{}, ErrInvalidManifest
	}

	// V3 静态安装只校验并保存惰性包，不执行包代码；迁移开关关闭时保留 v1 边界。
	if !s.trustChallengesEnabled {
		if err := requireSuperAdminForUntrustedBackend(actor, SourceUploaded, manifest); err != nil {
			s.denyUntrustedBackend(ctx, actor, manifest.ID, "install")
			return InstallResult{}, err
		}
	}

	var previous Extension
	var isUpgrade bool
	if existing, getErr := s.store.Get(ctx, manifest.ID); getErr == nil {
		previous = existing
		isUpgrade = true
		if existing.Source == SourceBuiltin || existing.IsSystem {
			return InstallResult{}, ErrNotDeletable
		}
	} else if !errors.Is(getErr, ErrExtensionNotFound) {
		return InstallResult{}, getErr
	}

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return InstallResult{}, err
	}
	ownedSnapshot, err := extensionpackage.SnapshotUploadedOwned(s.extensionRoot, manifestJSON, packageFiles)
	if err != nil {
		switch {
		case errors.Is(err, extensionpackage.ErrInvalidManifest):
			return InstallResult{}, ErrInvalidManifest
		case errors.Is(err, extensionpackage.ErrInvalidPath),
			errors.Is(err, extensionpackage.ErrNonRegular),
			errors.Is(err, extensionpackage.ErrSymlink):
			return InstallResult{}, ErrInvalidArchive
		}
		return InstallResult{}, err
	}
	defer ownedSnapshot.Release()
	snapshot := ownedSnapshot.Snapshot
	if manifest.Type == TypeTheme {
		// 上传安装仍是惰性的，但所有 L1 模板必须在进入权威 Store 前完成
		// 静态安全检查、受限 AST 校验和 html/template 上下文编译。
		if err := themecompiler.NewCompiler(themecompiler.Limits{}).PreflightFS(os.DirFS(snapshot.Root)); err != nil {
			installErr := fmt.Errorf("%w: theme template preflight: %w", ErrInvalidManifest, err)
			return InstallResult{}, errors.Join(installErr, s.discardUnreferencedUploadedSnapshot(ctx, ownedSnapshot))
		}
	}

	adminFrontendDigest, err := ComputeAdminFrontendDigest(manifest, snapshot.Root)
	if err != nil {
		return InstallResult{}, errors.Join(ErrInvalidManifest, s.discardUnreferencedUploadedSnapshot(ctx, ownedSnapshot))
	}
	installed, err := s.store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest:            manifest,
		PackagePath:         snapshot.Root,
		PackageDigest:       snapshot.Digest,
		AdminFrontendDigest: adminFrontendDigest,
	})
	if err != nil {
		// SaveInstalled may have committed before returning an error (for example,
		// a failed post-commit reload). Retain the bytes for later reconciliation.
		return InstallResult{}, err
	}
	_ = ownedSnapshot.Release()

	result := InstallResult{
		Extension:         s.decorateRuntime(ctx, installed),
		Upgraded:          isUpgrade,
		RequiredReEnable:  false,
		ActivationPending: isUpgrade && installed.StagedVersion != nil,
	}
	if isUpgrade {
		candidateVersion := installed.Version
		candidateDigest := installed.PackageDigest
		candidateAdminDigest := installed.AdminFrontendDigest
		if installed.StagedVersion != nil {
			candidateVersion = installed.StagedVersion.Version
			candidateDigest = installed.StagedVersion.PackageDigest
			candidateAdminDigest = installed.StagedVersion.AdminFrontendDigest
		}
		result.PreviousVersion = previous.Version
		result.PreviousDigest = previous.PackageDigest
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: installed.ID,
			ActorUserID: actor.ID,
			Action:      EventUpgraded,
			Message:     fmt.Sprintf("Staged upgrade from %s to %s; activation is pending.", previous.Version, candidateVersion),
		})
		upgradeMetadata := map[string]any{
			"extensionId":          installed.ID,
			"type":                 installed.Type,
			"previousVersion":      previous.Version,
			"candidateVersion":     candidateVersion,
			"previousDigest":       previous.PackageDigest,
			"candidateDigest":      candidateDigest,
			"candidateAdminDigest": candidateAdminDigest,
			"activationPending":    installed.StagedVersion != nil,
			"trustRevoked":         false,
		}
		mergeArchiveSourceMetadata(upgradeMetadata, input)
		s.appendAudit(ctx, actor, audit.ActionExtensionUpgraded, upgradeMetadata)
	} else {
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: installed.ID,
			ActorUserID: actor.ID,
			Action:      EventInstalled,
			Message:     "Extension archive installed.",
		})
		installMetadata := map[string]any{
			"extensionId": installed.ID,
			"type":        installed.Type,
		}
		mergeArchiveSourceMetadata(installMetadata, input)
		s.appendAudit(ctx, actor, audit.ActionExtensionInstalled, installMetadata)
	}
	return result, nil
}

// mergeArchiveSourceMetadata records provenance without letting signed URL
// credentials or fragments reach the audit log. Install behavior never reads it.
func mergeArchiveSourceMetadata(metadata map[string]any, input ArchiveInput) {
	if source := sanitizeArchiveSourceURL(input.SourceURL); source != "" {
		metadata["sourceUrl"] = source
	}
	if digest := strings.TrimSpace(input.SourceDigest); digest != "" {
		metadata["sourceDigest"] = digest
	}
	if label := sanitizeSourceLabel(input.SourceLabel); label != "" {
		metadata["sourceLabel"] = label
	}
}

func sanitizeArchiveSourceURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// discardUnreferencedUploadedSnapshot runs while the per-digest lock is held.
// Any Store uncertainty or active/staged reference retains the immutable bytes.
func (s *serviceCore) discardUnreferencedUploadedSnapshot(ctx context.Context, snapshot *extensionpackage.OwnedSnapshot) error {
	if snapshot == nil || !snapshot.Created() {
		return nil
	}
	if store, ok := s.store.(interface {
		PackagePathReferenced(context.Context, string) (bool, error)
	}); ok {
		referenced, err := store.PackagePathReferenced(ctx, snapshot.Root)
		if err != nil || referenced {
			return nil
		}
		if err := snapshot.RemoveIfCreated(); err != nil {
			return fmt.Errorf("discard unreferenced uploaded snapshot: %w", err)
		}
		return nil
	}
	items, err := s.store.List(ctx)
	if err != nil {
		return nil
	}
	for _, item := range items {
		if samePackagePath(item.PackagePath, snapshot.Root) ||
			(item.StagedVersion != nil && samePackagePath(item.StagedVersion.PackagePath, snapshot.Root)) {
			return nil
		}
	}
	if err := snapshot.RemoveIfCreated(); err != nil {
		return fmt.Errorf("discard unreferenced uploaded snapshot: %w", err)
	}
	return nil
}

func samePackagePath(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func (s *LifecycleService) Uninstall(ctx context.Context, actor identity.Actor, id string, input UninstallInput) error {
	_, err := s.UninstallWithResult(ctx, actor, id, input)
	return err
}

// UninstallWithResult 先检查 durable replay，再选择 V2 coordinator 或 V1 兼容路径。
// V2 的 package/runtime/authority 会保留到 terminal success，物理删除只能由 exact-receipt finalizer 执行。
func (s *LifecycleService) UninstallWithResult(ctx context.Context, actor identity.Actor, id string, input UninstallInput) (UninstallResult, error) {
	if !canManagePlugins(actor) {
		return UninstallResult{}, identity.ErrPermissionDenied
	}
	id = normalizeID(id)
	if id == "" {
		return UninstallResult{}, ErrExtensionNotFound
	}
	if validLifecycleServiceIdempotencyKey(input.IdempotencyKey) && s.lifecycleAuthority != nil {
		operation, err := s.lifecycleAuthority.OperationByIdempotencyKey(ctx, id, input.IdempotencyKey)
		switch {
		case err == nil:
			return s.replayLifecycleUninstall(ctx, actor, id, input, operation)
		case !errors.Is(err, ErrLifecycleOperationNotFound):
			return UninstallResult{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
		}
	}
	extension, err := s.store.Get(ctx, id)
	if err != nil {
		return UninstallResult{}, err
	}
	if extension.Source == SourceBuiltin || extension.IsSystem || !extension.IsDeletable {
		return UninstallResult{}, ErrNotDeletable
	}
	if extension.ID == DefaultThemeID {
		return UninstallResult{}, ErrNotDeletable
	}
	if err := requireArtifactAvailable(extension); err != nil {
		return UninstallResult{}, err
	}
	if usesLifecycleV2(extension) && (extension.Status == StatusEnabled || extension.Status == StatusDisabled) {
		return s.uninstallLifecycleV2(ctx, actor, extension, input)
	}
	if err := requireSuperAdminForUntrustedBackend(actor, extension.Source, extension.Manifest); err != nil {
		s.denyUntrustedBackend(ctx, actor, extension.ID, "uninstall")
		return UninstallResult{}, err
	}
	if extension.Status == StatusEnabled {
		return UninstallResult{}, ErrMustDisableFirst
	}
	s.assetPublicationMu.Lock()
	defer s.assetPublicationMu.Unlock()
	extension, err = s.store.Get(ctx, extension.ID)
	if err != nil {
		return UninstallResult{}, err
	}
	if extension.Status == StatusEnabled {
		return UninstallResult{}, ErrMustDisableFirst
	}
	if err := requireArtifactAvailable(extension); err != nil {
		return UninstallResult{}, err
	}
	assetBefore := s.captureAssetPublicationSnapshot()
	assetMutation, err := s.quarantineExactAssetPublication(ctx, assetBefore, extension)
	if err != nil {
		return UninstallResult{}, fmt.Errorf("remove exact asset publication before uninstall: %w", err)
	}

	_ = s.drainPluginRuntime(ctx, extension)
	// 立即清除页面贡献（即便已 disable 也应幂等清理）
	if s.pageRegistry != nil {
		s.pageRegistry.ClearExtension(extension.ID)
	}
	packagePath := extension.PackagePath
	identityRetained := false
	if err := s.store.Delete(ctx, extension.ID); err != nil {
		// 历史 plugin_runtime_publication_members 对 extension_versions 为 ON DELETE RESTRICT：
		// 一旦进入 desired full-set，精确版本行不可物理删除（见 lifecycle publication 门禁）。
		// 目录卸载仍须成功：保留不可变身份，删除包文件，扩展保持 disabled。
		if isPublishedPluginRuntimeIdentityRetained(err) {
			identityRetained = true
		} else {
			if restoreErr := s.rollbackExactAssetMutation(assetMutation); restoreErr != nil {
				return UninstallResult{}, errors.Join(err, fmt.Errorf("restore asset publication after uninstall failure: %w", restoreErr))
			}
			return UninstallResult{}, err
		}
	}

	if !input.RetainPackage && packagePath != "" {
		if safe, ok := packagePathUnderRoot(s.extensionRoot, packagePath); ok {
			_ = os.RemoveAll(safe)
		}
	}

	// extension_events 在硬删除时 CASCADE；身份保留时仅写宿主 audit。
	s.appendAudit(ctx, actor, audit.ActionExtensionUninstalled, map[string]any{
		"extensionId":      extension.ID,
		"type":             extension.Type,
		"version":          extension.Version,
		"retainSettings":   input.RetainSettings,
		"retainPackage":    input.RetainPackage,
		"settingsDeleted":  !input.RetainSettings && !identityRetained,
		"identityRetained": identityRetained,
		// v1：settings 随 extensions CASCADE 删除；RetainSettings 记入审计供后续独立备份表使用。
	})
	// 卸载后该扩展的插件会话状态不得残留（同 id 重装不得读到上一份字节）。
	erasePluginSessionState(ctx, s.serviceCore, extension.ID)
	return UninstallResult{Uninstalled: true, ExtensionID: extension.ID}, nil
}

// isPublishedPluginRuntimeIdentityRetained 判断删除是否被不可变 runtime publication 历史挡住。
func isPublishedPluginRuntimeIdentityRetained(err error) bool {
	if err == nil {
		return false
	}
	// pgx/pgconn 包装：SQLSTATE 23503 + plugin_runtime_publication_members。
	msg := err.Error()
	return strings.Contains(msg, "plugin_runtime_publication_members") ||
		strings.Contains(msg, "extension_version_id_extensi_fkey")
}

func (s *serviceCore) replayLifecycleUninstall(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	input UninstallInput,
	operation LifecycleOperation,
) (UninstallResult, error) {
	removalMode, err := normalizeLifecycleRemovalMode(input.RemovalMode)
	if err != nil {
		return UninstallResult{}, err
	}
	if operation.ExtensionID != extensionID || operation.Operation != string(LifecycleMachineUninstall) ||
		operation.RemovalMode != removalMode ||
		(operation.RequestedByUserID > 0 && operation.RequestedByUserID != actor.ID) {
		return UninstallResult{}, ErrLifecycleFingerprintConflict
	}
	if operation.CompletedAt != nil {
		switch operation.TerminalResult {
		case LifecycleTerminalSucceeded:
			return s.finalizeLifecycleUninstall(ctx, extensionID, removalMode, operation, true)
		case LifecycleTerminalFailed, LifecycleTerminalCancelled:
			return UninstallResult{}, ErrLifecycleCoordinatorRetryRequired
		default:
			return UninstallResult{}, ErrLifecycleCoordinatorInvalid
		}
	}
	current, err := s.store.Get(ctx, extensionID)
	if err != nil {
		return UninstallResult{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	_, found, err := s.replayLifecycleV2(ctx, actor, current, input.IdempotencyKey)
	if err != nil {
		return UninstallResult{}, err
	}
	if !found {
		return UninstallResult{}, ErrLifecycleCoordinatorInvalid
	}
	latest, err := s.lifecycleAuthority.OperationByIdempotencyKey(ctx, extensionID, input.IdempotencyKey)
	if err != nil {
		return UninstallResult{}, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	return s.finalizeLifecycleUninstall(ctx, extensionID, removalMode, latest, true)
}

// ApplyDeclaredMigrations 将 manifest.migrations 登记到账本（F2.4 v1）。
// 不执行任意 SQL：只校验文件并记录 checksum，避免插件写核心库。
// 非内置后端插件的迁移登记仍限 super_admin，与启用边界一致。
func (s *LifecycleService) ApplyDeclaredMigrations(ctx context.Context, actor identity.Actor, id string) ([]MigrationRecord, error) {
	if !canManagePlugins(actor) {
		return nil, identity.ErrPermissionDenied
	}
	if s.safeMode {
		return nil, ErrSafeModeActive
	}
	extension, err := s.store.Get(ctx, normalizeID(id))
	if err != nil {
		return nil, err
	}
	if err := requireSuperAdminForUntrustedBackend(actor, extension.Source, extension.Manifest); err != nil {
		s.denyUntrustedBackend(ctx, actor, extension.ID, "migrations")
		return nil, err
	}
	applied, err := s.recordDeclaredMigrations(ctx, extension)
	if err != nil {
		return nil, err
	}
	if len(applied) > 0 {
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: extension.ID,
			ActorUserID: actor.ID,
			Action:      EventMigrationsApplied,
			Message:     fmt.Sprintf("Recorded %d declared migration path(s).", len(applied)),
		})
	}
	return s.store.ListMigrationLedger(ctx, extension.ID)
}

// ListMigrations 读取迁移账本。
func (s *LifecycleService) ListMigrations(ctx context.Context, actor identity.Actor, id string) ([]MigrationRecord, error) {
	if !canViewExtensions(actor) && !canManagePlugins(actor) {
		return nil, identity.ErrPermissionDenied
	}
	if _, err := s.store.Get(ctx, normalizeID(id)); err != nil {
		return nil, err
	}
	return s.store.ListMigrationLedger(ctx, normalizeID(id))
}

// StorageSelectionClearer 在禁用存储插件时把 attachment.provider 从 plugin:<id> 回落 local（E6.1）。
type StorageSelectionClearer interface {
	ClearStorageProviderSelectionIfMatch(ctx context.Context, extensionID string) error
}

type mailProviderSelectionStore interface {
	SelectedMailProvider(context.Context) (string, error)
	RestoreMailProvider(context.Context) error
}

type RouteProviderSelectionInvalidator interface {
	InvalidateRouteProviderSelections(
		ctx context.Context,
		extensionID string,
		actorUserID int64,
		auditEventID int64,
		reasonCode string,
	) error
}

type ProviderSlotSelectionInvalidator interface {
	InvalidateProviderSlotSelections(
		ctx context.Context,
		extensionID string,
		actorUserID int64,
		auditEventID int64,
		reasonCode string,
	) error
}

// clearPluginProviderSelections 是 V1/V2 共用的 Host-owned 幂等清理边界。
// 它不触碰 runtime；V2 的进程 drain 只能由 durable coordinator 管理。
func (s *serviceCore) clearPluginProviderSelections(ctx context.Context, extensionID string) error {
	return s.clearPluginNonRouteProviderSelections(ctx, extensionID)
}

func (s *serviceCore) clearPluginProviderSelectionsWithAudit(
	ctx context.Context,
	extensionID string,
	actorUserID int64,
	auditEventID int64,
	reasonCode string,
) error {
	if s.providerSlotSelections != nil {
		if actorUserID <= 0 || auditEventID <= 0 || reasonCode == "" {
			return fmt.Errorf("provider slot selection invalidation requires actor and audit evidence")
		}
		if err := s.providerSlotSelections.InvalidateProviderSlotSelections(
			ctx, extensionID, actorUserID, auditEventID, reasonCode,
		); err != nil {
			return err
		}
	}
	if s.routeProviderSelections != nil {
		if actorUserID <= 0 || auditEventID <= 0 || reasonCode == "" {
			return fmt.Errorf("route provider selection invalidation requires actor and audit evidence")
		}
		if err := s.routeProviderSelections.InvalidateRouteProviderSelections(
			ctx, extensionID, actorUserID, auditEventID, reasonCode,
		); err != nil {
			return err
		}
	}
	return s.clearPluginNonRouteProviderSelections(ctx, extensionID)
}

func (s *serviceCore) clearPluginNonRouteProviderSelections(ctx context.Context, extensionID string) error {
	if selectionStore, ok := s.store.(mailProviderSelectionStore); ok {
		selected, err := selectionStore.SelectedMailProvider(ctx)
		if err != nil {
			return err
		}
		if selected == extensionID {
			if err := selectionStore.RestoreMailProvider(ctx); err != nil {
				return err
			}
		}
	}
	// E6.1：若附件存储选中了本插件，回落 local，避免孤儿 plugin: 选择。
	if s.storageSelection != nil {
		if err := s.storageSelection.ClearStorageProviderSelectionIfMatch(ctx, extensionID); err != nil {
			return err
		}
	}
	return nil
}

// drainPluginRuntime 禁用/升级/卸载共用的 V1 drain：停子进程、清 provider、发 disabled hook。
func (s *serviceCore) drainPluginRuntime(ctx context.Context, extension Extension) error {
	if extension.Type != TypePlugin {
		return nil
	}
	if err := s.clearPluginProviderSelections(ctx, extension.ID); err != nil {
		return err
	}
	if s.runtime != nil {
		_ = s.runtime.Stop(ctx, extension)
		if extension.Status == StatusEnabled {
			s.runtime.EmitHook(ctx, appevents.ExtensionDisabled, map[string]any{
				"extensionId": extension.ID,
				"reason":      "lifecycle_drain",
			})
		}
	}
	return nil
}

func (s *serviceCore) recordDeclaredMigrations(ctx context.Context, extension Extension) ([]MigrationRecord, error) {
	if len(extension.Manifest.Migrations) == 0 {
		return nil, nil
	}
	existing, _ := s.store.ListMigrationLedger(ctx, extension.ID)
	known := map[string]bool{}
	for _, row := range existing {
		known[row.Path] = true
	}
	recorded := []MigrationRecord{}
	for _, migration := range extension.Manifest.Migrations {
		path := strings.TrimSpace(migration.Path)
		if path == "" || known[path] {
			continue
		}
		checksum := ""
		message := "declared path recorded; SQL not executed by host v1 runner"
		status := "recorded"
		if full, ok := InstalledFilePathForRuntime(extension, path); ok {
			if body, err := os.ReadFile(full); err == nil {
				sum := sha256.Sum256(body)
				checksum = hex.EncodeToString(sum[:])
			} else {
				status = "failed"
				message = "migration file missing in package: " + err.Error()
			}
		} else {
			status = "failed"
			message = "unsafe or missing migration path"
		}
		record := MigrationRecord{Path: path, Checksum: checksum, Status: status, Message: message}
		if err := s.store.RecordMigration(ctx, extension.ID, record); err != nil {
			return recorded, fmt.Errorf("%w: %v", ErrMigrationFailed, err)
		}
		recorded = append(recorded, record)
	}
	return recorded, nil
}

func packagePathUnderRoot(root, packagePath string) (string, bool) {
	root = filepath.Clean(strings.TrimSpace(root))
	packagePath = filepath.Clean(strings.TrimSpace(packagePath))
	if root == "" || packagePath == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, packagePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return packagePath, true
}

// ErrArchiveExpectationMismatch 表示调用方在下载前声明的扩展身份与包内清单
// 不一致。它是中止条件，不是警告：期望值由调用方（管理页 bridge、私有目录）
// 在拉包之前锁定，包到手后必须逐项对上。
var ErrArchiveExpectationMismatch = errors.New("extensions: archive does not match the declared extension")

// ArchiveExpectationMismatchError 携带声明值与实际清单值，便于 API 以结构化
// 字段回报“差在哪一项”，也便于测试断言。
type ArchiveExpectationMismatchError struct {
	ExpectedExtensionID string
	ExpectedVersion     string
	ExpectedType        string
	ActualExtensionID   string
	ActualVersion       string
	ActualType          string
	Fields              []string
}

func (e *ArchiveExpectationMismatchError) Error() string {
	detail := fmt.Sprintf(
		"%s (declared id=%q version=%q type=%q; manifest has id=%q version=%q type=%q)",
		strings.Join(e.Fields, ", "),
		e.ExpectedExtensionID, e.ExpectedVersion, e.ExpectedType,
		e.ActualExtensionID, e.ActualVersion, e.ActualType,
	)
	return ErrArchiveExpectationMismatch.Error() + ": " + detail
}

func (e *ArchiveExpectationMismatchError) Unwrap() error { return ErrArchiveExpectationMismatch }

// 清单兼容求值的两个阶段分开表示：安装只暂存不可变候选，运行阶段会让制品
// 真正跑起来（启用/升级/回滚/主题激活/重启），对运维的含义不同。
const (
	CoreCompatibilityStageInstall = "install"
	CoreCompatibilityStageRuntime = "runtime"
)

// ErrCoreIncompatible 表示清单声明的 sforumVersion 不覆盖运行中的 coreVersion。
// 它是中止条件：core 是兼容性的最终裁判，插件不得自行推断宿主版本。
var ErrCoreIncompatible = errors.New("extensions: manifest sforumVersion excludes the running core")

// CoreIncompatibleError 携带声明范围与宿主身份，供 API 回传结构化字段。
type CoreIncompatibleError struct {
	Required     string
	CoreVersion  string
	BuildVersion string
	Stage        string
}

func (e *CoreIncompatibleError) Error() string {
	return fmt.Sprintf("%v: required=%q core=%q build=%q stage=%s",
		ErrCoreIncompatible, e.Required, e.CoreVersion, e.BuildVersion, e.Stage)
}

func (e *CoreIncompatibleError) Unwrap() error { return ErrCoreIncompatible }

// ensureCoreCompatible 用运行中的 coreVersion 求值清单约束。
//
// coreVersion 不是严格 semver 时跳过（启动时已 WARN）：那是构建标签缺陷，
// 不能把所有安装都判死；此时调用方看到的是「未知」，不是「不兼容」。
func ensureCoreCompatible(manifest Manifest, stage string) error {
	identity := platformversion.Identity()
	if !identity.Valid {
		return nil
	}
	err := extensionmanifest.CheckSForumVersion(manifest, identity.CoreVersion)
	if err == nil {
		return nil
	}
	var incompatible *extensionmanifest.SForumVersionIncompatibleError
	if errors.As(err, &incompatible) {
		return &CoreIncompatibleError{
			Required:     incompatible.Required,
			CoreVersion:  identity.CoreVersion,
			BuildVersion: identity.BuildVersion,
			Stage:        stage,
		}
	}
	return nil
}

// requireEnablePreflight 是「这个制品现在可以跑吗」的唯一闸门：制品必须在位，
// 非内置包必须被当前 core 的 sforumVersion 覆盖，后端可执行文件必须是本平台构建。
//
// 内置包对 coreVersion 豁免：它们与 core 同一个发行物，约束写错时运维没有修复
// 手段；把它们一起判死只会让默认主题在自己的发行版里装不上。架构不豁免——错
// 架构的内置包同样跑不起来，而且那种错是构建产物问题，不是声明问题。
func requireEnablePreflight(extension Extension) error {
	if err := requireArtifactAvailable(extension); err != nil {
		return err
	}
	if err := requireCoreCompatibleRun(extension.Manifest, extension.Source); err != nil {
		return err
	}
	return requireExecutablePlatform(extension)
}

// requireCoreCompatibleRun 只看「运行中的制品」这一层。source 由持有制品的
// 扩展传入：暂存版本与历史版本都会继承来源，所以内置包在任何入口都豁免。
func requireCoreCompatibleRun(manifest Manifest, source string) error {
	if source == SourceBuiltin {
		return nil
	}
	return ensureCoreCompatible(manifest, CoreCompatibilityStageRuntime)
}

// checkArchiveExpectations compares the caller-declared identity with the parsed
// manifest. Empty expectations are allowed so manual URL installs from the core
// panel keep working; the bridge always sends id and type.
func checkArchiveExpectations(input ArchiveInput, manifest Manifest) error {
	expectedID := normalizeID(strings.TrimSpace(input.ExpectedExtensionID))
	expectedType := strings.ToLower(strings.TrimSpace(input.ExpectedType))
	expectedVersion := strings.TrimSpace(input.ExpectedVersion)

	mismatch := &ArchiveExpectationMismatchError{
		ExpectedExtensionID: expectedID,
		ExpectedVersion:     expectedVersion,
		ExpectedType:        expectedType,
		ActualExtensionID:   manifest.ID,
		ActualVersion:       manifest.Version,
		ActualType:          manifest.Type,
	}
	if expectedID != "" && expectedID != normalizeID(manifest.ID) {
		mismatch.Fields = append(mismatch.Fields, "extensionId")
	}
	if expectedType != "" && expectedType != strings.ToLower(manifest.Type) {
		mismatch.Fields = append(mismatch.Fields, "type")
	}
	if expectedVersion != "" && expectedVersion != manifest.Version {
		mismatch.Fields = append(mismatch.Fields, "version")
	}
	if len(mismatch.Fields) == 0 {
		return nil
	}
	return mismatch
}

var sourceLabelPattern = regexp.MustCompile(`[^a-z0-9._:-]+`)

// maxSourceLabelLength keeps an audit label readable in a single log line.
const maxSourceLabelLength = 64

// sanitizeSourceLabel normalizes an audit-only provenance label such as
// "admin-page:sforum.store". Unknown characters are dropped rather than escaped
// so a label can never smuggle a newline or a URL into the audit log.
func sanitizeSourceLabel(raw string) string {
	value := sourceLabelPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(raw)), "")
	value = strings.Trim(value, ".:-")
	if len(value) > maxSourceLabelLength {
		value = value[:maxSourceLabelLength]
	}
	return value
}

// ValidSourceLabel reports whether raw is already a canonical audit label, so a
// route can reject junk instead of silently rewriting what the caller sent.
func ValidSourceLabel(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed != "" && trimmed == sanitizeSourceLabel(trimmed)
}

// WithLifecycleCleanupSweepSource 注入 pending tombstone 读取边界；
// nil 时 sweep 与免键收尾端点 fail closed 为 503。
func WithLifecycleCleanupSweepSource(source LifecycleCleanupSweepSource) ServiceOption {
	return func(service *Service) {
		service.lifecycleCleanupSweeper = source
	}
}

// LifecycleCleanupSweepFailure 记录 sweep 中单条失败的 operation 与原因。
type LifecycleCleanupSweepFailure struct {
	OperationID int64  `json:"operationId"`
	ExtensionID string `json:"extensionId"`
	Err         string `json:"err"`
}

// LifecycleCleanupSweepResult 汇总一轮收敛的结果。
type LifecycleCleanupSweepResult struct {
	Finalized []int64                        `json:"finalized"`
	Failed    []LifecycleCleanupSweepFailure `json:"failed,omitempty"`
}

// CleanupConvergenceService 是卸载物理清理的聚焦协作对象：把「台账已
// succeeded 但 tombstone 仍 pending」的收尾工作（周期 sweep、免键收尾端点、
// 管理端徽标投影）从 Service/LifecycleService 的 God-object 表面上分离。
// 它经内嵌 *Service 复用 serviceCore 的 finalize 边界与审计器，自身不持有状态。
type CleanupConvergenceService struct {
	*Service
}

// NewCleanupConvergenceService 由 bootstrap/controller 装配；与 Service 共享
// 同一份 WithLifecycleCleanupSweepSource 注入的 pending tombstone 读取边界。
func NewCleanupConvergenceService(service *Service) *CleanupConvergenceService {
	return &CleanupConvergenceService{Service: service}
}

// RunPendingUninstallCleanupSweep 是宿主自有的收敛任务：把「台账已 succeeded
// 但 tombstone 仍 pending」的卸载物理清理从失败点续跑。每一步幂等且受
// FOR UPDATE 锁与 purge fence 保护，可与手动重试并发。失败条目隔离并留待下一轮。
func (s *CleanupConvergenceService) RunPendingUninstallCleanupSweep(
	ctx context.Context, limit int,
) (LifecycleCleanupSweepResult, error) {
	items, err := s.pendingUninstallCleanups(ctx, "", limit)
	if err != nil {
		return LifecycleCleanupSweepResult{}, err
	}
	result := LifecycleCleanupSweepResult{Finalized: []int64{}}
	for _, item := range items {
		if _, finalizeErr := s.finalizePendingUninstallCleanup(ctx, item); finalizeErr != nil {
			result.Failed = append(result.Failed, LifecycleCleanupSweepFailure{
				OperationID: item.OperationID, ExtensionID: item.ExtensionID, Err: finalizeErr.Error(),
			})
			slog.WarnContext(ctx, "uninstall cleanup sweep item failed",
				"operationId", item.OperationID, "extensionId", item.ExtensionID, "error", finalizeErr.Error())
			continue
		}
		result.Finalized = append(result.Finalized, item.OperationID)
		slog.InfoContext(ctx, "uninstall cleanup sweep finalized operation",
			"operationId", item.OperationID, "extensionId", item.ExtensionID)
	}
	return result, nil
}

// FinalizeUninstallCleanupForExtension 是免键恢复入口：原幂等键丢失时，
// super_admin 按扩展维度把全部 pending tombstone 收敛到 finalized。
// 幂等：无 pending 条目时同样返回成功（Uninstalled=false）。
func (s *CleanupConvergenceService) FinalizeUninstallCleanupForExtension(
	ctx context.Context, actor identity.Actor, extensionID string,
) (UninstallResult, error) {
	if !actor.IsSuperAdmin() {
		return UninstallResult{}, identity.ErrPermissionDenied
	}
	extensionID = normalizeID(extensionID)
	if extensionID == "" {
		return UninstallResult{}, ErrLifecycleInvalidInput
	}
	items, err := s.pendingUninstallCleanups(ctx, extensionID, 100)
	if err != nil {
		return UninstallResult{}, err
	}
	if len(items) == 0 {
		return UninstallResult{Uninstalled: false, ExtensionID: extensionID}, nil
	}
	var last UninstallResult
	for _, item := range items {
		result, finalizeErr := s.finalizePendingUninstallCleanup(ctx, item)
		if finalizeErr != nil {
			return UninstallResult{}, finalizeErr
		}
		last = result
	}
	s.appendAudit(ctx, actor, audit.ActionExtensionUninstalled, map[string]any{
		"extensionId": extensionID,
		"operation":   "uninstall_cleanup_finalized",
		"operations":  lifecycleOperationIDList(items),
	})
	return last, nil
}

// PendingUninstallCleanupExtensions 返回存在 pending tombstone 的扩展集合，
// 供管理端列表投影「清理挂起」徽标。查询失败时返回错误，由调用方决定降级。
func (s *CleanupConvergenceService) PendingUninstallCleanupExtensions(ctx context.Context) (map[string]bool, error) {
	items, err := s.pendingUninstallCleanups(ctx, "", 500)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item.ExtensionID] = true
	}
	return set, nil
}

func (s *CleanupConvergenceService) pendingUninstallCleanups(ctx context.Context, extensionID string, limit int) ([]PendingUninstallCleanup, error) {
	if s == nil || s.lifecycleCleanupSweeper == nil {
		return nil, ErrLifecycleCoordinatorUnavailable
	}
	items, err := s.lifecycleCleanupSweeper.PendingUninstallCleanups(ctx, extensionID, limit)
	if err != nil {
		return nil, errors.Join(ErrLifecycleCoordinatorUnavailable, err)
	}
	return items, nil
}

func (s *CleanupConvergenceService) finalizePendingUninstallCleanup(ctx context.Context, item PendingUninstallCleanup) (UninstallResult, error) {
	if s == nil || s.lifecycleInspector == nil {
		return UninstallResult{}, ErrLifecycleCoordinatorUnavailable
	}
	operation, err := s.lifecycleInspector.Operation(ctx, item.ExtensionID, item.OperationID)
	if err != nil {
		// sweep 查询与加载之间的 ledger 不一致（记录被并发收口等）按无效输入
		// 处理：pending tombstone 引用的 succeeded operation 不会凭空消失。
		if errors.Is(err, ErrLifecycleOperationNotFound) {
			return UninstallResult{}, fmt.Errorf(
				"%w: pending cleanup operation %d/%s vanished", ErrLifecycleInvalidInput, item.OperationID, item.ExtensionID)
		}
		return UninstallResult{}, err
	}
	if operation.ID != item.OperationID || operation.ExtensionID != item.ExtensionID {
		return UninstallResult{}, fmt.Errorf("%w: sweep operation identity mismatch %d/%s", ErrLifecycleInvalidInput, item.OperationID, item.ExtensionID)
	}
	return s.finalizeLifecycleUninstall(ctx, item.ExtensionID, item.RemovalMode, operation, true)
}

func lifecycleOperationIDList(items []PendingUninstallCleanup) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.OperationID)
	}
	return ids
}
