package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

// NewCompositeQueryProviderResolver keeps Core HostAPI providers authoritative
// and resolves third-party executable handlers through Protocol V2 at call time.
// Composition uses the Query Registry package-private resolve surface via
// NewFallbackProviderResolver so Extensions never type-asserts unexported methods.
func NewCompositeQueryProviderResolver(
	core queryregistry.ExecutableProviderResolver,
	manager *Manager,
	registry *queryregistry.Registry,
) (queryregistry.ExecutableProviderResolver, error) {
	if core == nil || manager == nil || registry == nil {
		return nil, queryregistry.ErrExecutionInvalid
	}
	protocol := &protocolV2QueryProviderResolver{manager: manager, registry: registry}
	return queryregistry.NewFallbackProviderResolver(
		core,
		queryregistry.NewProviderResolverFunc(protocol.resolve),
	)
}

type protocolV2QueryProviderResolver struct {
	manager  *Manager
	registry *queryregistry.Registry
}

func (r *protocolV2QueryProviderResolver) resolve(
	ctx context.Context,
	plan queryregistry.QueryPlan,
) (queryregistry.ExecutableProviderBinding, error) {
	if r == nil || r.manager == nil || r.registry == nil || ctx == nil {
		return queryregistry.ExecutableProviderBinding{}, queryregistry.ErrProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return queryregistry.ExecutableProviderBinding{}, err
	}
	if plan.Query.Artifact.Core || strings.TrimSpace(plan.Query.Handler) == "" {
		return queryregistry.ExecutableProviderBinding{}, queryregistry.ErrProviderUnavailable
	}
	contribution, err := r.registry.Resolve(plan.Query.ID)
	if err != nil || contribution.Artifact != plan.Query.Artifact ||
		contribution.ContractVersion != plan.Query.ContractVersion ||
		contribution.PlanVersion != plan.Query.PlanVersion ||
		contribution.ResultSchema != plan.Query.ResultSchema ||
		contribution.Handler != plan.Query.Handler {
		return queryregistry.ExecutableProviderBinding{}, queryregistry.ErrProviderUnavailable
	}
	provider := &protocolV2QueryProvider{manager: r.manager, contribution: contribution}
	digest := contribution.ProviderDigest
	if digest == "" {
		// 无 BindExecutableRuntime 时用 public Handler/artifact 推导稳定 digest。
		digest = protocolV2QueryProviderDigest(contribution)
	}
	return queryregistry.ExecutableProviderBinding{
		QueryID: contribution.ID, ContractVersion: contribution.ContractVersion,
		PlanVersion: contribution.PlanVersion, ResultSchema: contribution.ResultSchema,
		Artifact: contribution.Artifact, FailurePolicy: queryregistry.ProviderFailureFailClosed,
		ProviderDigest: digest, Provider: provider,
	}, nil
}

type protocolV2QueryProvider struct {
	manager      *Manager
	contribution queryregistry.QueryContribution
}

