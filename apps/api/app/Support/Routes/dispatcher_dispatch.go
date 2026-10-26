package routes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// dispatchSession 承载 Dispatch 各阶段之间的可变状态。
type dispatchSession struct {
	request                  DispatchRequest
	core                     CoreInvoker
	plan                     RouteExecutionPlan
	commit                   *RouteCommitObserver
	idempotencyLease         RouteIdempotencyLease
	preservePending          bool
	mutableReplay            bool
	replayBinding            RouteReplayBinding
	replayMutations          []RouteReplayRequestMutation
	response                 *DispatchResponse
	committingStep           int
	committingStarted        time.Time
	committingStage          InvocationStage
	committedAfterFailure    *RouteCommittedAfterFailure
	responseEligible         []bool
	sequence                 []routeInvocationExecution
	finalResponseContract    routeInvocationExecution
	hasFinalResponseContract bool
	finalResponseCheckpoint  *DispatchResponse
}

// Dispatch 是路由分发入口：resolve 计划 → 幂等租约 → 调用链 → finalize/记录。
func (d *Dispatcher) Dispatch(ctx context.Context, request DispatchRequest, core CoreInvoker) (DispatchResult, error) {
	plan, request, early, err := d.dispatchResolvePlan(ctx, request)
	if err != nil {
		return DispatchResult{}, err
	}
	if early != nil {
		return *early, nil
	}
	s := &dispatchSession{
		plan:    plan,
		request: request,
		core:    core,
	}
	if early, err := d.dispatchBeginIdempotency(ctx, s); early != nil || err != nil {
		if early != nil {
			return *early, err
		}
		return DispatchResult{}, err
	}
	if s.idempotencyLease != nil {
		defer func() {
			if !s.preservePending && s.commit != nil && !s.commit.ExecutionObserved() {
				_ = s.idempotencyLease.Abort(ctx)
			}
		}()
	}
	if err := d.dispatchInvokeSequence(ctx, s); err != nil {
		return DispatchResult{}, err
	}
	return d.dispatchFinalize(ctx, s)
}

func (d *Dispatcher) dispatchResolvePlan(ctx context.Context, request DispatchRequest) (RouteExecutionPlan, DispatchRequest, *DispatchResult, error) {
	if d == nil || d.plans == nil || ctx == nil {
		return RouteExecutionPlan{}, request, nil, ErrDispatchInvalid
	}
	request.Method = strings.ToUpper(strings.TrimSpace(request.Method))
	if request.Method == "" || strings.TrimSpace(request.Path) == "" {
		return RouteExecutionPlan{}, request, nil, ErrDispatchInvalid
	}
	plan, err := d.plans.BuildExecutionPlan(ctx, request.Method, request.Path)
	if err != nil {
		if errors.Is(err, ErrRouteNotFound) {
			return RouteExecutionPlan{}, request, &DispatchResult{}, nil
		}
		return RouteExecutionPlan{}, request, nil, err
	}
	if !plan.Valid() {
		return RouteExecutionPlan{}, request, nil, ErrInvalidExecutionPlan
	}
	chain := plan.Chain()
	if !dispatchPlanHasPluginStep(chain) {
		// Core-only requests stay entirely on Fiber's existing path. Capturing them
		// would silently turn downloads, streams, and protocol upgrades into buffers.
		return RouteExecutionPlan{}, request, &DispatchResult{}, nil
	}
	if plan.UnsafeMethod() && routeChainHasResponseModifiers(chain) && d.failures == nil {
		// Unsafe response modifiers may fail only after the handler has written.
		// Without the Host-owned audit/quarantine sink, executing the writer would
		// leave retries able to create a second writer with no durable incident.
		return RouteExecutionPlan{}, request, nil, fmt.Errorf("%w: unsafe response modifiers require a failure recorder", ErrDispatchTransport)
	}
	request.Params = plan.Params()
	request.hostMutatedParams = false
	request.Headers = cloneHTTPHeader(request.Headers)
	request.Body = append([]byte(nil), request.Body...)
	request.Permissions = cloneDispatchPermissions(request.Permissions)
	return plan, request, nil, nil
}

