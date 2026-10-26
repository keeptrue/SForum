package identity

import (
	"strings"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestExternalIdentityLinkFingerprintBindsHostEffectAndPersistentArtifact(t *testing.T) {
	input := externalIdentityLinkTestInput()
	prepared, err := prepareExternalIdentityLink(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.fingerprint) != 64 || prepared.fingerprint == input.ProviderSubjectDigest {
		t.Fatalf("external link fingerprint = %q", prepared.fingerprint)
	}

	restarted := input
	restarted.Provider.Artifact.RuntimeInstanceID = "runtime-after-restart"
	restartedPrepared, err := prepareExternalIdentityLink(restarted)
	if err != nil {
		t.Fatal(err)
	}
	if restartedPrepared.fingerprint != prepared.fingerprint {
		t.Fatal("ephemeral runtime instance changed durable idempotency fingerprint")
	}

	mutations := []struct {
		name   string
		change func(*LinkExternalIdentityInput)
	}{
		{name: "user", change: func(value *LinkExternalIdentityInput) {
			value.UserID++
			value.ActorUserID = value.UserID
		}},
		{name: "subject digest", change: func(value *LinkExternalIdentityInput) { value.ProviderSubjectDigest = strings.Repeat("b", 64) }},
		{name: "artifact version", change: func(value *LinkExternalIdentityInput) { value.Provider.Artifact.VersionID++ }},
		{name: "artifact digest", change: func(value *LinkExternalIdentityInput) {
			value.Provider.Artifact.PackageDigest = strings.Repeat("c", 64)
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := externalIdentityLinkTestInput()
			test.change(&changed)
			got, err := prepareExternalIdentityLink(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got.fingerprint == prepared.fingerprint {
				t.Fatal("authority-relevant mutation did not change fingerprint")
			}
		})
	}
	registration := externalIdentityLinkTestInput()
	registration.ProviderOperation = "registration.complete"
	registration.ActorUserID = 0
	preparedRegistration, err := prepareExternalIdentityLink(registration)
	if err != nil {
		t.Fatal(err)
	}
	if preparedRegistration.fingerprint == prepared.fingerprint {
		t.Fatal("provider operation and actor authority did not change fingerprint")
	}
}

func TestExternalIdentityLinkInputRejectsUnsafeProviderAndSecrets(t *testing.T) {
	tests := []struct {
		name   string
		change func(*LinkExternalIdentityInput)
	}{
		{name: "missing user", change: func(value *LinkExternalIdentityInput) { value.UserID = 0 }},
		{name: "negative actor", change: func(value *LinkExternalIdentityInput) { value.ActorUserID = -1 }},
		{name: "missing live link actor", change: func(value *LinkExternalIdentityInput) { value.ActorUserID = 0 }},
		{name: "mismatched live link actor", change: func(value *LinkExternalIdentityInput) { value.ActorUserID++ }},
		{name: "registration claims actor", change: func(value *LinkExternalIdentityInput) {
			value.ProviderOperation = "registration.complete"
		}},
		{name: "profile provider", change: func(value *LinkExternalIdentityInput) { value.Provider.Kind = identityregistry.ProviderKindProfile }},
		{name: "Core provider", change: func(value *LinkExternalIdentityInput) { value.Provider.Artifact.Core = true }},
		{name: "catalog only", change: func(value *LinkExternalIdentityInput) { value.Provider.Operations = nil }},
		{name: "missing runtime", change: func(value *LinkExternalIdentityInput) { value.Provider.Artifact.RuntimeInstanceID = "" }},
		{name: "raw subject", change: func(value *LinkExternalIdentityInput) { value.ProviderSubjectDigest = "vendor-user-42" }},
		{name: "unknown operation", change: func(value *LinkExternalIdentityInput) { value.ProviderOperation = "recovery.complete" }},
		{name: "whitespace idempotency", change: func(value *LinkExternalIdentityInput) { value.IdempotencyKey = " link:key" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := externalIdentityLinkTestInput()
			test.change(&input)
			if _, err := prepareExternalIdentityLink(input); err != ErrExternalIdentityLinkInvalid {
				t.Fatalf("prepare error = %v", err)
			}
		})
	}
}

func TestExternalIdentityTransitionFingerprintBindsCAS(t *testing.T) {
	input := TransitionExternalIdentityLinkInput{
		LinkID: 17, ExpectedRevision: 3, ActorUserID: 9, IdempotencyKey: "link:erase:17:3",
	}
	erase, err := prepareExternalIdentityTransition(ExternalIdentityLinkActionErase, input)
	if err != nil {
		t.Fatal(err)
	}
	unlinked, err := prepareExternalIdentityTransition(ExternalIdentityLinkActionUnlink, input)
	if err != nil {
		t.Fatal(err)
	}
	if erase.fingerprint == unlinked.fingerprint {
		t.Fatal("transition action did not change fingerprint")
	}
	input.ExpectedRevision++
	changed, err := prepareExternalIdentityTransition(ExternalIdentityLinkActionErase, input)
	if err != nil {
		t.Fatal(err)
	}
	if changed.fingerprint == erase.fingerprint {
		t.Fatal("expected revision did not change fingerprint")
	}
	input.ExpectedRevision = 0
	if _, err := prepareExternalIdentityTransition(ExternalIdentityLinkActionErase, input); err != ErrExternalIdentityLinkInvalid {
		t.Fatalf("zero revision error = %v", err)
	}
}

func externalIdentityLinkTestInput() LinkExternalIdentityInput {
	return LinkExternalIdentityInput{
		UserID: 42,
		Provider: identityregistry.ProviderContribution{
			Provider: identityregistry.Provider{
				ID: "plugin.membership.auth", ContractVersion: "plugin.membership.auth@1",
				Kind: identityregistry.ProviderKindAuth, Handler: "identity.auth", Priority: 10,
				Operations: []identityregistry.ProviderOperation{
					{Name: "registration.complete"},
					{Name: "link.complete"},
				},
			},
			Artifact: identityregistry.Artifact{
				ExtensionID: "plugin.membership", ExtensionVersion: "1.0.0",
				PackageDigest: strings.Repeat("a", 64), VersionID: 7,
				RuntimeInstanceID: "runtime-membership",
			},
		},
		ProviderOperation: "link.complete", ProviderSubjectDigest: strings.Repeat("d", 64),
		ActorUserID: 42, IdempotencyKey: "link:plugin.membership.auth:42",
	}
}
