package hostapi

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

type contentRegistryAdmissionBackendStub struct {
	identity ContentRegistryRuntimeIdentity
}

func (s *contentRegistryAdmissionBackendStub) AcquireContentRegistryRuntime(
	ctx context.Context,
	identity ContentRegistryRuntimeIdentity,
) (ContentRegistryRuntimeLease, error) {
	s.identity = identity
	return &contentRegistryRuntimeLeaseStub{ctx: ctx}, nil
}

type contentRegistryRuntimeLeaseStub struct {
	ctx context.Context
}

func (l *contentRegistryRuntimeLeaseStub) Context() context.Context { return l.ctx }
func (l *contentRegistryRuntimeLeaseStub) Release()                 {}

type contentRegistryAdmissionBackendFunc func(context.Context, ContentRegistryRuntimeIdentity) (ContentRegistryRuntimeLease, error)

func (f contentRegistryAdmissionBackendFunc) AcquireContentRegistryRuntime(
	ctx context.Context,
	identity ContentRegistryRuntimeIdentity,
) (ContentRegistryRuntimeLease, error) {
	return f(ctx, identity)
}

type contentRegistryTrackingRuntimeLease struct {
	ctx          context.Context
	panicContext bool
	releases     atomic.Int64
}

func (l *contentRegistryTrackingRuntimeLease) Context() context.Context {
	if l.panicContext {
		panic("runtime context detail")
	}
	return l.ctx
}

func (l *contentRegistryTrackingRuntimeLease) Release() { l.releases.Add(1) }

