package extensions

import (
	"context"
	"errors"
	"testing"
	"time"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestActivationCoordinatorPersistsFailureAndSkipsSameDigest(t *testing.T) {
	extension := installedExtension("crashing.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	extension.Status = StatusEnabled
	extension.PackageDigest = "digest-v1"
	store := &memoryActivationStore{}
	coordinator := NewActivationCoordinator(store)
	runtime := &activationRuntime{startErr: errors.New("plugin crashed during startup")}

	if err := coordinator.Start(context.Background(), runtime, extension, ActivationTriggerStartup, 0, "boot-1"); err == nil {
		t.Fatal("expected startup failure")
	}
	if store.latest.Status != ActivationStatusFailed || store.latest.FailureReason == "" {
		t.Fatalf("failed attempt=%#v", store.latest)
	}
	skip, err := coordinator.ShouldSkipStartup(context.Background(), extension, "boot-2")
	if err != nil || !skip {
		t.Fatalf("same digest skip=%v err=%v", skip, err)
	}
	if store.latest.Status != ActivationStatusSkipped || runtime.starts != 1 {
		t.Fatalf("skipped attempt=%#v starts=%d", store.latest, runtime.starts)
	}

	changed := extension
	changed.PackageDigest = "digest-v2"
	skip, err = coordinator.ShouldSkipStartup(context.Background(), changed, "boot-3")
	if err != nil || skip {
		t.Fatalf("changed digest skip=%v err=%v", skip, err)
	}
}

func TestActivationCoordinatorManualStartRetriesAfterSkip(t *testing.T) {
	extension := installedExtension("recovered.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	extension.Status = StatusEnabled
	extension.PackageDigest = "digest-v1"
	store := &memoryActivationStore{latest: ActivationAttempt{
		ID: 1, ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
		Status: ActivationStatusSkipped,
	}}
	coordinator := NewActivationCoordinator(store)
	runtime := &activationRuntime{}
	if err := coordinator.Start(context.Background(), runtime, extension, ActivationTriggerEnable, 42, "manual"); err != nil {
		t.Fatal(err)
	}
	if runtime.starts != 1 || store.latest.Status != ActivationStatusHealthy || store.latest.ActorUserID != 42 {
		t.Fatalf("manual recovery attempt=%#v starts=%d", store.latest, runtime.starts)
	}
}

func TestServiceEnableRecordsActivationAttempt(t *testing.T) {
	extension := exactTrustExtension(t, "tracked.enable")
	store := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	attempts := &memoryActivationStore{}
	runtime := &activationRuntime{}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", runtime,
		WithActivationCoordinator(NewActivationCoordinator(attempts)),
	)
	if _, err := service.Enable(context.Background(), extensionManager(), extension.ID, EnableInput{ConfirmCapabilities: true}); err != nil {
		t.Fatal(err)
	}
	if attempts.latest.Status != ActivationStatusHealthy || attempts.latest.Trigger != ActivationTriggerEnable || attempts.latest.ActorUserID != extensionManager().ID {
		t.Fatalf("activation attempt=%#v", attempts.latest)
	}
}

type memoryActivationStore struct {
	latest ActivationAttempt
	nextID int64
}

func (s *memoryActivationStore) LatestActivationAttempt(_ context.Context, extensionID, packageDigest string) (ActivationAttempt, error) {
	if s.latest.ID == 0 || s.latest.ExtensionID != extensionID || s.latest.PackageDigest != packageDigest {
		return ActivationAttempt{}, ErrActivationAttemptNotFound
	}
	return s.latest, nil
}

func (s *memoryActivationStore) BeginActivationAttempt(_ context.Context, extension Extension, trigger, bootID string, actorUserID int64) (ActivationAttempt, error) {
	s.nextID++
	if s.nextID == 0 {
		s.nextID = 1
	}
	s.latest = ActivationAttempt{
		ID: s.nextID, ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, Trigger: trigger, BootID: bootID,
		Status: ActivationStatusStarting, ActorUserID: actorUserID, StartedAt: time.Now(),
	}
	return s.latest, nil
}

func (s *memoryActivationStore) CompleteActivationAttempt(_ context.Context, attemptID int64, status, reason string) error {
	if s.latest.ID != attemptID {
		return ErrActivationAttemptNotFound
	}
	now := time.Now()
	s.latest.Status = status
	s.latest.FailureReason = reason
	s.latest.CompletedAt = &now
	return nil
}

func (s *memoryActivationStore) RecordSkippedActivation(_ context.Context, extension Extension, bootID, reason string) error {
	s.nextID++
	if s.nextID == 0 {
		s.nextID = 1
	}
	now := time.Now()
	s.latest = ActivationAttempt{
		ID: s.nextID, ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, BootID: bootID,
		Trigger: ActivationTriggerStartup, Status: ActivationStatusSkipped,
		FailureReason: reason, StartedAt: now, CompletedAt: &now,
	}
	return nil
}

type activationRuntime struct {
	starts   int
	stops    int
	startErr error
	startCtx context.Context
}

func (*activationRuntime) Check(context.Context, Extension) error { return nil }
func (r *activationRuntime) Start(ctx context.Context, _ Extension) error {
	r.starts++
	r.startCtx = ctx
	return r.startErr
}
func (r *activationRuntime) Stop(context.Context, Extension) error {
	r.stops++
	return nil
}
func (*activationRuntime) Status(context.Context, Extension) RuntimeStatus {
	return RuntimeStatus{State: RuntimeRunning}
}
func (*activationRuntime) EmitHook(context.Context, string, map[string]any) {}

// countingAuditWriter 记录写过的审计事件并返回稳定 id，用来观察
// ActivationCoordinator 是否为「操作者发起的启用」写了授权锚点。
type countingAuditWriter struct {
	events []audit.Event
	fail   error
	nextID int64
}

func (w *countingAuditWriter) Append(ctx context.Context, event audit.Event) error {
	_, err := w.AppendReturningID(ctx, event)
	return err
}

func (w *countingAuditWriter) AppendReturningID(_ context.Context, event audit.Event) (int64, error) {
	if w.fail != nil {
		return 0, w.fail
	}
	w.events = append(w.events, event)
	w.nextID++
	return w.nextID, nil
}

// databaseGrantExtension 构造一个声明了 own_schema 的启用态插件。
func databaseGrantExtension(t *testing.T, id string) Extension {
	t.Helper()
	extension := installedExtension(id, TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	extension.Status = StatusEnabled
	extension.PackageDigest = "digest-v1"
	extension.Manifest.Database = &extensionmanifest.ManifestDatabase{
		ContractVersion: "sforum.database@1",
		Grants:          []string{extensionmanifest.DatabaseGrantOwnSchema},
	}
	return extension
}

func TestActivationStartStampsOperatorDatabaseGrantAuthority(t *testing.T) {
	extension := databaseGrantExtension(t, "grant.plugin")
	store := &memoryActivationStore{}
	auditor := &countingAuditWriter{}
	coordinator := NewActivationCoordinator(store).WithAuditor(auditor)
	runtime := &activationRuntime{}

	if err := coordinator.Start(context.Background(), runtime, extension, ActivationTriggerEnable, 42, "boot-grant"); err != nil {
		t.Fatal(err)
	}
	if len(auditor.events) != 1 || auditor.events[0].Action != audit.ActionExtensionDatabaseGrant {
		t.Fatalf("activation must write one database grant anchor: %#v", auditor.events)
	}
	if auditor.events[0].ActorUserID != 42 {
		t.Fatalf("anchor actor=%d want 42", auditor.events[0].ActorUserID)
	}
	authority, ok := DatabaseGrantAuthorityFrom(runtime.startCtx)
	if !ok || authority.ActorUserID != 42 || authority.AuditEventID != 1 {
		t.Fatalf("runtime start context authority=%#v ok=%v", authority, ok)
	}
}

func TestActivationStartWithoutOperatorKeepsHostAuthority(t *testing.T) {
	extension := databaseGrantExtension(t, "host-start.plugin")
	auditor := &countingAuditWriter{}
	coordinator := NewActivationCoordinator(&memoryActivationStore{}).WithAuditor(auditor)
	runtime := &activationRuntime{}

	// 启动期 Reconcile 的 actorUserID 是 0：没有操作者就没有可归属的授权锚点，
	// 租约层因此保持 host 权威，缺精确授权即失败关闭。
	if err := coordinator.Start(context.Background(), runtime, extension, ActivationTriggerStartup, 0, "boot-host"); err != nil {
		t.Fatal(err)
	}
	if len(auditor.events) != 0 {
		t.Fatalf("host start must not write an ownership anchor: %#v", auditor.events)
	}
	if _, ok := DatabaseGrantAuthorityFrom(runtime.startCtx); ok {
		t.Fatal("host start must not carry an operator grant authority")
	}
}

func TestActivationStartSkipsAuthorityForExtensionsWithoutDatabasePowers(t *testing.T) {
	extension := installedExtension("no-db.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	extension.Status = StatusEnabled
	extension.PackageDigest = "digest-v1"
	auditor := &countingAuditWriter{}
	coordinator := NewActivationCoordinator(&memoryActivationStore{}).WithAuditor(auditor)
	runtime := &activationRuntime{}

	if err := coordinator.Start(context.Background(), runtime, extension, ActivationTriggerEnable, 42, "boot-plain"); err != nil {
		t.Fatal(err)
	}
	if len(auditor.events) != 0 {
		t.Fatalf("no database powers means no anchor: %#v", auditor.events)
	}
}

func TestActivationStartFailsClosedWhenAnchorCannotBeWritten(t *testing.T) {
	extension := databaseGrantExtension(t, "audit-down.plugin")
	auditor := &countingAuditWriter{fail: errors.New("audit store unavailable")}
	coordinator := NewActivationCoordinator(&memoryActivationStore{}).WithAuditor(auditor)
	runtime := &activationRuntime{}

	// 拿不到审计锚点就不能自建授权行；启动仍然继续，但退回 host 权威，
	// 于是缺少既有授权时会失败关闭——绝不能静默造出无锚点的授权。
	if err := coordinator.Start(context.Background(), runtime, extension, ActivationTriggerEnable, 42, "boot-down"); err != nil {
		t.Fatal(err)
	}
	if _, ok := DatabaseGrantAuthorityFrom(runtime.startCtx); ok {
		t.Fatal("an unwritable audit anchor must not produce an authority")
	}
}

func TestDatabaseGrantAuthorityRequiresBothIdentities(t *testing.T) {
	if ctx := WithDatabaseGrantAuthority(context.Background(), DatabaseGrantAuthority{ActorUserID: 1}); ctx != context.Background() {
		t.Fatal("missing audit event id must not write an authority")
	}
	if ctx := WithDatabaseGrantAuthority(nil, DatabaseGrantAuthority{ActorUserID: 1, AuditEventID: 2}); ctx != nil {
		t.Fatal("nil context must stay nil")
	}
	ctx := WithDatabaseGrantAuthority(context.Background(), DatabaseGrantAuthority{ActorUserID: 7, AuditEventID: 9})
	authority, ok := DatabaseGrantAuthorityFrom(ctx)
	if !ok || authority.ActorUserID != 7 || authority.AuditEventID != 9 {
		t.Fatalf("authority round trip=%#v ok=%v", authority, ok)
	}
	if _, ok := DatabaseGrantAuthorityFrom(context.Background()); ok {
		t.Fatal("plain context must not resolve an authority")
	}
}

func TestRequiresDatabaseGrantAuthorityOnlyForDirectPowers(t *testing.T) {
	cases := []struct {
		name     string
		manifest *extensionmanifest.ManifestDatabase
		want     bool
	}{
		{name: "none"},
		{
			name:     "host commands only",
			manifest: &extensionmanifest.ManifestDatabase{Grants: []string{extensionmanifest.DatabaseGrantHostCommands}},
		},
		{
			name:     "own schema",
			manifest: &extensionmanifest.ManifestDatabase{Grants: []string{extensionmanifest.DatabaseGrantOwnSchema}},
			want:     true,
		},
		{
			name:     "legacy raw core authority",
			manifest: &extensionmanifest.ManifestDatabase{Authority: extensionmanifest.DatabaseGrantRawCore},
			want:     true,
		},
	}
	for _, item := range cases {
		extension := installedExtension("probe.plugin", TypePlugin, ManifestBackend{})
		extension.Manifest.Database = item.manifest
		if got := RequiresDatabaseGrantAuthority(extension); got != item.want {
			t.Fatalf("%s: RequiresDatabaseGrantAuthority=%v want %v", item.name, got, item.want)
		}
	}
}
