package extensionsruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	ErrLifecyclePreflightUnavailable = errors.New("extension lifecycle static preflight dependency is unavailable")
	ErrLifecyclePreflightUntrusted   = errors.New("extension lifecycle target artifact is not trusted")
	ErrLifecyclePreflightMigrations  = errors.New("extension lifecycle target migration state is not ready")
)

// LifecycleStaticPreflightRequest intentionally has no runtime binding or
// coordinator step. Service calls this same immutable fact check before
// coordinator.Run and therefore before StageRuntimeInstance. Position-level
// boundary replay calls it again after validating the durable Host step.
type LifecycleStaticPreflightRequest struct {
	Operation       extensions.LifecycleMachineOperation
	SourceExtension *extensions.Extension
	TargetExtension extensions.Extension
}

type LifecycleBoundaryStaticPreflight interface {
	CheckLifecycleStaticPreflight(context.Context, LifecycleStaticPreflightRequest) error
}

type LifecycleBoundaryExtensionInventory interface {
	List(context.Context) ([]extensions.Extension, error)
}

type LifecycleBoundaryArtifactTrust interface {
	TrustedArtifact(context.Context, extensions.Extension) (bool, error)
}

type LifecycleBoundaryMigrationFacts interface {
	LifecycleArtifactMigrationReady(context.Context, extensions.Extension) (bool, error)
	CanPrepareLifecycleMigrations(context.Context, LifecycleStaticPreflightRequest) (bool, error)
}

type ProductionLifecycleBoundaryPreflightConfig struct {
	Pool          *pgxpool.Pool
	Inventory     LifecycleBoundaryExtensionInventory
	Features      extensions.FeatureFlagSource
	Compatibility extensions.RuntimePreflight
	Trust         LifecycleBoundaryArtifactTrust
	Migrations    LifecycleBoundaryMigrationFacts
}

// ProductionLifecycleBoundaryPreflight uses only Host-owned static facts:
// installed manifests, feature flags, exact trust rows, package/Host protocol
// compatibility, and durable migration readiness. No dependency exposes a
// process start method through this adapter.
type ProductionLifecycleBoundaryPreflight struct {
	pool          *pgxpool.Pool
	inventory     LifecycleBoundaryExtensionInventory
	features      extensions.FeatureFlagSource
	compatibility extensions.RuntimePreflight
	trust         LifecycleBoundaryArtifactTrust
	migrations    LifecycleBoundaryMigrationFacts
}

func NewProductionLifecycleBoundaryPreflight(
	config ProductionLifecycleBoundaryPreflightConfig,
) *ProductionLifecycleBoundaryPreflight {
	return &ProductionLifecycleBoundaryPreflight{
		pool: config.Pool, inventory: config.Inventory, features: config.Features,
		compatibility: config.Compatibility, trust: config.Trust, migrations: config.Migrations,
	}
}

func (p *ProductionLifecycleBoundaryPreflight) CheckLifecycleBoundary(
	ctx context.Context,
	request LifecycleBoundaryRequest,
) error {
	if p == nil || p.pool == nil || ctx == nil {
		return ErrLifecyclePreflightUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fence, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		return err
	}
	if _, _, err := lifecyclePreflightArtifactFor(request.Operation, request.SourceExtension, request.TargetExtension); err != nil {
		return err
	}

	// Keep the database lock window read-only and short. Static filesystem,
	// feature, dependency, and trust checks run only after the transaction ends.
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin lifecycle preflight fence: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateLifecycleBoundaryPostgresFence(ctx, tx, fence, false); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit lifecycle preflight fence: %w", err)
	}
	return p.CheckLifecycleStaticPreflight(ctx, LifecycleStaticPreflightRequest{
		Operation: request.Operation, SourceExtension: request.SourceExtension,
		TargetExtension: request.TargetExtension,
	})
}

