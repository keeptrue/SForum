package identity

import (
	"context"
	"strings"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestAuthProviderFlowProbeSuccessAndRedaction(t *testing.T) {
	provider := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.auth", ContractVersion: "demo.auth@1",
			Kind: identityregistry.ProviderKindAuth, Handler: "demo.identity",
			Operations: []identityregistry.ProviderOperation{{
				Name: AuthOperationProviderProbe, InputSchema: "demo.probe.input@1",
				OutputSchema: "demo.probe.output@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
			}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "demo", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), VersionID: 1, RuntimeInstanceID: "rt-1",
		},
	}
	invoker := &fakeAuthProviderInvoker{
		probeOutput: map[string]any{
			"ok":      true,
			"reason":  "demo.probe_ok",
			"message": "looks good client_secret=supersecretvalue",
		},
	}
	flow, err := NewAuthProviderFlow(
		&fakeAuthProviderSource{provider: provider},
		invoker,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := flow.Probe(context.Background(), "demo.auth")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !result.OK || result.Reason != "demo.probe_ok" {
		t.Fatalf("result=%#v", result)
	}
	if strings.Contains(result.Message, "supersecretvalue") {
		t.Fatalf("message must redact secret material: %q", result.Message)
	}
	if invoker.lastOperation != AuthOperationProviderProbe {
		t.Fatalf("operation=%q", invoker.lastOperation)
	}
	if invoker.lastActorUserID != 0 {
		t.Fatalf("probe must be actorless, actor=%d", invoker.lastActorUserID)
	}
}

func TestAuthProviderFlowProbeRejectsPendingReason(t *testing.T) {
	provider := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.auth", ContractVersion: "demo.auth@1",
			Kind: identityregistry.ProviderKindAuth, Handler: "demo.identity",
			Operations: []identityregistry.ProviderOperation{{
				Name: AuthOperationProviderProbe, InputSchema: "demo.probe.input@1",
				OutputSchema: "demo.probe.output@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
			}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "demo", PackageDigest: strings.Repeat("a", 64),
			VersionID: 1, RuntimeInstanceID: "rt-1",
		},
	}
	flow, err := NewAuthProviderFlow(
		&fakeAuthProviderSource{provider: provider},
		&fakeAuthProviderInvoker{probeOutput: map[string]any{"ok": true, "reason": ProbeReasonPending}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Probe(context.Background(), "demo.auth"); err == nil {
		t.Fatal("probe_pending must not be accepted as product probe output")
	}
}

func TestAuthProviderFlowProbeRequiresLiveRuntimeAndOperation(t *testing.T) {
	base := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.auth", ContractVersion: "demo.auth@1",
			Kind: identityregistry.ProviderKindAuth, Handler: "demo.identity",
			Operations: []identityregistry.ProviderOperation{{
				Name: AuthOperationLoginStart, InputSchema: "x@1", OutputSchema: "y@1",
				TimeoutMS: 1000, FailurePolicy: "fail_closed",
			}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "demo", PackageDigest: strings.Repeat("a", 64),
			VersionID: 1, RuntimeInstanceID: "rt-1",
		},
	}
	flow, err := NewAuthProviderFlow(
		&fakeAuthProviderSource{provider: base},
		&fakeAuthProviderInvoker{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Probe(context.Background(), "demo.auth"); err == nil {
		t.Fatal("missing provider.probe operation must fail")
	}

	noRuntime := base
	noRuntime.Artifact.RuntimeInstanceID = ""
	noRuntime.Operations = []identityregistry.ProviderOperation{{
		Name: AuthOperationProviderProbe, InputSchema: "x@1", OutputSchema: "y@1",
		TimeoutMS: 1000, FailurePolicy: "fail_closed",
	}}
	flow2, err := NewAuthProviderFlow(
		&fakeAuthProviderSource{provider: noRuntime},
		&fakeAuthProviderInvoker{probeOutput: map[string]any{"ok": true, "reason": "ok"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow2.Probe(context.Background(), "demo.auth"); err == nil {
		t.Fatal("missing runtime must fail closed")
	}
}
