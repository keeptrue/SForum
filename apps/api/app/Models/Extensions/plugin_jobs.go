package extensions

import (
	"context"
	"fmt"
	"strings"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// PluginJobContract loads one enabled plugin's immutable manifest contract.
// The Host API combines it with the authenticated runtime trust grant before
// persisting a River row.
func (s *CatalogService) PluginJobContract(ctx context.Context, extensionID, jobName string) (supportjobs.PluginJobContract, error) {
	if s == nil || s.safeMode {
		return supportjobs.PluginJobContract{}, ErrExtensionDisabled
	}
	extension, err := s.store.Get(ctx, normalizeID(extensionID))
	if err != nil {
		return supportjobs.PluginJobContract{}, err
	}
	if extension.Type != TypePlugin {
		return supportjobs.PluginJobContract{}, ErrExtensionNotFound
	}
	if extension.Status != StatusEnabled {
		return supportjobs.PluginJobContract{}, ErrExtensionDisabled
	}
	return PluginJobContractForExtension(extension, jobName)
}

func PluginJobContractForExtension(extension Extension, jobName string) (supportjobs.PluginJobContract, error) {
	jobName = strings.TrimSpace(jobName)
	for _, declared := range extension.Manifest.Jobs {
		if strings.TrimSpace(declared.Name) != jobName {
			continue
		}
		schemaID, schemaVersion, ok := supportjobs.SplitVersionedSchema(declared.PayloadSchema)
		contract := supportjobs.PluginJobContract{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version, ArtifactDigest: extension.PackageDigest,
			JobName: jobName, JobContract: strings.TrimSpace(declared.ContractVersion),
			PayloadSchemaID: schemaID, PayloadSchemaVersion: schemaVersion,
			RetryPolicy: declared.RetryPolicy, MaxAttempts: declared.MaxAttempts,
			RetryDelaySeconds: declared.RetryDelaySeconds, ConcurrencyLimit: declared.ConcurrencyLimit,
			Queue: declared.Queue,
		}
		contract = contract.Normalized()
		if !ok || !contract.Valid() {
			return supportjobs.PluginJobContract{}, ErrInvalidManifest
		}
		return contract, nil
	}
	return supportjobs.PluginJobContract{}, fmt.Errorf("%w: plugin job %q", ErrExtensionNotFound, jobName)
}
