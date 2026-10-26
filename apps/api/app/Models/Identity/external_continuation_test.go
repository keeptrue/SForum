package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func continuationServiceForTest(
	t *testing.T,
	recent bool,
	linkEnabled bool,
) (*ExternalAuthService, *countingLinkStore, ExternalAuthAssertion, string) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	live := t1aLiveContribution("demo.auth", digest, "1.0.0")
	activation := NewMemoryProviderActivationStore()
	loginEnabled := true
	if _, err := activation.Upsert(t.Context(), ProviderActivationInput{
		ProviderID: live.ID, OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: live.Artifact.PackageDigest,
		LoginEnabled:       &loginEnabled, LinkEnabled: &linkEnabled,
	}); err != nil {
		t.Fatal(err)
	}
	links := &countingLinkStore{}
	fingerprint := strings.Repeat("f", 64)
	service := NewExternalAuthService(ExternalAuthDeps{
		LinkStore: links, ActivationStore: activation,
		RecentAuth: fixedRecentAuth{ok: recent, wantFingerprint: fingerprint},
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
		LoadCurrentUser: func(context.Context, int64) (CurrentUser, error) {
			return CurrentUser{ID: 42, Username: "member", Status: UserStatusActive}, nil
		},
	})
	assertion := ExternalAuthAssertion{
		ProviderID: live.ID, ProviderContractVersion: live.ContractVersion,
		OwnerExtensionID: live.Artifact.ExtensionID, OwnerExtensionVersion: live.Artifact.ExtensionVersion,
		OwnerPackageDigest: digest, Operation: ExternalAuthOperationLogin,
		ProviderSubject: "provider-subject", CorrelationID: "continuation-test",
	}
	return service, links, assertion, fingerprint
}

func TestCompleteAuthenticatedContinuationUsesSingleLoginAssertionBinding(t *testing.T) {
	service, links, assertion, fingerprint := continuationServiceForTest(t, true, true)
	result, err := service.CompleteAuthenticatedContinuation(t.Context(), assertion, 42, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if result.User.ID != 42 || result.LinkID != 99 || links.linkCalls != 1 {
		t.Fatalf("result=%#v linkCalls=%d", result, links.linkCalls)
	}
	if links.lastInput.ProviderOperation != AuthOperationLoginComplete || links.lastInput.ActorUserID != 42 {
		t.Fatalf("binding input=%#v", links.lastInput)
	}
}

func TestCompleteAuthenticatedContinuationRequiresRecentAuthBeforeWrite(t *testing.T) {
	service, links, assertion, fingerprint := continuationServiceForTest(t, false, true)
	_, err := service.CompleteAuthenticatedContinuation(t.Context(), assertion, 42, fingerprint)
	if !errors.Is(err, ErrExternalAuthRecentAuthRequired) {
		t.Fatalf("err=%v", err)
	}
	if links.linkCalls != 0 {
		t.Fatalf("denied continuation wrote link")
	}
}

func TestCompleteAuthenticatedContinuationHonorsLinkActivation(t *testing.T) {
	service, links, assertion, fingerprint := continuationServiceForTest(t, true, false)
	_, err := service.CompleteAuthenticatedContinuation(t.Context(), assertion, 42, fingerprint)
	if !errors.Is(err, ErrExternalAuthOperationNotActivated) {
		t.Fatalf("err=%v", err)
	}
	if links.linkCalls != 0 {
		t.Fatalf("disabled link operation wrote link")
	}
}

func TestCompleteAuthenticatedContinuationFailsClosedWithoutLinkStore(t *testing.T) {
	service, _, assertion, fingerprint := continuationServiceForTest(t, true, true)
	service.deps.LinkStore = nil
	_, err := service.CompleteAuthenticatedContinuation(t.Context(), assertion, 42, fingerprint)
	if !errors.Is(err, ErrExternalAuthProviderUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
