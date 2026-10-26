package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func (r *componentCompositionRun) invokeContribution(
	ctx context.Context,
	plan ComponentResolvePlan,
	contribution ComponentContribution,
	props map[string]any,
	result map[string]any,
	children []ComponentRenderSegment,
	depth int,
) (componentCallOutcome, error) {
	policy, err := r.executor.componentPolicy(contribution)
	if err != nil {
		return componentCallOutcome{}, err
	}
	if contribution.SSRTemplate == "" {
		evidence := ComponentFallbackEvidence{
			ContributionID: contribution.ID, Action: contribution.Action,
			Reason: "ssr_template_unavailable", FailurePolicy: policy.FailurePolicy,
		}
		r.addTraceStep(plan.Target.ID, contribution, policy, "l2_skipped", evidence.Reason, 0)
		if policy.FailurePolicy == appevents.FailurePolicyFailClosed {
			return componentCallOutcome{}, fmt.Errorf("%w: %s has no SSR template", ErrComponentCompositionInvalid, contribution.ID)
		}
		return componentCallOutcome{fallback: &evidence}, nil
	}
	started := time.Now()
	response, callErr := r.callExactContribution(ctx, plan, contribution, props, result, children, depth, policy)
	duration := time.Since(started)
	if callErr == nil {
		r.addTraceStep(plan.Target.ID, contribution, policy, "succeeded", "", duration)
		return componentCallOutcome{response: response, applied: true}, nil
	}
	if errorsIsComponentStale(callErr) {
		r.addTraceStep(plan.Target.ID, contribution, policy, "rejected", componentFailureReason(callErr), duration)
		return componentCallOutcome{}, ErrComponentCompositionStale
	}
	if errors.Is(callErr, ErrComponentCompositionPermissionDenied) {
		r.addTraceStep(plan.Target.ID, contribution, policy, "denied", "permission_denied", duration)
		return componentCallOutcome{permissionDenied: true}, nil
	}
	if policy.FailurePolicy == appevents.FailurePolicyFailClosed {
		r.addTraceStep(plan.Target.ID, contribution, policy, "failed", componentFailureReason(callErr), duration)
		return componentCallOutcome{}, callErr
	}
	evidence := ComponentFallbackEvidence{
		ContributionID: contribution.ID, Action: contribution.Action,
		Reason: componentFailureReason(callErr), FailurePolicy: policy.FailurePolicy,
	}
	r.addTraceStep(plan.Target.ID, contribution, policy, "fallback", evidence.Reason, duration)
	return componentCallOutcome{fallback: &evidence}, nil
}