func TestContentRegistryAdmissionPreservesExactIdentity(t *testing.T) {
	backend := &contentRegistryAdmissionBackendStub{}
	adapter := NewContentRegistryAdmission(backend)
	artifact := contentregistry.Artifact{
		ExtensionID: "demo.content", ExtensionVersion: "1.2.3",
		PackageDigest: strings.Repeat("a", 64), VersionID: 42, RuntimeInstanceID: "runtime-42",
	}
	lease, err := adapter.AcquireContentExecution(t.Context(), contentregistry.AdmissionRequest{
		TargetID: "demo.content.block.card", TargetContractVersion: "demo.content.block.card@1",
		TargetSchema: "demo.content.block.card.schema@1", TargetArtifact: artifact,
		ContentID:       "demo.content.filter.safe",
		ContractVersion: "demo.content.filter.safe@1", Kind: contentregistry.KindRenderFilter,
		Schema: "demo.content.filter.safe.schema@1", Action: contentregistry.ActionFilter,
		Operation: contentregistry.OperationFilter, HandlerReference: "filter.safe", Artifact: artifact,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if backend.identity.ExtensionID != artifact.ExtensionID || backend.identity.ExtensionVersion != artifact.ExtensionVersion ||
		backend.identity.PackageDigest != artifact.PackageDigest || backend.identity.VersionID != artifact.VersionID ||
		backend.identity.RuntimeInstanceID != artifact.RuntimeInstanceID ||
		backend.identity.TargetID != "demo.content.block.card" ||
		backend.identity.TargetContractVersion != "demo.content.block.card@1" ||
		backend.identity.TargetPackageDigest != artifact.PackageDigest ||
		backend.identity.ContentID != "demo.content.filter.safe" ||
		backend.identity.ContractVersion != "demo.content.filter.safe@1" ||
		backend.identity.HandlerReference != "filter.safe" || backend.identity.Operation != contentregistry.OperationFilter {
		t.Fatalf("exact identity = %#v", backend.identity)
	}
}

func TestContentRegistryAdmissionRejectsNonCanonicalTargetArtifact(t *testing.T) {
	backend := &contentRegistryAdmissionBackendStub{}
	adapter := NewContentRegistryAdmission(backend)
	artifact := contentregistry.Artifact{
		ExtensionID: "demo.content", ExtensionVersion: "1.2.3",
		PackageDigest: strings.Repeat("a", 64), VersionID: 42, RuntimeInstanceID: "runtime-42",
	}
	nonCanonicalTarget := artifact
	nonCanonicalTarget.ExtensionID = " Demo.Content "
	if _, err := adapter.AcquireContentExecution(t.Context(), contentregistry.AdmissionRequest{
		TargetID: "demo.content.block.card", TargetContractVersion: "demo.content.block.card@1",
		TargetSchema: "demo.content.block.card.schema@1", TargetArtifact: nonCanonicalTarget,
		ContentID: "demo.content.block.card", ContractVersion: "demo.content.block.card@1",
		Kind: contentregistry.KindBlock, Schema: "demo.content.block.card.schema@1",
		HandlerReference: "card", Operation: contentregistry.OperationRenderer, Artifact: artifact,
	}); !errors.Is(err, ErrContentRegistryAdmissionInvalid) {
		t.Fatalf("non-canonical target artifact = %v", err)
	}
	if backend.identity != (ContentRegistryRuntimeIdentity{}) {
		t.Fatalf("backend observed invalid identity = %#v", backend.identity)
	}
}

func TestContentRegistryAdmissionReleasesRejectedBackendLease(t *testing.T) {
	artifact := contentregistry.Artifact{
		ExtensionID: "demo.content", ExtensionVersion: "1.2.3",
		PackageDigest: strings.Repeat("a", 64), VersionID: 42, RuntimeInstanceID: "runtime-42",
	}
	request := contentregistry.AdmissionRequest{
		TargetID: "demo.content.block.card", TargetContractVersion: "demo.content.block.card@1",
		TargetSchema: "demo.content.block.card.schema@1", TargetArtifact: artifact,
		ContentID: "demo.content.block.card", ContractVersion: "demo.content.block.card@1",
		Kind: contentregistry.KindBlock, Schema: "demo.content.block.card.schema@1",
		HandlerReference: "card", Action: contentregistry.ActionAdd,
		Operation: contentregistry.OperationRenderer, Artifact: artifact,
	}
	for _, scenario := range []struct {
		name         string
		panicContext bool
		backendErr   error
	}{
		{name: "context panic", panicContext: true},
		{name: "lease plus error", backendErr: errors.New("backend rejected")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			tracked := &contentRegistryTrackingRuntimeLease{ctx: t.Context(), panicContext: scenario.panicContext}
			adapter := NewContentRegistryAdmission(contentRegistryAdmissionBackendFunc(
				func(context.Context, ContentRegistryRuntimeIdentity) (ContentRegistryRuntimeLease, error) {
					return tracked, scenario.backendErr
				},
			))
			if lease, err := adapter.AcquireContentExecution(t.Context(), request); !errors.Is(err, ErrContentRegistryAdmissionInvalid) || lease != nil || tracked.releases.Load() != 1 {
				t.Fatalf("rejected backend lease=%#v error=%v releases=%d", lease, err, tracked.releases.Load())
			}
		})
	}
}

func TestContentRegistryAdmissionRejectsCancellationAndReleasesLateLeaseOnce(t *testing.T) {
	artifact := contentregistry.Artifact{
		ExtensionID: "demo.content", ExtensionVersion: "1.2.3",
		PackageDigest: strings.Repeat("e", 64), VersionID: 45, RuntimeInstanceID: "runtime-45",
	}
	request := contentregistry.AdmissionRequest{
		TargetID: "demo.content.block.card", TargetContractVersion: "demo.content.block.card@1",
		TargetSchema: "demo.content.block.card.schema@1", TargetArtifact: artifact,
		ContentID: "demo.content.block.card", ContractVersion: "demo.content.block.card@1",
		Kind: contentregistry.KindBlock, Schema: "demo.content.block.card.schema@1",
		HandlerReference: "card", Action: contentregistry.ActionAdd,
		Operation: contentregistry.OperationRenderer, Artifact: artifact,
	}

	t.Run("pre-cancelled parent never reaches backend", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var calls atomic.Int64
		adapter := NewContentRegistryAdmission(contentRegistryAdmissionBackendFunc(
			func(context.Context, ContentRegistryRuntimeIdentity) (ContentRegistryRuntimeLease, error) {
				calls.Add(1)
				return &contentRegistryTrackingRuntimeLease{ctx: context.Background()}, nil
			},
		))
		if lease, err := adapter.AcquireContentExecution(ctx, request); !errors.Is(err, ErrContentRegistryAdmissionInvalid) || lease != nil || calls.Load() != 0 {
			t.Fatalf("pre-cancelled lease=%#v error=%v backend calls=%d", lease, err, calls.Load())
		}
	})

	t.Run("parent cancelled by backend releases returned lease", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		tracked := &contentRegistryTrackingRuntimeLease{ctx: context.Background()}
		adapter := NewContentRegistryAdmission(contentRegistryAdmissionBackendFunc(
			func(context.Context, ContentRegistryRuntimeIdentity) (ContentRegistryRuntimeLease, error) {
				cancel()
				return tracked, nil
			},
		))
		if lease, err := adapter.AcquireContentExecution(ctx, request); !errors.Is(err, ErrContentRegistryAdmissionInvalid) || lease != nil || tracked.releases.Load() != 1 {
			t.Fatalf("late parent cancellation lease=%#v error=%v releases=%d", lease, err, tracked.releases.Load())
		}
	})

	t.Run("cancelled lease context is released", func(t *testing.T) {
		leaseCtx, cancelLease := context.WithCancel(context.Background())
		cancelLease()
		tracked := &contentRegistryTrackingRuntimeLease{ctx: leaseCtx}
		adapter := NewContentRegistryAdmission(contentRegistryAdmissionBackendFunc(
			func(context.Context, ContentRegistryRuntimeIdentity) (ContentRegistryRuntimeLease, error) {
				return tracked, nil
			},
		))
		if lease, err := adapter.AcquireContentExecution(t.Context(), request); !errors.Is(err, ErrContentRegistryAdmissionInvalid) || lease != nil || tracked.releases.Load() != 1 {
			t.Fatalf("cancelled runtime lease=%#v error=%v releases=%d", lease, err, tracked.releases.Load())
		}
	})
}

