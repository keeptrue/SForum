package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

type ExactPluginGuardRuntime interface {
	InspectRuntimeInstance(extensionsruntime.RuntimeInstanceIdentity) (extensionsruntime.RuntimeInstanceSnapshot, error)
	AcquireRuntimeCall(context.Context, extensionsruntime.RuntimeInstanceIdentity, extensionsruntime.RuntimeCallClass) (*extensionsruntime.RuntimeAdmissionLease, error)
	InvokeGuardInstance(context.Context, extensionsruntime.RuntimeInstanceIdentity, extensionsruntime.ProtocolV2GuardRequest) error
}

type RuntimePluginRouteGuardEvaluator struct {
	runtime ExactPluginGuardRuntime
	policy  ExtensionGuardPolicy
}

func NewRuntimePluginRouteGuardEvaluator(
	runtime ExactPluginGuardRuntime,
	policy ExtensionGuardPolicy,
) *RuntimePluginRouteGuardEvaluator {
	return &RuntimePluginRouteGuardEvaluator{runtime: runtime, policy: policy}
}

func (e *RuntimePluginRouteGuardEvaluator) EvaluatePluginGuard(
	ctx context.Context,
	evaluation routes.PluginGuardEvaluation,
) error {
	if e == nil || e.runtime == nil || e.policy == nil || ctx == nil {
		return preInvocationPluginGuardFailure(nil)
	}
	step := evaluation.Step
	artifact := step.Provider.Artifact
	wireAuthority, err := protocolV2RequestAuthority(evaluation.Authority)
	if err != nil {
		return preInvocationPluginGuardFailure(err)
	}
	rawAuthority := evaluation.Authority.Mode == routes.RequestAuthorityRaw
	lookup, ok := e.policy.Lookup(artifact.ExtensionID)
	if !ok || lookup.Revision == 0 || !lookup.Found || lookup.SafeMode || lookup.Entry.ExtensionID != artifact.ExtensionID ||
		lookup.Entry.ExtensionType != extensions.TypePlugin || lookup.Entry.Status != extensions.StatusEnabled ||
		lookup.Entry.Version != artifact.ExtensionVersion || lookup.Entry.PackageDigest != artifact.PackageDigest ||
		(lookup.Entry.CurrentTrustRequired || rawAuthority) && !lookup.Entry.CurrentArtifactTrusted {
		return preInvocationPluginGuardFailure(nil)
	}
	// 更正（2026-10-16，决定 R1）：这里的「空绑定即放行」原本是 Host 守卫
	// `core.guard.raw_request` 的语义——它在**零守卫代码**下放行，并且（在该守卫铸造
	// RequestAuthorityRaw 时）让 `protocolV2AuthorizedRequestHeaders` 不再剔除
	// cookie/authorization/x-api-key/x-auth-token，于是凭证被转发给插件处理器。
	// 运维方判定这是漏洞：raw 权限只能来自包内**显式声明**的 kind=raw_request 守卫。
	//
	// 因此这里改成 fail closed：携带已退役守卫的步骤直接拒绝（生产链已在
	// `Routes/core_guard_authorizer.go` 的 validPluginGuardBinding 处拦下它，这里是纵深防御，
	// 避免任何未来调用方把该求值器当成「照单确认传入权限」的盖章）。
	if step.Guard == extensionmanifest.GuardCoreRaw || step.PluginGuard.ID == "" {
		return preInvocationPluginGuardFailure(nil)
	}
	identity := extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID,
	}
	snapshot, err := e.runtime.InspectRuntimeInstance(identity)
	if err != nil || !snapshot.Active || snapshot.Identity != identity ||
		snapshot.ExtensionVersion != artifact.ExtensionVersion || snapshot.ArtifactDigest != artifact.PackageDigest {
		return preInvocationPluginGuardFailure(err)
	}
	lease, err := e.runtime.AcquireRuntimeCall(ctx, identity, extensionsruntime.RuntimeCallGuard)
	if err != nil {
		return preInvocationPluginGuardFailure(err)
	}
	defer lease.Release()
	query, queryValues, err := exactProtocolV2RouteQuery(evaluation.Request.Query)
	if err != nil {
		return preInvocationPluginGuardFailure(err)
	}
	body, present, err := exactProtocolV2RouteBody(evaluation.Request.Body, step.RequestSchema, "request")
	if err != nil {
		return preInvocationPluginGuardFailure(err)
	}
	headers := make(stdhttp.Header)
	if err := copyRouteRequestHeaders(headers, evaluation.Request.Headers, evaluation.Authority); err != nil {
		return preInvocationPluginGuardFailure(err)
	}
	timeout := 3 * time.Second
	if step.TimeoutMS > 0 {
		timeout = time.Duration(step.TimeoutMS) * time.Millisecond
	}
	err = e.runtime.InvokeGuardInstance(lease.Context, identity, extensionsruntime.ProtocolV2GuardRequest{
		GuardID: step.PluginGuard.ID, GuardContractVersion: step.PluginGuard.ContractVersion,
		RouteID: step.RouteID, RouteContractVersion: step.ContractVersion,
		Method: evaluation.RequestMethod, Path: evaluation.RequestPath, Headers: headers,
		PathParameters: evaluation.Request.Params, QueryParameters: query, QueryParameterValues: queryValues,
		RequestSchema: step.RequestSchema, Body: body, BodyPresent: present,
		Authority: wireAuthority,
		Actor: extensionsruntime.NewProtocolV2RouteActor(
			evaluation.Request.ActorID, evaluation.Request.Authenticated, evaluation.Request.Permissions,
		),
		Timeout: timeout,
	})
	return mappedPluginGuardInvocationFailure(err)
}

func mappedPluginGuardInvocationFailure(err error) error {
	if err == nil {
		return nil
	}
	var callFailure *extensionsruntime.ProtocolV2GuardCallFailure
	if errors.As(err, &callFailure) {
		kind := routes.PluginGuardFailureUnavailable
		switch callFailure.Kind() {
		case extensionsruntime.ProtocolV2GuardFailureDenied:
			kind = routes.PluginGuardFailureDenied
		case extensionsruntime.ProtocolV2GuardFailureCrash:
			kind = routes.PluginGuardFailureCrash
		case extensionsruntime.ProtocolV2GuardFailureTimeout:
			kind = routes.PluginGuardFailureTimeout
		case extensionsruntime.ProtocolV2GuardFailureProtocol:
			kind = routes.PluginGuardFailureProtocol
		case extensionsruntime.ProtocolV2GuardFailureCanceled:
			kind = routes.PluginGuardFailureCanceled
		}
		return routes.NewPluginGuardFailure(kind, callFailure.RuntimeExecutionObserved())
	}
	// Compatibility runtimes may still return the legacy deny sentinel directly.
	if errors.Is(err, extensionsruntime.ErrProtocolV2GuardDenied) {
		return routes.NewPluginGuardFailure(routes.PluginGuardFailureDenied, true)
	}
	return preInvocationPluginGuardFailure(err)
}

func preInvocationPluginGuardFailure(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return routes.NewPluginGuardFailure(routes.PluginGuardFailureTimeout, false)
	case errors.Is(err, context.Canceled):
		return routes.NewPluginGuardFailure(routes.PluginGuardFailureCanceled, false)
	default:
		return routes.NewPluginGuardFailure(routes.PluginGuardFailureUnavailable, false)
	}
}

var _ routes.PluginGuardEvaluator = (*RuntimePluginRouteGuardEvaluator)(nil)
