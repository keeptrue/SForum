package extensions

import (
	"context"
	"errors"
	"fmt"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

const (
	CodeMissingArtifactCleanupInvalid     = "extension.missing_artifact_cleanup_invalid"
	CodeMissingArtifactCleanupUnavailable = "extension.missing_artifact_cleanup_unavailable"

	MissingArtifactDataPreserve        = "preserve"
	MissingArtifactDataDiscardSettings = "discard_settings"
)

var (
	ErrMissingArtifactCleanupInvalid     = errors.New("extensions: missing artifact cleanup invalid")
	ErrMissingArtifactCleanupUnavailable = errors.New("extensions: missing artifact cleanup unavailable")
)

type MissingArtifactCleanupInput struct {
	ExtensionIDs []string `json:"extensionIds"`
	DataMode     string   `json:"dataMode,omitempty"`
}

type MissingArtifactCleanupItem struct {
	ExtensionID      string `json:"extensionId"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Version          string `json:"version"`
	PackageDigest    string `json:"packageDigest"`
	PackagePath      string `json:"-"`
	DataMode         string `json:"dataMode"`
	RetainSettings   bool   `json:"retainSettings"`
	BusinessDataKept bool   `json:"businessDataKept"`
}

type MissingArtifactCleanupResult struct {
	Removed []MissingArtifactCleanupItem `json:"removed"`
}

type missingArtifactCleanupStore interface {
	CleanupMissingArtifacts(context.Context, int64, []MissingArtifactCleanupItem) error
}

func (s *LifecycleService) CleanupMissingArtifacts(
	ctx context.Context,
	actor identity.Actor,
	input MissingArtifactCleanupInput,
) (MissingArtifactCleanupResult, error) {
	if !actor.IsSuperAdmin() {
		return MissingArtifactCleanupResult{}, identity.ErrPermissionDenied
	}
	dataMode, err := normalizeMissingArtifactDataMode(input.DataMode)
	if err != nil {
		return MissingArtifactCleanupResult{}, err
	}
	ids := normalizedMissingArtifactIDs(input.ExtensionIDs)
	if len(ids) == 0 || len(ids) > 100 {
		return MissingArtifactCleanupResult{}, ErrMissingArtifactCleanupInvalid
	}
	repository, ok := s.store.(missingArtifactCleanupStore)
	if !ok {
		return MissingArtifactCleanupResult{}, ErrMissingArtifactCleanupUnavailable
	}

	items := make([]MissingArtifactCleanupItem, 0, len(ids))
	for _, id := range ids {
		extension, loadErr := s.store.Get(ctx, id)
		if loadErr != nil {
			return MissingArtifactCleanupResult{}, loadErr
		}
		if extension.Source == SourceBuiltin || extension.IsSystem || !extension.IsDeletable ||
			extension.ID == DefaultThemeID {
			return MissingArtifactCleanupResult{}, ErrNotDeletable
		}
		if extension.Status == StatusEnabled {
			return MissingArtifactCleanupResult{}, ErrMustDisableFirst
		}
		if artifactState(extension.PackagePath) != ArtifactMissing {
			return MissingArtifactCleanupResult{}, ErrMissingArtifactCleanupInvalid
		}
		items = append(items, MissingArtifactCleanupItem{
			ExtensionID:      extension.ID,
			Name:             extension.Name,
			Type:             extension.Type,
			Version:          extension.Version,
			PackageDigest:    extension.PackageDigest,
			PackagePath:      extension.PackagePath,
			DataMode:         dataMode,
			RetainSettings:   dataMode == MissingArtifactDataPreserve,
			BusinessDataKept: true,
		})
	}
	if err := repository.CleanupMissingArtifacts(ctx, actor.ID, items); err != nil {
		return MissingArtifactCleanupResult{}, err
	}
	for _, item := range items {
		if s.pageRegistry != nil {
			s.pageRegistry.ClearExtension(item.ExtensionID)
		}
		s.appendAudit(ctx, actor, audit.ActionExtensionUninstalled, map[string]any{
			"extensionId":      item.ExtensionID,
			"type":             item.Type,
			"version":          item.Version,
			"reason":           "artifact_missing",
			"dataMode":         item.DataMode,
			"retainSettings":   item.RetainSettings,
			"businessDataKept": true,
		})
	}
	return MissingArtifactCleanupResult{Removed: items}, nil
}

// normalizeMissingArtifactDataMode 与 normalizedMissingArtifactIDs 位于
// artifact_retention.go（GC 与缺失制品清理共用的制品保留判据文件）。

func (s *CatalogService) pruneMissingBuiltins(ctx context.Context, activeIDs []string) error {
	result, err := s.store.PruneMissingBuiltins(ctx, activeIDs)
	if err != nil {
		return err
	}
	for _, extensionID := range result.DisabledPluginIDs {
		if err := s.clearPluginProviderSelections(ctx, extensionID, clearMailProviderSelection); err != nil {
			return fmt.Errorf("clear removed builtin provider selections for %s: %w", extensionID, err)
		}
		if s.pageRegistry != nil {
			s.pageRegistry.ClearExtension(extensionID)
		}
	}
	return nil
}
