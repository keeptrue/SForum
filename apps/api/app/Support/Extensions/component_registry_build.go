package extensionsruntime

import (
	"fmt"
	"sort"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	componentcatalog "github.com/keeptrue/sforum/apps/api/app/Support/ComponentCatalog"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func emptyComponentRegistryState() *componentRegistryState {
	state := &componentRegistryState{
		registrations:         make(map[string]componentRuntimeRegistration),
		targetsByID:           make(map[string]ComponentTarget),
		contributionsByID:     make(map[string]ComponentContribution),
		contributionsByTarget: make(map[string][]ComponentContribution),
		replaceByTarget:       make(map[string][]ComponentContribution),
		replaceWinnerByTarget: make(map[string]ComponentContribution),
		conflictsByTarget:     make(map[string]ComponentProviderConflict),
		selectionsByTarget:    make(map[string]ComponentProviderSelection),
	}
	for _, core := range componentcatalog.CoreComponentCatalog() {
		state.targetsByID[core.ID] = ComponentTarget{
			ID: core.ID, ContractVersion: core.ContractVersion, Core: true,
			Kind: core.Kind, Owners: append([]componentcatalog.Owner(nil), core.Owners...),
			Route: core.Route, Source: core.Source,
		}
	}
	return state
}

func buildComponentRegistryState(
	revision uint64,
	registrations map[string]componentRuntimeRegistration,
	previousSelections map[string]ComponentProviderSelection,
) (*componentRegistryState, error) {
	state := emptyComponentRegistryState()
	state.revision = revision
	state.registrations = cloneComponentRegistrations(registrations)

	all := make(map[string]ComponentContribution)
	definitions := make(map[string]ComponentContribution)
	extensionIDs := sortedComponentRegistrationIDs(state.registrations)
	for _, extensionID := range extensionIDs {
		registration := state.registrations[extensionID]
		if registration.extension.ID != extensionID || validateComponentRuntime(registration.extension, registration.instanceID) != nil {
			return nil, ErrComponentRegistryInvalid
		}
		for _, declaration := range registration.extension.Manifest.Components {
			contribution, err := compileComponentContribution(registration, declaration)
			if err != nil {
				return nil, err
			}
			if _, duplicate := all[contribution.ID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate contribution %s", ErrComponentRegistryConflict, contribution.ID)
			}
			all[contribution.ID] = contribution
			if contribution.Action != extensionmanifest.ComponentActionAdd {
				continue
			}
			if _, core := state.targetsByID[contribution.ID]; core {
				return nil, fmt.Errorf("%w: contribution redefines Core target %s", ErrComponentRegistryConflict, contribution.ID)
			}
			if _, duplicate := definitions[contribution.ID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate target %s", ErrComponentRegistryConflict, contribution.ID)
			}
			definitions[contribution.ID] = contribution
		}
	}

	active := make(map[string]bool, len(all))
	for id := range all {
		active[id] = true
	}
	contributionIDs := sortedComponentContributionIDs(all)
	for {
		changed := false
		for _, id := range contributionIDs {
			if !active[id] {
				continue
			}
			contribution := all[id]
			keep, err := validateComponentContributionTarget(
				state, state.registrations[contribution.Artifact.ExtensionID], contribution,
				definitions, active,
			)
			if err != nil {
				return nil, err
			}
			if !keep {
				active[id] = false
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	if err := validateComponentCompositionGraph(all, active, DefaultComponentCompositionMaxDepth); err != nil {
		return nil, err
	}

	for _, id := range contributionIDs {
		if !active[id] {
			continue
		}
		contribution := all[id]
		state.contributionsByID[id] = contribution
		if contribution.Action == extensionmanifest.ComponentActionAdd {
			provider := contribution
			state.targetsByID[id] = ComponentTarget{
				ID: id, ContractVersion: contribution.ContractVersion, Provider: &provider,
			}
		}
		if contribution.TargetID == "" {
			continue
		}
		state.contributionsByTarget[contribution.TargetID] = append(
			state.contributionsByTarget[contribution.TargetID], contribution,
		)
		if contribution.Action == extensionmanifest.ComponentActionReplace {
			state.replaceByTarget[contribution.TargetID] = append(
				state.replaceByTarget[contribution.TargetID], contribution,
			)
		}
	}
	for targetID := range state.contributionsByTarget {
		sort.Slice(state.contributionsByTarget[targetID], func(i, j int) bool {
			return componentContributionBefore(
				state.contributionsByTarget[targetID][i], state.contributionsByTarget[targetID][j],
			)
		})
	}
	for targetID := range state.replaceByTarget {
		sort.Slice(state.replaceByTarget[targetID], func(i, j int) bool {
			return componentContributionBefore(state.replaceByTarget[targetID][i], state.replaceByTarget[targetID][j])
		})
	}
	applyComponentProviderSelections(state, previousSelections)
	return state, nil
}

func validateComponentContributionTarget(
	state *componentRegistryState,
	registration componentRuntimeRegistration,
	contribution ComponentContribution,
	definitions map[string]ComponentContribution,
	active map[string]bool,
) (bool, error) {
	if contribution.TargetID == "" {
		if contribution.Action != extensionmanifest.ComponentActionAdd {
			return false, fmt.Errorf("%w: contribution %s has no target", ErrComponentRegistryInvalid, contribution.ID)
		}
		return true, nil
	}
	target, core, found := componentTargetForBuild(state, definitions, active, contribution.TargetID)
	if !found {
		dependency, declared := hookDependencyForTarget(registration.extension, contribution.TargetID)
		if declared && dependency.Kind == "optional" {
			return false, nil
		}
		return false, fmt.Errorf(
			"%w: contribution %s target %s", ErrComponentRegistryConflict, contribution.ID, contribution.TargetID,
		)
	}
	if target.ContractVersion != contribution.TargetContractVersion {
		if !core && target.Provider != nil && target.Provider.Artifact.ExtensionID != contribution.Artifact.ExtensionID {
			dependency, declared := hookDependencyForTarget(registration.extension, contribution.TargetID)
			if declared && dependency.Kind == "optional" {
				return false, nil
			}
		}
		return false, fmt.Errorf(
			"%w: contribution %s target contract %s", ErrComponentRegistryConflict,
			contribution.ID, contribution.TargetContractVersion,
		)
	}
	if core {
		if registration.extension.Type == extensions.TypeTheme && !componentTargetOwnedBy(target, componentcatalog.OwnerPublic) {
			return false, fmt.Errorf("%w: theme contribution %s targets admin Core UI", ErrComponentRegistryInvalid, contribution.ID)
		}
		return true, nil
	}
	if target.Provider == nil || target.Provider.Artifact.ExtensionID == contribution.Artifact.ExtensionID {
		return true, nil
	}
	dependency, declared, compatible := hookDependency(
		registration.extension,
		target.Provider.Artifact.ExtensionID,
		target.Provider.Artifact.ExtensionVersion,
	)
	if declared && dependency.Kind == "optional" && !compatible {
		return false, nil
	}
	if !declared || !compatible {
		return false, fmt.Errorf(
			"%w: contribution %s requires %s@%s", ErrComponentRegistryConflict,
			contribution.ID, target.Provider.Artifact.ExtensionID, target.Provider.Artifact.ExtensionVersion,
		)
	}
	return true, nil
}

func componentTargetForBuild(
	state *componentRegistryState,
	definitions map[string]ComponentContribution,
	active map[string]bool,
	targetID string,
) (ComponentTarget, bool, bool) {
	if target, found := state.targetsByID[targetID]; found {
		return target, true, true
	}
	definition, found := definitions[targetID]
	if !found || !active[targetID] {
		return ComponentTarget{}, false, false
	}
	provider := definition
	return ComponentTarget{
		ID: definition.ID, ContractVersion: definition.ContractVersion, Provider: &provider,
	}, false, true
}

func componentTargetOwnedBy(target ComponentTarget, owner componentcatalog.Owner) bool {
	for _, candidate := range target.Owners {
		if candidate == owner {
			return true
		}
	}
	return false
}

func applyComponentProviderSelections(
	state *componentRegistryState,
	previous map[string]ComponentProviderSelection,
) {
	state.replaceWinnerByTarget = make(map[string]ComponentContribution)
	state.conflictsByTarget = make(map[string]ComponentProviderConflict)
	state.selectionsByTarget = make(map[string]ComponentProviderSelection)
	for targetID, candidates := range state.replaceByTarget {
		if len(candidates) == 0 {
			continue
		}
		winner := candidates[0]
		explicit := false
		if selection, selected := previous[targetID]; selected {
			if target, found := state.targetsByID[targetID]; found &&
				selection.TargetContractVersion == target.ContractVersion {
				for _, candidate := range candidates {
					if candidate.ID == selection.ContributionID && sameHookArtifact(candidate.Artifact, selection.Artifact) {
						winner = candidate
						explicit = true
						state.selectionsByTarget[targetID] = selection
						break
					}
				}
			}
		}
		state.replaceWinnerByTarget[targetID] = winner
		if len(candidates) > 1 {
			value := winner
			state.conflictsByTarget[targetID] = ComponentProviderConflict{
				TargetID: targetID, TargetContractVersion: winner.TargetContractVersion,
				Candidates: append([]ComponentContribution(nil), candidates...), Winner: &value,
				ExplicitSelection: explicit,
			}
		}
	}
}

func rebuildComponentProviderState(
	current *componentRegistryState,
	revision uint64,
	selections map[string]ComponentProviderSelection,
) *componentRegistryState {
	next := *current
	next.revision = revision
	applyComponentProviderSelections(&next, selections)
	return &next
}

func componentContributionBefore(left, right ComponentContribution) bool {
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if left.Artifact.ExtensionID != right.Artifact.ExtensionID {
		return left.Artifact.ExtensionID < right.Artifact.ExtensionID
	}
	return left.ID < right.ID
}

func sortedComponentRegistrationIDs(values map[string]componentRuntimeRegistration) []string {
	result := make([]string, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func sortedComponentContributionIDs(values map[string]ComponentContribution) []string {
	result := make([]string, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func validateComponentRuntime(extension extensions.Extension, instanceID string) error {
	extensionID := strings.TrimSpace(extension.ID)
	version := strings.TrimSpace(extension.Version)
	extensionType := strings.TrimSpace(extension.Type)
	if extensionID == "" || extensionID != extension.ID || version == "" || version != extension.Version ||
		strings.TrimSpace(extension.PackageDigest) == "" || strings.TrimSpace(instanceID) == "" ||
		extension.Manifest.ManifestVersion != 3 || extension.Manifest.ID != extensionID ||
		extension.Manifest.Version != version || extension.Manifest.Type != extensionType ||
		(extensionType != extensions.TypePlugin && extensionType != extensions.TypeTheme) {
		return ErrComponentRegistryInvalid
	}
	return nil
}

func validComponentRegistryAction(action string) bool {
	switch action {
	case extensionmanifest.ComponentActionAdd,
		extensionmanifest.ComponentActionBefore,
		extensionmanifest.ComponentActionAfter,
		extensionmanifest.ComponentActionWrap,
		extensionmanifest.ComponentActionReplace,
		extensionmanifest.ComponentActionHide,
		extensionmanifest.ComponentActionFilterProps,
		extensionmanifest.ComponentActionFilterResult:
		return true
	default:
		return false
	}
}

func cloneComponentRegistrations(
	values map[string]componentRuntimeRegistration,
) map[string]componentRuntimeRegistration {
	result := make(map[string]componentRuntimeRegistration, len(values))
	for id, registration := range values {
		result[id] = componentRuntimeRegistration{
			extension: cloneComponentExtension(registration.extension), instanceID: registration.instanceID,
		}
	}
	return result
}

func cloneComponentSelections(
	values map[string]ComponentProviderSelection,
) map[string]ComponentProviderSelection {
	result := make(map[string]ComponentProviderSelection, len(values))
	for id, selection := range values {
		result[id] = selection
	}
	return result
}

func cloneComponentExtension(extension extensions.Extension) extensions.Extension {
	extension.Manifest.Components = append([]extensions.ManifestComponent(nil), extension.Manifest.Components...)
	extension.Manifest.Dependencies = append([]extensions.ManifestDependency(nil), extension.Manifest.Dependencies...)
	extension.Manifest.PackageFiles = append([]extensions.ManifestPackageFile(nil), extension.Manifest.PackageFiles...)
	extension.Manifest.Templates = append([]extensions.ManifestTemplate(nil), extension.Manifest.Templates...)
	return extension
}

func cloneComponentContribution(value ComponentContribution) ComponentContribution {
	value.manifest = extensions.ManifestComponent{}
	value.propsValidator = nil
	value.resultValidator = nil
	return value
}

func cloneComponentContributions(values []ComponentContribution) []ComponentContribution {
	result := make([]ComponentContribution, len(values))
	for index, value := range values {
		result[index] = cloneComponentContribution(value)
	}
	return result
}

func cloneComponentTarget(value ComponentTarget) ComponentTarget {
	value.Owners = append([]componentcatalog.Owner(nil), value.Owners...)
	if value.Provider != nil {
		provider := cloneComponentContribution(*value.Provider)
		value.Provider = &provider
	}
	return value
}

func cloneComponentProviderConflict(value ComponentProviderConflict) ComponentProviderConflict {
	value.Candidates = cloneComponentContributions(value.Candidates)
	if value.Winner != nil {
		winner := cloneComponentContribution(*value.Winner)
		value.Winner = &winner
	}
	return value
}

func cloneComponentResolvePlan(value ComponentResolvePlan) ComponentResolvePlan {
	value.Target = cloneComponentTarget(value.Target)
	value.Contributions = cloneComponentContributions(value.Contributions)
	value.ReplaceCandidates = cloneComponentContributions(value.ReplaceCandidates)
	if value.ReplaceWinner != nil {
		winner := cloneComponentContribution(*value.ReplaceWinner)
		value.ReplaceWinner = &winner
	}
	if value.Conflict != nil {
		conflict := cloneComponentProviderConflict(*value.Conflict)
		value.Conflict = &conflict
	}
	if value.Selection != nil {
		selection := *value.Selection
		value.Selection = &selection
	}
	return value
}

func sameHookArtifact(left, right HookArtifact) bool {
	return left.ExtensionID == right.ExtensionID && left.ExtensionVersion == right.ExtensionVersion &&
		left.PackageDigest == right.PackageDigest && left.RuntimeInstanceID == right.RuntimeInstanceID
}

func sameComponentRuntimeContribution(left, right ComponentContribution) bool {
	return left.ID == right.ID && left.ContractVersion == right.ContractVersion && left.Action == right.Action &&
		left.TargetID == right.TargetID && left.TargetContractVersion == right.TargetContractVersion &&
		left.Priority == right.Priority && left.Permission == right.Permission && left.SSRTemplate == right.SSRTemplate &&
		left.L2Component == right.L2Component && left.PropsSchema == right.PropsSchema &&
		left.PropsSchemaDigest == right.PropsSchemaDigest && left.ResultSchema == right.ResultSchema &&
		left.ResultSchemaDigest == right.ResultSchemaDigest && left.ThemeOverrideKey == right.ThemeOverrideKey &&
		sameHookArtifact(left.Artifact, right.Artifact)
}