func (r *componentCompositionRun) callExactContribution(
	ctx context.Context,
	plan ComponentResolvePlan,
	contribution ComponentContribution,
	props map[string]any,
	result map[string]any,
	children []ComponentRenderSegment,
	depth int,
	policy ComponentCallPolicy,
) (ComponentRenderResponse, error) {
	if !r.executor.registry.admitComponentContribution(r.revision, plan.Target, contribution) {
		return ComponentRenderResponse{}, ErrComponentCompositionStale
	}
	held, err := r.acquireComponentAdmission(ctx, plan, contribution)
	if err != nil {
		return ComponentRenderResponse{}, err
	}
	leaseRetained := false
	defer func() {
		if !leaseRetained && held.lease != nil {
			held.lease.Release()
		}
	}()
	if err := validateComponentOutputBounds(
		children, props, result, r.executor.maxSegments, r.executor.maxOutputBytes,
	); err != nil {
		return ComponentRenderResponse{}, err
	}
	if err := r.executor.registry.ValidateProps(contribution, props); err != nil {
		return ComponentRenderResponse{}, fmt.Errorf("%w: contribution props: %v", ErrComponentCompositionInvalid, err)
	}
	if componentActionNeedsResult(contribution.Action) {
		if err := r.executor.registry.ValidateResult(contribution, result); err != nil {
			return ComponentRenderResponse{}, fmt.Errorf("%w: contribution input result: %v", ErrComponentCompositionInvalid, err)
		}
	}
	callProps, err := cloneComponentDocument(props, r.executor.maxOutputBytes)
	if err != nil {
		return ComponentRenderResponse{}, fmt.Errorf("renderer props: %w", err)
	}
	callResult, err := cloneComponentDocument(result, r.executor.maxOutputBytes)
	if err != nil {
		return ComponentRenderResponse{}, fmt.Errorf("renderer result: %w", err)
	}
	call := ComponentRenderCall{
		TargetID: plan.Target.ID, TargetContractVersion: plan.Target.ContractVersion,
		Contribution: cloneComponentContribution(contribution), Artifact: contribution.Artifact,
		Props: callProps, Result: callResult,
		Children: cloneComponentRenderSegments(children), Depth: depth,
	}
	response, ownership, err := r.executor.callComponentRenderer(
		ctx, policy.Timeout, held.lease, held.request,
		func(callCtx context.Context) (ComponentRenderResponse, error) {
			return r.executor.renderer.RenderComponent(callCtx, call)
		},
	)
	if err != nil {
		// A timed-out non-cooperative call owns the lease until its goroutine
		// really exits. callComponentRenderer has already transferred ownership.
		if ownership != nil {
			ownership.Decide(false)
			held.lease = nil
		}
		return ComponentRenderResponse{}, err
	}
	defer ownership.DecideAndWait(false)
	if !sameHookArtifact(response.Artifact, contribution.Artifact) {
		return ComponentRenderResponse{}, ErrComponentCompositionUnauthorized
	}
	if !r.executor.registry.admitComponentContribution(r.revision, plan.Target, contribution) {
		return ComponentRenderResponse{}, ErrComponentCompositionStale
	}
	if err := validateComponentAdmissionLease(ctx, held.lease); err != nil {
		return ComponentRenderResponse{}, err
	}
	if err := validateComponentFragmentBounds(response.Fragments, r.executor.maxSegments, r.executor.maxOutputBytes); err != nil {
		return ComponentRenderResponse{}, err
	}
	response, err = cloneComponentRenderResponse(response, r.executor.maxOutputBytes)
	if err != nil {
		return ComponentRenderResponse{}, fmt.Errorf("response clone: %w", err)
	}
	if err := validateComponentContributionResponse(r.executor.registry, contribution, response); err != nil {
		return ComponentRenderResponse{}, err
	}
	if err := preflightComponentSegmentExpansion(
		response.Fragments, nil, r.executor.maxSegments, r.executor.maxOutputBytes,
	); err != nil {
		return ComponentRenderResponse{}, err
	}
	// Validation can be non-trivial. Fence publication once more immediately
	// before the result becomes visible to the next composition boundary.
	if !r.executor.registry.admitComponentContribution(r.revision, plan.Target, contribution) {
		return ComponentRenderResponse{}, ErrComponentCompositionStale
	}
	if err := validateComponentAdmissionLease(ctx, held.lease); err != nil {
		return ComponentRenderResponse{}, err
	}
	ownership.DecideAndWait(true)
	r.holdComponentAdmission(held)
	leaseRetained = true
	return response, nil
}

func validateComponentContributionResponse(
	registry *ComponentRegistry,
	contribution ComponentContribution,
	response ComponentRenderResponse,
) error {
	switch contribution.Action {
	case extensionmanifest.ComponentActionFilterProps:
		if response.Document == nil {
			return fmt.Errorf("%w: props filter returned no document", ErrComponentCompositionInvalid)
		}
		if err := registry.ValidateProps(contribution, response.Document); err != nil {
			return fmt.Errorf("%w: contribution props result: %v", ErrComponentCompositionInvalid, err)
		}
	case extensionmanifest.ComponentActionWrap,
		extensionmanifest.ComponentActionReplace,
		extensionmanifest.ComponentActionFilterResult:
		if response.Document == nil {
			return fmt.Errorf("%w: result action returned no document", ErrComponentCompositionInvalid)
		}
		if err := registry.ValidateResult(contribution, response.Document); err != nil {
			return fmt.Errorf("%w: contribution result: %v", ErrComponentCompositionInvalid, err)
		}
	}
	if contribution.Action != extensionmanifest.ComponentActionFilterProps &&
		contribution.Action != extensionmanifest.ComponentActionFilterResult &&
		len(response.Fragments) == 0 {
		return fmt.Errorf("%w: render action returned no SSR fragments", ErrComponentCompositionInvalid)
	}
	return nil
}

