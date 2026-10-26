package extensionsruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

type versionedSEOInvoker interface {
	InvokeVersionedSEO(context.Context, extensions.Extension, VersionedSEORequest) (VersionedSEOResponse, error)
}

// ProtocolV2SEOContribution is the public wire projection of a frozen SEO
// declaration. Exact artifact identity remains in RequestContext and the Host
// admission lease; PostgreSQL ids and trust digests are not duplicated through
// google.protobuf.Struct numbers.
type ProtocolV2SEOContribution struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Scope           string `json:"scope"`
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Handler         string `json:"handler"`
	Priority        int    `json:"priority,omitempty"`
	FailurePolicy   string `json:"failurePolicy"`
	TimeoutMS       int    `json:"timeoutMs"`
}

type ProtocolV2SEOApplyRequest struct {
	Scope        string                    `json:"scope"`
	Contribution ProtocolV2SEOContribution `json:"contribution"`
	Current      seoregistry.Document      `json:"current"`
}

type ProtocolV2SEOApplyResponse struct {
	Document seoregistry.Document `json:"document"`
}

type protocolV2SEOProviderResolver struct {
	manager *Manager
}

// NewProtocolV2SEOProviderResolver maps immutable SEO Registry declarations to
// the existing Protocol V2 ProviderCall transport. Resolution performs no
// database work and never captures a startup-only runtime instance.
func NewProtocolV2SEOProviderResolver(manager *Manager) seoregistry.ProviderResolver {
	return &protocolV2SEOProviderResolver{manager: manager}
}

func (r *protocolV2SEOProviderResolver) ResolveSEOProvider(
	ctx context.Context,
	contribution seoregistry.Contribution,
) (seoregistry.ProviderBinding, error) {
	if r == nil || r.manager == nil || ctx == nil || contribution.Artifact.Core {
		return seoregistry.ProviderBinding{}, seoregistry.ErrProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return seoregistry.ProviderBinding{}, err
	}
	return seoregistry.ProviderBinding{
		ContributionID: contribution.ID, ContractVersion: contribution.ContractVersion,
		Handler: contribution.Handler, Artifact: contribution.Artifact,
		Provider: &protocolV2SEOProvider{manager: r.manager, contribution: contribution},
	}, nil
}

type protocolV2SEOProvider struct {
	manager      *Manager
	contribution seoregistry.Contribution
}

func (p *protocolV2SEOProvider) ApplySEO(
	ctx context.Context,
	request seoregistry.ProviderRequest,
) (seoregistry.ProviderResult, error) {
	if p == nil || p.manager == nil || ctx == nil || request.Contribution != p.contribution ||
		request.Scope != p.contribution.Scope {
		return seoregistry.ProviderResult{}, seoregistry.ErrProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return seoregistry.ProviderResult{}, err
	}
	extension, available := p.manager.runningExtension(p.contribution.Artifact.ExtensionID)
	if !available || !exactSEOExtension(extension, p.contribution.Artifact) {
		return seoregistry.ProviderResult{}, seoregistry.ErrArtifactUnavailable
	}
	active, err := p.manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || !exactSEORuntime(active, p.contribution.Artifact) {
		return seoregistry.ProviderResult{}, errors.Join(seoregistry.ErrArtifactUnavailable, err)
	}
	invoker, ok := p.manager.starter.(versionedSEOInvoker)
	if !ok {
		return seoregistry.ProviderResult{}, seoregistry.ErrProviderUnavailable
	}
	input, err := encodeSEOTransportValue(ProtocolV2SEOApplyRequest{
		Scope: request.Scope,
		Contribution: ProtocolV2SEOContribution{
			ID: p.contribution.ID, ContractVersion: p.contribution.ContractVersion,
			Scope: p.contribution.Scope, Kind: p.contribution.Kind, Action: p.contribution.Action,
			Handler: p.contribution.Handler, Priority: p.contribution.Priority,
			FailurePolicy: p.contribution.FailurePolicy, TimeoutMS: int(p.contribution.Timeout / time.Millisecond),
		},
		Current: request.Current,
	})
	if err != nil {
		return seoregistry.ProviderResult{}, errors.Join(seoregistry.ErrOutputInvalid, err)
	}
	response, err := invoker.InvokeVersionedSEO(ctx, extension, VersionedSEORequest{
		DeclarationID: p.contribution.ID, ContractVersion: p.contribution.ContractVersion,
		Handler: p.contribution.Handler, Timeout: p.contribution.Timeout, Input: input,
	})
	if err != nil {
		return seoregistry.ProviderResult{}, err
	}
	wireResult := ProtocolV2SEOApplyResponse{}
	if err := decodeSEOTransportValue(response.Output, &wireResult); err != nil {
		return seoregistry.ProviderResult{}, errors.Join(seoregistry.ErrOutputInvalid, err)
	}
	result := seoregistry.ProviderResult{Document: wireResult.Document}
	return result, nil
}

type managerSEOAdmissionLease struct {
	lease *RuntimeAdmissionLease
}

func (l *managerSEOAdmissionLease) Context() context.Context {
	if l == nil || l.lease == nil {
		return nil
	}
	return l.lease.Context
}

func (l *managerSEOAdmissionLease) Release() {
	if l != nil && l.lease != nil {
		l.lease.Release()
	}
}

type managerSEOExecutionAdmission struct {
	manager *Manager
}

// NewSEOExecutionAdmission holds an exact Manager runtime lease across all SEO
// callbacks and the non-overridable Host final-policy fence.
func NewSEOExecutionAdmission(manager *Manager) seoregistry.ExecutionAdmission {
	return &managerSEOExecutionAdmission{manager: manager}
}

func (a *managerSEOExecutionAdmission) AcquireSEOExecution(
	ctx context.Context,
	artifact seoregistry.Artifact,
) (seoregistry.AdmissionLease, error) {
	if a == nil || a.manager == nil || ctx == nil || artifact.Core {
		return nil, seoregistry.ErrArtifactUnavailable
	}
	snapshot, lease, err := a.manager.AcquireActiveRuntimeCall(ctx, artifact.ExtensionID, RuntimeCallProvider)
	if err != nil || lease == nil {
		return nil, errors.Join(seoregistry.ErrArtifactUnavailable, err)
	}
	extension, available := a.manager.runningExtension(artifact.ExtensionID)
	if !available || !exactSEOExtension(extension, artifact) || !exactSEORuntime(snapshot, artifact) ||
		lease.Context == nil || lease.Context.Err() != nil {
		lease.Release()
		return nil, seoregistry.ErrArtifactUnavailable
	}
	return &managerSEOAdmissionLease{lease: lease}, nil
}

func exactSEOExtension(extension extensions.Extension, artifact seoregistry.Artifact) bool {
	return extension.ID == artifact.ExtensionID && extension.Version == artifact.ExtensionVersion &&
		extension.PackageDigest == artifact.PackageDigest && extension.ActiveVersionID == artifact.VersionID
}

func exactSEORuntime(snapshot RuntimeInstanceSnapshot, artifact seoregistry.Artifact) bool {
	return snapshot.Active && !snapshot.Admission.Draining && !snapshot.Admission.Forced &&
		snapshot.Identity.ExtensionID == artifact.ExtensionID && snapshot.Identity.InstanceID == artifact.RuntimeInstanceID &&
		snapshot.ExtensionVersion == artifact.ExtensionVersion && snapshot.ArtifactDigest == artifact.PackageDigest
}

func encodeSEOTransportValue(value any) (map[string]any, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeSEOTransportValue(values map[string]any, target any) error {
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("SEO provider response has trailing data")
	}
	return nil
}