func (d *Dispatcher) dispatchBeginIdempotency(ctx context.Context, s *dispatchSession) (*DispatchResult, error) {
	var err error
	chain := s.plan.Chain()
	s.commit = NewRouteCommitObserver()
	s.preservePending = false
	s.mutableReplay = routeChainHasMutableRequestFields(chain)
	terminal := s.plan.Terminal()
	if terminal.Provider.Kind == ProviderPlugin {
		policy, policyExists, policyErr := resolvePlanRouteExecutionPolicy(s.plan, terminal, d.policies)
		if policyErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrDispatchIdempotencyUnavailable, policyErr)
		}
		if policyExists && policy.IdempotencyRequired {
			if terminal.Mode != extensionmanifest.RouteModeHTTP || d.idempotency == nil {
				return nil, ErrDispatchIdempotencyUnavailable
			}
			if routeChainHasCredentialMutableRequestFields(chain) {
				// Credential patches cannot be persisted and replaying the modifier would
				// violate the no-second-execution contract.
				return nil, ErrDispatchIdempotencyUnavailable
			}
			if s.mutableReplay {
				capability, ok := d.idempotency.(RouteMutationReplayCapability)
				if !ok || !capability.MutationReplayAvailable() {
					return nil, ErrDispatchIdempotencyUnavailable
				}
			}
			s.replayBinding, err = BuildRouteReplayBinding(s.plan, s.request)
			if err != nil {
				return nil, ErrDispatchIdempotencyKeyInvalid
			}
			var replay *RouteIdempotencyReplay
			s.idempotencyLease, replay, err = d.idempotency.Begin(ctx, s.plan, terminal, policy, s.request)
			if err != nil {
				return nil, err
			}
			if replay != nil {
				if s.mutableReplay && replay.Authorization == nil || !s.mutableReplay && replay.Authorization != nil {
					return nil, ErrDispatchIdempotencyUnavailable
				}
				validationResponse := cloneDispatchResponse(replay.Response)
				// 这是 Host 在验证完成后返回给客户端的传输证据，不能参与
				// 当前 guard 或响应 Schema 的授权输入。
				validationResponse.Headers.Del(routeIdempotencyReplayedHeader)
				if err := d.authorizeReplay(
					ctx, s.plan, s.request, &validationResponse, replay.Authorization, s.replayBinding,
					replay.ResponseContractKnown, replay.ResponseContract,
				); err != nil {
					return nil, err
				}
				result := DispatchResult{Handled: true, Response: cloneDispatchResponse(replay.Response)}
				return &result, nil
			}
			if s.idempotencyLease == nil {
				return nil, ErrDispatchIdempotencyUnavailable
			}
			// Abort defer 由 Dispatch 编排层注册，避免 begin 返回时过早释放租约。
		}
	}

	return nil, nil
}

