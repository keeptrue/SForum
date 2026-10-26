package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHookRegistryAllowsSameEventNameAndDispatchesByID(t *testing.T) {
	var seen []string
	registry, err := NewHookRegistry(
		HookDefinition{
			ID: "demo.hook.a", Name: "topic.before_create", Kind: "filter",
			ContractVersion: "demo.hook.a@1", Handler: "filterA",
			InputSchema: "demo.hook.input@1", ResultSchema: "demo.hook.result@1",
			MutableFields: []string{"title"}, Execution: "sync", FailurePolicy: "fail_closed", TimeoutMS: 1000,
			Execute: func(_ context.Context, call *HookCall) (*HookResult, error) {
				seen = append(seen, call.ID)
				result, err := NewTypedDocument("demo.hook.result@1", map[string]any{"ok": true})
				if err != nil {
					return nil, err
				}
				patch, err := NewTypedDocument("demo.hook.result.patch@1", map[string]any{"title": "clean"})
				if err != nil {
					return nil, err
				}
				return &HookResult{Accepted: true, Result: result, Patch: patch}, nil
			},
		},
		HookDefinition{
			ID: "demo.hook.b", Name: "topic.before_create", Kind: "filter",
			ContractVersion: "demo.hook.b@1", Handler: "filterB",
			InputSchema: "demo.hook.input@1", ResultSchema: "demo.hook.result@1",
			Execute: func(_ context.Context, call *HookCall) (*HookResult, error) {
				seen = append(seen, call.ID)
				result, err := NewTypedDocument("demo.hook.result@1", map[string]any{"ok": true})
				if err != nil {
					return nil, err
				}
				return &HookResult{Accepted: true, Result: result}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Definitions()) != 2 {
		t.Fatalf("expected two declarations for same event name, got %d", len(registry.Definitions()))
	}
	identity := familyTestIdentity()
	payload, err := NewTypedDocument("demo.hook.input@1", map[string]any{"title": "x"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.InvokeHook(context.Background(), &pluginwire.HookRequest{
		Context: familyTestContext(identity), HookId: "demo.hook.a", HookName: "topic.before_create",
		HookKind: "filter", ContractVersion: "demo.hook.a@1", Payload: payload, MutableFields: []string{"title"},
	})
	if err != nil || !first.GetAccepted() || first.GetError() != nil || first.GetPatch() == nil {
		t.Fatalf("hook a response=%#v err=%v", first, err)
	}
	second, err := registry.InvokeHook(context.Background(), &pluginwire.HookRequest{
		Context: familyTestContext(identity), HookId: "demo.hook.b", HookName: "topic.before_create",
		HookKind: "filter", ContractVersion: "demo.hook.b@1", Payload: payload,
	})
	if err != nil || !second.GetAccepted() || second.GetError() != nil {
		t.Fatalf("hook b response=%#v err=%v", second, err)
	}
	if len(seen) != 2 || seen[0] != "demo.hook.a" || seen[1] != "demo.hook.b" {
		t.Fatalf("dispatch by identity failed: %v", seen)
	}
}

func TestHookRegistryRejectsMutableFieldsDriftAndBareContract(t *testing.T) {
	if _, err := NewHookRegistry(HookDefinition{
		ID: "demo.hook.bare", Name: "topic.created", Kind: "observe",
		ContractVersion: "1", Handler: "h", InputSchema: "demo.in@1",
		Execute: func(context.Context, *HookCall) (*HookResult, error) { return &HookResult{Accepted: true}, nil },
	}); !errors.Is(err, ErrInvalidHookDefinition) {
		t.Fatalf("bare contract version must fail: %v", err)
	}
	if _, err := NewHookRegistry(HookDefinition{
		ID: "demo.hook.nohandler", Name: "topic.created", Kind: "observe",
		ContractVersion: "demo.hook.nohandler@1", InputSchema: "demo.in@1",
		Execute: func(context.Context, *HookCall) (*HookResult, error) { return &HookResult{Accepted: true}, nil },
	}); !errors.Is(err, ErrInvalidHookDefinition) {
		t.Fatalf("missing handler must fail: %v", err)
	}

	registry, err := NewHookRegistry(HookDefinition{
		ID: "demo.hook.filter", Name: "topic.before_create", Kind: "filter",
		ContractVersion: "demo.hook.filter@1", Handler: "filter",
		InputSchema: "demo.hook.input@1", ResultSchema: "demo.hook.result@1",
		MutableFields: []string{"title", "content"},
		Execute: func(_ context.Context, _ *HookCall) (*HookResult, error) {
			result, err := NewTypedDocument("demo.hook.result@1", map[string]any{"ok": true})
			if err != nil {
				return nil, err
			}
			return &HookResult{Accepted: true, Result: result}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := familyTestIdentity()
	payload, err := NewTypedDocument("demo.hook.input@1", map[string]any{"title": "x"})
	if err != nil {
		t.Fatal(err)
	}
	// Host 生产重放冻结列表；未声明字段 / 子集 / 乱序均拒绝。
	for _, fields := range [][]string{
		{"title", "content", "extra"},
		{"title"},
		{"content", "title"},
		nil,
	} {
		bad, err := registry.InvokeHook(context.Background(), &pluginwire.HookRequest{
			Context: familyTestContext(identity), HookId: "demo.hook.filter", HookName: "topic.before_create",
			HookKind: "filter", ContractVersion: "demo.hook.filter@1", Payload: payload, MutableFields: fields,
		})
		if err != nil || bad.GetAccepted() || bad.GetError().GetReason() != "hook.mutable_fields_mismatch" {
			t.Fatalf("mutable fields %#v expected mismatch, got %#v err=%v", fields, bad, err)
		}
	}
	ok, err := registry.InvokeHook(context.Background(), &pluginwire.HookRequest{
		Context: familyTestContext(identity), HookId: "demo.hook.filter", HookName: "topic.before_create",
		HookKind: "filter", ContractVersion: "demo.hook.filter@1", Payload: payload,
		MutableFields: []string{"title", "content"},
	})
	if err != nil || !ok.GetAccepted() || ok.GetError() != nil {
		t.Fatalf("exact mutable fields should pass: %#v err=%v", ok, err)
	}
}

func TestHookRegistryRejectsBadPatchSchemaAndDeadline(t *testing.T) {
	registry, err := NewHookRegistry(HookDefinition{
		ID: "demo.hook.filter", Name: "topic.before_create", Kind: "filter",
		ContractVersion: "demo.hook.filter@1", Handler: "filter",
		InputSchema: "demo.hook.input@1", ResultSchema: "demo.hook.result@1",
		Execute: func(_ context.Context, _ *HookCall) (*HookResult, error) {
			result, err := NewTypedDocument("demo.hook.result@1", map[string]any{"ok": true})
			if err != nil {
				return nil, err
			}
			patch, err := NewTypedDocument("demo.hook.patch@1", map[string]any{"title": "x"})
			if err != nil {
				return nil, err
			}
			return &HookResult{Accepted: true, Result: result, Patch: patch}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := familyTestIdentity()
	payload, err := NewTypedDocument("demo.hook.input@1", map[string]any{"title": "x"})
	if err != nil {
		t.Fatal(err)
	}
	badPatch, err := registry.InvokeHook(context.Background(), &pluginwire.HookRequest{
		Context: familyTestContext(identity), HookId: "demo.hook.filter", HookName: "topic.before_create",
		HookKind: "filter", ContractVersion: "demo.hook.filter@1", Payload: payload,
	})
	if err != nil || badPatch.GetAccepted() || badPatch.GetError().GetReason() != "hook.schema_mismatch" {
		t.Fatalf("expected patch schema mismatch, got %#v err=%v", badPatch, err)
	}
	expired := familyTestContext(identity)
	expired.Deadline = timestamppb.New(time.Now().Add(-time.Second))
	deadline, err := registry.InvokeHook(context.Background(), &pluginwire.HookRequest{
		Context: expired, HookId: "demo.hook.filter", HookName: "topic.before_create",
		HookKind: "filter", ContractVersion: "demo.hook.filter@1", Payload: payload,
	})
	if err != nil || deadline.GetError().GetReason() != "hook.deadline_expired" {
		t.Fatalf("expected deadline expired, got %#v err=%v", deadline, err)
	}
}

func TestProviderRegistryAcceptsOnlyInvoke(t *testing.T) {
	providers, err := NewProviderRegistry(ProviderDefinition{
		ID: "demo.provider", Slot: "mail.provider", ContractVersion: "demo.provider@1",
		Label: "Demo mail", Handler: "deliver",
		RequestSchema: "demo.provider.in@1", ResponseSchema: "demo.provider.out@1",
		Execute: func(_ context.Context, call *ProviderCall) (*protocolwire.TypedDocument, error) {
			if call.Operation != VersionedProviderOperationInvoke {
				return nil, errors.New("unexpected operation")
			}
			return NewTypedDocument("demo.provider.out@1", map[string]any{"ok": true})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewTypedDocument("demo.provider.in@1", map[string]any{"to": "a@b.c"})
	if err != nil {
		t.Fatal(err)
	}
	identity := familyTestIdentity()
	ok, err := providers.ProviderCall(context.Background(), &pluginwire.ProviderCallRequest{
		Context: familyTestContext(identity), SlotId: "mail.provider", ContractVersion: "demo.provider@1",
		Operation: VersionedProviderOperationInvoke, Input: input, DeclarationId: "demo.provider",
	})
	if err != nil || ok.GetError() != nil {
		t.Fatalf("invoke response=%#v err=%v", ok, err)
	}
	for _, operation := range []string{"send", "probe", "call", ""} {
		denied, err := providers.ProviderCall(context.Background(), &pluginwire.ProviderCallRequest{
			Context: familyTestContext(identity), SlotId: "mail.provider", ContractVersion: "demo.provider@1",
			Operation: operation, Input: input, DeclarationId: "demo.provider",
		})
		if err != nil || denied.GetError() == nil || denied.GetError().GetReason() != "provider.operation_not_invoke" {
			t.Fatalf("operation %q must be rejected, got %#v err=%v", operation, denied, err)
		}
	}
	// bare contractVersion 拒绝。
	if _, err := NewProviderRegistry(ProviderDefinition{
		ID: "p", Slot: "mail.provider", ContractVersion: "1", Label: "x", Handler: "h",
		RequestSchema: "a@1", ResponseSchema: "b@1",
		Execute: func(context.Context, *ProviderCall) (*protocolwire.TypedDocument, error) { return nil, nil },
	}); !errors.Is(err, ErrInvalidProviderDefinition) {
		t.Fatalf("bare contract version must fail: %v", err)
	}
}

func TestHostInvokeProviderRejectsNonInvoke(t *testing.T) {
	host := &Host{}
	_, err := host.InvokeProvider(context.Background(), nil, "mail.provider", "demo.provider@1", "send", nil)
	if !errors.Is(err, ErrProviderOperationRejected) {
		t.Fatalf("expected operation rejected, got %v", err)
	}
	_, err = host.InvokeProvider(context.Background(), nil, "mail.provider", "demo.provider@1", "probe", nil)
	if !errors.Is(err, ErrProviderOperationRejected) {
		t.Fatalf("expected probe rejected, got %v", err)
	}
	_, err = host.InvokeProvider(context.Background(), nil, "mail.provider", "demo.provider@1", VersionedProviderOperationInvoke, nil)
	if !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("invoke with no broker should be host unavailable, got %v", err)
	}
}

func TestCommandAndJobContractVersions(t *testing.T) {
	if _, err := NewCommandRegistry(CommandDefinition{
		ID: "demo.cmd", ContractVersion: "1", Handler: "run",
		InputSchema: "demo.cmd.in@1", ResultSchema: "demo.cmd.out@1",
		Execute: func(context.Context, *CommandCall) (*protocolwire.TypedDocument, error) { return nil, nil },
	}); !errors.Is(err, ErrInvalidCommandDefinition) {
		t.Fatalf("bare command contract must fail: %v", err)
	}
	commands, err := NewCommandRegistry(CommandDefinition{
		ID: "demo.cmd", ContractVersion: "demo.cmd@1", Handler: "run", Permission: "demo.run",
		InputSchema: "demo.cmd.in@1", ResultSchema: "demo.cmd.out@1", Description: "demo",
		Execute: func(_ context.Context, _ *CommandCall) (*protocolwire.TypedDocument, error) {
			return NewTypedDocument("demo.cmd.out@1", map[string]any{"done": true})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmdInput, err := NewTypedDocument("demo.cmd.in@1", map[string]any{"arg": "1"})
	if err != nil {
		t.Fatal(err)
	}
	identity := familyTestIdentity()
	cmdResponse, err := commands.InvokeCommand(context.Background(), &pluginwire.CommandInvocationRequest{
		Context: familyTestContext(identity), CommandId: "demo.cmd", ContractVersion: "demo.cmd@1",
		Handler: "run", Input: cmdInput,
	})
	if err != nil || cmdResponse.GetError() != nil {
		t.Fatalf("command response=%#v err=%v", cmdResponse, err)
	}

	if _, err := NewJobRegistry(JobDefinition{
		ID: "demo.job.id", ContractVersion: "1", Name: "demo.job", Handler: "runJob",
		PayloadSchema: "demo.job.payload@1", RetryPolicy: "none",
		Execute: func(context.Context, *JobCall) error { return nil },
	}); !errors.Is(err, ErrInvalidJobDefinition) {
		t.Fatalf("bare job contract must fail: %v", err)
	}
	jobs, err := NewJobRegistry(JobDefinition{
		ID: "demo.job.id", ContractVersion: "demo.job@1", Name: "demo.job", Handler: "runJob",
		PayloadSchema: "demo.job.payload@1", RetryPolicy: "bounded",
		Execute: func(context.Context, *JobCall) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	// normalize defaults applied.
	defs := jobs.Definitions()
	if len(defs) != 1 || defs[0].MaxAttempts <= 0 || defs[0].ConcurrencyLimit <= 0 || defs[0].RetryDelaySeconds <= 0 {
		t.Fatalf("job defaults not applied: %#v", defs)
	}
	handler := jobs.StreamHandler()
	payload, err := NewTypedDocument("demo.job.payload@1", map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), &pluginwire.JobRequest{
		Context: familyTestContext(identity), JobId: "job-1", JobKind: "demo.job",
		PayloadVersion: "1", Payload: payload,
	}, nil); err != nil {
		t.Fatal(err)
	}
	err = handler(context.Background(), &pluginwire.JobRequest{
		Context: familyTestContext(identity), JobId: "job-1", JobKind: "demo.job", Payload: payload,
	}, nil)
	var runtimeErr *RuntimeStreamError
	if err == nil || !errors.As(err, &runtimeErr) || runtimeErr.Reason != "job.payload_version_required" {
		t.Fatalf("expected payload_version_required, got %v", err)
	}
}

func TestFamilyErrorSanitizesAndCarriesRetryMetadata(t *testing.T) {
	registry, err := NewHookRegistry(HookDefinition{
		ID: "demo.observe", Name: "topic.created", Kind: "observe",
		ContractVersion: "demo.observe@1", Handler: "onCreated", InputSchema: "demo.observe.in@1",
		Execute: func(context.Context, *HookCall) (*HookResult, error) {
			return nil, errors.New("secret implementation detail")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := NewTypedDocument("demo.observe.in@1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := registry.InvokeHook(context.Background(), &pluginwire.HookRequest{
		Context: familyTestContext(familyTestIdentity()), HookId: "demo.observe", HookName: "topic.created",
		HookKind: "observe", ContractVersion: "demo.observe@1", Payload: payload,
	})
	if err != nil || response.GetError() == nil ||
		response.GetError().GetMessage() != "Plugin hook handler failed." ||
		response.GetError().GetReason() != "hook.handler_failed" {
		t.Fatalf("expected sanitized error, got %#v err=%v", response, err)
	}

	retryAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	typed, err := NewHookRegistry(HookDefinition{
		ID: "demo.typed", Name: "topic.created", Kind: "observe",
		ContractVersion: "demo.typed@1", Handler: "onCreated", InputSchema: "demo.observe.in@1",
		Execute: func(context.Context, *HookCall) (*HookResult, error) {
			return nil, &FamilyError{
				Code: protocolwire.ErrorCode_ERROR_CODE_RATE_LIMITED, Reason: "demo.busy",
				Message: "Busy.", Retryable: true, RetryAfter: retryAt,
				Metadata: map[string]string{"queue": "default"},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	typedResp, err := typed.InvokeHook(context.Background(), &pluginwire.HookRequest{
		Context: familyTestContext(familyTestIdentity()), HookId: "demo.typed", HookName: "topic.created",
		HookKind: "observe", ContractVersion: "demo.typed@1", Payload: payload,
	})
	if err != nil || typedResp.GetError() == nil ||
		typedResp.GetError().GetReason() != "demo.busy" ||
		!typedResp.GetError().GetRetryable() ||
		typedResp.GetError().GetMetadata()["queue"] != "default" ||
		typedResp.GetError().GetRetryAfter() == nil ||
		!typedResp.GetError().GetRetryAfter().AsTime().Equal(retryAt) {
		t.Fatalf("typed FamilyError not preserved: %#v err=%v", typedResp, err)
	}
}

func TestJobRegistryBufconnStreamSuccessAndFreeze(t *testing.T) {
	var executed bool
	jobs, err := NewJobRegistry(JobDefinition{
		ID: "demo.job.id", ContractVersion: "demo.job@1", Name: "demo.job", Handler: "runJob",
		PayloadSchema: "demo.job.payload@1", RetryPolicy: "none",
		Execute: func(_ context.Context, call *JobCall) error {
			executed = true
			if call.Progress == nil || call.PayloadVersion != "1" {
				return fmt.Errorf("bad call: version=%s progress=%v", call.PayloadVersion, call.Progress != nil)
			}
			if err := call.Progress.Send(&protocolwire.ProgressUpdate{
				StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
				CompletedUnits: 1, TotalUnits: 2, Checkpoint: "half",
			}); err != nil {
				return err
			}
			return call.Progress.Send(&protocolwire.ProgressUpdate{
				StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
				CompletedUnits: 2, TotalUnits: 2, Checkpoint: "done",
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer().WithJobRegistry(jobs)
	handshake := validHandshakeRequest()
	client := startRuntimeStreamTestServer(t, server, handshake)

	// 握手后冻结：清空 registry 不得影响已绑定分发。
	server.WithJobRegistry(nil)

	payload, err := NewTypedDocument("demo.job.payload@1", map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := ExecuteJobStream(context.Background(), client, &pluginwire.JobRequest{
		Context: runtimeStreamTestContext(handshake, "job-stream-1"),
		JobId:   "job-1", JobKind: "demo.job", PayloadVersion: "1", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, state := range []protocolwire.ProgressState{
		protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
	} {
		update, err := stream.Recv()
		if err != nil || update.GetState() != state || update.GetContext().GetRequestId() != "job-stream-1" {
			t.Fatalf("progress %d = %#v err=%v", index, update, err)
		}
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("expected EOF after terminal progress, got %v", err)
	}
	if !executed {
		t.Fatal("JobRegistry handler was not executed over bufconn stream")
	}
}

func TestJobRegistryBufconnStreamPreservesFamilyError(t *testing.T) {
	retryAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	jobs, err := NewJobRegistry(JobDefinition{
		ID: "demo.job.id", ContractVersion: "demo.job@1", Name: "demo.job", Handler: "runJob",
		PayloadSchema: "demo.job.payload@1", RetryPolicy: "none",
		Execute: func(context.Context, *JobCall) error {
			return &FamilyError{
				Code: protocolwire.ErrorCode_ERROR_CODE_RATE_LIMITED, Reason: "demo.job_busy",
				Message: "Job queue is busy.", Retryable: true, RetryAfter: retryAt,
				Metadata: map[string]string{"queue": "default"},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer().WithJobRegistry(jobs)
	handshake := validHandshakeRequest()
	client := startRuntimeStreamTestServer(t, server, handshake)
	payload, err := NewTypedDocument("demo.job.payload@1", map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := ExecuteJobStream(context.Background(), client, &pluginwire.JobRequest{
		Context: runtimeStreamTestContext(handshake, "job-stream-family-error"),
		JobId:   "job-1", JobKind: "demo.job", PayloadVersion: "1", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := stream.Recv()
	detail := failed.GetError()
	if err != nil || failed.GetState() != protocolwire.ProgressState_PROGRESS_STATE_FAILED ||
		detail.GetCode() != protocolwire.ErrorCode_ERROR_CODE_RATE_LIMITED ||
		detail.GetReason() != "demo.job_busy" || detail.GetMessage() != "Job queue is busy." ||
		!detail.GetRetryable() || detail.GetMetadata()["queue"] != "default" ||
		detail.GetRetryAfter() == nil || !detail.GetRetryAfter().AsTime().Equal(retryAt) {
		t.Fatalf("job FamilyError not preserved: update=%#v err=%v", failed, err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("expected EOF after failed progress, got %v", err)
	}
}

func TestFilterPatchSchemaRefConvention(t *testing.T) {
	ref, ok := filterPatchSchemaRef("demo.hook.result@1")
	if !ok || ref != "demo.hook.result.patch@1" {
		t.Fatalf("patch schema ref = %q ok=%v", ref, ok)
	}
}

func TestSplitSchemaRefAndDocumentHelpers(t *testing.T) {
	id, version, ok := SplitSchemaRef("demo.payload@1")
	if !ok || id != "demo.payload" || version != "1" {
		t.Fatalf("split failed: %s %s %v", id, version, ok)
	}
	if validContractVersion("1") || !validContractVersion("demo.job@1") {
		t.Fatal("contract version validation drifted")
	}
}

func familyTestIdentity() *protocolwire.ExtensionIdentity {
	return &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest",
		TrustGrantId: "grant", RuntimeEpoch: 1, InstanceId: "instance-1",
	}
}

func familyTestContext(identity *protocolwire.ExtensionIdentity) *protocolwire.RequestContext {
	return &protocolwire.RequestContext{
		RequestId: "req-1", Locale: "und", Deadline: timestamppb.New(time.Now().Add(5 * time.Second)),
		Extension: identity,
	}
}
