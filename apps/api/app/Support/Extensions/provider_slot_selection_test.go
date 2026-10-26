package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestProviderSlotSelectionAPIReordersExactCandidateAndPreservesFallback(t *testing.T) {
	registry, owner, preferred := providerSelectionFixture(t, "next")
	store := &providerSlotSelectionMemoryStore{}
	api := NewProviderSlotSelectionAPI(registry, store)

	selection, err := api.Select(t.Context(), providerSlotID, owner.Manifest.Providers[0].ID, 0, 11, 21)
	if err != nil {
		t.Fatal(err)
	}
	store.selection = selection
	resolution, status, err := api.Resolve(t.Context(), ProviderSlotCaller{}, providerSlotID, providerContractVersion)
	if err != nil {
		t.Fatal(err)
	}
	if status != "selected" || len(resolution.Candidates) != 2 || resolution.Candidates[0].ID != owner.Manifest.Providers[0].ID ||
		resolution.Candidates[1].ID != preferred.Manifest.Providers[0].ID {
		t.Fatalf("selected resolution = status %q candidates %#v", status, resolution.Candidates)
	}
}

func TestProviderSlotSelectionAPIRejectsForgedAndStaleArtifacts(t *testing.T) {
	registry, owner, _ := providerSelectionFixture(t, "next")
	store := &providerSlotSelectionMemoryStore{}
	api := NewProviderSlotSelectionAPI(registry, store)
	if _, err := api.Select(t.Context(), providerSlotID, "forged.candidate", 0, 11, 21); !errors.Is(err, ErrProviderSlotSelectionStale) {
		t.Fatalf("forged candidate = %v", err)
	}
	selection, err := api.Select(t.Context(), providerSlotID, owner.Manifest.Providers[0].ID, 0, 11, 21)
	if err != nil {
		t.Fatal(err)
	}
	selection.ProviderArtifact.PackageDigest = strings.Repeat("f", 64)
	store.selection = selection
	resolution, status, err := api.Resolve(t.Context(), ProviderSlotCaller{}, providerSlotID, providerContractVersion)
	if err != nil || status != "stale_fallback" || len(resolution.Candidates) != 2 {
		t.Fatalf("next stale fallback = status %q candidates %#v err %v", status, resolution.Candidates, err)
	}
}

func TestProviderSlotSelectionAPIClosedSelectionDoesNotTryAnotherProvider(t *testing.T) {
	registry, owner, _ := providerSelectionFixture(t, "closed")
	store := &providerSlotSelectionMemoryStore{}
	api := NewProviderSlotSelectionAPI(registry, store)
	selection, err := api.Select(t.Context(), providerSlotID, owner.Manifest.Providers[0].ID, 0, 11, 21)
	if err != nil {
		t.Fatal(err)
	}
	store.selection = selection
	resolution, status, err := api.Resolve(t.Context(), ProviderSlotCaller{}, providerSlotID, providerContractVersion)
	if err != nil || status != "selected" || len(resolution.Candidates) != 1 || resolution.Candidates[0].ID != owner.Manifest.Providers[0].ID {
		t.Fatalf("closed selection = status %q candidates %#v err %v", status, resolution.Candidates, err)
	}
	store.selection.ProviderArtifact.PackageDigest = strings.Repeat("f", 64)
	if _, status, err = api.Resolve(t.Context(), ProviderSlotCaller{}, providerSlotID, providerContractVersion); status != "stale_closed" || !errors.Is(err, ErrProviderSlotSelectionStale) {
		t.Fatalf("closed stale selection = status %q err %v", status, err)
	}
}

func providerSelectionFixture(t *testing.T, fallback string) (*VersionedProviderSlotRegistry, extensions.Extension, extensions.Extension) {
	t.Helper()
	registry := NewVersionedProviderSlotRegistry()
	ownerExtension := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
	ownerExtension.Manifest.Providers[0].Fallback = fallback
	preferredExtension := versionedProviderExtension("providers.preferred", strings.Repeat("b", 64), providerSlotConsumer("providers.preferred.delivery", 50))
	preferredExtension.Manifest.Providers[0].Fallback = fallback
	preferredExtension.Manifest.Dependencies = []extensions.ManifestDependency{{ID: ownerExtension.ID, Version: "^1.0.0", Kind: "required"}}
	if err := registry.ReplaceRuntime(ownerExtension, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(preferredExtension, "preferred-runtime"); err != nil {
		t.Fatal(err)
	}
	return registry, ownerExtension, preferredExtension
}

type providerSlotSelectionMemoryStore struct {
	selection ProviderSlotSelection
}

func (s *providerSlotSelectionMemoryStore) Desired(context.Context, string) (ProviderSlotSelection, error) {
	if s.selection.ContractID == "" {
		return ProviderSlotSelection{}, ErrProviderSlotSelectionNotFound
	}
	return s.selection, nil
}

func (s *providerSlotSelectionMemoryStore) Selected(ctx context.Context, id string) (ProviderSlotSelection, error) {
	return s.Desired(ctx, id)
}

func (s *providerSlotSelectionMemoryStore) Select(_ context.Context, request SelectProviderSlotRequest) (ProviderSlotSelection, error) {
	return ProviderSlotSelection{
		ContractID: request.Contract.ID, ContractVersion: request.Contract.ContractVersion, Slot: request.Contract.Slot,
		ContractArtifact: request.Contract.Artifact, CandidateID: request.Candidate.ID, ProviderArtifact: request.Candidate.Artifact,
		SelectedByUserID: request.ActorUserID, SelectionAuditID: request.AuditEventID, Revision: request.ExpectedRevision + 1,
	}, nil
}

func (s *providerSlotSelectionMemoryStore) Reset(context.Context, ResetProviderSlotRequest) error {
	s.selection = ProviderSlotSelection{}
	return nil
}

func (*providerSlotSelectionMemoryStore) InvalidateExtension(context.Context, InvalidateProviderSlotRequest) (int64, error) {
	return 0, nil
}

func (*providerSlotSelectionMemoryStore) ListEvents(context.Context, string, int) ([]ProviderSlotSelectionEvent, error) {
	return nil, nil
}