func (d *Dispatcher) dispatchInvokeSequence(ctx context.Context, s *dispatchSession) error {
	var err error
	chain := s.plan.Chain()
	s.committingStep = -1
	s.responseEligible = make([]bool, len(chain))
	s.sequence, err = bufferedRouteInvocationSequence(s.plan)
	if err != nil {
		return err
	}
dispatchSequence:
	for _, execution := range s.sequence {
		index, stage := execution.index, execution.stage
		step := chain[index]
		stopAfterResponse := false
		if ctx.Err() != nil && s.response != nil {
			break dispatchSequence
		}
		if stage == InvocationStageResponse && pairedResponseStageAction(step.Action) && !s.responseEligible[index] {
			continue
		}
		started := time.Now()
		mutationBeforeDigest := ""
		authority, err := d.authorize(ctx, s.plan, index, step, s.request, s.response, stage, s.commit)
		if err != nil {
			if callerErr, canceled := routeObservedCallerCancellation(err); canceled {
				if stage == InvocationStageResponse && s.response != nil {
					break dispatchSequence
				}
				return callerErr
			}
			outcome, code, observed := classifyRouteGuardFailure(err)
			d.appendTrace(s.plan, index, step, stage, outcome, started, s.commit.State())
			if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, code, observed); event != nil {
				s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
				break dispatchSequence
			}
			return err
		}
		if d.schemas == nil && step.RequestSchema != "" {
			d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
			if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureRequestSchemaRejected, false); event != nil {
				s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
				break dispatchSequence
			}
			return fmt.Errorf("%w: s.request validator is unavailable", ErrDispatchSchema)
		}
		if d.schemas != nil && step.RequestSchema != "" {
			if err := d.schemas.ValidateRequest(ctx, step, s.request); err != nil {
				if preserveRouteResponseOnCallerCancellation(ctx, err, stage, s.response) {
					break dispatchSequence
				}
				d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
				if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureRequestSchemaRejected, false); event != nil {
					s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
					break dispatchSequence
				}
				return fmt.Errorf("%w: %v", ErrDispatchSchema, err)
			}
		}
		if s.idempotencyLease != nil && s.mutableReplay && stage == InvocationStageRequest {
			mutationBeforeDigest, err = routeReplayRequestDigest(s.request)
			if err != nil {
				return ErrDispatchIdempotencyUnavailable
			}
		}
		if stage == InvocationStageResponse {
			if s.response == nil {
				return fmt.Errorf("%w: s.response stage has no prior s.response", ErrDispatchTransport)
			}
			if d.schemas == nil && step.ResponseSchema != "" {
				d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
				if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureResponseSchemaRejected, false); event != nil {
					s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
					break dispatchSequence
				}
				return fmt.Errorf("%w: s.response validator is unavailable", ErrDispatchSchema)
			}
			if d.schemas != nil && step.ResponseSchema != "" {
				if err := d.schemas.ValidateResponse(ctx, step, s.request, *s.response); err != nil {
					if preserveRouteResponseOnCallerCancellation(ctx, err, stage, s.response) {
						break dispatchSequence
					}
					d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
					if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureResponseSchemaRejected, false); event != nil {
						s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
						break dispatchSequence
					}
					return fmt.Errorf("%w: %v", ErrDispatchSchema, err)
				}
				if s.hasFinalResponseContract && execution == s.finalResponseContract {
					value := cloneDispatchResponse(*s.response)
					s.finalResponseCheckpoint = &value
				}
			}
		}

		var invocation RouteInvocationResult
		if stage == InvocationStageHandler && step.Provider.Kind == ProviderCore ||
			stage == InvocationStageHandler && (step.Action == extensionmanifest.RouteActionAlias || step.Action == extensionmanifest.RouteActionRewrite) {
			if s.core == nil {
				d.appendTrace(s.plan, index, step, stage, RouteTraceTransportFailed, started, s.commit.State())
				return fmt.Errorf("%w: s.core handler is unavailable", ErrDispatchTransport)
			}
			value, callErr := invokeCoreWithCommitEvidence(ctx, step, s.request, s.core, s.commit)
			if callErr != nil {
				d.appendTrace(s.plan, index, step, stage, RouteTraceTransportFailed, started, s.commit.State())
				return callErr
			}
			invocation.Response = &value
		} else if stage == InvocationStageHandler && step.Action == extensionmanifest.RouteActionRedirect {
			location, locationErr := routeRedirectLocation(step)
			if locationErr != nil {
				return locationErr
			}
			invocation.Response = &DispatchResponse{Status: step.StatusCode, Headers: http.Header{"Location": []string{location}}}
		} else {
			invocation, err = d.invokePlugin(ctx, s.plan, index, step, stage, s.request, s.response, s.commit, authority)
			if err != nil {
				// Transport errors may arrive after a remote side effect or s.response began.
				// Advance the fence before evaluating any safe-method fallback.
				if invocation.SideEffectStarted {
					s.commit.SideEffectStarted()
				}
				if invocation.ResponseStarted {
					s.commit.ResponseStarted()
				}
				if preserveRouteResponseOnObservedCallerCancellation(err, stage, s.response) {
					break dispatchSequence
				}
				d.appendTrace(s.plan, index, step, stage, RouteTraceTransportFailed, started, s.commit.State())
				observed := invocation.SideEffectStarted || invocation.ResponseStarted
				if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureTransportFailed, observed); event != nil {
					s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
					break dispatchSequence
				}
				var fallback *DispatchResponse
				var fallbackErr error
				if stage != InvocationStageResponse {
					fallback, fallbackErr = d.fallback(ctx, s.plan, index, step, s.request, s.core, s.commit)
				}
				if fallbackErr != nil {
					return fallbackErr
				}
				if fallback != nil {
					s.response = fallback
					// The fallback owns this s.response; a plugin contract that never
					// produced it is not an applicable final-s.response contract.
					s.hasFinalResponseContract = false
					s.finalResponseCheckpoint = nil
					if s.idempotencyLease != nil && s.mutableReplay && stage == InvocationStageRequest {
						s.replayMutations, err = appendRouteReplayRequestMutation(s.replayBinding, s.replayMutations, RouteReplayRequestMutation{
							StepIndex: index, BeforeDigest: mutationBeforeDigest, AfterDigest: mutationBeforeDigest,
						})
						if err != nil {
							return ErrDispatchIdempotencyUnavailable
						}
					}
					d.appendTrace(s.plan, index, step, stage, RouteTraceFallbackUsed, started, s.commit.State())
					s.committingStep = index
					s.committingStarted = started
					s.committingStage = stage
					break dispatchSequence
				}
				if stage == InvocationStageRequest && s.plan.AllowsFallback(index, s.commit.State()) {
					if s.idempotencyLease != nil && s.mutableReplay {
						s.replayMutations, err = appendRouteReplayRequestMutation(s.replayBinding, s.replayMutations, RouteReplayRequestMutation{
							StepIndex: index, BeforeDigest: mutationBeforeDigest, AfterDigest: mutationBeforeDigest,
						})
						if err != nil {
							return ErrDispatchIdempotencyUnavailable
						}
					}
					d.appendTrace(s.plan, index, step, stage, RouteTraceFallbackUsed, started, s.commit.State())
					s.committingStep = index
					s.committingStarted = started
					s.committingStage = stage
					continue
				}
				return err
			}
		}

		if invocation.SideEffectStarted {
			s.commit.SideEffectStarted()
		}
		if invocation.ResponseStarted {
			s.commit.ResponseStarted()
		}
		if err := validateRouteInvocationResult(stage, invocation); err != nil {
			d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
			if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureResponseSchemaRejected, true); event != nil {
				s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
				break dispatchSequence
			}
			return err
		}
		switch stage {
		case InvocationStageRequest:
			// Required replay records every s.request stage, including an empty patch.
			// Run the same post-schema boundary on first execution and replay.
			if len(invocation.RequestPatch) != 0 || s.idempotencyLease != nil && s.mutableReplay {
				if planTerminalUsesFrozenPathParams(s.plan) && routeRequestPatchMutatesParams(invocation.RequestPatch) {
					d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
					return fmt.Errorf("%w: s.core-bound route params cannot be mutated after route selection", ErrDispatchSchema)
				}
				value, patchErr := applyRouteRequestPatch(
					s.request, invocation.RequestPatch, step.MutableRequestFields, rawMutationAuthority(authority),
				)
				if patchErr != nil {
					d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
					return fmt.Errorf("%w: %v", ErrDispatchSchema, patchErr)
				}
				if routeRequestPatchMutatesParams(invocation.RequestPatch) {
					value.hostMutatedParams = true
				}
				if d.schemas == nil && step.RequestSchema != "" {
					return fmt.Errorf("%w: s.request validator is unavailable", ErrDispatchSchema)
				}
				if d.schemas != nil && step.RequestSchema != "" {
					if err := d.schemas.ValidateRequest(ctx, step, value); err != nil {
						d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
						return fmt.Errorf("%w: %v", ErrDispatchSchema, err)
					}
				}
				s.request = value
			}
			if s.idempotencyLease != nil && s.mutableReplay {
				afterDigest, digestErr := routeReplayRequestDigest(s.request)
				if digestErr != nil {
					return ErrDispatchIdempotencyUnavailable
				}
				s.replayMutations, err = appendRouteReplayRequestMutation(s.replayBinding, s.replayMutations, RouteReplayRequestMutation{
					StepIndex: index, BeforeDigest: mutationBeforeDigest, AfterDigest: afterDigest,
					Operations: cloneRoutePatchOperations(invocation.RequestPatch),
				})
				if err != nil {
					return ErrDispatchIdempotencyUnavailable
				}
			}
		case InvocationStageHandler:
			value := cloneDispatchResponse(*invocation.Response)
			if d.schemas == nil && step.ResponseSchema != "" {
				d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
				if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureResponseSchemaRejected, true); event != nil {
					s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
					break dispatchSequence
				}
				return fmt.Errorf("%w: s.response validator is unavailable", ErrDispatchSchema)
			}
			if d.schemas != nil && step.ResponseSchema != "" {
				validationErr := d.schemas.ValidateResponse(ctx, step, s.request, value)
				if routeCallerCancellation(ctx, validationErr) {
					validationCtx, cancelValidation := routeResponseFinalizationContext(ctx, d.defaultTimeout)
					validationErr = d.schemas.ValidateResponse(validationCtx, step, s.request, value)
					cancelValidation()
					stopAfterResponse = validationErr == nil
				}
				if validationErr != nil {
					d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
					if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureResponseSchemaRejected, true); event != nil {
						s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
						break dispatchSequence
					}
					return fmt.Errorf("%w: %v", ErrDispatchSchema, validationErr)
				}
			}
			s.response = &value
		case InvocationStageResponse:
			if len(invocation.ResponsePatch) == 0 {
				break
			}
			value, patchErr := applyRouteResponsePatch(*s.response, invocation.ResponsePatch, step.MutableResponseFields)
			if patchErr != nil {
				d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
				if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureResponseSchemaRejected, true); event != nil {
					s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
					break dispatchSequence
				}
				return fmt.Errorf("%w: %v", ErrDispatchSchema, patchErr)
			}
			if d.schemas == nil && step.ResponseSchema != "" {
				return fmt.Errorf("%w: s.response validator is unavailable", ErrDispatchSchema)
			}
			if d.schemas != nil && step.ResponseSchema != "" {
				if err := d.schemas.ValidateResponse(ctx, step, s.request, value); err != nil {
					if preserveRouteResponseOnCallerCancellation(ctx, err, stage, s.response) {
						break dispatchSequence
					}
					d.appendTrace(s.plan, index, step, stage, RouteTraceSchemaRejected, started, s.commit.State())
					if event := d.committedAfterFailure(s.plan, index, step, stage, s.request, s.response, RouteFailureResponseSchemaRejected, true); event != nil {
						s.committedAfterFailure, s.committingStep, s.committingStarted = event, index, started
						break dispatchSequence
					}
					return fmt.Errorf("%w: %v", ErrDispatchSchema, err)
				}
			}
			s.response = &value
		}
		if stage != InvocationStageRequest && strings.TrimSpace(step.ResponseSchema) != "" && s.response != nil {
			s.finalResponseContract = execution
			s.hasFinalResponseContract = true
			value := cloneDispatchResponse(*s.response)
			s.finalResponseCheckpoint = &value
		}
		if stage == InvocationStageRequest && pairedResponseStageAction(step.Action) {
			s.responseEligible[index] = true
		}
		if step.Provider.Kind == ProviderPlugin {
			d.appendTrace(s.plan, index, step, stage, RouteTraceSucceeded, started, s.commit.State())
			s.committingStep = index
			s.committingStarted = started
			s.committingStage = stage
		}
		if stopAfterResponse || s.response != nil && ctx.Err() != nil {
			break dispatchSequence
		}
	}
	return nil
}

