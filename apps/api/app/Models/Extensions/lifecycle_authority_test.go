package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestConfirmLifecycleAuthorityPersistsExactLiveGrant(t *testing.T) {
	extension := lifecycleAuthorityTestExtension(t, "authority.plugin", SourceUploaded)
	extensions := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	trustStore := &memoryExecutableTrustStore{}
	service := NewExecutableTrustService(extensions, trustStore)
	actor := extensionManager()
	challenge, err := service.Challenge(context.Background(), actor, extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := service.ConfirmLifecycleAuthority(context.Background(), actor, extension, challenge.Token)
	if err != nil {
		t.Fatal(err)
	}
	if authority.SchemaVersion != LifecycleAuthoritySnapshotSchemaV1 ||
		authority.AuthorityType != LifecycleAuthorityTrustGrant || authority.ActorUserID != actor.ID ||
		authority.Grant == nil || authority.Grant.ID <= 0 || authority.Grant.ImpactDigest != authority.Impact.Digest ||
		authority.Impact.ArtifactDigests["package"] != extension.PackageDigest {
		t.Fatalf("authority = %#v", authority)
	}
	replayed, err := service.ConfirmLifecycleAuthority(context.Background(), techAdminPluginManager(), extension, "")
	if err != nil || replayed.Grant == nil || replayed.Grant.ID != authority.Grant.ID {
		t.Fatalf("delegated live-grant replay = %#v, %v", replayed, err)
	}
}

func TestBuildLifecycleCoordinatorRunInputCanonicalizesAndBindsIntent(t *testing.T) {
	extension := lifecycleAuthorityTestExtension(t, "fingerprint.plugin", SourceUploaded)
	authority := lifecycleAuthorityTestGrant(t, extension)
	actor := extensionManager()
	intent := LifecycleOperationIntent{
		Operation: LifecycleMachineEnable, IdempotencyKey: "enable-request-1",
		ActionInputs: map[LifecycleMachineAction]json.RawMessage{
			LifecycleMachineEnableAction: json.RawMessage(`{ "z": 2, "a": 1 }`),
		},
	}
	first, err := BuildLifecycleCoordinatorRunInput(extension, actor, authority, intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.ActionInputs[LifecycleMachineEnableAction] = json.RawMessage(`{"a":1,"z":2}`)
	second, err := BuildLifecycleCoordinatorRunInput(extension, actor, authority, intent)
	if err != nil {
		t.Fatal(err)
	}
	if first.Acquire.RequestFingerprint != second.Acquire.RequestFingerprint ||
		first.Acquire.TrustGrantID != authority.Grant.ID || first.Acquire.AuthorityType != LifecycleAuthorityTrustGrant ||
		len(first.Acquire.RequestFingerprint) != 64 || string(first.ActionInputs[LifecycleMachineEnableAction]) != `{"a":1,"z":2}` {
		t.Fatalf("canonical inputs first=%#v second=%#v", first, second)
	}

	changed := intent
	changed.ActionInputs = map[LifecycleMachineAction]json.RawMessage{
		LifecycleMachineEnableAction: json.RawMessage(`{"a":1,"z":3}`),
	}
	changedInput, err := BuildLifecycleCoordinatorRunInput(extension, actor, authority, changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedInput.Acquire.RequestFingerprint == first.Acquire.RequestFingerprint {
		t.Fatal("changed action input reused request fingerprint")
	}

	otherActor := actor
	otherActor.ID++
	otherAuthority := authority
	otherAuthority.ActorUserID = otherActor.ID
	otherInput, err := BuildLifecycleCoordinatorRunInput(extension, otherActor, otherAuthority, intent)
	if err != nil {
		t.Fatal(err)
	}
	if otherInput.Acquire.RequestFingerprint == first.Acquire.RequestFingerprint {
		t.Fatal("changed actor reused request fingerprint")
	}

	recoveryActor := actor
	recoveryActor.ID = 99
	recoveryIntent := intent
	recoveryIntent.Retry = true
	recoveryIntent.AuditEventID = 9002
	recovery, err := BuildLifecycleCoordinatorRunInput(extension, recoveryActor, authority, recoveryIntent)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Acquire.RequestFingerprint != first.Acquire.RequestFingerprint ||
		!recovery.Acquire.ExistingOnly || recovery.Acquire.RequestedByUserID != authority.ActorUserID ||
		recovery.Acquire.AuditEventID != 0 || recovery.RecoveryActorUserID != recoveryActor.ID ||
		recovery.RecoveryAuditEventID != 9002 ||
		string(recovery.Acquire.AuthoritySnapshot) != string(first.Acquire.AuthoritySnapshot) {
		t.Fatalf("recovery authority split = %#v", recovery)
	}
}

func TestBuildLifecycleCoordinatorRunInputAllowsCrossContractUpgradeSource(t *testing.T) {
	target := lifecycleAuthorityTestExtension(t, "cross-contract.plugin", SourceUploaded)
	target.ActiveVersionID = 12
	authority := lifecycleAuthorityTestGrant(t, target)
	source := target
	source.Version = "0.9.0"
	source.Manifest.Version = source.Version
	source.Manifest.Lifecycle = &ManifestLifecycle{ContractVersion: "cross-contract.plugin.lifecycle@0"}
	source.PackageDigest = strings.Repeat("b", 64)
	source.ActiveVersionID = 11

	input, err := BuildLifecycleCoordinatorRunInput(target, extensionManager(), authority, LifecycleOperationIntent{
		Operation: LifecycleMachineUpgrade, IdempotencyKey: "cross-contract-upgrade-1", SourceExtension: &source,
	})
	if err != nil || input.SourceExtension == nil ||
		input.SourceExtension.Manifest.Lifecycle.ContractVersion != "cross-contract.plugin.lifecycle@0" {
		t.Fatalf("cross-contract upgrade input = %#v, %v", input, err)
	}
}

func TestBuildLifecycleCoordinatorRunInputAllowsFrozenUninstallAuthority(t *testing.T) {
	extension := lifecycleAuthorityTestExtension(t, "frozen-uninstall.plugin", SourceUploaded)
	authority := lifecycleAuthorityTestGrant(t, extension)
	actor := techAdminPluginManager()
	actor.ID = authority.ActorUserID + 100

	input, err := BuildLifecycleCoordinatorRunInput(extension, actor, authority, LifecycleOperationIntent{
		Operation: LifecycleMachineUninstall, IdempotencyKey: "frozen-uninstall-1",
		SourceExtension: exactLifecycleCopy(extension), RemovalMode: LifecycleRemovalPreserve,
		FrozenAuthority: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.Acquire.RequestedByUserID != actor.ID || input.Acquire.RemovalMode != LifecycleRemovalPreserve ||
		input.SourceExtension == nil || !sameLifecycleExactArtifact(*input.SourceExtension, extension) ||
		lifecycleOperationAuthorityActorUserID(LifecycleOperation{
			AuthoritySnapshot: input.Acquire.AuthoritySnapshot,
			RequestedByUserID: input.Acquire.RequestedByUserID,
		}) != authority.ActorUserID {
		t.Fatalf("frozen uninstall input = %#v", input)
	}
}

func TestLifecycleAuthorityRejectsMismatchedOrUnsafeIntent(t *testing.T) {
	extension := lifecycleAuthorityTestExtension(t, "invalid-authority.plugin", SourceUploaded)
	extension.ActiveVersionID = 12
	authority := lifecycleAuthorityTestGrant(t, extension)
	actor := extensionManager()
	base := LifecycleOperationIntent{Operation: LifecycleMachineEnable, IdempotencyKey: "enable-1"}
	exactSource := extension
	historicalSource := extension
	historicalSource.Version = "0.9.0"
	historicalSource.Manifest.Version = historicalSource.Version
	historicalSource.PackageDigest = strings.Repeat("b", 64)
	historicalSource.ActiveVersionID = 11

	tests := []struct {
		name      string
		extension Extension
		authority LifecycleAuthoritySnapshot
		intent    LifecycleOperationIntent
	}{
		{name: "spaced key", extension: extension, authority: authority, intent: LifecycleOperationIntent{Operation: LifecycleMachineEnable, IdempotencyKey: " enable-1"}},
		{name: "foreign actor", extension: extension, authority: func() LifecycleAuthoritySnapshot { value := authority; value.ActorUserID++; return value }(), intent: base},
		{name: "foreign digest", extension: func() Extension { value := extension; value.PackageDigest = strings.Repeat("f", 64); return value }(), authority: authority, intent: base},
		{name: "unknown action input", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineEnable, IdempotencyKey: "enable-1",
			ActionInputs: map[LifecycleMachineAction]json.RawMessage{LifecycleMachineDisableAction: json.RawMessage(`{}`)},
		}},
		{name: "non object input", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineEnable, IdempotencyKey: "enable-1",
			ActionInputs: map[LifecycleMachineAction]json.RawMessage{LifecycleMachineEnableAction: json.RawMessage(`[]`)},
		}},
		{name: "enable removal", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineEnable, IdempotencyKey: "enable-1", RemovalMode: LifecycleRemovalPreserve,
		}},
		{name: "uninstall without mode", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineUninstall, IdempotencyKey: "uninstall-1",
		}},
		{name: "install with source", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineInstall, IdempotencyKey: "install-1", SourceExtension: &exactSource,
		}},
		{name: "disable with historical source", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineDisable, IdempotencyKey: "disable-1", SourceExtension: &historicalSource,
		}},
		{name: "uninstall with historical source", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineUninstall, IdempotencyKey: "uninstall-1", RemovalMode: LifecycleRemovalPreserve,
			SourceExtension: &historicalSource,
		}},
		{name: "upgrade without source", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineUpgrade, IdempotencyKey: "upgrade-1",
		}},
		{name: "upgrade with target as source", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineUpgrade, IdempotencyKey: "upgrade-1", SourceExtension: &exactSource,
		}},
		{name: "foreign upgrade source", extension: extension, authority: authority, intent: LifecycleOperationIntent{
			Operation: LifecycleMachineUpgrade, IdempotencyKey: "upgrade-1",
			SourceExtension: func() *Extension {
				value := extension
				value.ID = "other.plugin"
				return &value
			}(),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildLifecycleCoordinatorRunInput(test.extension, actor, test.authority, test.intent)
			if !errors.Is(err, ErrLifecycleCoordinatorInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestConfirmLifecycleAuthorityUsesBuiltinProvenanceWithoutGrant(t *testing.T) {
	extension := lifecycleAuthorityTestExtension(t, "builtin.authority", SourceBuiltin)
	service := NewExecutableTrustService(&fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}, &memoryExecutableTrustStore{})
	actor := identity.Actor{ID: 11}
	authority, err := service.ConfirmLifecycleAuthority(context.Background(), actor, extension, "")
	if err != nil {
		t.Fatal(err)
	}
	if authority.AuthorityType != LifecycleAuthorityBuiltin || authority.Grant != nil {
		t.Fatalf("builtin authority = %#v", authority)
	}
	input, err := BuildLifecycleCoordinatorRunInput(extension, actor, authority, LifecycleOperationIntent{
		Operation: LifecycleMachineEnable, IdempotencyKey: "builtin-enable-1",
	})
	if err != nil || input.Acquire.TrustGrantID != 0 || input.Acquire.AuthorityType != LifecycleAuthorityBuiltin {
		t.Fatalf("builtin input = %#v, %v", input, err)
	}
}

func lifecycleAuthorityTestExtension(t *testing.T, id, source string) Extension {
	t.Helper()
	extension := exactTrustExtension(t, id)
	extension.Source = source
	extension.Manifest.Lifecycle = &ManifestLifecycle{
		ContractVersion: id + ".lifecycle@1",
		Enable:          extensionLifecycleOperationForAuthorityTest(id),
	}
	refreshTrustPackageIdentity(t, &extension)
	return extension
}

func extensionLifecycleOperationForAuthorityTest(id string) *extensionmanifest.ManifestLifecycleOperation {
	return &extensionmanifest.ManifestLifecycleOperation{
		Plan: id + ".enable.plan", Execute: id + ".enable",
		ProgressSchema: id + ".progress@1", CheckpointSchema: id + ".checkpoint@1",
	}
}

func lifecycleAuthorityTestGrant(t *testing.T, extension Extension) LifecycleAuthoritySnapshot {
	t.Helper()
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	actor := extensionManager()
	return LifecycleAuthoritySnapshot{
		SchemaVersion: LifecycleAuthoritySnapshotSchemaV1,
		AuthorityType: LifecycleAuthorityTrustGrant,
		ActorUserID:   actor.ID,
		Impact:        impact,
		Grant: &TrustGrant{
			ID: 41, ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, Action: TrustActionEnable, ImpactDigest: impact.Digest,
		},
	}
}
