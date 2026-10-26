package routes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestProviderSelectionAPIRequiresExactSnapshotCandidateAndBuildsPlan(t *testing.T) {
	registry, artifact, key, request := providerSelectionTestRegistry(t)
	store := newMemoryProviderSelectionStore()
	api := NewProviderSelectionAPI(registry, store)

	if _, err := api.BuildExecutionPlan(t.Context(), "POST", "/topics"); !errors.Is(err, ErrAmbiguousRoute) {
		t.Fatalf("unselected replacement error = %v", err)
	}
	selected, err := api.Select(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Revision != 1 || selected.ProviderExtensionID != artifact.ExtensionID || selected.Key != key {
		t.Fatalf("selected = %#v", selected)
	}
	plan, err := api.BuildExecutionPlan(t.Context(), "POST", "/topics")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid() || plan.Terminal().RouteID != request.ProviderRouteID ||
		plan.Terminal().Provider.Artifact != artifact {
		t.Fatalf("selected plan = %#v", plan.Terminal())
	}

	forged := request
	forged.ProviderContractVersion = "selection.plugin.writer@2"
	forged.ExpectedRevision = 1
	if _, err := api.Select(t.Context(), forged); !errors.Is(err, ErrProviderSelectionStale) {
		t.Fatalf("forged route contract error = %v", err)
	}

	replacement := modifierRoute(request.ProviderRouteID, key.TargetRouteID, "/topics", extensionmanifest.RouteActionReplace, "POST", 200)
	changedArtifact := artifact
	changedArtifact.PackageDigest = string([]byte(artifact.PackageDigest[:63])) + "b"
	changedArtifact.RuntimeInstanceID = "runtime-new"
	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{coreRoute(key.TargetRouteID, "POST", "/topics")},
		Plugins: []PluginRouteSet{{Artifact: changedArtifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.BuildExecutionPlan(t.Context(), "POST", "/topics"); !errors.Is(err, ErrProviderSelectionStale) {
		t.Fatalf("stale exact artifact error = %v", err)
	}
}

func TestProviderSelectionAPIRejectsPersistedChoiceAfterTargetContractBump(t *testing.T) {
	registry, artifact, key, request := providerSelectionTestRegistry(t)
	store := newMemoryProviderSelectionStore()
	api := NewProviderSelectionAPI(registry, store)
	if _, err := api.Select(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	target := coreRoute(key.TargetRouteID, "POST", "/topics")
	target.ContractVersion = "sforum.route.selection.create@2"
	target.Guard.ContractVersion = target.ContractVersion
	replacement := modifierRoute(request.ProviderRouteID, key.TargetRouteID, "/topics", extensionmanifest.RouteActionReplace, "POST", 100)
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.BuildExecutionPlan(t.Context(), "POST", "/topics"); !errors.Is(err, ErrProviderSelectionStale) {
		t.Fatalf("target contract bump error = %v", err)
	}
}

func TestProviderSelectionInspectorMarksProviderContractDriftStale(t *testing.T) {
	registry, artifact, key, request := providerSelectionTestRegistry(t)
	store := newMemoryProviderSelectionStore()
	api := NewProviderSelectionAPI(registry, store)
	if _, err := api.Select(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	target := coreRoute(key.TargetRouteID, "POST", "/topics")
	replacement := modifierRoute(request.ProviderRouteID, key.TargetRouteID, "/topics", extensionmanifest.RouteActionReplace, "POST", 100)
	replacement.ContractVersion = request.ProviderRouteID + "@2"
	if _, err := registry.Publish(Publication{
		Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}
	conflicts, err := api.Conflicts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].SelectionStatus != "stale" || conflicts[0].Selection == nil ||
		conflicts[0].Selection.ProviderContractVersion != request.ProviderContractVersion {
		t.Fatalf("provider contract drift conflicts = %#v", conflicts)
	}
}

func TestProviderSelectionAPICASFencesSelectionResetAndInvalidation(t *testing.T) {
	registry, _, key, request := providerSelectionTestRegistry(t)
	store := newMemoryProviderSelectionStore()
	api := NewProviderSelectionAPI(registry, store)
	if _, err := api.Select(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Select(t.Context(), request); !errors.Is(err, ErrProviderSelectionRevisionConflict) {
		t.Fatalf("stale select CAS error = %v", err)
	}
	if err := api.Reset(t.Context(), ResetProviderRequest{
		Key: key, ExpectedRevision: 2, ActorUserID: 8, AuditEventID: 18,
	}); !errors.Is(err, ErrProviderSelectionRevisionConflict) {
		t.Fatalf("stale reset CAS error = %v", err)
	}
	if err := api.Reset(t.Context(), ResetProviderRequest{
		Key: key, ExpectedRevision: 1, ActorUserID: 8, AuditEventID: 18, ReasonCode: "operator_reset",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Selected(t.Context(), key); !errors.Is(err, ErrProviderSelectionNotFound) {
		t.Fatalf("reset selection = %v", err)
	}

	request.ExpectedRevision = 0
	request.AuditEventID = 19
	if _, err := api.Select(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	count, err := api.InvalidateExtension(t.Context(), InvalidateProviderRequest{
		ExtensionID: request.ProviderArtifact.ExtensionID, ActorUserID: 9,
		AuditEventID: 20, ReasonCode: "extension_disabled",
	})
	if err != nil || count != 1 {
		t.Fatalf("invalidate count=%d err=%v", count, err)
	}
	if len(store.events) != 4 || store.events[1].Action != "reset" || store.events[3].Action != "invalidate" {
		t.Fatalf("events = %#v", store.events)
	}
}

func providerSelectionTestRegistry(t *testing.T) (*Registry, PluginArtifact, ProviderSelectionKey, SelectProviderRequest) {
	t.Helper()
	registry := NewRegistry()
	artifact := routeArtifact("selection.plugin", "1.0.0", 'a')
	target := coreRoute("core.route.selection.create", "POST", "/topics")
	replacement := modifierRoute("selection.plugin.writer", target.ID, "/topics", extensionmanifest.RouteActionReplace, "POST", 100)
	snapshot, err := registry.Publish(Publication{
		Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var targetRoute Route
	for _, route := range snapshot.Routes {
		if route.ID == target.ID {
			targetRoute = route
		}
	}
	key := ProviderSelectionKey{
		TargetRouteID: target.ID, TargetContractVersion: target.ContractVersion,
		Method: "POST", PathSignature: targetRoute.PathSignature,
	}
	request := SelectProviderRequest{
		Key: key, ProviderRouteID: replacement.ID, ProviderContractVersion: replacement.ContractVersion,
		ProviderArtifact: artifact, ExpectedRevision: 0, ActorUserID: 7, AuditEventID: 17,
	}
	return registry, artifact, key, request
}

type memoryProviderSelectionStore struct {
	mu      sync.Mutex
	current map[ProviderSelectionKey]ProviderSelection
	events  []ProviderSelectionEvent
}

func newMemoryProviderSelectionStore() *memoryProviderSelectionStore {
	return &memoryProviderSelectionStore{current: make(map[ProviderSelectionKey]ProviderSelection)}
}

func (s *memoryProviderSelectionStore) Selected(_ context.Context, key ProviderSelectionKey) (ProviderSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.current[key]
	if !ok {
		for existingKey := range s.current {
			if existingKey.TargetRouteID == key.TargetRouteID && existingKey.Method == key.Method &&
				existingKey.PathSignature == key.PathSignature {
				return ProviderSelection{}, ErrProviderSelectionStale
			}
		}
		return ProviderSelection{}, ErrProviderSelectionNotFound
	}
	return value, nil
}

func (s *memoryProviderSelectionStore) Desired(_ context.Context, key ProviderSelectionKey) (ProviderSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.current[key]; ok {
		return value, nil
	}
	for existingKey, value := range s.current {
		if existingKey.TargetRouteID == key.TargetRouteID && existingKey.Method == key.Method &&
			existingKey.PathSignature == key.PathSignature {
			return value, nil
		}
	}
	return ProviderSelection{}, ErrProviderSelectionNotFound
}

func (s *memoryProviderSelectionStore) Select(_ context.Context, request SelectProviderRequest) (ProviderSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.current[request.Key]
	if !ok && request.ExpectedRevision != 0 || ok && previous.Revision != request.ExpectedRevision {
		return ProviderSelection{}, ErrProviderSelectionRevisionConflict
	}
	revision := int64(1)
	if ok {
		revision = previous.Revision + 1
	}
	now := time.Now().UTC()
	value := ProviderSelection{
		Key: request.Key, ProviderRouteID: request.ProviderRouteID,
		ProviderContractVersion: request.ProviderContractVersion,
		ProviderExtensionID:     request.ProviderArtifact.ExtensionID, ProviderExtensionVersionID: 42,
		ProviderExtensionVersion: request.ProviderArtifact.ExtensionVersion,
		ProviderPackageDigest:    request.ProviderArtifact.PackageDigest,
		SelectedByUserID:         request.ActorUserID, SelectionAuditEventID: request.AuditEventID,
		Revision: revision, SelectedAt: now, UpdatedAt: now,
	}
	s.current[request.Key] = value
	s.events = append(s.events, ProviderSelectionEvent{Action: "select", Key: request.Key, SelectedProvider: &value, SelectionRevision: revision})
	return value, nil
}

func (s *memoryProviderSelectionStore) Reset(_ context.Context, request ResetProviderRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.current[request.Key]
	if !ok {
		return ErrProviderSelectionNotFound
	}
	if previous.Revision != request.ExpectedRevision {
		return ErrProviderSelectionRevisionConflict
	}
	delete(s.current, request.Key)
	s.events = append(s.events, ProviderSelectionEvent{Action: "reset", Key: request.Key, PreviousProvider: &previous, SelectionRevision: previous.Revision + 1})
	return nil
}

func (s *memoryProviderSelectionStore) InvalidateExtension(_ context.Context, request InvalidateProviderRequest) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for key, previous := range s.current {
		if previous.ProviderExtensionID != request.ExtensionID {
			continue
		}
		delete(s.current, key)
		count++
		s.events = append(s.events, ProviderSelectionEvent{Action: "invalidate", Key: key, PreviousProvider: &previous, SelectionRevision: previous.Revision + 1})
	}
	return count, nil
}

func (s *memoryProviderSelectionStore) ListEvents(context.Context, ProviderSelectionKey, int) ([]ProviderSelectionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ProviderSelectionEvent(nil), s.events...), nil
}

var _ ProviderSelectionStore = (*memoryProviderSelectionStore)(nil)