func (r *componentCompositionRun) renderFallback(
	ctx context.Context,
	plan ComponentResolvePlan,
	props map[string]any,
	binding ComponentTargetBinding,
	depth int,
) ([]ComponentRenderSegment, map[string]any, error) {
	if binding.Fallback == nil {
		if plan.Target.Core {
			return nil, nil, fmt.Errorf("%w: Core target requires an SSR fallback", ErrComponentCompositionInvalid)
		}
		result := map[string]any{}
		if plan.Target.Provider == nil {
			if err := binding.Contract.ValidateResult(ctx, result); err != nil {
				return nil, nil, fmt.Errorf("%w: empty target result: %v", ErrComponentCompositionInvalid, err)
			}
		}
		return nil, result, nil
	}
	if !r.executor.registry.admitComponentRevision(r.revision) {
		return nil, nil, ErrComponentCompositionStale
	}
	fallbackProps, err := cloneComponentDocument(props, r.executor.maxOutputBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("fallback props: %w", err)
	}
	started := time.Now()
	response, _, err := r.executor.callComponentRenderer(
		ctx, r.executor.defaultTimeout, nil, ComponentRuntimeAdmissionRequest{},
		func(callCtx context.Context) (ComponentRenderResponse, error) {
			return binding.Fallback(callCtx, ComponentFallbackCall{
				TargetID: plan.Target.ID, TargetContractVersion: plan.Target.ContractVersion,
				Props: fallbackProps, Depth: depth,
			})
		},
	)
	duration := time.Since(started)
	if err != nil {
		r.addCoreTraceStep(plan.Target.ID, "failed", componentFailureReason(err), duration)
		return nil, nil, err
	}
	if response.Artifact != (HookArtifact{}) {
		return nil, nil, ErrComponentCompositionUnauthorized
	}
	if !r.executor.registry.admitComponentRevision(r.revision) {
		return nil, nil, ErrComponentCompositionStale
	}
	if err := validateComponentFragmentBounds(response.Fragments, r.executor.maxSegments, r.executor.maxOutputBytes); err != nil {
		return nil, nil, err
	}
	response, err = cloneComponentRenderResponse(response, r.executor.maxOutputBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("fallback response: %w", err)
	}
	result := response.Document
	if result == nil {
		result = map[string]any{}
	}
	if err := binding.Contract.ValidateResult(ctx, result); err != nil {
		return nil, nil, fmt.Errorf("%w: fallback result: %v", ErrComponentCompositionInvalid, err)
	}
	if err := preflightComponentSegmentExpansion(
		response.Fragments, nil, r.executor.maxSegments, r.executor.maxOutputBytes,
	); err != nil {
		return nil, nil, err
	}
	segments := coreComponentSegments(plan.Target, response.Fragments, depth)
	r.addCoreTraceStep(plan.Target.ID, "succeeded", "", duration)
	return segments, result, nil
}

func (r *componentCompositionRun) composeHiddenTarget(
	ctx context.Context,
	plan ComponentResolvePlan,
	props map[string]any,
	binding ComponentTargetBinding,
	hide ComponentContribution,
	policy ComponentCallPolicy,
	depth int,
) (composedComponentTarget, error) {
	segments, result, err := r.renderFallback(ctx, plan, props, binding, depth)
	if err != nil {
		return composedComponentTarget{}, err
	}
	reason := "hidden"
	if binding.Contract.RetainPrimaryContent && segmentsHavePrimaryContent(segments) {
		segments = retainPrimaryComponentSegments(segments)
		reason = "seo_content_retained"
	} else {
		segments = nil
	}
	evidence := ComponentFallbackEvidence{
		ContributionID: hide.ID, Action: hide.Action, Reason: reason,
	}
	addComponentFallbackEvidence(segments, []ComponentFallbackEvidence{evidence})
	r.addTraceStep(plan.Target.ID, hide, policy, "hidden", reason, 0)
	return composedComponentTarget{
		target: plan.Target, props: props, result: result, segments: segments, hidden: true,
	}, nil
}

