package builtinauto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	"github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions/systemactor"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	extensionpackage "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionPackage"
)

type fakeInventory struct {
	items    []extensions.Extension
	getItems map[string]extensions.Extension
	events   []extensions.ExtensionEvent
}

func (f *fakeInventory) List(context.Context) ([]extensions.Extension, error) {
	return f.items, nil
}

func (f *fakeInventory) Get(_ context.Context, id string) (extensions.Extension, error) {
	if item, ok := f.getItems[id]; ok {
		return item, nil
	}
	for _, item := range f.items {
		if item.ID == id {
			return item, nil
		}
	}
	return extensions.Extension{}, extensions.ErrExtensionNotFound
}

func (f *fakeInventory) CreateEvent(_ context.Context, input extensions.EventInput) (extensions.ExtensionEvent, error) {
	event := extensions.ExtensionEvent{
		ID: int64(len(f.events) + 1), ExtensionID: input.ExtensionID,
		ActorUserID: input.ActorUserID, Action: input.Action, Message: input.Message,
	}
	f.events = append(f.events, event)
	return event, nil
}

type fakePolicy struct {
	enabled bool
	err     error
}

func (p fakePolicy) BuiltinAutoUpdateEnabled(context.Context) (bool, error) { return p.enabled, p.err }

type fakeActors struct {
	actor identity.Actor
	err   error
}

func (a fakeActors) EnsureBuiltinAutoActor(context.Context) (identity.Actor, error) {
	return a.actor, a.err
}

type fakeProbe struct {
	selected map[string]bool
	err      error
	calls    []string
}

func (p *fakeProbe) ProviderSelected(_ context.Context, extensionID string) (bool, error) {
	p.calls = append(p.calls, extensionID)
	if p.err != nil {
		return false, p.err
	}
	return p.selected[extensionID], nil
}

type fakePromoter struct {
	upgraded     []string
	restarted    []string
	upgradeKeys  []string
	restartInput []extensions.RestartInput
	upgradeErr   map[string]error
	restartErr   map[string]error
}

func (p *fakePromoter) Upgrade(
	_ context.Context, _ identity.Actor, id string, input extensions.UpgradeInput,
) (extensions.Extension, error) {
	if err := p.upgradeErr[id]; err != nil {
		return extensions.Extension{}, err
	}
	p.upgraded = append(p.upgraded, id)
	p.upgradeKeys = append(p.upgradeKeys, input.IdempotencyKey)
	return extensions.Extension{ID: id, Status: extensions.StatusEnabled}, nil
}

func (p *fakePromoter) Restart(
	_ context.Context, _ identity.Actor, id string, input extensions.RestartInput,
) (extensions.Extension, error) {
	if err := p.restartErr[id]; err != nil {
		return extensions.Extension{}, err
	}
	p.restarted = append(p.restarted, id)
	p.restartInput = append(p.restartInput, input)
	return extensions.Extension{ID: id, Status: extensions.StatusEnabled}, nil
}

func newTestUpdater(items []extensions.Extension) (*Updater, *fakeInventory, *fakePromoter, *fakeProbe) {
	inventory := &fakeInventory{items: items, getItems: map[string]extensions.Extension{}}
	promoter := &fakePromoter{}
	probe := &fakeProbe{selected: map[string]bool{}}
	updater := New(
		inventory, promoter, fakePolicy{enabled: true},
		fakeActors{actor: identity.Actor{ID: 77, Status: identity.UserStatusActive}},
		probe, slog.New(slog.NewTextHandler(io.Discard, nil)), false, false,
	)
	return updater, inventory, promoter, probe
}

func TestUpdaterRestartsLegacyBuiltin(t *testing.T) {
	item := testExtension(t, "builtin.demo", "1.0.0", "1.1.0", false)
	updater, inventory, promoter, _ := newTestUpdater([]extensions.Extension{item})

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Updated) != 1 || summary.Updated[0] != item.ID {
		t.Fatalf("summary = %#v", summary)
	}
	if len(promoter.restarted) != 1 || promoter.restarted[0] != item.ID {
		t.Fatalf("restarted = %#v", promoter.restarted)
	}
	if len(promoter.upgraded) != 0 {
		t.Fatalf("legacy plugin must use restart, got %#v", promoter.upgraded)
	}
	if !promoter.restartInput[0].ConfirmCapabilities {
		t.Fatal("builtin restart must confirm host-owned capabilities")
	}
	if want := IdempotencyKey(item.ID, item.StagedVersion.PackageDigest); promoter.restartInput[0].IdempotencyKey != want {
		t.Fatalf("key = %q, want %q", promoter.restartInput[0].IdempotencyKey, want)
	}
	if !hasEvent(inventory, extensions.EventBuiltinAutoUpdated) {
		t.Fatalf("missing updated event: %#v", inventory.events)
	}
}

