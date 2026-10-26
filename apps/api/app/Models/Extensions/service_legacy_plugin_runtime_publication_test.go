package extensions

import (
	"context"
	"errors"
	"slices"
	"testing"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestLegacyServiceUsesAtomicPluginRuntimePublicationStore(t *testing.T) {
	item := legacyPluginRuntimeServiceFixture(t, StatusInstalled)
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	runtime := &orderedQueryRuntime{events: &events}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	enabled, err := service.Enable(
		t.Context(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Status != StatusEnabled || !slices.Equal(events, []string{
		"desired.enable", "store.enable", "runtime.start",
	}) {
		t.Fatalf("legacy desired enable order=%v enabled=%+v", events, enabled)
	}

	events = events[:0]
	disabled, err := service.Disable(t.Context(), extensionManager(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != StatusDisabled || !slices.Equal(events, []string{
		"runtime.stop", "desired.disable", "store.disable",
	}) {
		t.Fatalf("legacy desired disable order=%v disabled=%+v", events, disabled)
	}
	if !slices.Equal(store.reasons, []PluginRuntimePublicationReason{
		PluginRuntimePublicationEnable, PluginRuntimePublicationDisable,
	}) || !slices.Equal(store.actors, []int64{extensionManager().ID, extensionManager().ID}) {
		t.Fatalf("legacy desired evidence reasons=%v actors=%v", store.reasons, store.actors)
	}
}

func TestLegacyServicePublishesRuntimeIdentityAroundExplicitLifecycle(t *testing.T) {
	item := legacyPluginRuntimeServiceFixture(t, StatusInstalled)
	item.Manifest.Identity = &ManifestIdentity{
		ContractVersion: item.ID + ".identity@1",
		Providers: []ManifestIdentityProvider{{
			ID: item.ID + ".auth", ContractVersion: item.ID + ".auth@1",
			Kind: "auth", Handler: item.ID + ".auth",
		}},
	}
	refreshTrustPackageIdentity(t, &item)
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	runtime := &orderedQueryRuntime{events: &events}
	identityPublications := &recordingRuntimeIdentityPublicationBoundary{events: &events}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime).
		BindRuntimeIdentityPublications(identityPublications)
	WithAuditor(&recordingAuditWriter{events: &events})(service)

	enabled, err := service.Enable(
		t.Context(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Status != StatusEnabled || identityPublications.publishCalls != 1 ||
		identityPublications.lastPublishAuditID <= 0 ||
		!slices.Equal(events, []string{
			"desired.enable", "store.enable", "runtime.start",
			"audit.extension.enable", "identity.publish",
		}) {
		t.Fatalf("legacy identity enable order=%v enabled=%+v identity=%+v",
			events, enabled, identityPublications)
	}

	events = events[:0]
	disabled, err := service.Disable(t.Context(), extensionManager(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != StatusDisabled || identityPublications.quarantineCalls != 1 ||
		identityPublications.lastQuarantineAuditID <= 0 ||
		!slices.Equal(events, []string{
			"audit.extension.disable", "identity.quarantine",
			"desired.disable", "store.disable", "runtime.stop",
		}) {
		t.Fatalf("legacy identity disable order=%v disabled=%+v identity=%+v",
			events, disabled, identityPublications)
	}
}

func TestLegacyServiceRuntimeFailurePublishesCompensatingDisable(t *testing.T) {
	item := legacyPluginRuntimeServiceFixture(t, StatusInstalled)
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	runtime := &orderedQueryRuntime{events: &events}
	runtime.startErr = errors.New("start failed")
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	_, err := service.Enable(
		t.Context(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true},
	)
	if !errors.Is(err, ErrRuntimeFailed) || !slices.Equal(events, []string{
		"desired.enable", "store.enable", "runtime.start", "desired.disable", "store.disable",
	}) {
		t.Fatalf("legacy runtime compensation err=%v order=%v", err, events)
	}
	if got := store.items[item.ID].Status; got != StatusDisabled {
		t.Fatalf("legacy runtime compensation status=%q", got)
	}
	if !slices.Equal(store.reasons, []PluginRuntimePublicationReason{
		PluginRuntimePublicationEnable, PluginRuntimePublicationDisable,
	}) {
		t.Fatalf("legacy runtime compensation reasons=%v", store.reasons)
	}
}

func TestLegacyServiceReportsCompensatingDesiredDisableFailure(t *testing.T) {
	item := legacyPluginRuntimeServiceFixture(t, StatusInstalled)
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	disableErr := errors.New("desired compensation failed")
	store := &recordingLegacyPluginRuntimeStore{
		orderedQueryStore: base, events: &events, disableErr: disableErr,
	}
	runtime := &orderedQueryRuntime{events: &events}
	runtime.startErr = errors.New("start failed")
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	_, err := service.Enable(
		t.Context(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true},
	)
	if !errors.Is(err, ErrRuntimeFailed) || !errors.Is(err, disableErr) || !slices.Equal(events, []string{
		"desired.enable", "store.enable", "runtime.start", "desired.disable",
	}) {
		t.Fatalf("legacy desired compensation failure err=%v order=%v", err, events)
	}
	if got := store.items[item.ID].Status; got != StatusEnabled {
		t.Fatalf("failed desired compensation guessed disabled state=%q", got)
	}
}

func TestLegacyServiceDesiredDisableFailureRestoresQuarantinedQuery(t *testing.T) {
	item := legacyQueryServiceExtension(t, StatusEnabled)
	events := []string{}
	base := &orderedQueryStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	store := &recordingLegacyPluginRuntimeStore{
		orderedQueryStore: base,
		events:            &events,
		disableErr:        errors.New("desired disable failed"),
	}
	runtime := &orderedQueryRuntime{events: &events}
	queries := &recordingQueryPublicationBoundary{events: &events}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime).BindRuntimeQueryPublications(queries)

	_, err := service.Disable(t.Context(), extensionManager(), item.ID)
	if !errors.Is(err, store.disableErr) || !slices.Equal(events, []string{
		"query.quarantine", "desired.disable", "query.rollback",
	}) {
		t.Fatalf("legacy desired disable failure err=%v order=%v", err, events)
	}
	if store.items[item.ID].Status != StatusEnabled || len(runtime.stopped) != 0 || queries.rollbackCalls != 1 {
		t.Fatalf("legacy desired disable compensation store=%+v runtime=%+v queries=%+v",
			store.items[item.ID], runtime, queries)
	}
}

func TestLegacyServicePublishesAndQuarantinesExactRuntimeContent(t *testing.T) {
	item := legacyContentServiceFixture(t, StatusInstalled)
	events := []string{}
	base := &orderedQueryStore{fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}), events: &events}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	runtime := &orderedQueryRuntime{events: &events}
	content := &recordingRuntimeContentPublicationBoundary{events: &events}
	service := NewServiceWithOptions(store, t.TempDir(), "", runtime, WithRuntimeContentPublications(content))

	enabled, err := service.Enable(t.Context(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true})
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Status != StatusEnabled || !slices.Equal(events, []string{
		"desired.enable", "store.enable", "runtime.start", "content.publish",
	}) {
		t.Fatalf("legacy content enable order=%v enabled=%+v", events, enabled)
	}

	events = events[:0]
	disabled, err := service.Disable(t.Context(), extensionManager(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != StatusDisabled || !slices.Equal(events, []string{
		"content.quarantine", "desired.disable", "store.disable", "runtime.stop",
	}) {
		t.Fatalf("legacy content disable order=%v disabled=%+v", events, disabled)
	}
}

func TestLegacyContentDisableFailureRestoresPublicationAndRuntimeAdmission(t *testing.T) {
	item := legacyContentServiceFixture(t, StatusEnabled)
	events := []string{}
	base := &orderedQueryStore{fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}), events: &events}
	store := &recordingLegacyPluginRuntimeStore{
		orderedQueryStore: base, events: &events, disableErr: errors.New("desired disable failed"),
	}
	content := &recordingRuntimeContentPublicationBoundary{events: &events}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", &orderedQueryRuntime{events: &events}, WithRuntimeContentPublications(content),
	)

	_, err := service.Disable(t.Context(), extensionManager(), item.ID)
	if !errors.Is(err, store.disableErr) || !slices.Equal(events, []string{
		"content.quarantine", "desired.disable", "content.rollback",
	}) || content.rollbackCalls != 1 || store.items[item.ID].Status != StatusEnabled {
		t.Fatalf("legacy content disable compensation err=%v events=%v content=%+v", err, events, content)
	}
}

func TestLegacyServicePublishesAndQuarantinesExactRuntimeEditor(t *testing.T) {
	item := legacyContentEditorServiceFixture(t, StatusInstalled)
	events := []string{}
	base := &orderedQueryStore{fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}), events: &events}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	content := &recordingRuntimeContentPublicationBoundary{events: &events}
	editor := &recordingRuntimeEditorPublicationBoundary{events: &events}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", &orderedQueryRuntime{events: &events},
		WithRuntimeContentPublications(content), WithRuntimeEditorPublications(editor),
	)

	enabled, err := service.Enable(t.Context(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true})
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Status != StatusEnabled || !slices.Equal(events, []string{
		"desired.enable", "store.enable", "runtime.start", "content.publish", "editor.publish",
	}) {
		t.Fatalf("legacy editor enable order=%v enabled=%+v", events, enabled)
	}

	events = events[:0]
	disabled, err := service.Disable(t.Context(), extensionManager(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != StatusDisabled || !slices.Equal(events, []string{
		"content.quarantine", "editor.quarantine", "desired.disable", "store.disable", "runtime.stop",
	}) {
		t.Fatalf("legacy editor disable order=%v disabled=%+v", events, disabled)
	}
}

func TestLegacyEditorPublishFailureRestoresContentAndDisablesRuntime(t *testing.T) {
	item := legacyContentEditorServiceFixture(t, StatusInstalled)
	events := []string{}
	base := &orderedQueryStore{fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}), events: &events}
	store := &recordingLegacyPluginRuntimeStore{orderedQueryStore: base, events: &events}
	content := &recordingRuntimeContentPublicationBoundary{events: &events}
	editor := &recordingRuntimeEditorPublicationBoundary{events: &events, publishErr: errors.New("editor publish failed")}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", &orderedQueryRuntime{events: &events},
		WithRuntimeContentPublications(content), WithRuntimeEditorPublications(editor),
	)

	_, err := service.Enable(t.Context(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true})
	if !errors.Is(err, ErrRuntimeFailed) || !errors.Is(err, editor.publishErr) || !slices.Equal(events, []string{
		"desired.enable", "store.enable", "runtime.start", "content.publish", "editor.publish",
		"content.rollback", "runtime.stop", "desired.disable", "store.disable",
	}) {
		t.Fatalf("legacy editor publish compensation err=%v events=%v", err, events)
	}
	if content.rollbackCalls != 1 || store.items[item.ID].Status != StatusDisabled {
		t.Fatalf("legacy editor publish compensation content=%+v status=%s", content, store.items[item.ID].Status)
	}
}