func TestContentRegistryAdmissionLeaseReleaseIsIdempotent(t *testing.T) {
	artifact := contentregistry.Artifact{
		ExtensionID: "demo.content", ExtensionVersion: "1.2.3",
		PackageDigest: strings.Repeat("b", 64), VersionID: 43, RuntimeInstanceID: "runtime-43",
	}
	tracked := &contentRegistryTrackingRuntimeLease{ctx: t.Context()}
	adapter := NewContentRegistryAdmission(contentRegistryAdmissionBackendFunc(
		func(context.Context, ContentRegistryRuntimeIdentity) (ContentRegistryRuntimeLease, error) {
			return tracked, nil
		},
	))
	lease, err := adapter.AcquireContentExecution(t.Context(), contentregistry.AdmissionRequest{
		TargetID: "demo.content.block.card", TargetContractVersion: "demo.content.block.card@1",
		TargetSchema: "demo.content.block.card.schema@1", TargetArtifact: artifact,
		ContentID: "demo.content.block.card", ContractVersion: "demo.content.block.card@1",
		Kind: contentregistry.KindBlock, Schema: "demo.content.block.card.schema@1",
		HandlerReference: "card", Action: contentregistry.ActionAdd,
		Operation: contentregistry.OperationRenderer, Artifact: artifact,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	lease.Release()
	if tracked.releases.Load() != 1 {
		t.Fatalf("backend release count = %d", tracked.releases.Load())
	}
}

func TestContentRegistryAdmissionRejectsExecutableArtifactWithoutRuntimeInstance(t *testing.T) {
	backend := &contentRegistryAdmissionBackendStub{}
	adapter := NewContentRegistryAdmission(backend)
	artifact := contentregistry.Artifact{
		ExtensionID: "demo.content", ExtensionVersion: "1.2.3",
		PackageDigest: strings.Repeat("c", 64), VersionID: 44,
	}
	if _, err := adapter.AcquireContentExecution(t.Context(), contentregistry.AdmissionRequest{
		TargetID: "demo.content.block.card", TargetContractVersion: "demo.content.block.card@1",
		TargetSchema: "demo.content.block.card.schema@1", TargetArtifact: artifact,
		ContentID: "demo.content.block.card", ContractVersion: "demo.content.block.card@1",
		Kind: contentregistry.KindBlock, Schema: "demo.content.block.card.schema@1",
		HandlerReference: "card", Action: contentregistry.ActionAdd,
		Operation: contentregistry.OperationRenderer, Artifact: artifact,
	}); !errors.Is(err, ErrContentRegistryAdmissionInvalid) {
		t.Fatalf("missing runtime instance = %v", err)
	}
	if backend.identity != (ContentRegistryRuntimeIdentity{}) {
		t.Fatalf("backend observed incomplete executable identity = %#v", backend.identity)
	}
}

func TestContentRegistryAdmissionRejectsForgedCoreArtifact(t *testing.T) {
	adapter := NewContentRegistryAdmission(nil)
	forged := contentregistry.Artifact{
		ExtensionID: "core.forged", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), Core: true,
	}
	if _, err := adapter.AcquireContentExecution(t.Context(), contentregistry.AdmissionRequest{
		TargetID: "core.forged.block.card", TargetContractVersion: "core.forged.block.card@1",
		TargetSchema: "core.forged.schema@1", TargetArtifact: forged,
		ContentID: "core.forged.block.card", ContractVersion: "core.forged.block.card@1",
		Kind: contentregistry.KindBlock, Schema: "core.forged.schema@1",
		HandlerReference: "card", Action: contentregistry.ActionAdd,
		Operation: contentregistry.OperationRenderer, Artifact: forged,
	}); !errors.Is(err, ErrContentRegistryAdmissionInvalid) {
		t.Fatalf("forged Core admission = %v", err)
	}
	trusted, err := contentregistry.NewCoreArtifact("core.content", "1.0.0", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := adapter.AcquireContentExecution(t.Context(), contentregistry.AdmissionRequest{
		TargetID: "core.content.block.card", TargetContractVersion: "core.content.block.card@1",
		TargetSchema: "core.content.schema@1", TargetArtifact: trusted,
		ContentID: "core.content.block.card", ContractVersion: "core.content.block.card@1",
		Kind: contentregistry.KindBlock, Schema: "core.content.schema@1",
		HandlerReference: "card", Action: contentregistry.ActionAdd,
		Operation: contentregistry.OperationRenderer, Artifact: trusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
}

type contentRegistryPermissionBackendStub struct {
	decision ContentRegistryPermissionDecision
	err      error
}

func (s *contentRegistryPermissionBackendStub) AuthorizeContentRegistry(
	_ context.Context,
	decision ContentRegistryPermissionDecision,
) error {
	s.decision = decision
	return s.err
}

func TestContentRegistryPermissionAdapterMapsHostDecision(t *testing.T) {
	backend := &contentRegistryPermissionBackendStub{}
	adapter := NewContentRegistryPermissionRecheck(backend)
	claim := contentregistry.PermissionClaim{
		TargetID: "demo.content.block.card", TargetContractVersion: "demo.content.block.card@1",
		TargetSchema:    "demo.content.schema@1",
		ContentID:       "demo.content.block.card",
		ContractVersion: "demo.content.block.card@1", Schema: "demo.content.schema@1",
		Action: contentregistry.ActionAdd, Operation: contentregistry.OperationRelease,
		Artifact: contentregistry.Artifact{
			ExtensionID: "demo.content", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("c", 64), VersionID: 7, RuntimeInstanceID: "runtime-7",
		},
		ResourceID: "topic:42", Locale: "zh-CN", Scope: "public",
	}
	claim.TargetArtifact = claim.Artifact
	if err := adapter.AuthorizeContent(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	if backend.decision.ContentID != claim.ContentID ||
		backend.decision.TargetContractVersion != claim.TargetContractVersion ||
		backend.decision.TargetPackageDigest != claim.TargetArtifact.PackageDigest ||
		backend.decision.PackageDigest != claim.Artifact.PackageDigest ||
		backend.decision.VersionID != 7 || backend.decision.RuntimeInstanceID != "runtime-7" ||
		backend.decision.Operation != contentregistry.OperationRelease || backend.decision.ResourceID != "topic:42" {
		t.Fatalf("decision = %#v", backend.decision)
	}
	backend.err = ErrContentRegistryPermissionDenied
	if err := adapter.AuthorizeContent(t.Context(), claim); !errors.Is(err, contentregistry.ErrExecutionDenied) {
		t.Fatalf("permission denial = %v", err)
	}
}

func TestContentRegistryPermissionAdapterRejectsIncompleteExactTarget(t *testing.T) {
	backend := &contentRegistryPermissionBackendStub{}
	adapter := NewContentRegistryPermissionRecheck(backend)
	artifact := contentregistry.Artifact{
		ExtensionID: "demo.content", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("d", 64), VersionID: 9, RuntimeInstanceID: "runtime-9",
	}
	claim := contentregistry.PermissionClaim{
		TargetID: "demo.content.block.card", TargetContractVersion: "demo.content.block.card@1",
		TargetSchema: "demo.content.schema@1", TargetArtifact: artifact,
		ContentID: "demo.content.block.card", ContractVersion: "demo.content.block.card@1",
		Schema: "demo.content.schema@1", Action: contentregistry.ActionAdd,
		Operation: contentregistry.OperationRenderer, Artifact: artifact,
	}
	claim.TargetArtifact.PackageDigest = "not-a-digest"
	if err := adapter.AuthorizeContent(t.Context(), claim); !errors.Is(err, contentregistry.ErrExecutionDenied) {
		t.Fatalf("invalid exact target permission = %v", err)
	}
	if backend.decision != (ContentRegistryPermissionDecision{}) {
		t.Fatalf("backend observed invalid permission = %#v", backend.decision)
	}
}
