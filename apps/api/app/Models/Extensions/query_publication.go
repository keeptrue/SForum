package extensions

import (
	"context"
	"errors"
	"fmt"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

var (
	ErrRuntimeQueryPublicationUnavailable     = errors.New("extensions: runtime query publication boundary is unavailable")
	ErrRuntimeQuerySettingsRestartUnavailable = errors.New("extensions: exact runtime query settings restart is unavailable")
	ErrRuntimeSettingsRestartUnavailable      = errors.New("extensions: aggregate runtime settings restart is unavailable")
)

// RuntimeQueryPublicationMutation is an exact Host-owned Registry mutation.
// Rollback must use artifact CAS and must never overwrite or remove a newer
// runtime publication.
type RuntimeQueryPublicationMutation interface {
	Rollback() error
}

// RuntimeQueryPublicationBoundary keeps Models independent from QueryRegistry
// and the process-owning Manager. Query/filter-bearing plugins without
// Lifecycle V2 use this boundary after runtime start and before legacy disable
// side effects.
type RuntimeQueryPublicationBoundary interface {
	PublishRuntimeQueries(context.Context, Extension) (RuntimeQueryPublicationMutation, error)
	QuarantineRuntimeQueries(context.Context, Extension) (RuntimeQueryPublicationMutation, error)
}

// RuntimeQuerySettingsRestartTransaction keeps source admission closed across
// the setting-store mutation. Restore may reopen it only after the old setting
// document is durable again; KeepClosed finishes a failed rollback without
// reopening runtime admission.
type RuntimeQuerySettingsRestartTransaction interface {
	RestartRuntimeQueriesForSettings(context.Context, Extension) error
	RestoreRuntimeQueriesAfterSettingsRollback(context.Context) error
	KeepRuntimeQueriesClosed() error
}

// RuntimeQuerySettingsRestarter is a compatibility-safe secondary boundary.
// Prepare must retain and drain the old exact runtime before Service mutates
// the setting document.
type RuntimeQuerySettingsRestarter interface {
	PrepareRuntimeQueriesForSettings(context.Context, Extension) (RuntimeQuerySettingsRestartTransaction, error)
}

// BindRuntimeQueryPublications is late-bound by production bootstrap after the
// shared Manager and Query Registry have been constructed.
func (s *Service) BindRuntimeQueryPublications(boundary RuntimeQueryPublicationBoundary) *Service {
	if s == nil {
		return nil
	}
	s.queryPublications = boundary
	return s
}

func hasRuntimeQueryPublication(manifest Manifest) bool {
	return len(manifest.Queries) > 0 || len(manifest.QueryResultFilters) > 0
}

func (s *serviceCore) compensateLegacyQueryEnable(
	ctx context.Context,
	enabled Extension,
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	actorUserID int64,
	cause error,
) error {
	errs := []error{cause}
	// Close the newly published query before stopping its exact runtime.
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

func (s *serviceCore) disableLegacyQueryPlugin(
	ctx context.Context,
	extension Extension,
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	identityMutation RuntimeIdentityPublicationMutation,
	actorUserID int64,
) (Extension, error) {
	if queryMutation == nil {
		return Extension{}, ErrRuntimeQueryPublicationUnavailable
	}
	// QuarantineRuntimeQueries has already closed exact runtime admission. Keep
	// the process retained until Store.Disable commits so compensation can resume
	// that same instance rather than starting an unbound replacement.
	if err := s.clearPluginProviderSelections(ctx, extension.ID, keepMailProviderSelection); err != nil {
		return Extension{}, s.compensateLegacyQueryDisable(assetMutation, queryMutation, identityMutation, err)
	}
	disabled, err := s.disableLegacyPluginState(ctx, extension, actorUserID)
	if err != nil {
		return Extension{}, s.compensateLegacyQueryDisable(assetMutation, queryMutation, identityMutation, err)
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

func (s *serviceCore) compensateLegacyQueryDisable(
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	identityMutation RuntimeIdentityPublicationMutation,
	cause error,
) error {
	// Runtime admission must remain closed if another surface cannot be restored.
	if err := s.rollbackExactAssetMutation(assetMutation); err != nil {
		return errors.Join(cause, fmt.Errorf("restore asset publication: %w", err))
	}
	if err := queryMutation.Rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("restore query publication and runtime admission: %w", err))
	}
	if identityMutation != nil {
		if err := identityMutation.Rollback(); err != nil {
			return errors.Join(cause, fmt.Errorf("restore identity publication: %w", err))
		}
	}
	return cause
}