func TestUpdaterUsesUpgradeForLifecycleV2(t *testing.T) {
	item := testExtension(t, "builtin.v2", "1.0.0", "1.1.0", true)
	updater, _, promoter, probe := newTestUpdater([]extensions.Extension{item})
	probe.selected[item.ID] = true

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Updated) != 1 || len(promoter.upgraded) != 1 {
		t.Fatalf("summary=%#v upgraded=%#v", summary, promoter.upgraded)
	}
	if len(promoter.restarted) != 0 {
		t.Fatalf("V2 plugin must use upgrade, got %#v", promoter.restarted)
	}
	if len(probe.calls) != 0 {
		t.Fatalf("V2 upgrade must not probe provider selections: %#v", probe.calls)
	}
}

func TestUpdaterSkipsDisabledInstalledAndUploaded(t *testing.T) {
	enabled := testExtension(t, "builtin.enabled", "1.0.0", "1.1.0", false)
	disabled := testExtension(t, "builtin.disabled", "1.0.0", "1.1.0", false)
	disabled.Status = extensions.StatusDisabled
	installed := testExtension(t, "builtin.installed", "1.0.0", "1.1.0", false)
	installed.Status = extensions.StatusInstalled
	uploaded := testExtension(t, "uploaded.demo", "1.0.0", "1.1.0", false)
	uploaded.Source = extensions.SourceUploaded
	uploaded.IsSystem = false
	updater, _, promoter, _ := newTestUpdater([]extensions.Extension{enabled, disabled, installed, uploaded})

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Updated) != 1 || summary.Updated[0] != enabled.ID {
		t.Fatalf("summary = %#v", summary)
	}
	if len(promoter.restarted) != 1 || promoter.restarted[0] != enabled.ID {
		t.Fatalf("restarted = %#v", promoter.restarted)
	}
}

func TestUpdaterSkipsLegacyProviderInUse(t *testing.T) {
	item := testExtension(t, "builtin.smtp", "1.0.0", "1.1.0", false)
	updater, inventory, promoter, probe := newTestUpdater([]extensions.Extension{item})
	probe.selected[item.ID] = true

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 1 || len(summary.Updated) != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(promoter.restarted) != 0 || len(promoter.upgraded) != 0 {
		t.Fatalf("provider in use must not run: %#v", promoter)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "provider_in_use") {
		t.Fatalf("missing provider_in_use event: %#v", inventory.events)
	}
}

func TestUpdaterSkipsDowngradeAndSameVersion(t *testing.T) {
	downgrade := testExtension(t, "builtin.downgrade", "2.0.0", "1.0.0", false)
	sameVersion := testExtension(t, "builtin.same", "2.0.0", "2.0.0", false)
	updater, inventory, promoter, _ := newTestUpdater([]extensions.Extension{downgrade, sameVersion})

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 2 || len(summary.Updated) != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(promoter.restarted) != 0 || len(promoter.upgraded) != 0 {
		t.Fatalf("non-forward versions must not run: %#v", promoter)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "downgrade") {
		t.Fatalf("missing downgrade event: %#v", inventory.events)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "same_version") {
		t.Fatalf("missing same-version event: %#v", inventory.events)
	}
}

func TestUpdaterRespectsPolicySafeModeAndRecovery(t *testing.T) {
	item := testExtension(t, "builtin.demo", "1.0.0", "1.1.0", false)
	for _, tc := range []struct {
		name     string
		policy   Policy
		safe     bool
		recovery bool
	}{
		{name: "policy off", policy: fakePolicy{enabled: false}},
		{name: "safe mode", policy: fakePolicy{enabled: true}, safe: true},
		{name: "recovery", policy: fakePolicy{enabled: true}, recovery: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inventory := &fakeInventory{items: []extensions.Extension{item}}
			promoter := &fakePromoter{}
			updater := New(
				inventory, promoter, tc.policy,
				fakeActors{actor: identity.Actor{ID: 77, Status: identity.UserStatusActive}},
				&fakeProbe{}, slog.New(slog.NewTextHandler(io.Discard, nil)), tc.safe, tc.recovery,
			)
			summary, err := updater.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !summary.Empty() || len(promoter.restarted) != 0 || len(promoter.upgraded) != 0 {
				t.Fatalf("expected no-op, summary=%#v promoter=%#v", summary, promoter)
			}
		})
	}
}

