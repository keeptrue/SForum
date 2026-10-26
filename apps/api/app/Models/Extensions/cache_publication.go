package extensions

import (
	"context"
	"errors"
	"fmt"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

var ErrRuntimeCachePublicationUnavailable = errors.New("extensions: runtime cache publication boundary is unavailable")

// RuntimeCachePublicationMutation is an exact Host-owned Registry mutation.
// Rollback must use artifact CAS and must never overwrite or remove a newer
// runtime publication.
type RuntimeCachePublicationMutation interface {
	Rollback() error
}

// RuntimeCachePublicationBoundary keeps Models independent from CacheRegistry
// and the process-owning Manager. Cache-bearing plugins without Lifecycle V2
// use this boundary after runtime start and before legacy disable side effects.
type RuntimeCachePublicationBoundary interface {
	PublishRuntimeCaches(context.Context, Extension) (RuntimeCachePublicationMutation, error)
	QuarantineRuntimeCaches(context.Context, Extension) (RuntimeCachePublicationMutation, error)
}

func (s *serviceCore) compensateLegacyCacheEnable(
	ctx context.Context,
	enabled Extension,
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	cacheMutation RuntimeCachePublicationMutation,
	actorUserID int64,
	cause error,
) error {
	errs := []error{cause}
	// Cache publishes after Query, so compensation closes it first and keeps
	// every runtime-backed surface unavailable before stopping the instance.
	if cacheMutation != nil {
		if err := cacheMutation.Rollback(); err != nil {
			errs = append(errs, fmt.Errorf("restore cache publication: %w", err))
		}
	}
	if queryMutation != nil {
		if err := queryMutation.Rollback(); err != nil {
			errs = append(errs, fmt.Errorf("restore query publication: %w", err))
		}
	}
	if s.runtime != nil {
		if err := s.runtime.Stop(ctx, enabled); err != nil {
			errs = append(errs, fmt.Errorf("stop runtime: %w", err))
		}
	}
	if _, err := s.disableLegacyPluginState(ctx, enabled, actorUserID); err != nil {
		errs = append(errs, fmt.Errorf("disable extension: %w", err))
	}
	if err := s.rollbackExactAssetMutation(assetMutation); err != nil {
		errs = append(errs, fmt.Errorf("restore asset publication: %w", err))
	}
	return errors.Join(errs...)
}

func (s *serviceCore) disableLegacyCachePlugin(
	ctx context.Context,
	extension Extension,
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	cacheMutation RuntimeCachePublicationMutation,
	identityMutation RuntimeIdentityPublicationMutation,
	actorUserID int64,
) (Extension, error) {
	if cacheMutation == nil {
		return Extension{}, ErrRuntimeCachePublicationUnavailable
	}
	// Both Registry publications are quarantined before persistent state changes.
	// Query owns the final resume when present because it drained the runtime first.
	if err := s.clearPluginProviderSelections(ctx, extension.ID); err != nil {
		return Extension{}, s.compensateLegacyCacheDisable(assetMutation, queryMutation, cacheMutation, identityMutation, err)
	}
	disabled, err := s.disableLegacyPluginState(ctx, extension, actorUserID)
	if err != nil {
		return Extension{}, s.compensateLegacyCacheDisable(assetMutation, queryMutation, cacheMutation, identityMutation, err)
	}
	if s.pageRegistry != nil {
		s.pageRegistry.ClearExtension(extension.ID)
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
	return disabled, nil
}

func (s *serviceCore) compensateLegacyCacheDisable(
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	cacheMutation RuntimeCachePublicationMutation,
	identityMutation RuntimeIdentityPublicationMutation,
	cause error,
) error {
	// Admission must remain closed if an earlier surface cannot be restored.
	if err := s.rollbackExactAssetMutation(assetMutation); err != nil {
		return errors.Join(cause, fmt.Errorf("restore asset publication: %w", err))
	}
	if cacheMutation == nil {
		// Without an exact Cache rollback token, the Host cannot prove that all
		// runtime-backed surfaces are restorable. Keep Query/runtime quarantined.
		return cause
	}
	if err := cacheMutation.Rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("restore cache publication and runtime admission: %w", err))
	}
	if queryMutation != nil {
		if err := queryMutation.Rollback(); err != nil {
			return errors.Join(cause, fmt.Errorf("restore query publication and runtime admission: %w", err))
		}
	}
	if identityMutation != nil {
		if err := identityMutation.Rollback(); err != nil {
			return errors.Join(cause, fmt.Errorf("restore identity publication: %w", err))
		}
	}
	return cause
}