func (d *Dispatcher) dispatchFinalize(ctx context.Context, s *dispatchSession) (DispatchResult, error) {
	var err error
	chain := s.plan.Chain()
	terminal := s.plan.Terminal()
	if s.response == nil {
		return DispatchResult{}, fmt.Errorf("%w: chain produced no s.response", ErrDispatchTransport)
	}
	finalizationCtx, cancelFinalization := routeResponseFinalizationContext(ctx, d.defaultTimeout)
	defer cancelFinalization()
	if s.hasFinalResponseContract && s.committedAfterFailure == nil {
		if d.schemas == nil {
			return DispatchResult{}, fmt.Errorf("%w: s.response validator is unavailable", ErrDispatchSchema)
		}
		contractStep := chain[s.finalResponseContract.index]
		if err := d.schemas.ValidateResponse(finalizationCtx, contractStep, s.request, *s.response); err != nil {
			// A schema-less s.response modifier may only preserve the most recent
			// declared s.response contract. Unsafe routes retain the last s.response
			// that passed that contract and record the exact failing modifier.
			if s.finalResponseCheckpoint != nil && s.committingStep >= 0 {
				checkpoint := cloneDispatchResponse(*s.finalResponseCheckpoint)
				d.appendTrace(s.plan, s.committingStep, chain[s.committingStep], s.committingStage, RouteTraceSchemaRejected, s.committingStarted, s.commit.State())
				if event := d.committedAfterFailure(
					s.plan, s.committingStep, chain[s.committingStep], s.committingStage, s.request,
					&checkpoint, RouteFailureResponseSchemaRejected, true,
				); event != nil {
					s.committedAfterFailure = event
					s.response = &checkpoint
				} else {
					return DispatchResult{}, fmt.Errorf("%w: %v", ErrDispatchSchema, err)
				}
			} else {
				return DispatchResult{}, fmt.Errorf("%w: %v", ErrDispatchSchema, err)
			}
		}
	}
	switch terminal.Action {
	case extensionmanifest.RouteActionAlias:
		s.response.CanonicalPath = terminal.TargetPath
	case extensionmanifest.RouteActionRedirect:
		location, locationErr := routeRedirectLocation(terminal)
		if locationErr != nil {
			return DispatchResult{}, locationErr
		}
		s.response.Status = terminal.StatusCode
		s.response.CanonicalPath = location
		if s.response.Headers == nil {
			s.response.Headers = make(http.Header)
		}
		s.response.Headers.Set("Location", location)
	case extensionmanifest.RouteActionRewrite:
		s.response.CanonicalPath = s.plan.Path()
	}
	if !s.commit.Finalize() {
		return DispatchResult{}, ErrDispatchAlreadyCommitted
	}
	if s.committingStep >= 0 {
		if s.committedAfterFailure != nil {
			s.committingStage = s.committedAfterFailure.InvocationStage
		}
		d.appendTrace(s.plan, s.committingStep, chain[s.committingStep], s.committingStage, RouteTraceCommitted, s.committingStarted, s.commit.State())
	}
	if s.committedAfterFailure != nil {
		s.committedAfterFailure.CommitState = s.commit.State()
		d.failures.RecordCommittedAfterFailure(finalizationCtx, *s.committedAfterFailure)
	}
	if s.idempotencyLease != nil && s.response.Status >= http.StatusOK && s.response.Status < http.StatusMultipleChoices {
		// Complete 失败时保留 pending；客户端只能得到 fail-closed unavailable，
		// 不能在未知持久化结果后再次执行插件副作用。
		s.preservePending = true
		completion := RouteIdempotencyCompletion{
			Response: cloneDispatchResponse(*s.response), ResponseContractKnown: true,
		}
		if s.hasFinalResponseContract {
			completion.ResponseContract = newRouteReplayResponseContract(
				s.finalResponseContract, chain[s.finalResponseContract.index],
			)
		}
		if s.mutableReplay {
			completion.Authorization, err = newRouteReplayAuthorization(s.replayBinding, s.replayMutations)
			if err != nil || completion.Authorization == nil ||
				!routeReplayAuthorizationMatchesPlan(completion.Authorization, s.replayBinding, s.sequence) {
				return DispatchResult{}, ErrDispatchIdempotencyUnavailable
			}
		}
		if err := s.idempotencyLease.Complete(finalizationCtx, completion); err != nil {
			return DispatchResult{}, fmt.Errorf("%w: %v", ErrDispatchIdempotencyUnavailable, err)
		}
	}
	return DispatchResult{Handled: true, Response: cloneDispatchResponse(*s.response)}, nil
}
