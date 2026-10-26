package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestRecoveryProviderFlowStartAndComplete(t *testing.T) {
	provider := testRecoveryProviderContribution("demo.recovery", []string{
		RecoveryOperationStart, RecoveryOperationComplete,
	})
	source := &fakeRecoveryProviderSource{provider: provider}
	invoker := &fakeAuthProviderInvoker{
		startOutput: map[string]any{"status": "challenge", "challengeKind": "email_code", "continueToken": "r1"},
		completeOutput: map[string]any{
			"providerSubjectDigest": strings.Repeat("c", 64),
			"userHintId":            float64(55),
		},
	}
	flow, err := NewRecoveryProviderFlow(source, invoker)
	if err != nil {
		t.Fatal(err)
	}
	start, err := flow.Start(context.Background(), RecoveryProviderStartInput{
		ProviderID: "demo.recovery", CorrelationID: "rec-1", AccountHint: "user@example.com",
	})
	if err != nil || start.Status != RecoveryStartStatusChallenge || start.ChallengeKind != "email_code" {
		t.Fatalf("start=%#v err=%v", start, err)
	}
	complete, err := flow.Complete(context.Background(), RecoveryProviderCompleteInput{
		ProviderID: "demo.recovery", CorrelationID: "rec-1", CompletionToken: "code",
	})
	if err != nil || complete.SubjectDigest != strings.Repeat("c", 64) || complete.UserHintID != 55 {
		t.Fatalf("complete=%#v err=%v", complete, err)
	}
}

func TestRecoveryProviderFlowFailClosedOnEmptyComplete(t *testing.T) {
	provider := testRecoveryProviderContribution("demo.recovery", []string{RecoveryOperationComplete})
	flow, err := NewRecoveryProviderFlow(
		&fakeRecoveryProviderSource{provider: provider},
		&fakeAuthProviderInvoker{completeOutput: map[string]any{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Complete(context.Background(), RecoveryProviderCompleteInput{
		ProviderID: "demo.recovery", CorrelationID: "rec", CompletionToken: "x",
	}); !errors.Is(err, ErrRecoveryProviderUnavailable) {
		t.Fatalf("empty complete: %v", err)
	}
}

func testRecoveryProviderContribution(id string, operations []string) identityregistry.ProviderContribution {
	ops := make([]identityregistry.ProviderOperation, 0, len(operations))
	for _, name := range operations {
		ops = append(ops, identityregistry.ProviderOperation{
			Name: name, InputSchema: "in@1", OutputSchema: "out@1",
			TimeoutMS: 1000, FailurePolicy: identityregistry.ProviderFailureFailClosed,
		})
	}
	return identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: id, ContractVersion: id + "@1", Kind: identityregistry.ProviderKindRecovery,
			Handler: "identity.recovery", Operations: ops,
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "demo.membership", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("3", 64), VersionID: 9,
			RuntimeInstanceID: "runtime-recovery",
		},
	}
}

type fakeRecoveryProviderSource struct {
	provider identityregistry.ProviderContribution
	err      error
}

func (f *fakeRecoveryProviderSource) ResolveRecoveryProvider(context.Context, string) (identityregistry.ProviderContribution, error) {
	if f.err != nil {
		return identityregistry.ProviderContribution{}, f.err
	}
	return f.provider, nil
}
