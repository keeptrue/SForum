package extensions

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestLegacyQuerySettingsUseExactRestartBoundary(t *testing.T) {
	for _, surface := range legacyQuerySurfaceCases() {
		t.Run(surface.name, func(t *testing.T) {
			// settings reset 端点在 #217 下线（web 的「恢复默认」走 PUT /settings 提交推荐值），
			// 这条用例只覆盖 update 这一条写入路径。
			item := querySettingsRestartExtension(t, surface.build)
			events := []string{}
			store := &querySettingsRestartStore{
				fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
				events:             &events,
			}
			store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
			runtime := &fakeRuntimeManager{}
			boundary := newRecordingQuerySettingsRestartBoundary(&events)
			service := NewServiceWithRuntime(store, t.TempDir(), runtime).BindRuntimeQueryPublications(boundary)

			if _, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
				Values: map[string]string{"name": "after"},
			}, "zh-CN"); err != nil {
				t.Fatal(err)
			}
			want := []string{"query.settings.prepare", "store.update", "query.settings.restart"}
			if !slices.Equal(events, want) || boundary.restartCalls != 1 ||
				boundary.prepareCalls != 1 ||
				len(runtime.started) != 0 || len(runtime.stopped) != 0 {
				t.Fatalf("settings restart order=%v boundary=%#v runtime=%#v", events, boundary, runtime)
			}
		})
	}
}

func TestLegacyQuerySettingsRestartPreflightsBeforeStoreMutation(t *testing.T) {
	for _, surface := range legacyQuerySurfaceCases() {
		t.Run(surface.name, func(t *testing.T) {
			item := querySettingsRestartExtension(t, surface.build)
			store := newFakeExtensionStore(map[string]Extension{item.ID: item})
			store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
			service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).
				BindRuntimeQueryPublications(&recordingQueryPublicationBoundary{})

			_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
				Values: map[string]string{"name": "after"},
			}, "zh-CN")
			if !errors.Is(err, ErrRuntimeQuerySettingsRestartUnavailable) || store.replaceCalls != 0 ||
				store.settings[item.ID]["name"] != "before" {
				t.Fatalf("missing settings restarter mutated store: err=%v calls=%d settings=%v", err, store.replaceCalls, store.settings)
			}
		})
	}
}

func TestLegacyQuerySettingsRequireRuntimeBeforeStoreMutation(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	boundary := newRecordingQuerySettingsRestartBoundary(nil)
	service := NewService(store, t.TempDir()).BindRuntimeQueryPublications(boundary)
	service.runtime = nil

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, ErrRuntimeQuerySettingsRestartUnavailable) || store.replaceCalls != 0 || boundary.prepareCalls != 0 {
		t.Fatalf("missing runtime mutated Query settings: err=%v calls=%d boundary=%#v", err, store.replaceCalls, boundary)
	}
}

func TestLifecycleV2QuerySettingsRequireLifecycleCoordinatorBeforeStoreMutation(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	item.Manifest.Lifecycle = &ManifestLifecycle{ContractVersion: item.ID + ".lifecycle@1"}
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	boundary := newRecordingQuerySettingsRestartBoundary(nil)
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, ErrSettingsRestartUnavailable) || store.replaceCalls != 0 ||
		boundary.prepareCalls != 0 || boundary.restartCalls != 0 || store.settings[item.ID]["name"] != "before" {
		t.Fatalf("Lifecycle V2 Query settings did not fail closed: err=%v calls=%d boundary=%#v", err, store.replaceCalls, boundary)
	}
}

func TestLifecycleV2SettingsWithoutQueryRequireLifecycleCoordinatorBeforeStoreMutation(t *testing.T) {
	item := installedExtension("lifecycle-settings.plugin", TypePlugin, ManifestBackend{
		Entry: "backend/plugin", ProtocolVersion: 2,
	})
	item.Status = StatusEnabled
	item.Manifest.Lifecycle = &ManifestLifecycle{ContractVersion: item.ID + ".lifecycle@1"}
	item.Manifest.Settings = []ManifestSetting{{Key: "name", Label: LocalizedText{Default: "Name"}, Type: "text"}}
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	runtime := &fakeRuntimeManager{}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, ErrSettingsRestartUnavailable) || store.replaceCalls != 0 ||
		store.settings[item.ID]["name"] != "before" || len(runtime.started) != 0 || len(runtime.stopped) != 0 {
		t.Fatalf("Lifecycle V2 update crossed fail-closed preflight: err=%v store=%#v runtime=%#v", err, store, runtime)
	}
}

func TestLegacyQuerySettingsRestartFailureRestoresSnapshot(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	events := []string{}
	store := &querySettingsRestartStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	restartErr := errors.New("exact restart failed")
	boundary := newRecordingQuerySettingsRestartBoundary(&events)
	boundary.restartErr = restartErr
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, restartErr) || store.settings[item.ID]["name"] != "before" ||
		!slices.Equal(events, []string{
			"query.settings.prepare", "store.update", "query.settings.restart", "store.update", "query.settings.restore",
		}) || boundary.restoreCalls != 1 || boundary.keepClosedCalls != 0 {
		t.Fatalf("settings compensation: err=%v settings=%v events=%v", err, store.settings, events)
	}
}