func TestUpdaterDefersWhenNoUserExists(t *testing.T) {
	item := testExtension(t, "builtin.demo", "1.0.0", "1.1.0", false)
	inventory := &fakeInventory{items: []extensions.Extension{item}}
	promoter := &fakePromoter{}
	updater := New(
		inventory, promoter, fakePolicy{enabled: true},
		fakeActors{err: systemactor.ErrNoUsers},
		&fakeProbe{}, slog.New(slog.NewTextHandler(io.Discard, nil)), false, false,
	)
	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatalf("no-user deferral must not be an error: %v", err)
	}
	if !summary.Empty() || len(promoter.restarted) != 0 {
		t.Fatalf("expected deferral, summary=%#v", summary)
	}
}

func TestUpdaterRecordsFailureAndContinues(t *testing.T) {
	first := testExtension(t, "builtin.fail", "1.0.0", "1.1.0", false)
	second := testExtension(t, "builtin.ok", "1.0.0", "1.1.0", false)
	updater, inventory, promoter, _ := newTestUpdater([]extensions.Extension{first, second})
	promoter.restartErr = map[string]error{first.ID: errors.New("runtime failed")}

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Failed) != 1 || summary.Failed[0] != first.ID {
		t.Fatalf("summary = %#v", summary)
	}
	if len(summary.Updated) != 1 || summary.Updated[0] != second.ID {
		t.Fatalf("summary = %#v", summary)
	}
	if !hasEvent(inventory, extensions.EventBuiltinAutoUpdateFailed) {
		t.Fatalf("missing failed event: %#v", inventory.events)
	}
}

func TestUpdaterSkipsOperationInProgress(t *testing.T) {
	item := testExtension(t, "builtin.demo", "1.0.0", "1.1.0", false)
	updater, inventory, promoter, _ := newTestUpdater([]extensions.Extension{item})
	promoter.restartErr = map[string]error{item.ID: extensions.ErrLifecycleOperationInProgress}

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 1 || len(summary.Failed) != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "operation_in_progress") {
		t.Fatalf("missing operation_in_progress event: %#v", inventory.events)
	}
}

func TestIdempotencyKeyIsDeterministic(t *testing.T) {
	first := IdempotencyKey("builtin.demo", "abc")
	second := IdempotencyKey("builtin.demo", "abc")
	if first != second || !strings.HasPrefix(first, IdempotencyKeyPrefix) {
		t.Fatalf("keys = %q %q", first, second)
	}
}

func testExtension(t *testing.T, id, activeVersion, stagedVersion string, lifecycleV2 bool) extensions.Extension {
	t.Helper()
	manifest := extensions.Manifest{
		ManifestVersion: 3,
		ID:              id,
		Name:            "Builtin Auto Test",
		Description:     "Builtin auto-update unit fixture.",
		URL:             "https://example.com",
		Author:          extensions.ManifestAuthor{Name: "SForum"},
		Version:         activeVersion,
		Type:            extensions.TypePlugin,
		SForumVersion:   ">=0.1.0",
		Backend: extensions.ManifestBackend{
			Entry: "backend/plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2", RPC: "hashicorp-go-plugin",
		},
	}
	if lifecycleV2 {
		manifest.Lifecycle = &extensions.ManifestLifecycle{ContractVersion: id + ".lifecycle@1"}
	}
	activePath, activeDigest, activeManifest := writeTestArtifact(t, manifest)
	stagedManifest := manifest
	stagedManifest.Version = stagedVersion
	stagedPath, stagedDigest, stagedManifest := writeTestArtifact(t, stagedManifest)
	return extensions.Extension{
		ID: id, Name: activeManifest.Name, Version: activeVersion, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceBuiltin, IsSystem: true,
		Manifest: activeManifest, PackagePath: activePath, PackageDigest: activeDigest,
		ActiveVersionID: 1,
		StagedVersion: &extensions.ExtensionVersion{
			ID: 2, Version: stagedVersion, Manifest: stagedManifest,
			PackageDigest: stagedDigest, PackagePath: stagedPath,
		},
	}
}

