package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type componentCompositionRun struct {
	executor   *ComponentCompositionExecutor
	revision   uint64
	trace      *ComponentCompositionTrace
	path       map[string]bool
	actor      ComponentActorAuthority
	admissions []componentHeldAdmission
}

type composedComponentTarget struct {
	target   ComponentTarget
	props    map[string]any
	result   map[string]any
	segments []ComponentRenderSegment
	hidden   bool
}

type componentCallOutcome struct {
	response         ComponentRenderResponse
	applied          bool
	permissionDenied bool
	fallback         *ComponentFallbackEvidence
}

func (e *ComponentCompositionExecutor) Compose(
	ctx context.Context,
	request ComponentCompositionRequest,
) (result ComponentCompositionResult, err error) {
	trace := e.newTrace(request)
	started := time.Now()
	defer func() {
		trace.DurationMicros = time.Since(started).Microseconds()
		if err != nil {
			trace.Status = "failed"
			trace.Error = componentFailureReason(err)
		} else {
			trace.Status = "succeeded"
		}
		e.recordTrace(*trace)
	}()
	if e == nil || ctx == nil || e.registry == nil || e.renderer == nil {
		return ComponentCompositionResult{}, ErrComponentCompositionInvalid
	}
	request.TargetID = strings.TrimSpace(request.TargetID)
	request.TargetContractVersion = strings.TrimSpace(request.TargetContractVersion)
	if request.TargetID == "" || request.TargetContractVersion == "" {
		return ComponentCompositionResult{}, ErrComponentCompositionInvalid
	}
	plan, resolveErr := e.registry.resolveRuntimePlan(request.TargetID, request.TargetContractVersion)
	if resolveErr != nil {
		return ComponentCompositionResult{}, resolveErr
	}
	actor := cloneComponentActorAuthority(request.Actor)
	plan, resolveErr = e.authorizeComponentPlan(ctx, actor, plan)
	if resolveErr != nil {
		return ComponentCompositionResult{}, resolveErr
	}
	trace.Revision = plan.Revision
	if request.ExpectedRevision != 0 && request.ExpectedRevision != plan.Revision {
		return ComponentCompositionResult{}, ErrComponentCompositionStale
	}
	run := &componentCompositionRun{
		executor: e, revision: plan.Revision, trace: trace, path: make(map[string]bool),
		actor: actor,
	}
	defer run.releaseComponentAdmissions()
	composed, composeErr := run.composeTarget(ctx, plan, request.Props, request.Binding, 0)
	if composeErr != nil {
		return ComponentCompositionResult{}, composeErr
	}
	if ctx.Err() != nil {
		return ComponentCompositionResult{}, ctx.Err()
	}
	if !e.registry.admitComponentRevision(run.revision) {
		return ComponentCompositionResult{}, ErrComponentCompositionStale
	}
	// Bound the existing tree before cloning it. In particular, a nested wrap
	// cannot allocate another copy of an already-over-budget subtree here.
	if boundsErr := validateComponentOutputBounds(
		composed.segments, composed.props, composed.result,
		e.maxSegments, e.maxOutputBytes,
	); boundsErr != nil {
		return ComponentCompositionResult{}, boundsErr
	}
	segments := cloneComponentRenderSegments(composed.segments)
	assignComponentSegmentOrder(segments, new(int))
	props, cloneErr := cloneComponentDocument(composed.props, e.maxOutputBytes)
	if cloneErr != nil {
		return ComponentCompositionResult{}, fmt.Errorf("detached props: %w", cloneErr)
	}
	resultDocument, cloneErr := cloneComponentDocument(composed.result, e.maxOutputBytes)
	if cloneErr != nil {
		return ComponentCompositionResult{}, fmt.Errorf("detached result: %w", cloneErr)
	}
	if err := run.validateComponentAdmissions(ctx); err != nil {
		return ComponentCompositionResult{}, err
	}
	if err := run.validateComponentPermissions(ctx); err != nil {
		return ComponentCompositionResult{}, err
	}
	if err := run.validateComponentAdmissions(ctx); err != nil {
		return ComponentCompositionResult{}, err
	}
	if !e.registry.admitComponentRevision(run.revision) {
		return ComponentCompositionResult{}, ErrComponentCompositionStale
	}
	return ComponentCompositionResult{
		Revision: run.revision, Target: cloneComponentTarget(composed.target),
		Props: props, Result: resultDocument,
		Segments: segments, Hidden: composed.hidden, TraceID: trace.ID,
	}, nil
}

