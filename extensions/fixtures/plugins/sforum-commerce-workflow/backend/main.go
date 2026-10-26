package main

import (
	"log"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

// P13 commerce-workflow reference: production Route Runtime + custom guard +
// hooks/services/jobs/commands/lifecycle/stream 在 Protocol V2 子进程内真实执行。

func main() {
	server, err := newCommerceServer()
	if err != nil {
		log.Fatalf("configure commerce workflow: %v", err)
	}
	pluginv2.Serve(server)
}

func newCommerceServer() (*commerceServer, error) {
	hooks, err := pluginv2.NewHookRegistry(
		pluginv2.HookDefinition{
			ID: orderEvaluateHookID, Name: orderEvaluateName, Kind: "filter",
			ContractVersion: orderEvaluateHookID + "@1",
			Handler:         "sforum.commerce-workflow.hook.order-evaluate",
			InputSchema:     orderEvaluateInput, ResultSchema: orderEvaluateResult,
			Execution: "sync", FailurePolicy: "fail_closed", TimeoutMS: 1000,
			MutableFields: []string{"status"}, Execute: evaluateOrder,
		},
		pluginv2.HookDefinition{
			ID: orderCreatedHookID, Name: orderCreatedName, Kind: "observe",
			ContractVersion: orderCreatedHookID + "@1",
			Handler:         "sforum.commerce-workflow.hook.order-created",
			InputSchema:     orderCreatedInput,
			Execution:       "async", FailurePolicy: "fail_open", TimeoutMS: 2000,
			Execute: observeOrderCreated,
		},
	)
	if err != nil {
		return nil, err
	}
	services, err := pluginv2.NewServiceRegistry(pluginv2.ServiceDefinition{
		ServiceID: serviceID, Version: serviceVersion,
		RequestSchemaID: serviceRequest, ResponseSchemaID: serviceResponse,
		Operations: []pluginv2.ServiceOperation{{Name: "lookup", Unary: lookupOrder}},
	})
	if err != nil {
		return nil, err
	}
	jobs, err := pluginv2.NewJobRegistry(pluginv2.JobDefinition{
		ID: jobID, ContractVersion: jobID + "@1", Name: jobKind,
		Handler: jobHandler, PayloadSchema: jobPayload,
		RetryPolicy: "exponential", MaxAttempts: 5, ConcurrencyLimit: 1,
		Execute: settleOrderJob,
	})
	if err != nil {
		return nil, err
	}
	commands, err := pluginv2.NewCommandRegistry(pluginv2.CommandDefinition{
		ID: commandID, ContractVersion: commandID + "@1",
		Handler: commandHandler, Permission: "sforum.commerce-workflow.manage",
		InputSchema: commandInputSchema, ResultSchema: commandResultSchema,
		Description: "Run one commerce settlement cycle from the Host CLI/admin console.",
		RecoverySafe: true, TimeoutMS: 3000, Execute: settleOnceCommand,
	})
	if err != nil {
		return nil, err
	}
	store := newOrderStore()
	server := &commerceServer{
		Server: pluginv2.NewServer().
			WithHookRegistry(hooks).
			WithServiceRegistry(services).
			WithJobRegistry(jobs).
			WithCommandRegistry(commands),
		store: store,
	}
	// Lifecycle + SSE/stream 走 RuntimeStreams；unary HTTP 走 InvokeRoute。
	server.WithRuntimeStreams(pluginv2.RuntimeStreams{
		Lifecycle: server.runLifecycle,
		Route:     server.streamRoute,
	})
	return server, nil
}