func TestLegacyQuerySettingsStoreFailureRestoresPreparedRuntime(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	events := []string{}
	store := &querySettingsRestartStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	store.replaceErrAt = 1
	boundary := newRecordingQuerySettingsRestartBoundary(&events)
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if err == nil || boundary.restartCalls != 0 || boundary.restoreCalls != 1 ||
		!slices.Equal(events, []string{"query.settings.prepare", "store.update", "query.settings.restore"}) {
		t.Fatalf("store failure compensation: err=%v boundary=%#v events=%v", err, boundary, events)
	}
}

func TestLegacyQuerySettingsVerifiesCommittedMutationAfterTransportError(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	events := []string{}
	store := &querySettingsRestartStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		events:             &events,
	}
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	store.replaceAppliedErr = errors.New("commit response lost")
	boundary := newRecordingQuerySettingsRestartBoundary(&events)
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	if _, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN"); err != nil || boundary.restartCalls != 1 || boundary.restoreCalls != 0 || boundary.keepClosedCalls != 0 {
		t.Fatalf("verified committed update: err=%v boundary=%#v", err, boundary)
	}
	if store.settings[item.ID]["name"] != "after" {
		t.Fatalf("verified committed update settings=%v", store.settings)
	}
}

func TestLegacyQuerySettingsUnknownCommitKeepsRuntimeClosed(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	store := &querySettingsRestartStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
	}
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	store.replaceAppliedErr = errors.New("commit response lost")
	store.afterReplace = func() {
		store.settings[item.ID] = map[string]string{"name": "concurrent"}
	}
	boundary := newRecordingQuerySettingsRestartBoundary(nil)
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, ErrSettingsCommitUnknown) || !errors.Is(err, ErrSettingsRollbackFailed) ||
		boundary.restartCalls != 0 || boundary.restoreCalls != 0 || boundary.keepClosedCalls != 1 ||
		store.settings[item.ID]["name"] != "concurrent" {
		t.Fatalf("unknown settings commit: err=%v boundary=%#v settings=%v", err, boundary, store.settings)
	}
}

func TestLegacyQuerySettingsUnknownCommitReadFailureKeepsRuntimeClosed(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	store := &querySettingsRestartStore{
		fakeExtensionStore: newFakeExtensionStore(map[string]Extension{item.ID: item}),
		listErrAt:          3,
		listErr:            errors.New("readback unavailable"),
	}
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	store.replaceAppliedErr = errors.New("commit response lost")
	boundary := newRecordingQuerySettingsRestartBoundary(nil)
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, ErrSettingsCommitUnknown) || !errors.Is(err, ErrSettingsRollbackFailed) ||
		!errors.Is(err, store.listErr) || boundary.restartCalls != 0 || boundary.restoreCalls != 0 ||
		boundary.keepClosedCalls != 1 {
		t.Fatalf("unknown settings readback: err=%v boundary=%#v", err, boundary)
	}
}

func TestLegacyQuerySettingsRollbackFailureKeepsRuntimeClosed(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	store.replaceErrAt = 2
	store.replaceErr = errors.New("database unavailable")
	boundary := newRecordingQuerySettingsRestartBoundary(nil)
	boundary.restartErr = errors.New("exact restart failed")
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, ErrSettingsRollbackFailed) || boundary.restoreCalls != 0 || boundary.keepClosedCalls != 1 {
		t.Fatalf("rollback failure reopened runtime: err=%v boundary=%#v", err, boundary)
	}
}

func TestLegacyQueryRuntimeRestoreFailureIsRollbackFailure(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	boundary := newRecordingQuerySettingsRestartBoundary(nil)
	boundary.restartErr = errors.New("exact restart failed")
	boundary.restoreErr = errors.New("source publish failed")
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, ErrSettingsRollbackFailed) || boundary.restoreCalls != 1 || boundary.keepClosedCalls != 1 {
		t.Fatalf("runtime restore failure lost rollback sentinel: err=%v boundary=%#v", err, boundary)
	}
}

