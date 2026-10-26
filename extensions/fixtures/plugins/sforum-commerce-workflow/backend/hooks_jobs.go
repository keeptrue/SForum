package main

import (
	"context"
	"errors"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func evaluateOrder(ctx context.Context, call *pluginv2.HookCall) (*pluginv2.HookResult, error) {
	values := pluginv2.TypedDocumentValues(call.Payload)
	status, _ := values["status"].(string)
	// 测试触发：status=fail 失败；status=timeout 等待取消。
	switch strings.TrimSpace(status) {
	case "fail":
		return nil, errors.New("reference commerce evaluate failure")
	case "timeout":
		<-ctx.Done()
		return nil, context.Cause(ctx)
	}
	result, err := pluginv2.NewTypedDocument(orderEvaluateResult, map[string]any{
		"accepted": true,
		"status":   "approved",
	})
	if err != nil {
		return nil, err
	}
	patch, err := pluginv2.NewTypedDocument(orderEvaluatePatch, map[string]any{
		"status": "approved",
	})
	if err != nil {
		return nil, err
	}
	return &pluginv2.HookResult{Accepted: true, Result: result, Patch: patch}, nil
}

func observeOrderCreated(_ context.Context, call *pluginv2.HookCall) (*pluginv2.HookResult, error) {
	_ = pluginv2.TypedDocumentValues(call.Payload)
	return &pluginv2.HookResult{Accepted: true}, nil
}

func lookupOrder(_ context.Context, call *pluginv2.ServiceCall) (*protocolwire.TypedDocument, error) {
	values := pluginv2.TypedDocumentValues(call.Input)
	orderID, _ := values["orderId"].(string)
	if strings.TrimSpace(orderID) == "" {
		return nil, &pluginv2.ServiceError{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason:  "commerce.order_id_required",
			Message: "orderId is required",
		}
	}
	if orderID == "fail" {
		return nil, errors.New("reference commerce service failure")
	}
	return pluginv2.NewTypedDocument(serviceResponse, map[string]any{
		"orderId": orderID,
		"status":  "open",
		"total":   "19.90",
	})
}

func settleOrderJob(ctx context.Context, call *pluginv2.JobCall) error {
	if call == nil || call.Progress == nil {
		return errors.New("missing job progress stream")
	}
	values := pluginv2.TypedDocumentValues(call.Payload)
	if orderID, _ := values["orderId"].(string); orderID == "fail" {
		return errors.New("reference commerce settle failure")
	}
	if err := call.Progress.Send(&protocolwire.ProgressUpdate{
		StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		CompletedUnits: 1, TotalUnits: 2, Checkpoint: "settling",
	}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
	}
	return call.Progress.Send(&protocolwire.ProgressUpdate{
		StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
		CompletedUnits: 2, TotalUnits: 2, Checkpoint: "settled",
	})
}

func settleOnceCommand(_ context.Context, call *pluginv2.CommandCall) (*protocolwire.TypedDocument, error) {
	values := pluginv2.TypedDocumentValues(call.Input)
	orderID, _ := values["orderId"].(string)
	if strings.TrimSpace(orderID) == "" {
		orderID = "ord-1"
	}
	if orderID == "fail" {
		return nil, errors.New("reference commerce command settle failure")
	}
	// resultSchema 要求 accepted（additionalProperties=false）；不得泄漏内部 order 字段。
	return pluginv2.NewTypedDocument(commandResultSchema, map[string]any{
		"accepted": true,
		"message":  "settled:" + orderID,
	})
}
