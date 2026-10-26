package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

func (c *protocolV2Client) RunLifecycleContext(parent context.Context, invocation LifecycleInvocation) (LifecycleRunResult, error) {
	if c == nil || c.client == nil || c.identity == nil {
		return LifecycleRunResult{}, extensions.ErrRuntimeUnavailable
	}
	if parent == nil {
		return LifecycleRunResult{}, fmt.Errorf("%w: caller context is required", ErrInvalidLifecycleRun)
	}
	if err := validateLifecycleInvocation(invocation); err != nil {
		return LifecycleRunResult{}, err
	}
	planVersion, resultSchema, checkpointSchema, err := lifecycleOperationContract(c.lifecycle, invocation.Action)
	if err != nil {
		return LifecycleRunResult{}, err
	}
	if invocation.PlanVersion != planVersion {
		return LifecycleRunResult{}, fmt.Errorf("%w: plan version does not match the frozen manifest lifecycle contract", ErrInvalidLifecycleRun)
	}
	if _, _, err := protocolV2SchemaRef(resultSchema); err != nil {
		return LifecycleRunResult{}, fmt.Errorf("%w: invalid frozen progress schema: %v", ErrInvalidLifecycleRun, err)
	}
	if _, _, err := protocolV2SchemaRef(checkpointSchema); err != nil {
		return LifecycleRunResult{}, fmt.Errorf("%w: invalid frozen checkpoint schema: %v", ErrInvalidLifecycleRun, err)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	requestContext := c.requestContext(ctx, "lifecycle:"+invocation.StepID)
	// sdk/plugin/v2 depends on this Host package for transport configuration,
	// so the Host side uses the same generated RPC directly and applies the
	// stricter state-machine validation while consuming RunLifecycleStream.
	stream, err := c.client.RunLifecycle(ctx, &protocolv2.LifecycleRequest{
		Context: requestContext, Action: invocation.Action, PlanVersion: invocation.PlanVersion,
		StepId: invocation.StepID, Checkpoint: invocation.Checkpoint,
		Input: cloneLifecycleDocument(invocation.Input), DryRun: invocation.DryRun, Forced: invocation.Forced,
	})
	if err != nil {
		if ctx.Err() != nil {
			return LifecycleRunResult{}, ctx.Err()
		}
		return LifecycleRunResult{}, err
	}

	result := LifecycleRunResult{
		StepID: invocation.StepID, Checkpoint: invocation.Checkpoint, CheckpointSchema: checkpointSchema,
	}
	validator := lifecycleProgressValidator{stepID: invocation.StepID, requestContext: requestContext, resultSchema: resultSchema}
	for {
		update, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			if !validator.terminal {
				return result, fmt.Errorf("%w: step %q ended without a terminal update", ErrInvalidLifecycleStream, invocation.StepID)
			}
			if validator.remoteError != nil {
				return result, validator.remoteError
			}
			return result, nil
		}
		if recvErr != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, recvErr
		}
		progress, terminalError, validateErr := validator.accept(update)
		if validateErr != nil {
			return result, validateErr
		}
		if invocation.OnProgress != nil {
			if callbackErr := invocation.OnProgress(cloneLifecycleProgress(progress)); callbackErr != nil {
				return result, callbackErr
			}
		}
		result.Progress = append(result.Progress, progress)
		result.State = progress.State
		if progress.Checkpoint != "" {
			result.Checkpoint = progress.Checkpoint
		}
		if progress.Result != nil {
			result.Result = cloneLifecycleDocument(progress.Result)
		}
		if terminalError != nil {
			terminalError.Checkpoint = result.Checkpoint
			validator.remoteError = terminalError
		}
	}
}

type lifecycleProgressValidator struct {
	stepID         string
	requestContext *protocolv2.RequestContext
	resultSchema   string
	lastState      LifecycleProgressState
	completed      uint32
	total          uint32
	seen           bool
	terminal       bool
	remoteError    *LifecycleRemoteError
}