func (r *componentCompositionRun) composeTarget(
	ctx context.Context,
	plan ComponentResolvePlan,
	inputProps map[string]any,
	binding ComponentTargetBinding,
	depth int,
) (composedComponentTarget, error) {
	if depth >= r.executor.maxDepth {
		return composedComponentTarget{}, ErrComponentCompositionDepth
	}
	key := plan.Target.ID + "\x00" + plan.Target.ContractVersion
	if r.path[key] {
		return composedComponentTarget{}, ErrComponentCompositionCycle
	}
	if plan.Revision != r.revision || !r.executor.registry.admitComponentRevision(r.revision) {
		return composedComponentTarget{}, ErrComponentCompositionStale
	}
	r.path[key] = true
	defer delete(r.path, key)

	if err := validateComponentBinding(plan, binding); err != nil {
		return composedComponentTarget{}, err
	}
	props, err := cloneComponentDocument(inputProps, r.executor.maxOutputBytes)
	if err != nil {
		return composedComponentTarget{}, fmt.Errorf("props: %w", err)
	}
	if err := binding.Contract.ValidateProps(ctx, props); err != nil {
		return composedComponentTarget{}, fmt.Errorf("%w: target props: %v", ErrComponentCompositionInvalid, err)
	}

	contributions := partitionComponentContributions(plan.Contributions)
	if len(contributions.propsFilters) > 0 && len(binding.Contract.MutablePropsFields) == 0 ||
		len(contributions.resultFilters) > 0 && len(binding.Contract.MutableResultFields) == 0 {
		return composedComponentTarget{}, fmt.Errorf("%w: filters require explicit mutable fields", ErrComponentCompositionInvalid)
	}

	pendingFallback := make([]ComponentFallbackEvidence, 0)
	// Hide invokes no extension code, but its authority is still exact-artifact
	// executable trust. Keep the same admission lease through fallback cloning,
	// validation, and the final result-release fence.
	if len(contributions.hides) > 0 {
		hide := contributions.hides[0]
		policy, policyErr := r.executor.componentPolicy(hide)
		if policyErr != nil {
			return composedComponentTarget{}, policyErr
		}
		held, admissionErr := r.acquireComponentAdmission(ctx, plan, hide)
		if errors.Is(admissionErr, ErrComponentCompositionPermissionDenied) {
			r.addTraceStep(plan.Target.ID, hide, policy, "denied", "permission_denied", 0)
		} else if admissionErr == nil {
			r.holdComponentAdmission(held)
			return r.composeHiddenTarget(ctx, plan, props, binding, hide, policy, depth)
		} else if errorsIsComponentStale(admissionErr) || ctx.Err() != nil {
			return composedComponentTarget{}, admissionErr
		} else if policy.FailurePolicy == appevents.FailurePolicyFailClosed {
			return composedComponentTarget{}, admissionErr
		} else {
			evidence := ComponentFallbackEvidence{
				ContributionID: hide.ID, Action: hide.Action,
				Reason: componentFailureReason(admissionErr), FailurePolicy: policy.FailurePolicy,
			}
			pendingFallback = append(pendingFallback, evidence)
			r.addTraceStep(plan.Target.ID, hide, policy, "fallback", evidence.Reason, 0)
		}
	}

	result := map[string]any{}
	for _, contribution := range contributions.propsFilters {
		outcome, callErr := r.invokeContribution(ctx, plan, contribution, props, result, nil, depth)
		if callErr != nil {
			return composedComponentTarget{}, callErr
		}
		if !outcome.applied {
			pendingFallback = appendFallback(pendingFallback, outcome.fallback)
			continue
		}
		candidate := outcome.response.Document
		if err := enforceComponentMutableFields(props, candidate, binding.Contract.MutablePropsFields); err != nil {
			if handled := r.handlePostCallFailure(plan, contribution, err, &pendingFallback); handled != nil {
				return composedComponentTarget{}, handled
			}
			continue
		}
		if err := binding.Contract.ValidateProps(ctx, candidate); err != nil {
			if handled := r.handlePostCallFailure(plan, contribution, fmt.Errorf("target props: %w", err), &pendingFallback); handled != nil {
				return composedComponentTarget{}, handled
			}
			continue
		}
		props = candidate
	}

	segments, baseResult, err := r.renderFallback(ctx, plan, props, binding, depth)
	if err != nil {
		return composedComponentTarget{}, err
	}
	result = baseResult
	if plan.Target.Provider != nil {
		provider := *plan.Target.Provider
		outcome, callErr := r.invokeContribution(ctx, plan, provider, props, result, segments, depth)
		if callErr != nil {
			return composedComponentTarget{}, callErr
		}
		if !outcome.applied {
			if outcome.permissionDenied {
				return composedComponentTarget{}, ErrComponentCompositionPermissionDenied
			}
			pendingFallback = appendFallback(pendingFallback, outcome.fallback)
		} else {
			candidateResult, documentErr := componentResponseDocumentOrCurrent(
				outcome.response, result, r.executor.maxOutputBytes,
			)
			if documentErr != nil {
				if handled := r.handlePostCallFailure(plan, provider, documentErr, &pendingFallback); handled != nil {
					return composedComponentTarget{}, handled
				}
			} else if err := binding.Contract.ValidateResult(ctx, candidateResult); err != nil {
				if handled := r.handlePostCallFailure(plan, provider, fmt.Errorf("target result: %w", err), &pendingFallback); handled != nil {
					return composedComponentTarget{}, handled
				}
			} else {
				candidateSegments, segmentErr := componentSegmentsFromResponse(
					provider, outcome.response, nil, depth,
					r.executor.maxSegments, r.executor.maxOutputBytes,
				)
				if segmentErr != nil {
					if handled := r.handlePostCallFailure(plan, provider, segmentErr, &pendingFallback); handled != nil {
						return composedComponentTarget{}, handled
					}
				} else if binding.Contract.RetainPrimaryContent && segmentsHavePrimaryContent(segments) &&
					!segmentsHavePrimaryContent(candidateSegments) {
					if handled := r.handlePostCallFailure(
						plan, provider, ErrComponentCompositionSEO, &pendingFallback,
					); handled != nil {
						return composedComponentTarget{}, handled
					}
				} else {
					if len(candidateSegments) > 0 {
						segments = candidateSegments
					}
					result = candidateResult
				}
			}
		}
	}

	if plan.ReplaceWinner != nil {
		winner := *plan.ReplaceWinner
		outcome, callErr := r.invokeContribution(ctx, plan, winner, props, result, segments, depth)
		if callErr != nil {
			return composedComponentTarget{}, callErr
		}
		if !outcome.applied {
			pendingFallback = appendFallback(pendingFallback, outcome.fallback)
		} else {
			candidateSegments, segmentErr := componentSegmentsFromResponse(
				winner, outcome.response, nil, depth,
				r.executor.maxSegments, r.executor.maxOutputBytes,
			)
			if segmentErr != nil {
				if handled := r.handlePostCallFailure(plan, winner, segmentErr, &pendingFallback); handled != nil {
					return composedComponentTarget{}, handled
				}
			} else {
				candidateResult := outcome.response.Document
				if err := r.validateReplacement(ctx, plan, binding.Contract, winner, result, segments, candidateResult, candidateSegments); err != nil {
					if handled := r.handlePostCallFailure(plan, winner, err, &pendingFallback); handled != nil {
						return composedComponentTarget{}, handled
					}
				} else {
					segments, result = candidateSegments, candidateResult
				}
			}
		}
	}

	for _, contribution := range contributions.adds {
		childPlan, childErr := r.executor.registry.resolveRuntimePlan(contribution.ID, contribution.ContractVersion)
		if childErr != nil || childPlan.Revision != r.revision {
			return composedComponentTarget{}, ErrComponentCompositionStale
		}
		childPlan, childErr = r.executor.authorizeComponentPlan(ctx, r.actor, childPlan)
		if errors.Is(childErr, ErrComponentCompositionPermissionDenied) {
			continue
		}
		if childErr != nil {
			return composedComponentTarget{}, childErr
		}
		if r.executor.resolveTarget == nil {
			return composedComponentTarget{}, fmt.Errorf("%w: added target %s has no Host binding", ErrComponentCompositionInvalid, contribution.ID)
		}
		childBinding, bindingErr := r.executor.resolveTarget(ctx, childPlan.Target)
		if bindingErr != nil {
			return composedComponentTarget{}, fmt.Errorf("%w: added target binding: %v", ErrComponentCompositionInvalid, bindingErr)
		}
		child, childErr := r.composeTarget(ctx, childPlan, props, childBinding, depth+1)
		if childErr != nil {
			return composedComponentTarget{}, childErr
		}
		segments = append(segments, child.segments...)
	}

	for index := len(contributions.wraps) - 1; index >= 0; index-- {
		contribution := contributions.wraps[index]
		outcome, callErr := r.invokeContribution(ctx, plan, contribution, props, result, segments, depth)
		if callErr != nil {
			return composedComponentTarget{}, callErr
		}
		if !outcome.applied {
			pendingFallback = appendFallback(pendingFallback, outcome.fallback)
			continue
		}
		candidateSegments, segmentErr := componentSegmentsFromResponse(
			contribution, outcome.response, segments, depth,
			r.executor.maxSegments, r.executor.maxOutputBytes,
		)
		if segmentErr != nil {
			if handled := r.handlePostCallFailure(plan, contribution, segmentErr, &pendingFallback); handled != nil {
				return composedComponentTarget{}, handled
			}
			continue
		}
		candidateResult := outcome.response.Document
		if err := r.validateReplacement(ctx, plan, binding.Contract, contribution, result, segments, candidateResult, candidateSegments); err != nil {
			if handled := r.handlePostCallFailure(plan, contribution, err, &pendingFallback); handled != nil {
				return composedComponentTarget{}, handled
			}
			continue
		}
		segments, result = candidateSegments, candidateResult
	}

	before, err := r.renderAdjacent(ctx, plan, contributions.before, props, result, depth, &pendingFallback)
	if err != nil {
		return composedComponentTarget{}, err
	}
	after, err := r.renderAdjacent(ctx, plan, contributions.after, props, result, depth, &pendingFallback)
	if err != nil {
		return composedComponentTarget{}, err
	}
	segments = append(before, append(segments, after...)...)

	for _, contribution := range contributions.resultFilters {
		outcome, callErr := r.invokeContribution(ctx, plan, contribution, props, result, segments, depth)
		if callErr != nil {
			return composedComponentTarget{}, callErr
		}
		if !outcome.applied {
			pendingFallback = appendFallback(pendingFallback, outcome.fallback)
			continue
		}
		candidate := outcome.response.Document
		if err := enforceComponentMutableFields(result, candidate, binding.Contract.MutableResultFields); err != nil {
			if handled := r.handlePostCallFailure(plan, contribution, err, &pendingFallback); handled != nil {
				return composedComponentTarget{}, handled
			}
			continue
		}
		if err := binding.Contract.ValidateResult(ctx, candidate); err != nil {
			if handled := r.handlePostCallFailure(plan, contribution, fmt.Errorf("target result: %w", err), &pendingFallback); handled != nil {
				return composedComponentTarget{}, handled
			}
			continue
		}
		candidateSegments, segmentErr := componentSegmentsFromResponse(
			contribution, outcome.response, segments, depth,
			r.executor.maxSegments, r.executor.maxOutputBytes,
		)
		if segmentErr != nil {
			if handled := r.handlePostCallFailure(plan, contribution, segmentErr, &pendingFallback); handled != nil {
				return composedComponentTarget{}, handled
			}
			continue
		}
		if binding.Contract.RetainPrimaryContent && segmentsHavePrimaryContent(segments) &&
			len(candidateSegments) > 0 && !segmentsHavePrimaryContent(candidateSegments) {
			if handled := r.handlePostCallFailure(plan, contribution, ErrComponentCompositionSEO, &pendingFallback); handled != nil {
				return composedComponentTarget{}, handled
			}
			continue
		}
		result = candidate
		if len(candidateSegments) > 0 {
			segments = candidateSegments
		}
	}
	addComponentFallbackEvidence(segments, pendingFallback)
	return composedComponentTarget{
		target: plan.Target, props: props, result: result, segments: segments,
	}, nil
}

