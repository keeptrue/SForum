package hostapi

import (
	"context"
	"errors"
	"sync"

	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

var ErrContentRegistryAdmissionInvalid = errors.New("hostapi: content registry admission is invalid")

// ContentRegistryRuntimeIdentity is the cycle-free HostAPI projection that the
// production Extensions adapter must resolve against Manager.AcquireRuntimeCall.
// Until that adapter dispatches HandlerReference through the returned exact
// lease, the in-process Content executor remains a non-production leaf.
type ContentRegistryRuntimeIdentity struct {
	TargetID                string
	TargetContractVersion   string
	TargetSchema            string
	TargetExtensionID       string
	TargetExtensionVersion  string
	TargetPackageDigest     string
	TargetVersionID         int64
	TargetRuntimeInstanceID string
	TargetCore              bool
	ExtensionID             string
	ExtensionVersion        string
	PackageDigest           string
	VersionID               int64
	RuntimeInstanceID       string
	ContentID               string
	ContractVersion         string
	Kind                    string
	Schema                  string
	HandlerReference        string
	RendererReference       string
	MigrationReference      string
	Action                  string
	Operation               string
}

type ContentRegistryRuntimeLease interface {
	Context() context.Context
	Release()
}

type ContentRegistryAdmissionBackend interface {
	AcquireContentRegistryRuntime(context.Context, ContentRegistryRuntimeIdentity) (ContentRegistryRuntimeLease, error)
}

type ContentRegistryAdmission struct {
	backend ContentRegistryAdmissionBackend
}

func NewContentRegistryAdmission(backend ContentRegistryAdmissionBackend) *ContentRegistryAdmission {
	return &ContentRegistryAdmission{backend: backend}
}

func (a *ContentRegistryAdmission) AcquireContentExecution(
	ctx context.Context,
	request contentregistry.AdmissionRequest,
) (contentregistry.AdmissionLease, error) {
	if a == nil || ctx == nil || !contentregistry.IsExactAdmissionRequest(request) {
		return nil, ErrContentRegistryAdmissionInvalid
	}
	if parentErr, panicked := contentRegistryContextErr(ctx); panicked || parentErr != nil {
		return nil, ErrContentRegistryAdmissionInvalid
	}
	if contentregistry.IsHostCoreArtifact(request.Artifact) {
		// Executor resolves this Artifact from an immutable Registry snapshot;
		// unsealed Core artifacts cannot enter that snapshot.
		return &contentRegistryCoreLease{ctx: ctx}, nil
	}
	if request.Artifact.Core {
		return nil, ErrContentRegistryAdmissionInvalid
	}
	if a.backend == nil || request.Artifact.VersionID <= 0 {
		return nil, ErrContentRegistryAdmissionInvalid
	}
	lease, err := a.backend.AcquireContentRegistryRuntime(ctx, ContentRegistryRuntimeIdentity{
		TargetID: request.TargetID, TargetContractVersion: request.TargetContractVersion,
		TargetSchema:            request.TargetSchema,
		TargetExtensionID:       request.TargetArtifact.ExtensionID,
		TargetExtensionVersion:  request.TargetArtifact.ExtensionVersion,
		TargetPackageDigest:     request.TargetArtifact.PackageDigest,
		TargetVersionID:         request.TargetArtifact.VersionID,
		TargetRuntimeInstanceID: request.TargetArtifact.RuntimeInstanceID,
		TargetCore:              request.TargetArtifact.Core,
		ExtensionID:             request.Artifact.ExtensionID, ExtensionVersion: request.Artifact.ExtensionVersion,
		PackageDigest: request.Artifact.PackageDigest, VersionID: request.Artifact.VersionID,
		RuntimeInstanceID: request.Artifact.RuntimeInstanceID, ContentID: request.ContentID,
		ContractVersion: request.ContractVersion, Kind: request.Kind, Schema: request.Schema,
		HandlerReference: request.HandlerReference, RendererReference: request.RendererReference,
		MigrationReference: request.MigrationReference, Action: request.Action, Operation: request.Operation,
	})
	if err != nil || lease == nil {
		if lease != nil {
			releaseContentRegistryRuntimeLease(lease)
		}
		return nil, ErrContentRegistryAdmissionInvalid
	}
	if parentErr, panicked := contentRegistryContextErr(ctx); panicked || parentErr != nil {
		releaseContentRegistryRuntimeLease(lease)
		return nil, ErrContentRegistryAdmissionInvalid
	}
	leaseCtx, panicked := contentRegistryRuntimeLeaseContext(lease)
	if panicked || leaseCtx == nil {
		releaseContentRegistryRuntimeLease(lease)
		return nil, ErrContentRegistryAdmissionInvalid
	}
	parentErr, parentPanicked := contentRegistryContextErr(ctx)
	leaseErr, leasePanicked := contentRegistryContextErr(leaseCtx)
	if parentPanicked || leasePanicked || parentErr != nil || leaseErr != nil {
		releaseContentRegistryRuntimeLease(lease)
		return nil, ErrContentRegistryAdmissionInvalid
	}
	return &contentRegistryLease{lease: lease, ctx: leaseCtx}, nil
}

func contentRegistryRuntimeLeaseContext(lease ContentRegistryRuntimeLease) (ctx context.Context, panicked bool) {
	if lease == nil {
		return nil, false
	}
	defer func() { panicked = recover() != nil }()
	return lease.Context(), false
}

func contentRegistryContextErr(ctx context.Context) (err error, panicked bool) {
	if ctx == nil {
		return ErrContentRegistryAdmissionInvalid, false
	}
	defer func() {
		if recover() != nil {
			err = ErrContentRegistryAdmissionInvalid
			panicked = true
		}
	}()
	return ctx.Err(), false
}

func releaseContentRegistryRuntimeLease(lease ContentRegistryRuntimeLease) (panicked bool) {
	if lease == nil {
		return false
	}
	defer func() { panicked = recover() != nil }()
	lease.Release()
	return false
}

type contentRegistryLease struct {
	lease ContentRegistryRuntimeLease
	ctx   context.Context
	once  sync.Once
}

func (l *contentRegistryLease) CallContext() context.Context {
	if l == nil {
		return nil
	}
	return l.ctx
}

func (l *contentRegistryLease) Release() {
	if l != nil {
		l.once.Do(func() {
			if l.lease != nil {
				l.lease.Release()
			}
		})
	}
}

type contentRegistryCoreLease struct {
	ctx  context.Context
	once sync.Once
}

func (l *contentRegistryCoreLease) CallContext() context.Context {
	if l == nil {
		return nil
	}
	return l.ctx
}

func (l *contentRegistryCoreLease) Release() {
	if l != nil {
		l.once.Do(func() {})
	}
}

var _ contentregistry.RuntimeAdmission = (*ContentRegistryAdmission)(nil)
