package extensionsruntime

import (
	"context"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// RestoreCachePublications exposes the cache-only startup boundary used by the
// standalone worker, which does not construct public Route/Page registries.
func (b *PostgresLifecycleBoundaryRegistries) RestoreCachePublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	return b.restoreCachePublications(ctx, items, safeMode)
}