type componentContributionPhases struct {
	adds, before, after, wraps, hides, propsFilters, resultFilters []ComponentContribution
}

func partitionComponentContributions(values []ComponentContribution) componentContributionPhases {
	var result componentContributionPhases
	for _, contribution := range values {
		switch contribution.Action {
		case extensionmanifest.ComponentActionAdd:
			result.adds = append(result.adds, contribution)
		case extensionmanifest.ComponentActionBefore:
			result.before = append(result.before, contribution)
		case extensionmanifest.ComponentActionAfter:
			result.after = append(result.after, contribution)
		case extensionmanifest.ComponentActionWrap:
			result.wraps = append(result.wraps, contribution)
		case extensionmanifest.ComponentActionHide:
			result.hides = append(result.hides, contribution)
		case extensionmanifest.ComponentActionFilterProps:
			result.propsFilters = append(result.propsFilters, contribution)
		case extensionmanifest.ComponentActionFilterResult:
			result.resultFilters = append(result.resultFilters, contribution)
		}
	}
	return result
}

func (r *componentCompositionRun) validateReplacement(
	ctx context.Context,
	plan ComponentResolvePlan,
	contract ComponentCompositionContract,
	contribution ComponentContribution,
	currentResult map[string]any,
	currentSegments []ComponentRenderSegment,
	candidateResult map[string]any,
	candidateSegments []ComponentRenderSegment,
) error {
	if err := contract.ValidateResult(ctx, candidateResult); err != nil {
		return fmt.Errorf("target result: %w", err)
	}
	if r.executor.registry.componentContributionOwnedByTheme(r.revision, contribution) &&
		!reflect.DeepEqual(currentResult, candidateResult) {
		return fmt.Errorf("%w: theme changed target result", ErrComponentCompositionMutation)
	}
	if contract.RetainPrimaryContent && segmentsHavePrimaryContent(currentSegments) &&
		!segmentsHavePrimaryContent(candidateSegments) {
		return ErrComponentCompositionSEO
	}
	if !r.executor.registry.admitComponentContribution(r.revision, plan.Target, contribution) {
		return ErrComponentCompositionStale
	}
	return nil
}

