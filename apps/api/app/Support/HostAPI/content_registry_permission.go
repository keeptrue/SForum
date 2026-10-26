package hostapi

import (
	"context"
	"errors"

	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

var ErrContentRegistryPermissionDenied = errors.New("hostapi: content registry permission denied")

type ContentRegistryPermissionDecision struct {
	TargetID                string
	TargetContractVersion   string
	TargetSchema            string
	TargetExtensionID       string
	TargetExtensionVersion  string
	TargetPackageDigest     string
	TargetVersionID         int64
	TargetRuntimeInstanceID string
	TargetCore              bool
	ContentID               string
	ContractVersion         string
	Schema                  string
	Action                  string
	Operation               string
	ExtensionID             string
	ExtensionVersion        string
	PackageDigest           string
	VersionID               int64
	RuntimeInstanceID       string
	ResourceID              string
	Locale                  string
	Scope                   string
}

// ContentRegistryPermissionBackend must derive the actor from Host request
// state. The content executor supplies resource/contract facts, never grants.
type ContentRegistryPermissionBackend interface {
	AuthorizeContentRegistry(context.Context, ContentRegistryPermissionDecision) error
}

type ContentRegistryPermissionRecheck struct {
	backend ContentRegistryPermissionBackend
}

func NewContentRegistryPermissionRecheck(backend ContentRegistryPermissionBackend) *ContentRegistryPermissionRecheck {
	return &ContentRegistryPermissionRecheck{backend: backend}
}

func (r *ContentRegistryPermissionRecheck) AuthorizeContent(
	ctx context.Context,
	claim contentregistry.PermissionClaim,
) error {
	if r == nil || r.backend == nil || ctx == nil || !contentregistry.IsExactPermissionClaim(claim) {
		return contentregistry.ErrExecutionDenied
	}
	err := r.backend.AuthorizeContentRegistry(ctx, ContentRegistryPermissionDecision{
		TargetID: claim.TargetID, TargetContractVersion: claim.TargetContractVersion,
		TargetSchema:            claim.TargetSchema,
		TargetExtensionID:       claim.TargetArtifact.ExtensionID,
		TargetExtensionVersion:  claim.TargetArtifact.ExtensionVersion,
		TargetPackageDigest:     claim.TargetArtifact.PackageDigest,
		TargetVersionID:         claim.TargetArtifact.VersionID,
		TargetRuntimeInstanceID: claim.TargetArtifact.RuntimeInstanceID,
		TargetCore:              claim.TargetArtifact.Core,
		ContentID:               claim.ContentID,
		ContractVersion:         claim.ContractVersion, Schema: claim.Schema,
		Action: claim.Action, Operation: claim.Operation,
		ExtensionID: claim.Artifact.ExtensionID, ExtensionVersion: claim.Artifact.ExtensionVersion,
		PackageDigest: claim.Artifact.PackageDigest, VersionID: claim.Artifact.VersionID,
		RuntimeInstanceID: claim.Artifact.RuntimeInstanceID,
		ResourceID:        claim.ResourceID, Locale: claim.Locale, Scope: claim.Scope,
	})
	if err != nil {
		return contentregistry.ErrExecutionDenied
	}
	return nil
}

var _ contentregistry.PermissionRecheck = (*ContentRegistryPermissionRecheck)(nil)
