package extensionsruntime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func (b *PostgresLifecycleBoundaryRegistries) RestoreRoutePublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || ctx == nil || b.manager == nil || b.routes == nil || b.routeSchemas == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.publicationMu.Lock()
	defer b.publicationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.components.RestoreRuntimes(items, safeMode); err != nil {
		return fmt.Errorf("restore component registry publication: %w", err)
	}
	if err := b.restoreIdentityPublications(ctx, items, safeMode); err != nil {
		return err
	}
	// A plugin whose Identity publication could not be proven exact stays
	// unpublished everywhere for this boot. Publishing its routes/assets/pages
	// while its identity surface is closed would hand out a half-registered
	// extension, so drop it from every following restore instead.
	if suppressed := b.SuppressedIdentityExtensions(); len(suppressed) > 0 {
		remaining := lifecycleItemsWithout(items, suppressed)
		slog.Error("extension suppressed for this boot: identity publication is not exact",
			"extensions", suppressed, "remaining", len(remaining))
		items = remaining
	}
	if err := b.restoreNotificationPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreQueryPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreCachePublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreSEOPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreNavigationPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreContentPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreMediaPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreEditorPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreEntityPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreAssetPublications(ctx, items, safeMode); err != nil {
		return err
	}
	if err := b.restoreExactPluginPagePublications(ctx, items, safeMode); err != nil {
		return err
	}
	pluginRoutes := make([]routes.PluginRouteSet, 0, len(items))
	schemaArtifacts := make([]extensionopenapi.Artifact, 0, len(items))
	if !safeMode {
		for _, item := range items {
			if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled ||
				strings.TrimSpace(item.Manifest.Backend.Entry) == "" ||
				(len(item.Manifest.Routes) == 0 && len(item.Manifest.OpenAPI) == 0) {
				continue
			}
			runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
			if err != nil {
				// Failed or boot-loop-suppressed plugins remain closed without
				// preventing Host startup or publishing stale package routes.
				slog.Warn("route restore: skip plugin without active runtime",
					"extension", item.ID, "routes", len(item.Manifest.Routes),
					"openapi", len(item.Manifest.OpenAPI), "error", err)
				continue
			}
			if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
				return fmt.Errorf("%w: startup runtime for %s is not exact and available", ErrLifecycleRegistryPublicationConflict, item.ID)
			}
			binding := extensions.LifecycleRuntimeBinding{
				ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
				RuntimeInstanceID: runtime.Identity.InstanceID, VersionID: item.ActiveVersionID,
			}
			material, err := buildStartupRoutePublicationMaterial(item, binding)
			if err != nil {
				return fmt.Errorf("restore route publication for %s: %w", item.ID, err)
			}
			pluginRoutes = append(pluginRoutes, material.routes)
			schemaArtifacts = append(schemaArtifacts, material.routeSchema)
		}
	}
	return b.restoreRoutePolicyPublications(ctx, pluginRoutes, schemaArtifacts, safeMode)
}

// lifecycleItemsWithout returns items minus the suppressed extension ids. A
// suppressed plugin must not publish any other surface either: a half
// registered extension (routes without its identity declarations) is worse than
// a closed one.
func lifecycleItemsWithout(items []extensions.Extension, suppressed []string) []extensions.Extension {
	if len(suppressed) == 0 {
		return items
	}
	dropped := make(map[string]struct{}, len(suppressed))
	for _, extensionID := range suppressed {
		dropped[extensionID] = struct{}{}
	}
	remaining := make([]extensions.Extension, 0, len(items))
	for _, item := range items {
		if _, skip := dropped[item.ID]; skip {
			continue
		}
		remaining = append(remaining, item)
	}
	return remaining
}