func (r *componentCompositionRun) handlePostCallFailure(
	plan ComponentResolvePlan,
	contribution ComponentContribution,
	err error,
	pending *[]ComponentFallbackEvidence,
) error {
	if errorsIsComponentStale(err) {
		return ErrComponentCompositionStale
	}
	policy, policyErr := r.executor.componentPolicy(contribution)
	if policyErr != nil || policy.FailurePolicy == appevents.FailurePolicyFailClosed {
		if policyErr != nil {
			return policyErr
		}
		return err
	}
	evidence := ComponentFallbackEvidence{
		ContributionID: contribution.ID, Action: contribution.Action,
		Reason: componentFailureReason(err), FailurePolicy: policy.FailurePolicy,
	}
	*pending = append(*pending, evidence)
	r.addTraceStep(plan.Target.ID, contribution, policy, "fallback", evidence.Reason, 0)
	return nil
}

func (e *ComponentCompositionExecutor) componentPolicy(contribution ComponentContribution) (ComponentCallPolicy, error) {
	policy := ComponentCallPolicy{FailurePolicy: appevents.FailurePolicyFailOpen, Timeout: e.defaultTimeout}
	if e.resolvePolicy != nil {
		resolved := e.resolvePolicy(cloneComponentContribution(contribution))
		if resolved.FailurePolicy != "" {
			policy.FailurePolicy = resolved.FailurePolicy
		}
		if resolved.Timeout > 0 {
			policy.Timeout = resolved.Timeout
		}
	}
	if policy.FailurePolicy != appevents.FailurePolicyFailOpen &&
		policy.FailurePolicy != appevents.FailurePolicyFailClosed ||
		policy.Timeout <= 0 || policy.Timeout > e.maxTimeout {
		return ComponentCallPolicy{}, ErrComponentCompositionInvalid
	}
	return policy, nil
}

func componentResponseDocumentOrCurrent(
	response ComponentRenderResponse,
	current map[string]any,
	maximumBytes int,
) (map[string]any, error) {
	if response.Document == nil {
		return cloneComponentDocument(current, maximumBytes)
	}
	return response.Document, nil
}