func TestLegacyQuerySettingsSerializeAgainstDisable(t *testing.T) {
	item := querySettingsRestartExtension(t, legacyQueryServiceExtension)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	store.settings = map[string]map[string]string{item.ID: {"name": "before"}}
	boundary := newRecordingQuerySettingsRestartBoundary(nil)
	boundary.restartStarted = make(chan struct{})
	boundary.restartContinue = make(chan struct{})
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{}).BindRuntimeQueryPublications(boundary)

	updateDone := make(chan error, 1)
	go func() {
		_, err := service.UpdateSettings(t.Context(), extensionManager(), item.ID, UpdateSettingsInput{
			Values: map[string]string{"name": "after"},
		}, "zh-CN")
		updateDone <- err
	}()
	<-boundary.restartStarted

	disableStarted := make(chan struct{})
	disableDone := make(chan error, 1)
	go func() {
		close(disableStarted)
		_, err := service.Disable(t.Context(), extensionManager(), item.ID)
		disableDone <- err
	}()
	<-disableStarted
	select {
	case err := <-disableDone:
		t.Fatalf("disable crossed an in-progress settings restart: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(boundary.restartContinue)
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disableDone; err != nil {
		t.Fatal(err)
	}
}

func querySettingsRestartExtension(
	t *testing.T,
	build func(*testing.T, string) Extension,
) Extension {
	t.Helper()
	item := build(t, StatusEnabled)
	item.Manifest.Admin = ManifestAdmin{}
	item.Manifest.AdminPages = nil
	item.Manifest.Routes = nil
	item.Manifest.Hooks = nil
	item.Manifest.Events = nil
	item.Manifest.Jobs = nil
	item.Manifest.Providers = nil
	item.Manifest.Contributions = nil
	item.Manifest.Guards = nil
	item.Manifest.Schedules = nil
	item.Manifest.Components = nil
	item.Manifest.Templates = nil
	item.Manifest.Assets = nil
	item.Manifest.Content = nil
	item.Manifest.Database = nil
	item.Manifest.Cache = nil
	item.Manifest.SEO = nil
	item.Manifest.Services = nil
	item.Manifest.Commands = nil
	item.Manifest.AdminSurfaces = nil
	item.Manifest.Identity = nil
	item.Manifest.Media = nil
	item.Manifest.Navigation = nil
	item.Manifest.Regions = nil
	item.Manifest.OpenAPI = nil
	item.Manifest.Settings = []ManifestSetting{{
		Key: "name", Label: LocalizedText{Default: "Name"}, Type: "text", Default: "recommended",
	}}
	return item
}

type querySettingsRestartStore struct {
	*fakeExtensionStore
	events            *[]string
	replaceAppliedErr error
	resetAppliedErr   error
	afterReplace      func()
	listCalls         int
	listErrAt         int
	listErr           error
}

func (s *querySettingsRestartStore) ListSettings(ctx context.Context, extensionID string) (map[string]string, error) {
	s.listCalls++
	if s.listCalls == s.listErrAt {
		return nil, s.listErr
	}
	return s.fakeExtensionStore.ListSettings(ctx, extensionID)
}

func (s *querySettingsRestartStore) ReplaceSettings(ctx context.Context, extensionID string, values map[string]string) error {
	if s.events != nil {
		*s.events = append(*s.events, "store.update")
	}
	if err := s.fakeExtensionStore.ReplaceSettings(ctx, extensionID, values); err != nil {
		return err
	}
	if s.afterReplace != nil {
		s.afterReplace()
	}
	return s.replaceAppliedErr
}

type recordingQuerySettingsRestartBoundary struct {
	*recordingQueryPublicationBoundary
	events          *[]string
	restartCalls    int
	prepareCalls    int
	prepareErr      error
	restoreCalls    int
	keepClosedCalls int
	restartErr      error
	restoreErr      error
	restartStarted  chan struct{}
	restartContinue chan struct{}
}

func newRecordingQuerySettingsRestartBoundary(events *[]string) *recordingQuerySettingsRestartBoundary {
	return &recordingQuerySettingsRestartBoundary{
		recordingQueryPublicationBoundary: &recordingQueryPublicationBoundary{},
		events:                            events,
	}
}

func (b *recordingQuerySettingsRestartBoundary) PrepareRuntimeQueriesForSettings(
	context.Context,
	Extension,
) (RuntimeQuerySettingsRestartTransaction, error) {
	b.prepareCalls++
	if b.events != nil {
		*b.events = append(*b.events, "query.settings.prepare")
	}
	if b.prepareErr != nil {
		return nil, b.prepareErr
	}
	return b, nil
}

func (b *recordingQuerySettingsRestartBoundary) RestartRuntimeQueriesForSettings(context.Context, Extension) error {
	b.restartCalls++
	if b.events != nil {
		*b.events = append(*b.events, "query.settings.restart")
	}
	if b.restartStarted != nil {
		close(b.restartStarted)
		<-b.restartContinue
	}
	return b.restartErr
}

func (b *recordingQuerySettingsRestartBoundary) RestoreRuntimeQueriesAfterSettingsRollback(context.Context) error {
	b.restoreCalls++
	if b.events != nil {
		*b.events = append(*b.events, "query.settings.restore")
	}
	return b.restoreErr
}

func (b *recordingQuerySettingsRestartBoundary) KeepRuntimeQueriesClosed() error {
	b.keepClosedCalls++
	return nil
}

var _ RuntimeQuerySettingsRestarter = (*recordingQuerySettingsRestartBoundary)(nil)
var _ RuntimeQuerySettingsRestartTransaction = (*recordingQuerySettingsRestartBoundary)(nil)
