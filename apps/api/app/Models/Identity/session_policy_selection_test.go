package identity

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestIdentitySessionPolicyInputAndEvidenceContract(t *testing.T) {
	candidate := identitySessionPolicyTestEvidence()
	prepared, err := prepareIdentitySessionPolicySelect(SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 2, ActorUserID: 7,
	})
	if err != nil || prepared.candidate != candidate || prepared.expectedRevision != 2 {
		t.Fatalf("prepare select = %#v, %v", prepared, err)
	}
	reset, err := prepareIdentitySessionPolicyReset(ResetIdentitySessionPolicyInput{
		ExpectedRevision: 2, ActorUserID: 7, ReasonCode: " Operator_Reset ",
	})
	if err != nil || reset.reasonCode != "operator_reset" {
		t.Fatalf("prepare reset = %#v, %v", reset, err)
	}

	invalidCandidates := []IdentitySessionPolicyEvidence{
		{},
		{PolicyID: IdentitySessionPolicyCoreDefault},
		{PolicyID: "UPPER.session"},
		{PolicyID: candidate.PolicyID, ProviderContractVersion: candidate.ProviderContractVersion},
	}
	for _, invalid := range invalidCandidates {
		if _, err := prepareIdentitySessionPolicySelect(SelectIdentitySessionPolicyInput{
			Candidate: invalid, ActorUserID: 7,
		}); !errors.Is(err, ErrIdentitySessionPolicyInvalid) {
			t.Fatalf("invalid candidate %#v error = %v", invalid, err)
		}
	}
	for _, input := range []ResetIdentitySessionPolicyInput{
		{ExpectedRevision: -1, ActorUserID: 7},
		{ExpectedRevision: 0, ActorUserID: 0},
		{ExpectedRevision: 0, ActorUserID: 7, ReasonCode: "bad reason"},
		{ExpectedRevision: 0, ActorUserID: 7, ReasonCode: strings.Repeat("x", 129)},
	} {
		if _, err := prepareIdentitySessionPolicyReset(input); !errors.Is(err, ErrIdentitySessionPolicyInvalid) {
			t.Fatalf("invalid reset %#v error = %v", input, err)
		}
	}
}

