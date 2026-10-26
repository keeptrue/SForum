package extensionsruntime

import (
	"context"
	"errors"
	"sort"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func (m *Manager) ProviderSlotSelections() *ProviderSlotSelectionAPI {
	return m.eventsProviders.ProviderSlotSelections()
}

const (
	providerSlotAvailable          = "available"
	providerSlotUnavailable        = "unavailable"
	providerSlotNoCandidates       = "no_candidates"
	providerSlotRuntimeUnavailable = "runtime_unavailable"
	providerSlotPriorityTie        = "priority_tie"
	providerSlotSelectionDefault   = "default"
	providerSlotSelectionSelected  = "selected"
	providerSlotSelectionStale     = "stale"
)

func (m *RuntimeEventsProviders) ProviderSlotInspection(ctx context.Context) (extensions.ProviderSlotInspection, error) {
	if ctx == nil {
		return extensions.ProviderSlotInspection{}, ErrProviderSlotSelectionInvalid
	}
	snapshot := m.HookBus().ProviderSlots().Snapshot()
	candidatesByTarget := make(map[string][]ProviderSlotCandidate, len(snapshot.Contracts))
	for _, candidate := range snapshot.Candidates {
		candidatesByTarget[candidate.TargetID] = append(candidatesByTarget[candidate.TargetID], candidate)
	}
	result := extensions.ProviderSlotInspection{
		Revision: snapshot.Revision,
		Slots:    make([]extensions.ProviderSlotInspectionItem, 0, len(snapshot.Contracts)),
	}
	for _, contract := range snapshot.Contracts {
		contractArtifact := providerSlotInspectionArtifact(contract.Artifact)
		item := extensions.ProviderSlotInspectionItem{
			Contract: extensions.ProviderSlotContractInspection{
				ID: contract.ID, Slot: contract.Slot, ContractVersion: contract.ContractVersion,
				RequestSchema: contract.RequestSchema, ResponseSchema: contract.ResponseSchema,
				RequestSchemaDigest: contract.RequestSchemaDigest, ResponseSchemaDigest: contract.ResponseSchemaDigest,
				Fallback: contract.Fallback, TimeoutMS: contract.TimeoutMS, Artifact: contractArtifact,
				ContractRuntimeAvailable: m.providerSlotArtifactAvailable(contract.Artifact),
			},
			Candidates:      []extensions.ProviderSlotCandidateInspection{},
			Conflicts:       []extensions.ProviderSlotConflictInspection{},
			SelectionStatus: providerSlotSelectionDefault,
		}
		priorityCandidates := make(map[int][]string)
		for index, candidate := range candidatesByTarget[contract.ID] {
			availability := providerSlotUnavailable
			if m.providerSlotArtifactAvailable(candidate.Artifact) {
				availability = providerSlotAvailable
				item.Availability = providerSlotAvailable
			}
			item.Candidates = append(item.Candidates, extensions.ProviderSlotCandidateInspection{
				ID: candidate.ID, TargetID: candidate.TargetID, Label: candidate.Label, Handler: candidate.Handler,
				Priority: candidate.Priority, Rank: index + 1,
				Artifact: providerSlotInspectionArtifact(candidate.Artifact), Availability: availability,
			})
			priorityCandidates[candidate.Priority] = append(priorityCandidates[candidate.Priority], candidate.ID)
		}
		for priority, ids := range priorityCandidates {
			if len(ids) > 1 {
				item.Conflicts = append(item.Conflicts, extensions.ProviderSlotConflictInspection{
					Kind: providerSlotPriorityTie, Priority: priority, CandidateIDs: append([]string(nil), ids...),
				})
			}
		}
		sort.Slice(item.Conflicts, func(i, j int) bool { return item.Conflicts[i].Priority > item.Conflicts[j].Priority })
		if item.Availability == "" {
			item.Availability = providerSlotUnavailable
			if len(item.Candidates) == 0 {
				item.UnavailabilityReason = providerSlotNoCandidates
			} else {
				item.UnavailabilityReason = providerSlotRuntimeUnavailable
			}
		}
		if selections := m.ProviderSlotSelections(); selections != nil {
			desired, selectionErr := selections.Current(ctx, contract.ID)
			switch {
			case selectionErr == nil:
				item.Selection = providerSlotSelectionInspection(desired)
				live, liveErr := selections.store.Selected(ctx, contract.ID)
				resolution := ProviderSlotResolution{Revision: snapshot.Revision, Contract: contract, Candidates: candidatesByTarget[contract.ID]}
				if liveErr == nil && exactProviderSlotSelectionIndex(resolution, live) >= 0 {
					item.SelectionStatus = providerSlotSelectionSelected
				} else if liveErr == nil || errors.Is(liveErr, ErrProviderSlotSelectionStale) || errors.Is(liveErr, ErrProviderSlotSelectionNotFound) {
					item.SelectionStatus = providerSlotSelectionStale
				} else {
					return extensions.ProviderSlotInspection{}, liveErr
				}
			case errors.Is(selectionErr, ErrProviderSlotSelectionNotFound):
			default:
				return extensions.ProviderSlotInspection{}, selectionErr
			}
		}
		result.Slots = append(result.Slots, item)
	}
	return result, nil
}

func providerSlotSelectionInspection(selection ProviderSlotSelection) *extensions.ProviderSlotSelectionInspection {
	return &extensions.ProviderSlotSelectionInspection{
		ContractID: selection.ContractID, ContractVersion: selection.ContractVersion, Slot: selection.Slot,
		ContractArtifact: providerSlotInspectionArtifact(selection.ContractArtifact), CandidateID: selection.CandidateID,
		ProviderArtifact: providerSlotInspectionArtifact(selection.ProviderArtifact),
		SelectedByUserID: selection.SelectedByUserID, SelectionAuditID: selection.SelectionAuditID,
		Revision: selection.Revision, SelectedAt: selection.SelectedAt, UpdatedAt: selection.UpdatedAt,
	}
}

func (m *managerCore) providerSlotArtifactAvailable(artifact HookArtifact) bool {
	if m == nil {
		return false
	}
	return m.host.RuntimeInstanceAvailable(RuntimeInstanceIdentity{
		ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID,
	})
}

func providerSlotInspectionArtifact(artifact HookArtifact) extensions.ProviderSlotArtifactInspection {
	return extensions.ProviderSlotArtifactInspection{
		ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion,
		PackageDigest: artifact.PackageDigest, RuntimeInstanceID: artifact.RuntimeInstanceID,
	}
}

var _ extensions.ProviderSlotInspectionSource = (*Manager)(nil)

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) ProviderSlotInspection(ctx context.Context) (extensions.ProviderSlotInspection, error) {
	return m.eventsProviders.ProviderSlotInspection(ctx)
}