func (v *lifecycleProgressValidator) accept(update *protocolv2.ProgressUpdate) (LifecycleProgress, *LifecycleRemoteError, error) {
	if update == nil {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: step %q returned a nil update", ErrInvalidLifecycleStream, v.stepID)
	}
	if v.terminal {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: step %q emitted progress after its terminal update", ErrInvalidLifecycleStream, v.stepID)
	}
	if update.GetStepId() != v.stepID {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: expected step %q, got %q", ErrInvalidLifecycleStream, v.stepID, update.GetStepId())
	}
	if err := validateLifecycleResponseContext(update.GetContext(), v.requestContext); err != nil {
		return LifecycleProgress{}, nil, err
	}
	state := update.GetState()
	if !validLifecycleProgressState(state) {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: step %q returned state %s", ErrInvalidLifecycleStream, v.stepID, state)
	}
	if state == LifecycleProgressPlanned && v.seen && v.lastState != LifecycleProgressPlanned {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: step %q regressed to planned", ErrInvalidLifecycleStream, v.stepID)
	}
	if update.GetCompletedUnits() < v.completed || update.GetTotalUnits() < v.total {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: step %q progress counters regressed", ErrInvalidLifecycleStream, v.stepID)
	}
	if update.GetTotalUnits() > 0 && update.GetCompletedUnits() > update.GetTotalUnits() {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: step %q completed units exceed total units", ErrInvalidLifecycleStream, v.stepID)
	}
	terminal := isLifecycleTerminal(state)
	if terminal && state == LifecycleProgressSucceeded && update.GetTotalUnits() > 0 && update.GetCompletedUnits() != update.GetTotalUnits() {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: successful step %q did not complete every unit", ErrInvalidLifecycleStream, v.stepID)
	}
	if terminal && (state == LifecycleProgressFailed || state == LifecycleProgressCancelled) {
		if update.GetError() == nil || update.GetError().GetCode() == protocolv2.ErrorCode_ERROR_CODE_UNSPECIFIED || strings.TrimSpace(update.GetError().GetReason()) == "" {
			return LifecycleProgress{}, nil, fmt.Errorf("%w: terminal step %q has no typed error", ErrInvalidLifecycleStream, v.stepID)
		}
		if (state == LifecycleProgressCancelled && update.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_CANCELLED) ||
			(state == LifecycleProgressFailed && update.GetError().GetCode() == protocolv2.ErrorCode_ERROR_CODE_CANCELLED) {
			return LifecycleProgress{}, nil, fmt.Errorf("%w: terminal step %q state and error code disagree", ErrInvalidLifecycleStream, v.stepID)
		}
		if retry := update.GetError().GetRetryAfter(); retry != nil && !retry.IsValid() {
			return LifecycleProgress{}, nil, fmt.Errorf("%w: terminal step %q has an invalid retry time", ErrInvalidLifecycleStream, v.stepID)
		}
	} else if update.GetError() != nil && update.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNSPECIFIED {
		return LifecycleProgress{}, nil, fmt.Errorf("%w: non-failed step %q returned an error", ErrInvalidLifecycleStream, v.stepID)
	}
	if update.GetResult() != nil {
		if err := validateProtocolV2DocumentRef(update.GetResult(), v.resultSchema, "lifecycle progress result"); err != nil {
			return LifecycleProgress{}, nil, fmt.Errorf("%w: %v", ErrInvalidLifecycleStream, err)
		}
	}

	progress := LifecycleProgress{
		State: state, CompletedUnits: update.GetCompletedUnits(), TotalUnits: update.GetTotalUnits(),
		Checkpoint: update.GetCheckpoint(), Message: update.GetMessage(), Result: cloneLifecycleDocument(update.GetResult()),
	}
	v.seen = true
	v.lastState = state
	v.completed = update.GetCompletedUnits()
	v.total = update.GetTotalUnits()
	v.terminal = terminal
	if state == LifecycleProgressFailed || state == LifecycleProgressCancelled {
		return progress, lifecycleRemoteError(v.stepID, state, update.GetError()), nil
	}
	return progress, nil, nil
}

func validateLifecycleInvocation(invocation LifecycleInvocation) error {
	if !validLifecycleAction(invocation.Action) {
		return fmt.Errorf("%w: lifecycle action %s is unsupported", ErrInvalidLifecycleRun, invocation.Action)
	}
	if invocation.PlanVersion == "" || strings.TrimSpace(invocation.PlanVersion) != invocation.PlanVersion {
		return fmt.Errorf("%w: exact plan version is required", ErrInvalidLifecycleRun)
	}
	if _, _, err := protocolV2SchemaRef(invocation.PlanVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLifecycleRun, err)
	}
	if invocation.StepID == "" || strings.TrimSpace(invocation.StepID) != invocation.StepID {
		return fmt.Errorf("%w: stable step id is required", ErrInvalidLifecycleRun)
	}
	if invocation.Input != nil && (strings.TrimSpace(invocation.Input.GetSchemaId()) == "" || strings.TrimSpace(invocation.Input.GetSchemaVersion()) == "") {
		return fmt.Errorf("%w: lifecycle input requires a schema id and version", ErrInvalidLifecycleRun)
	}
	return nil
}

func validateLifecycleResponseContext(response *protocolv2.ResponseContext, request *protocolv2.RequestContext) error {
	if response == nil || response.GetRequestId() != request.GetRequestId() || !proto.Equal(response.GetExtension(), request.GetExtension()) || !proto.Equal(response.GetTrace(), request.GetTrace()) {
		return fmt.Errorf("%w: progress response context does not match the exact runtime request", ErrInvalidLifecycleStream)
	}
	if response.GetServerTime() == nil || !response.GetServerTime().IsValid() {
		return fmt.Errorf("%w: progress response has no valid server time", ErrInvalidLifecycleStream)
	}
	return nil
}

func lifecycleRemoteError(stepID string, state LifecycleProgressState, detail *protocolv2.ErrorDetail) *LifecycleRemoteError {
	result := &LifecycleRemoteError{
		StepID: stepID, State: state, Code: detail.GetCode(), Reason: detail.GetReason(), Message: detail.GetMessage(),
		Retryable: detail.GetRetryable(), Metadata: make(map[string]string, len(detail.GetMetadata())),
	}
	if retry := detail.GetRetryAfter(); retry != nil && retry.IsValid() {
		result.RetryAfter = retry.AsTime()
	}
	for key, value := range detail.GetMetadata() {
		result.Metadata[key] = value
	}
	return result
}

func validLifecycleAction(action LifecycleAction) bool {
	return action >= LifecycleActionInstallPlan && action <= LifecycleActionUninstallAfter
}

func validLifecycleProgressState(state LifecycleProgressState) bool {
	return state >= LifecycleProgressPlanned && state <= LifecycleProgressCancelled
}

func isLifecycleTerminal(state LifecycleProgressState) bool {
	return state == LifecycleProgressSucceeded || state == LifecycleProgressFailed || state == LifecycleProgressCancelled
}

var _ pluginLifecycleContextInvoker = (*protocolV2Client)(nil)