func TestIdentitySessionPolicyEvidenceJSONIsStrictAndRedacted(t *testing.T) {
	plugin := identitySessionPolicyTestEvidence()
	encoded, err := marshalIdentitySessionPolicyEvidence(&plugin)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"policyId":"fixture.identity.session","providerContractVersion":"fixture.identity.session@1","ownerExtensionId":"fixture.identity","ownerExtensionVersionId":9,"ownerExtensionVersion":"1.2.3","ownerPackageDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","declarationRevision":4}`
	if encoded != want {
		t.Fatalf("plugin evidence = %v", encoded)
	}
	decoded, err := unmarshalIdentitySessionPolicyEvidence([]byte(want))
	if err != nil || decoded == nil || *decoded != plugin {
		t.Fatalf("decoded plugin = %#v, %v", decoded, err)
	}

	core := IdentitySessionPolicyEvidence{PolicyID: IdentitySessionPolicyCoreDefault}
	encoded, err = marshalIdentitySessionPolicyEvidence(&core)
	if err != nil || encoded != `{"policyId":"core.session.default"}` {
		t.Fatalf("core evidence = %v, %v", encoded, err)
	}
	for _, body := range []string{
		`{"policyId":"core.session.default","actorUserId":7}`,
		`{"policyId":"core.session.default","providerContractVersion":""}`,
		`{"policyId":"core.session.default","sessionId":"secret"}`,
		`{"policyId":"fixture.identity.session"}`,
		`{"policyId":"core.session.default"} {}`,
	} {
		if _, err := unmarshalIdentitySessionPolicyEvidence([]byte(body)); !errors.Is(err, ErrIdentitySessionPolicyStoreUnavailable) {
			t.Fatalf("unsafe evidence %q error = %v", body, err)
		}
	}
}

func TestIdentitySessionPolicyProviderComparisonUsesPublicContract(t *testing.T) {
	provider := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "fixture.identity.session", ContractVersion: "fixture.identity.session@1",
			Kind: identityregistry.ProviderKindSession, Handler: "identity.session",
			Operations: []identityregistry.ProviderOperation{{
				Name: "session.evaluate", InputSchema: "schemas/input.json",
				InputSchemaWireReference: "fixture.input@1", InputSchemaDigest: strings.Repeat("b", 64),
				OutputSchema: "schemas/output.json", OutputSchemaWireReference: "fixture.output@1",
				OutputSchemaDigest: strings.Repeat("c", 64), TimeoutMS: 500,
				FailurePolicy: identityregistry.ProviderFailureFailClosed,
			}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "fixture.identity", ExtensionVersion: "1.2.3",
			PackageDigest: strings.Repeat("a", 64), VersionID: 9, RuntimeInstanceID: "runtime-a",
		},
	}
	clone := provider
	clone.Operations = append([]identityregistry.ProviderOperation(nil), provider.Operations...)
	if !identitySessionPolicyProviderMatches(provider, clone) {
		t.Fatal("exact public provider did not match")
	}
	clone.Artifact.RuntimeInstanceID = "runtime-b"
	if identitySessionPolicyProviderMatches(provider, clone) {
		t.Fatal("runtime replacement matched exact Registry claim")
	}
	clone = provider
	clone.Operations = append([]identityregistry.ProviderOperation(nil), provider.Operations...)
	clone.Operations[0].OutputSchemaDigest = strings.Repeat("d", 64)
	if identitySessionPolicyProviderMatches(provider, clone) {
		t.Fatal("Schema digest drift matched exact Registry claim")
	}
}

func TestIdentitySessionPolicyStoreErrorMapping(t *testing.T) {
	commitUnknown := &IdentitySessionPolicyCommitUnknownError{
		CommitError:       &pgconn.PgError{Code: "40003"},
		VerificationError: errors.New("receipt unavailable"),
	}
	if got := mapIdentitySessionPolicyStoreError(commitUnknown); got != commitUnknown {
		t.Fatalf("commit unknown was collapsed to %v", got)
	}
	if identitySessionPolicyCommitDefinitelyFailed(commitUnknown.CommitError) {
		t.Fatal("statement completion unknown was classified as definite failure")
	}
	if !identitySessionPolicyCommitDefinitelyFailed(&pgconn.PgError{Code: "40001"}) {
		t.Fatal("serialization failure was not classified as definite")
	}
	for _, test := range []struct {
		err  error
		want error
	}{
		{err: &pgconn.PgError{Code: "40001"}, want: errIdentitySessionPolicyRetry},
		{err: &pgconn.PgError{Code: "40P01"}, want: errIdentitySessionPolicyRetry},
		{err: &pgconn.PgError{Code: "23505", ConstraintName: "identity_session_policy_selection_pkey"}, want: ErrIdentitySessionPolicyRevisionConflict},
		{err: &pgconn.PgError{Code: "23505", ConstraintName: "identity_session_policy_selection_events_revision_key"}, want: ErrIdentitySessionPolicyRevisionConflict},
		{err: &pgconn.PgError{Code: "23505", ConstraintName: "unrelated_unique"}, want: ErrIdentitySessionPolicyStoreUnavailable},
		{err: &pgconn.PgError{Code: "23514"}, want: ErrIdentitySessionPolicyInvalid},
	} {
		if got := mapIdentitySessionPolicyStoreError(test.err); !errors.Is(got, test.want) {
			t.Fatalf("map %v = %v, want %v", test.err, got, test.want)
		}
	}
}

func identitySessionPolicyTestEvidence() IdentitySessionPolicyEvidence {
	return IdentitySessionPolicyEvidence{
		PolicyID:                "fixture.identity.session",
		ProviderContractVersion: "fixture.identity.session@1",
		OwnerExtensionID:        "fixture.identity",
		OwnerExtensionVersionID: 9,
		OwnerExtensionVersion:   "1.2.3",
		OwnerPackageDigest:      strings.Repeat("a", 64),
		DeclarationRevision:     4,
	}
}