func (p *ProductionLifecycleBoundaryPreflight) CheckLifecycleStaticPreflight(
	ctx context.Context,
	request LifecycleStaticPreflightRequest,
) error {
	if p == nil || ctx == nil || p.inventory == nil || p.compatibility == nil {
		return ErrLifecyclePreflightUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	candidate, activate, err := lifecyclePreflightArtifactFor(
		request.Operation, request.SourceExtension, request.TargetExtension,
	)
	if err != nil {
		return err
	}
	items, err := p.inventory.List(ctx)
	if err != nil {
		return fmt.Errorf("list lifecycle dependency facts: %w", err)
	}
	if _, err := extensions.ResolveLifecycleDependencyGraph(items, candidate, activate); err != nil {
		return fmt.Errorf("%w: %v", extensions.ErrPreflightFailed, err)
	}
	if activate && len(candidate.Manifest.RequiresFeatures) > 0 {
		if p.features == nil {
			return fmt.Errorf("%w: feature facts", ErrLifecyclePreflightUnavailable)
		}
		missing, err := p.features.MissingRequiredFeatures(ctx, candidate.Manifest.RequiresFeatures)
		if err != nil {
			return fmt.Errorf("inspect lifecycle feature facts: %w", err)
		}
		if len(missing) > 0 {
			return fmt.Errorf("%w: %v", extensions.ErrFeaturesRequired, missing)
		}
	}
	if err := p.compatibility.Check(ctx, candidate); err != nil {
		return fmt.Errorf("%w: Host compatibility: %v", extensions.ErrPreflightFailed, err)
	}
	requiresLiveTrust := request.Operation == extensions.LifecycleMachineInstall ||
		request.Operation == extensions.LifecycleMachineEnable ||
		request.Operation == extensions.LifecycleMachineUpgrade
	if requiresLiveTrust {
		if p.trust == nil {
			if extensions.RequiresExecutableTrust(candidate) {
				return fmt.Errorf("%w: trust facts", ErrLifecyclePreflightUnavailable)
			}
		} else {
			trusted, err := p.trust.TrustedArtifact(ctx, candidate)
			if err != nil {
				return fmt.Errorf("inspect lifecycle trust facts: %w", err)
			}
			if !trusted {
				return ErrLifecyclePreflightUntrusted
			}
		}
	}
	if request.Operation == extensions.LifecycleMachineInstall ||
		request.Operation == extensions.LifecycleMachineUpgrade ||
		request.Operation == extensions.LifecycleMachineRollback {
		hasDeclaredMigrations := len(candidate.Manifest.Migrations) > 0
		if request.SourceExtension != nil {
			hasDeclaredMigrations = hasDeclaredMigrations || len(request.SourceExtension.Manifest.Migrations) > 0
		}
		if hasDeclaredMigrations {
			if p.migrations == nil {
				return fmt.Errorf("%w: migration preflight facts", ErrLifecyclePreflightUnavailable)
			}
			ready, err := p.migrations.CanPrepareLifecycleMigrations(ctx, request)
			if err != nil {
				return fmt.Errorf("inspect lifecycle migration preflight: %w", err)
			}
			if !ready {
				return ErrLifecyclePreflightMigrations
			}
		}
	}
	if request.Operation == extensions.LifecycleMachineEnable && len(candidate.Manifest.Migrations) > 0 {
		if p.migrations == nil {
			return fmt.Errorf("%w: migration facts", ErrLifecyclePreflightUnavailable)
		}
		ready, err := p.migrations.LifecycleArtifactMigrationReady(ctx, candidate)
		if err != nil {
			return fmt.Errorf("inspect lifecycle migration readiness: %w", err)
		}
		if !ready {
			return ErrLifecyclePreflightMigrations
		}
	}
	return nil
}

func lifecyclePreflightArtifactFor(
	operation extensions.LifecycleMachineOperation,
	source *extensions.Extension,
	target extensions.Extension,
) (extensions.Extension, bool, error) {
	if err := validateExactCoordinatorArtifact("preflight target", target); err != nil ||
		target.ActiveVersionID <= 0 || !validLifecycleCleanupDigest(target.PackageDigest) {
		return extensions.Extension{}, false, fmt.Errorf("%w: target artifact is not exact", ErrLifecycleBoundaryFenceInvalid)
	}
	switch operation {
	case extensions.LifecycleMachineInstall, extensions.LifecycleMachineEnable:
		if source != nil {
			return extensions.Extension{}, false, fmt.Errorf("%w: operation cannot carry a source", ErrLifecycleBoundaryFenceInvalid)
		}
		return target, true, nil
	case extensions.LifecycleMachineUpgrade, extensions.LifecycleMachineRollback:
		if source == nil {
			return extensions.Extension{}, false, fmt.Errorf("%w: version transition needs a source", ErrLifecycleBoundaryFenceInvalid)
		}
		if err := validateExactCoordinatorArtifact("preflight source", *source); err != nil ||
			source.ActiveVersionID <= 0 || !validLifecycleCleanupDigest(source.PackageDigest) ||
			source.ID != target.ID ||
			(source.Version == target.Version && source.PackageDigest == target.PackageDigest) {
			return extensions.Extension{}, false, fmt.Errorf("%w: source artifact is not exact", ErrLifecycleBoundaryFenceInvalid)
		}
		return target, true, nil
	case extensions.LifecycleMachineDisable, extensions.LifecycleMachineUninstall:
		if source == nil {
			return extensions.Extension{}, false, fmt.Errorf("%w: deactivation needs a source", ErrLifecycleBoundaryFenceInvalid)
		}
		if err := validateExactCoordinatorSelectedExtension(*source, target); err != nil {
			return extensions.Extension{}, false, fmt.Errorf("%w: deactivation source changed", ErrLifecycleBoundaryFenceInvalid)
		}
		// The ExactLifecycleCoordinatorHost already validates the frozen last
		// approved authority. Requiring a live grant here would make revocation
		// prevent disable/uninstall recovery.
		return *source, false, nil
	default:
		return extensions.Extension{}, false, ErrLifecycleBoundaryFenceInvalid
	}
}

var _ LifecycleBoundaryPreflight = (*ProductionLifecycleBoundaryPreflight)(nil)
var _ LifecycleBoundaryStaticPreflight = (*ProductionLifecycleBoundaryPreflight)(nil)