func writeTestArtifact(t *testing.T, manifest extensions.Manifest) (string, string, extensions.Manifest) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("plugin")
	if err := os.WriteFile(filepath.Join(root, "backend", "plugin"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	manifest.Backend.Digest = digest
	manifest.PackageFiles = []extensions.ManifestPackageFile{{
		ID: manifest.ID + ".file.backend", Kind: "executable", Path: "backend/plugin", Digest: digest,
	}}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, extensions.ManifestFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	packageDigest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, packageDigest, manifest
}

func hasEvent(inventory *fakeInventory, action string) bool {
	for _, event := range inventory.events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func hasEventContaining(inventory *fakeInventory, action, fragment string) bool {
	for _, event := range inventory.events {
		if event.Action == action && strings.Contains(event.Message, fragment) {
			return true
		}
	}
	return false
}

func TestUpdaterSkipsSameVersionDigestDrift(t *testing.T) {
	item := testExtension(t, "builtin.drift", "2.0.0", "2.0.0", false)
	item.StagedVersion.Manifest.Description = "changed content with the same version"
	body, err := json.Marshal(item.StagedVersion.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(item.StagedVersion.PackagePath, extensions.ManifestFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(item.StagedVersion.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	item.StagedVersion.PackageDigest = digest
	updater, inventory, promoter, _ := newTestUpdater([]extensions.Extension{item})

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 1 || len(promoter.restarted) != 0 {
		t.Fatalf("summary=%#v promoter=%#v", summary, promoter)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "same_version_digest_drift") {
		t.Fatalf("missing same_version_digest_drift event: %#v", inventory.events)
	}
}

func TestUpdaterSkipsWhenProviderProbeFails(t *testing.T) {
	item := testExtension(t, "builtin.probe", "1.0.0", "1.1.0", false)
	updater, inventory, promoter, probe := newTestUpdater([]extensions.Extension{item})
	probe.err = errors.New("probe unavailable")

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 1 || len(promoter.restarted) != 0 {
		t.Fatalf("summary=%#v promoter=%#v", summary, promoter)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "provider_probe_failed") {
		t.Fatalf("missing provider_probe_failed event: %#v", inventory.events)
	}
}

func TestUpdaterSkipsUnusableStagedArtifact(t *testing.T) {
	item := testExtension(t, "builtin.missing", "1.0.0", "1.1.0", false)
	if err := os.RemoveAll(item.StagedVersion.PackagePath); err != nil {
		t.Fatal(err)
	}
	updater, inventory, promoter, _ := newTestUpdater([]extensions.Extension{item})

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 1 || len(promoter.restarted) != 0 {
		t.Fatalf("summary=%#v promoter=%#v", summary, promoter)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "artifact_unusable") {
		t.Fatalf("missing artifact_unusable event: %#v", inventory.events)
	}
}

func TestUpdaterSkipsWhenPluginWasDisabledAfterList(t *testing.T) {
	item := testExtension(t, "builtin.race", "1.0.0", "1.1.0", false)
	updater, inventory, promoter, _ := newTestUpdater([]extensions.Extension{item})
	disabled := item
	disabled.Status = extensions.StatusDisabled
	inventory.getItems[item.ID] = disabled

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 1 || len(promoter.restarted) != 0 {
		t.Fatalf("summary=%#v promoter=%#v", summary, promoter)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "state_changed") {
		t.Fatalf("missing state_changed event: %#v", inventory.events)
	}
}

func TestUpdaterSkipsWhenActiveChangedToNewerVersionAfterList(t *testing.T) {
	item := testExtension(t, "builtin.stale", "1.0.0", "1.1.0", false)
	updater, inventory, promoter, _ := newTestUpdater([]extensions.Extension{item})
	stale := item
	stale.Version = "2.0.0"
	stale.Manifest.Version = "2.0.0"
	inventory.getItems[item.ID] = stale

	summary, err := updater.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 1 || len(promoter.restarted) != 0 {
		t.Fatalf("summary=%#v promoter=%#v", summary, promoter)
	}
	if !hasEventContaining(inventory, extensions.EventBuiltinAutoUpdateSkipped, "state_changed") {
		t.Fatalf("missing state_changed event: %#v", inventory.events)
	}
}
