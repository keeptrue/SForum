package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type activeWorkerRuntimeSource interface {
	ActiveRuntimeInstance(string) (extensionsruntime.RuntimeInstanceSnapshot, error)
}

// publishStandalonePluginSchedules reconstructs the same exact schedule
// snapshot that the API lifecycle stack already owns in embedded mode.
func publishStandalonePluginSchedules(
	ctx context.Context,
	store extensions.Store,
	runtime workerExtensionRuntime,
	trust extensionsruntime.RuntimeTrustSource,
	registry *supportjobs.PluginScheduleAdmissionRegistry,
) error {
	if ctx == nil || store == nil || runtime == nil || trust == nil || registry == nil {
		return fmt.Errorf("standalone plugin schedule dependencies are incomplete")
	}
	source, ok := runtime.(activeWorkerRuntimeSource)
	if !ok {
		return fmt.Errorf("standalone extension runtime does not expose active exact instances")
	}
	items, err := store.List(ctx)
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	for _, extension := range items {
		if extension.Type != extensions.TypePlugin || extension.Status != extensions.StatusEnabled || len(extension.Manifest.Schedules) == 0 {
			continue
		}
		active, err := source.ActiveRuntimeInstance(extension.ID)
		if err != nil {
			return fmt.Errorf("inspect active runtime %s: %w", extension.ID, err)
		}
		if active.ExtensionVersion != extension.Version || active.ArtifactDigest != extension.PackageDigest {
			return fmt.Errorf("active runtime %s does not match installed artifact", extension.ID)
		}
		identity, err := trust.RuntimeIdentity(ctx, extension)
		if err != nil {
			return fmt.Errorf("resolve schedule trust for %s: %w", extension.ID, err)
		}
		if strings.TrimSpace(identity.TrustGrantID) == "" {
			return fmt.Errorf("resolve schedule trust for %s: empty trust grant", extension.ID)
		}
		jobs := make(map[string]supportjobs.PluginJobContract, len(extension.Manifest.Jobs))
		for _, declaration := range extension.Manifest.Jobs {
			contract, err := extensions.PluginJobContractForExtension(extension, declaration.Name)
			if err != nil {
				return err
			}
			jobs[strings.TrimSpace(declaration.ID)] = contract
		}
		schedules := make([]supportjobs.PluginScheduleDeclaration, 0, len(extension.Manifest.Schedules))
		for _, declaration := range extension.Manifest.Schedules {
			contract, ok := jobs[strings.TrimSpace(declaration.JobID)]
			if !ok {
				return fmt.Errorf("schedule %s references an unknown job", declaration.ID)
			}
			timezone := strings.TrimSpace(declaration.Timezone)
			if timezone == "" {
				timezone = "UTC"
			}
			schedules = append(schedules, supportjobs.PluginScheduleDeclaration{
				ScheduleID: strings.TrimSpace(declaration.ID), JobName: contract.JobName,
				JobContract: contract.JobContract, Cron: strings.TrimSpace(declaration.Cron), Timezone: timezone,
				Contract: contract, TrustGrantID: strings.TrimSpace(identity.TrustGrantID),
			})
		}
		_, err = registry.PublishActive(supportjobs.PluginScheduleRuntime{
			Identity: supportjobs.PluginScheduleRuntimeIdentity{
				ExtensionID: extension.ID, ExtensionVersion: extension.Version,
				ArtifactDigest: extension.PackageDigest, InstanceID: active.Identity.InstanceID,
			},
			Schedules: schedules,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