func (r *componentCompositionRun) renderAdjacent(
	ctx context.Context,
	plan ComponentResolvePlan,
	contributions []ComponentContribution,
	props map[string]any,
	result map[string]any,
	depth int,
	pending *[]ComponentFallbackEvidence,
) ([]ComponentRenderSegment, error) {
	segments := make([]ComponentRenderSegment, 0, len(contributions))
	for _, contribution := range contributions {
		outcome, err := r.invokeContribution(ctx, plan, contribution, props, result, nil, depth)
		if err != nil {
			return nil, err
		}
		if !outcome.applied {
			*pending = appendFallback(*pending, outcome.fallback)
			continue
		}
		if outcome.response.Document != nil && !reflect.DeepEqual(outcome.response.Document, result) {
			if handled := r.handlePostCallFailure(plan, contribution, ErrComponentCompositionMutation, pending); handled != nil {
				return nil, handled
			}
			continue
		}
		candidate, segmentErr := componentSegmentsFromResponse(
			contribution, outcome.response, nil, depth,
			r.executor.maxSegments, r.executor.maxOutputBytes,
		)
		if segmentErr != nil {
			if handled := r.handlePostCallFailure(plan, contribution, segmentErr, pending); handled != nil {
				return nil, handled
			}
			continue
		}
		segments = append(segments, candidate...)
	}
	return segments, nil
}

type componentRendererResult struct {
	response ComponentRenderResponse
	err      error
}

type componentRendererOwnership struct {
	once     sync.Once
	decision chan bool
	done     chan struct{}
}

func (o *componentRendererOwnership) Decide(retain bool) {
	if o == nil {
		return
	}
	o.once.Do(func() { o.decision <- retain })
}

func (o *componentRendererOwnership) DecideAndWait(retain bool) {
	if o == nil {
		return
	}
	o.Decide(retain)
	<-o.done
}

func (e *ComponentCompositionExecutor) callComponentRenderer(
	ctx context.Context,
	timeout time.Duration,
	lease ComponentRuntimeAdmissionLease,
	request ComponentRuntimeAdmissionRequest,
	call func(context.Context) (ComponentRenderResponse, error),
) (ComponentRenderResponse, *componentRendererOwnership, error) {
	slots := e.callSlots
	if lease == nil {
		// Host fallbacks have an independent bounded lane so one stuck extension
		// cannot consume the capacity required to render safe Core output.
		slots = e.fallbackSlots
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return ComponentRenderResponse{}, nil, ctx.Err()
	default:
		return ComponentRenderResponse{}, nil, ErrComponentCompositionBusy
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	stopLeaseCancellation := func() bool { return true }
	if lease != nil {
		stopLeaseCancellation = context.AfterFunc(lease.Context(), cancel)
	}
	defer cancel()
	defer stopLeaseCancellation()
	result := make(chan componentRendererResult, 1)
	ownership := &componentRendererOwnership{decision: make(chan bool, 1), done: make(chan struct{})}
	go func() {
		current := componentRendererResult{}
		defer func() {
			if recovered := recover(); recovered != nil {
				current.response = ComponentRenderResponse{}
				current.err = fmt.Errorf("%w: %v", ErrComponentCompositionCrash, recovered)
			}
			result <- current
			if lease != nil && !<-ownership.decision {
				lease.Release()
			}
			<-slots
			close(ownership.done)
		}()
		current.response, current.err = call(callCtx)
	}()
	select {
	case <-callCtx.Done():
		ownership.Decide(false)
		if request.ContributionID != "" && e.terminator != nil {
			e.terminator.TerminateComponentCall(ComponentRendererTermination{
				Request: request, Cause: callCtx.Err(),
			})
		}
		if ctx.Err() != nil {
			return ComponentRenderResponse{}, ownership, ctx.Err()
		}
		if lease != nil && lease.Context().Err() != nil {
			return ComponentRenderResponse{}, ownership, fmt.Errorf(
				"%w: %v", ErrComponentCompositionUnauthorized, lease.Context().Err(),
			)
		}
		return ComponentRenderResponse{}, ownership, ErrComponentCompositionTimeout
	case current := <-result:
		return current.response, ownership, current.err
	}
}