func TestLegacyServiceStoreWithoutPublicationBoundaryPreservesV1(t *testing.T) {
	item := legacyPluginRuntimeServiceFixture(t, StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	if _, err := service.Enable(
		t.Context(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Disable(t.Context(), extensionManager(), item.ID); err != nil {
		t.Fatal(err)
	}
	if store.enabledID != item.ID || store.disabledID != item.ID {
		t.Fatalf("V1 store calls enabled=%q disabled=%q", store.enabledID, store.disabledID)
	}
}

func legacyPluginRuntimeServiceFixture(t *testing.T, status string) Extension {
	t.Helper()
	item := legacyQueryServiceExtension(t, status)
	item.Manifest.Queries = nil
	item.Manifest.Cache = nil
	refreshTrustPackageIdentity(t, &item)
	return item
}

func legacyContentServiceFixture(t *testing.T, status string) Extension {
	t.Helper()
	item := completeV3TrustExtension(t, "demo.content")
	item.Status = status
	item.ActiveVersionID = 41
	item.Manifest.Lifecycle = nil
	item.Manifest.Dependencies = nil
	item.Manifest.Queries = nil
	item.Manifest.Cache = nil
	refreshTrustPackageIdentity(t, &item)
	return item
}

func legacyContentEditorServiceFixture(t *testing.T, status string) Extension {
	t.Helper()
	item := legacyContentServiceFixture(t, status)
	item.Manifest.Editor = []extensionmanifest.ManifestEditor{
		{
			ID: item.ID + ".command.references", ContractVersion: item.ID + ".command.references@1",
			Kind: "command", CommandKey: "openReferenceMenu",
		},
		{
			ID: item.ID + ".toolbar.references", ContractVersion: item.ID + ".toolbar.references@1",
			Kind: "toolbar", CommandID: item.ID + ".command.references", Label: "References",
		},
	}
	refreshTrustPackageIdentity(t, &item)
	return item
}

type recordingLegacyPluginRuntimeStore struct {
	*orderedQueryStore
	events     *[]string
	reasons    []PluginRuntimePublicationReason
	actors     []int64
	enableErr  error
	disableErr error
}

func (s *recordingLegacyPluginRuntimeStore) EnableLegacyPluginRuntime(
	ctx context.Context,
	target Extension,
	actorUserID int64,
) (Extension, PluginRuntimePublication, error) {
	*s.events = append(*s.events, "desired.enable")
	if s.enableErr != nil {
		return Extension{}, PluginRuntimePublication{}, s.enableErr
	}
	enabled, err := s.orderedQueryStore.Enable(ctx, target.ID, target.Type)
	if err != nil {
		return Extension{}, PluginRuntimePublication{}, err
	}
	s.reasons = append(s.reasons, PluginRuntimePublicationEnable)
	s.actors = append(s.actors, actorUserID)
	return enabled, PluginRuntimePublication{Reason: PluginRuntimePublicationEnable}, nil
}

func (s *recordingLegacyPluginRuntimeStore) DisableLegacyPluginRuntime(
	ctx context.Context,
	target Extension,
	actorUserID int64,
) (Extension, PluginRuntimePublication, error) {
	*s.events = append(*s.events, "desired.disable")
	if s.disableErr != nil {
		return Extension{}, PluginRuntimePublication{}, s.disableErr
	}
	disabled, err := s.orderedQueryStore.Disable(ctx, target.ID)
	if err != nil {
		return Extension{}, PluginRuntimePublication{}, err
	}
	s.reasons = append(s.reasons, PluginRuntimePublicationDisable)
	s.actors = append(s.actors, actorUserID)
	return disabled, PluginRuntimePublication{Reason: PluginRuntimePublicationDisable}, nil
}

var _ LegacyPluginRuntimePublicationStore = (*recordingLegacyPluginRuntimeStore)(nil)

type recordingRuntimeContentPublicationMutation struct {
	boundary *recordingRuntimeContentPublicationBoundary
}

func (m *recordingRuntimeContentPublicationMutation) Rollback() error {
	m.boundary.rollbackCalls++
	if m.boundary.events != nil {
		*m.boundary.events = append(*m.boundary.events, "content.rollback")
	}
	return m.boundary.rollbackErr
}

type recordingRuntimeContentPublicationBoundary struct {
	events          *[]string
	publishCalls    int
	quarantineCalls int
	rollbackCalls   int
	publishErr      error
	quarantineErr   error
	rollbackErr     error
}

type recordingRuntimeEditorPublicationMutation struct {
	boundary *recordingRuntimeEditorPublicationBoundary
}

func (m *recordingRuntimeEditorPublicationMutation) Rollback() error {
	m.boundary.rollbackCalls++
	if m.boundary.events != nil {
		*m.boundary.events = append(*m.boundary.events, "editor.rollback")
	}
	return m.boundary.rollbackErr
}

type recordingRuntimeEditorPublicationBoundary struct {
	events          *[]string
	publishCalls    int
	quarantineCalls int
	rollbackCalls   int
	publishErr      error
	quarantineErr   error
	rollbackErr     error
}

func (b *recordingRuntimeEditorPublicationBoundary) PublishRuntimeEditor(
	context.Context,
	Extension,
) (RuntimeEditorPublicationMutation, error) {
	b.publishCalls++
	if b.events != nil {
		*b.events = append(*b.events, "editor.publish")
	}
	if b.publishErr != nil {
		return nil, b.publishErr
	}
	return &recordingRuntimeEditorPublicationMutation{boundary: b}, nil
}

func (b *recordingRuntimeEditorPublicationBoundary) QuarantineRuntimeEditor(
	context.Context,
	Extension,
) (RuntimeEditorPublicationMutation, error) {
	b.quarantineCalls++
	if b.events != nil {
		*b.events = append(*b.events, "editor.quarantine")
	}
	if b.quarantineErr != nil {
		return nil, b.quarantineErr
	}
	return &recordingRuntimeEditorPublicationMutation{boundary: b}, nil
}

func (b *recordingRuntimeContentPublicationBoundary) PublishRuntimeContent(
	context.Context,
	Extension,
) (RuntimeContentPublicationMutation, error) {
	b.publishCalls++
	if b.events != nil {
		*b.events = append(*b.events, "content.publish")
	}
	if b.publishErr != nil {
		return nil, b.publishErr
	}
	return &recordingRuntimeContentPublicationMutation{boundary: b}, nil
}

func (b *recordingRuntimeContentPublicationBoundary) QuarantineRuntimeContent(
	context.Context,
	Extension,
) (RuntimeContentPublicationMutation, error) {
	b.quarantineCalls++
	if b.events != nil {
		*b.events = append(*b.events, "content.quarantine")
	}
	if b.quarantineErr != nil {
		return nil, b.quarantineErr
	}
	return &recordingRuntimeContentPublicationMutation{boundary: b}, nil
}

type recordingRuntimeIdentityPublicationBoundary struct {
	events                *[]string
	publishCalls          int
	quarantineCalls       int
	rollbackCalls         int
	lastPublishAuditID    int64
	lastQuarantineAuditID int64
	publishErr            error
	quarantineErr         error
	rollbackErr           error
}

func (b *recordingRuntimeIdentityPublicationBoundary) PublishRuntimeIdentity(
	_ context.Context,
	_ Extension,
	_ int64,
	auditEventID int64,
) (RuntimeIdentityPublicationMutation, error) {
	if b.events != nil {
		*b.events = append(*b.events, "identity.publish")
	}
	b.publishCalls++
	b.lastPublishAuditID = auditEventID
	if b.publishErr != nil {
		return nil, b.publishErr
	}
	return &recordingRuntimeIdentityPublicationMutation{boundary: b}, nil
}

func (b *recordingRuntimeIdentityPublicationBoundary) QuarantineRuntimeIdentity(
	_ context.Context,
	_ Extension,
	_ int64,
	auditEventID int64,
) (RuntimeIdentityPublicationMutation, error) {
	if b.events != nil {
		*b.events = append(*b.events, "identity.quarantine")
	}
	b.quarantineCalls++
	b.lastQuarantineAuditID = auditEventID
	if b.quarantineErr != nil {
		return nil, b.quarantineErr
	}
	return &recordingRuntimeIdentityPublicationMutation{boundary: b}, nil
}

type recordingRuntimeIdentityPublicationMutation struct {
	boundary *recordingRuntimeIdentityPublicationBoundary
}

func (m *recordingRuntimeIdentityPublicationMutation) Rollback() error {
	m.boundary.rollbackCalls++
	if m.boundary.events != nil {
		*m.boundary.events = append(*m.boundary.events, "identity.rollback")
	}
	return m.boundary.rollbackErr
}

type recordingAuditWriter struct {
	events *[]string
	nextID int64
}

func (w *recordingAuditWriter) Append(ctx context.Context, event audit.Event) error {
	_, err := w.AppendReturningID(ctx, event)
	return err
}

func (w *recordingAuditWriter) AppendReturningID(_ context.Context, event audit.Event) (int64, error) {
	if w.events != nil {
		*w.events = append(*w.events, "audit."+event.Action)
	}
	w.nextID++
	return w.nextID, nil
}
