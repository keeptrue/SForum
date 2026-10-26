package main

import (
	"context"
	"errors"
	"log"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// P13 commerce extender: depends on sforum.commerce-workflow and extends hooks/services.

const (
	enrichHookID     = "sforum.commerce-workflow-ext.hook.order-enrich"
	orderEvaluateName = "sforum.commerce-workflow.order.evaluate"
	orderTargetID    = "sforum.commerce-workflow.hook.order-evaluate"
	orderInput       = "sforum.commerce-workflow.hook.order-evaluate.input@1"
	orderResult      = "sforum.commerce-workflow.hook.order-evaluate.result@1"
	orderPatch       = "sforum.commerce-workflow.hook.order-evaluate.result.patch@1"

	auditServiceID  = "sforum.commerce-workflow-ext.service.audit"
	auditVersion    = "1.0.0"
	auditRequest    = "sforum.commerce-workflow-ext.service.audit.request@1"
	auditResponse   = "sforum.commerce-workflow-ext.service.audit.response@1"
)

func main() {
	hooks, err := pluginv2.NewHookRegistry(pluginv2.HookDefinition{
		ID: enrichHookID, Name: orderEvaluateName, Kind: "filter",
		ContractVersion: "sforum.commerce-workflow.hook.order-evaluate@1",
		TargetID:        orderTargetID,
		Handler:         "sforum.commerce-workflow-ext.hook.order-enrich",
		InputSchema:     orderInput, ResultSchema: orderResult,
		Execution: "sync", FailurePolicy: "fail_closed", TimeoutMS: 1000,
		MutableFields: []string{"status"}, Execute: enrichOrder,
	})
	if err != nil {
		log.Fatalf("configure commerce extender hooks: %v", err)
	}
	services, err := pluginv2.NewServiceRegistry(pluginv2.ServiceDefinition{
		ServiceID: auditServiceID, Version: auditVersion,
		RequestSchemaID: auditRequest, ResponseSchemaID: auditResponse,
		Operations: []pluginv2.ServiceOperation{{Name: "record", Unary: recordAudit}},
	})
	if err != nil {
		log.Fatalf("configure commerce extender services: %v", err)
	}
	pluginv2.Serve(pluginv2.NewServer().
		WithHookRegistry(hooks).
		WithServiceRegistry(services),
	)
}

func enrichOrder(_ context.Context, call *pluginv2.HookCall) (*pluginv2.HookResult, error) {
	values := pluginv2.TypedDocumentValues(call.Payload)
	status, _ := values["status"].(string)
	if strings.TrimSpace(status) == "ext-fail" {
		return nil, errors.New("reference commerce extender failure")
	}
	// Extender 只能收紧：把 approved 标注为 audited。
	next := "audited"
	if status == "" {
		next = "pending-audit"
	}
	result, err := pluginv2.NewTypedDocument(orderResult, map[string]any{
		"accepted": true,
		"status":   next,
		"source":   "extender",
	})
	if err != nil {
		return nil, err
	}
	patch, err := pluginv2.NewTypedDocument(orderPatch, map[string]any{
		"status": next,
	})
	if err != nil {
		return nil, err
	}
	return &pluginv2.HookResult{Accepted: true, Result: result, Patch: patch}, nil
}

func recordAudit(_ context.Context, call *pluginv2.ServiceCall) (*protocolwire.TypedDocument, error) {
	values := pluginv2.TypedDocumentValues(call.Input)
	orderID, _ := values["orderId"].(string)
	if strings.TrimSpace(orderID) == "" {
		return nil, &pluginv2.ServiceError{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason:  "commerce.audit_order_required",
			Message: "orderId is required",
		}
	}
	return pluginv2.NewTypedDocument(auditResponse, map[string]any{
		"orderId": orderID,
		"audited": true,
	})
}