func (p *protocolV2QueryProvider) ExecuteQuery(
	ctx context.Context,
	request queryregistry.ProviderExecutionRequest,
) (queryregistry.ProviderExecutionResult, error) {
	if p == nil || p.manager == nil || ctx == nil ||
		request.Plan.Query.ID != p.contribution.ID ||
		request.Plan.Query.Artifact != p.contribution.Artifact ||
		request.Plan.Query.Handler != p.contribution.Handler {
		return queryregistry.ProviderExecutionResult{}, queryregistry.ErrProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return queryregistry.ProviderExecutionResult{}, err
	}
	if len(request.Plan.Relations) > 0 {
		return queryregistry.ProviderExecutionResult{}, queryregistry.ErrContractInsufficient
	}
	extension, available := p.manager.runningExtension(p.contribution.Artifact.ExtensionID)
	if !available || !exactQueryExtension(extension, p.contribution.Artifact) {
		return queryregistry.ProviderExecutionResult{}, queryregistry.ErrArtifactUnavailable
	}
	active, err := p.manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || !exactQueryRuntime(active, p.contribution.Artifact) {
		return queryregistry.ProviderExecutionResult{}, errors.Join(queryregistry.ErrArtifactUnavailable, err)
	}
	rows, err := p.manager.InvokeQueryInstance(ctx, active.Identity, extension, VersionedQueryRequest{
		QueryID: p.contribution.ID, ContractVersion: p.contribution.ContractVersion,
		PlanVersion: p.contribution.PlanVersion, ResultSchema: p.contribution.ResultSchema,
		Handler: p.contribution.Handler, Plan: request.Plan, FetchLimit: request.FetchLimit,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		return queryregistry.ProviderExecutionResult{}, err
	}
	return queryregistry.ProviderExecutionResult{Rows: rows}, nil
}

// protocolV2QueryResultFilterSource materializes independent result filters from
// the immutable Query Registry snapshot into Host execution registrations.
type protocolV2QueryResultFilterSource struct {
	manager  *Manager
	registry *queryregistry.Registry
}

// NewProtocolV2QueryResultFilterSource builds Protocol V2 filter adapters from
// published ResultFilter declarations. Callables are resolved at execution time
// against the exact active runtime, not captured at process start.
func NewProtocolV2QueryResultFilterSource(
	manager *Manager,
	registry *queryregistry.Registry,
) (queryregistry.ResultFilterSource, error) {
	if manager == nil || registry == nil {
		return nil, queryregistry.ErrExecutionInvalid
	}
	return &protocolV2QueryResultFilterSource{manager: manager, registry: registry}, nil
}

func (s *protocolV2QueryResultFilterSource) ResultFiltersFor(
	query queryregistry.QueryContribution,
) ([]queryregistry.ResultFilterRegistration, error) {
	if s == nil || s.manager == nil || s.registry == nil {
		return nil, queryregistry.ErrExecutionInvalid
	}
	snapshot := s.registry.Snapshot()
	hasFilters := false
	for _, publication := range snapshot.Publications {
		if len(publication.ResultFilters) > 0 {
			hasFilters = true
			break
		}
	}
	if !hasFilters {
		return nil, nil
	}
	var active queryregistry.QueryContribution
	foundQuery := false
	for _, candidate := range snapshot.Queries {
		if candidate.ID != query.ID {
			continue
		}
		active = candidate
		foundQuery = true
		break
	}
	if !foundQuery || active.Artifact != query.Artifact || active.ContractVersion != query.ContractVersion ||
		active.PlanVersion != query.PlanVersion || active.ResultSchema != query.ResultSchema ||
		active.Handler != query.Handler {
		return nil, queryregistry.ErrArtifactConflict
	}
	result := make([]queryregistry.ResultFilterRegistration, 0)
	for _, publication := range snapshot.Publications {
		for _, filter := range publication.ResultFilters {
			if filter.QueryID != query.ID {
				continue
			}
			timeout := time.Duration(filter.TimeoutMS) * time.Millisecond
			if timeout <= 0 {
				timeout = time.Second
			}
			registration := queryregistry.ResultFilterRegistration{
				ID: filter.ID, ContractVersion: filter.ContractVersion,
				QueryID: filter.QueryID, QueryContractVersion: filter.QueryContractVersion,
				QueryPlanVersion: filter.QueryPlanVersion, Priority: filter.Priority,
				Artifact: publication.Artifact, IdentityFields: append([]string(nil), filter.IdentityFields...),
				FailurePolicy: filter.FailurePolicy, Timeout: timeout,
				Filter: &protocolV2QueryResultFilter{
					manager: s.manager, artifact: publication.Artifact, declaration: filter,
					resultSchema: query.ResultSchema,
				},
			}
			if filter.Dependency != nil {
				registration.Dependency = queryregistry.ResultFilterDependency{
					ExtensionID:       filter.Dependency.ExtensionID,
					VersionConstraint: filter.Dependency.VersionConstraint,
				}
			}
			result = append(result, registration)
		}
	}
	return result, nil
}

type protocolV2QueryResultFilter struct {
	manager      *Manager
	artifact     queryregistry.Artifact
	declaration  queryregistry.ResultFilterDeclaration
	resultSchema string
}

func (f *protocolV2QueryResultFilter) FilterQueryResult(
	ctx context.Context,
	request queryregistry.ResultFilterRequest,
) (queryregistry.ResultFilterResult, error) {
	if f == nil || f.manager == nil || ctx == nil ||
		request.Plan.Query.ID != f.declaration.QueryID {
		return queryregistry.ResultFilterResult{}, queryregistry.ErrProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return queryregistry.ResultFilterResult{}, err
	}
	extension, available := f.manager.runningExtension(f.artifact.ExtensionID)
	if !available || !exactQueryExtension(extension, f.artifact) {
		return queryregistry.ResultFilterResult{}, queryregistry.ErrArtifactUnavailable
	}
	active, err := f.manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || !exactQueryRuntime(active, f.artifact) {
		return queryregistry.ResultFilterResult{}, errors.Join(queryregistry.ErrArtifactUnavailable, err)
	}
	timeout := time.Duration(f.declaration.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	rows, err := f.manager.FilterQueryResultInstance(ctx, active.Identity, extension, VersionedQueryResultFilterRequest{
		FilterID: f.declaration.ID, FilterContractVersion: f.declaration.ContractVersion,
		QueryID: f.declaration.QueryID, QueryContractVersion: f.declaration.QueryContractVersion,
		QueryPlanVersion: f.declaration.QueryPlanVersion, ResultSchema: f.resultSchema,
		Handler: f.declaration.Handler, Plan: request.Plan, Rows: request.Rows, Timeout: timeout,
	})
	if err != nil {
		return queryregistry.ResultFilterResult{}, err
	}
	return queryregistry.ResultFilterResult{Rows: rows}, nil
}

func exactQueryExtension(extension extensions.Extension, artifact queryregistry.Artifact) bool {
	return extension.ID == artifact.ExtensionID && extension.Version == artifact.ExtensionVersion &&
		extension.PackageDigest == artifact.PackageDigest && extension.ActiveVersionID == artifact.VersionID
}

func exactQueryRuntime(snapshot RuntimeInstanceSnapshot, artifact queryregistry.Artifact) bool {
	return snapshot.Active && !snapshot.Admission.Draining && !snapshot.Admission.Forced &&
		snapshot.Identity.ExtensionID == artifact.ExtensionID &&
		snapshot.Identity.InstanceID == artifact.RuntimeInstanceID &&
		snapshot.ExtensionVersion == artifact.ExtensionVersion &&
		snapshot.ArtifactDigest == artifact.PackageDigest && snapshot.VersionID == artifact.VersionID
}

func protocolV2QueryProviderDigest(contribution queryregistry.QueryContribution) string {
	material := queryregistry.SchemaVersion + "\x00protocol-v2-query-provider\x00" +
		contribution.ID + "\x00" + contribution.ContractVersion + "\x00" +
		contribution.PlanVersion + "\x00" + contribution.ResultSchema + "\x00" +
		contribution.Handler + "\x00" + contribution.Artifact.ExtensionID + "\x00" +
		contribution.Artifact.ExtensionVersion + "\x00" + contribution.Artifact.PackageDigest + "\x00" +
		fmt.Sprintf("%d", contribution.Artifact.VersionID) + "\x00" + contribution.Artifact.RuntimeInstanceID
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}
