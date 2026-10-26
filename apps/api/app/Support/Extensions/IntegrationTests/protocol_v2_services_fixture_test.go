package extensionsruntime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	serviceE2EVersion           = "1.0.0"
	serviceE2ESharedUnaryID     = "e2e.shared.unary"
	serviceE2ESharedStreamID    = "e2e.shared.stream"
	serviceE2ERequestSchema     = "e2e.shared.request@1"
	serviceE2EResponseSchema    = "e2e.shared.response@1"
	serviceE2EConsumerHookName  = "e2e.consumer.probe"
	serviceE2EConsumerInput     = "e2e.consumer.hook-input@1"
	serviceE2EConsumerResult    = "e2e.consumer.hook-result@1"
	serviceE2EHelperEnvironment = "SFORUM_SERVICE_DISCOVERY_E2E"
	serviceE2ERoleEnvironment   = "SFORUM_SERVICE_DISCOVERY_ROLE"
	serviceE2EFlavorEnvironment = "SFORUM_SERVICE_DISCOVERY_FLAVOR"
)

type serviceE2ETrust struct{}

func (serviceE2ETrust) RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error) {
	return extensions.RuntimeTrustIdentity{TrustGrantID: "service-e2e", ImpactDigest: "service-e2e"}, nil
}

type serviceE2EReport struct {
	ListCount       int      `json:"listCount"`
	ListIDs         []string `json:"listIds,omitempty"`
	ListReason      string   `json:"listReason,omitempty"`
	ResolveProvider string   `json:"resolveProvider,omitempty"`
	ResolveVersion  string   `json:"resolveVersion,omitempty"`
	ResolveReason   string   `json:"resolveReason,omitempty"`
	UnaryProvider   string   `json:"unaryProvider,omitempty"`
	UnaryInstance   string   `json:"unaryInstance,omitempty"`
	UnaryValue      string   `json:"unaryValue,omitempty"`
	InvokeReason    string   `json:"invokeReason,omitempty"`
	StreamProviders []string `json:"streamProviders,omitempty"`
	StreamInstances []string `json:"streamInstances,omitempty"`
	StreamValues    []string `json:"streamValues,omitempty"`
	StreamReason    string   `json:"streamReason,omitempty"`
}

func (r *serviceE2EReport) Unmarshal(value string) error {
	return json.Unmarshal([]byte(value), r)
}

func TestProtocolV2ServiceDiscoveryE2EHelperProcess(t *testing.T) {
	if os.Getenv(serviceE2EHelperEnvironment) != "1" {
		return
	}
	switch os.Getenv(serviceE2ERoleEnvironment) {
	case "provider":
		definitions := []pluginv2sdk.ServiceDefinition{serviceE2EUnaryDefinition(os.Getenv(serviceE2EFlavorEnvironment))}
		if os.Getenv(serviceE2EFlavorEnvironment) == "low" {
			definitions = append(definitions, serviceE2EStreamDefinition())
		}
		registry, err := pluginv2sdk.NewServiceRegistry(definitions...)
		if err != nil {
			panic(err)
		}
		pluginv2sdk.Serve(pluginv2sdk.NewServer().WithServiceRegistry(registry))
		os.Exit(0)
	case "consumer":
		pluginv2sdk.Serve(&serviceE2EConsumer{Server: pluginv2sdk.NewServer()})
		os.Exit(0)
	default:
		t.Fatalf("unknown service discovery helper role %q", os.Getenv(serviceE2ERoleEnvironment))
	}
}

func serviceE2EUnaryDefinition(flavor string) pluginv2sdk.ServiceDefinition {
	return pluginv2sdk.ServiceDefinition{
		ServiceID: "e2e.provider." + flavor + ".unary", Version: serviceE2EVersion,
		RequestSchemaID: serviceE2ERequestSchema, ResponseSchemaID: serviceE2EResponseSchema,
		RequiredAuthority: []string{capabilities.UsersRead},
		Operations: []pluginv2sdk.ServiceOperation{
			{Name: "echo", Unary: serviceE2EUnaryHandler},
			{Name: "crash", Unary: serviceE2EUnaryHandler},
		},
	}
}

func serviceE2EStreamDefinition() pluginv2sdk.ServiceDefinition {
	return pluginv2sdk.ServiceDefinition{
		ServiceID: "e2e.provider.low.stream", Version: serviceE2EVersion,
		RequestSchemaID: serviceE2ERequestSchema, ResponseSchemaID: serviceE2EResponseSchema,
		RequiredAuthority: []string{capabilities.UsersRead},
		Operations:        []pluginv2sdk.ServiceOperation{{Name: "echo", Stream: serviceE2EStreamHandler}},
	}
}

func serviceE2EUnaryHandler(_ context.Context, call *pluginv2sdk.ServiceCall) (*protocolwire.TypedDocument, error) {
	if call.Operation == "crash" {
		os.Exit(37)
	}
	values := serviceE2EDocumentValues(call.Input)
	return serviceE2EResponseDocument(map[string]any{
		"provider": call.Context.GetExtension().GetExtensionId(),
		"instance": call.Context.GetExtension().GetInstanceId(),
		"value":    values["value"],
	})
}

func serviceE2EStreamHandler(stream *pluginv2sdk.ServiceStream) error {
	for {
		message, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		values := serviceE2EDocumentValues(message)
		if err := stream.Send(serviceE2EMustResponseDocument(map[string]any{
			"provider": stream.Open().GetContext().GetExtension().GetExtensionId(),
			"instance": stream.Open().GetContext().GetExtension().GetInstanceId(),
			"value":    values["value"],
		})); err != nil {
			return err
		}
	}
}

type serviceE2EConsumer struct {
	*pluginv2sdk.Server
}

func (s *serviceE2EConsumer) InvokeHook(ctx context.Context, request *pluginwire.HookRequest) (*pluginwire.HookResponse, error) {
	response := &pluginwire.HookResponse{Context: &protocolwire.ResponseContext{
		RequestId: request.GetContext().GetRequestId(), Extension: request.GetContext().GetExtension(),
	}}
	health, err := s.Server.Health(ctx, &protocolwire.HealthRequest{Context: request.GetContext()})
	if err != nil {
		return nil, err
	}
	if health.GetError() != nil {
		response.Error = health.GetError()
		return response, nil
	}
	if request.GetHookId() != "e2e.consumer.event.probe" || request.GetHookName() != serviceE2EConsumerHookName ||
		request.GetHookKind() != "filter" || request.GetContractVersion() != "e2e.consumer.event.probe@1" ||
		!serviceE2EDocumentMatches(request.GetPayload(), serviceE2EConsumerInput) {
		response.Error = &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "service_e2e.hook_contract_invalid", Message: "Service E2E consumer hook contract is invalid.",
		}
		return response, nil
	}
	host, err := s.Server.Host()
	if err != nil {
		return nil, err
	}
	action, _ := serviceE2EDocumentValues(request.GetPayload())["action"].(string)
	report, err := serviceE2ERunConsumerAction(ctx, host, request.GetContext(), action)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	result, err := structpb.NewStruct(map[string]any{"reason": "", "message": string(encoded)})
	if err != nil {
		return nil, err
	}
	response.Accepted = true
	response.Result = serviceE2ETypedDocument(serviceE2EConsumerResult, result)
	return response, nil
}

func serviceE2ERunConsumerAction(
	ctx context.Context,
	host *pluginv2sdk.Host,
	parent *protocolwire.RequestContext,
	action string,
) (serviceE2EReport, error) {
	report := serviceE2EReport{}
	if action == "probe" || action == "denied" {
		list, err := host.Services.List(ctx, &hostwire.ServiceListRequest{
			Context: host.RequestContext(parent), VersionConstraint: "^1.0.0",
		})
		if err != nil {
			return report, err
		}
		report.ListReason = list.GetError().GetReason()
		report.ListCount = len(list.GetServices())
		for _, service := range list.GetServices() {
			report.ListIDs = append(report.ListIDs, service.GetServiceId())
		}
	}
	if action == "probe" || action == "resolve" || action == "denied" {
		resolved, err := host.Services.Resolve(ctx, &hostwire.ServiceResolveRequest{
			Context: host.RequestContext(parent), ServiceId: serviceE2ESharedUnaryID, VersionConstraint: "^1.0.0",
		})
		if err != nil {
			return report, err
		}
		report.ResolveReason = resolved.GetError().GetReason()
		report.ResolveProvider = resolved.GetProviderExtensionId()
		report.ResolveVersion = resolved.GetService().GetVersion()
	}
	if action == "probe" || action == "invoke" || action == "crash" || action == "denied" {
		operation := "echo"
		value := "unary-value"
		if action == "crash" {
			operation = "crash"
			value = "crash"
		}
		invoked, err := host.Services.Invoke(ctx, &hostwire.ServiceInvokeRequest{
			Context: host.RequestContext(parent), ServiceId: serviceE2ESharedUnaryID,
			Version: serviceE2EVersion, Operation: operation, Input: serviceE2EDocument(value),
		})
		if err != nil {
			return report, err
		}
		report.InvokeReason = invoked.GetError().GetReason()
		output := serviceE2EDocumentValues(invoked.GetOutput())
		report.UnaryProvider, _ = output["provider"].(string)
		report.UnaryInstance, _ = output["instance"].(string)
		report.UnaryValue, _ = output["value"].(string)
	}
	if action == "probe" || action == "denied" {
		providers, instances, values, reason, err := serviceE2EConsumerStream(ctx, host, parent, action == "probe")
		if err != nil {
			return report, err
		}
		report.StreamProviders, report.StreamInstances = providers, instances
		report.StreamValues, report.StreamReason = values, reason
	}
	return report, nil
}

func serviceE2EConsumerStream(
	ctx context.Context,
	host *pluginv2sdk.Host,
	parent *protocolwire.RequestContext,
	sendMessages bool,
) ([]string, []string, []string, string, error) {
	stream, err := host.Services.Stream(ctx)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if err := stream.Send(&hostwire.ServiceStreamFrame{Frame: &hostwire.ServiceStreamFrame_Open{Open: &hostwire.ServiceStreamOpen{
		Context: host.RequestContext(parent), ServiceId: serviceE2ESharedStreamID,
		Version: serviceE2EVersion, Operation: "echo",
	}}}); err != nil {
		return nil, nil, nil, "", err
	}
	if sendMessages {
		for _, value := range []string{"stream-one", "stream-two"} {
			if err := stream.Send(&hostwire.ServiceStreamFrame{Frame: &hostwire.ServiceStreamFrame_Message{Message: serviceE2EDocument(value)}}); err != nil {
				return nil, nil, nil, "", err
			}
		}
	}
	if err := stream.CloseSend(); err != nil {
		return nil, nil, nil, "", err
	}
	var providers, instances, values []string
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return providers, instances, values, "", nil
		}
		if err != nil {
			return nil, nil, nil, "", err
		}
		if detail := frame.GetError(); detail != nil {
			return providers, instances, values, detail.GetReason(), nil
		}
		output := serviceE2EDocumentValues(frame.GetMessage())
		provider, _ := output["provider"].(string)
		instance, _ := output["instance"].(string)
		value, _ := output["value"].(string)
		providers, instances, values = append(providers, provider), append(instances, instance), append(values, value)
	}
}

func serviceE2EProviderExtension(t *testing.T, flavor string, priority int) extensions.Extension {
	t.Helper()
	id := "e2e.provider." + flavor
	services := []extensions.ManifestService{serviceE2EManifestService(id+".unary", serviceE2ESharedUnaryID, priority)}
	if flavor == "low" {
		services = append(services, serviceE2EManifestService(id+".stream", serviceE2ESharedStreamID, priority))
	}
	return serviceE2EExtension(t, id, "provider", flavor, services, nil, true)
}

func serviceE2EConsumerExtension(t *testing.T, allowed bool) extensions.Extension {
	t.Helper()
	events := []extensions.ManifestEvent{{
		ID: "e2e.consumer.event.probe", ContractVersion: "e2e.consumer.event.probe@1",
		Name: serviceE2EConsumerHookName, Kind: "filter", Handler: "e2e.consumer.probe",
		InputSchema: serviceE2EConsumerInput, ResultSchema: serviceE2EConsumerResult,
	}}
	extension := serviceE2EExtension(t, "e2e.consumer", "consumer", "consumer", nil, events, allowed)
	extension.Manifest.Dependencies = []extensions.ManifestDependency{
		{ID: "e2e.provider.low", Version: "^1.0.0", Kind: "optional"},
		{ID: "e2e.provider.high", Version: "^1.0.0", Kind: "optional"},
	}
	return extension
}

func serviceE2EExtension(
	t *testing.T,
	id, role, flavor string,
	services []extensions.ManifestService,
	events []extensions.ManifestEvent,
	usersRead bool,
) extensions.Extension {
	t.Helper()
	root := filepath.Join(t.TempDir(), id, serviceE2EVersion)
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := fmt.Sprintf("#!/bin/sh\n%s=1 %s=%s %s=%s exec %s -test.run=TestProtocolV2ServiceDiscoveryE2EHelperProcess -- \"$@\"\n",
		serviceE2EHelperEnvironment, serviceE2ERoleEnvironment, shellQuote(role),
		serviceE2EFlavorEnvironment, shellQuote(flavor), shellQuote(os.Args[0]))
	if err := os.WriteFile(filepath.Join(root, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(id + ":" + role + ":" + flavor))
	// Service discovery requires Host process capability extensions.call in
	// addition to any service-declared authority (e.g. users.read).
	grants := []extensions.CapabilityGrant{
		{Key: capabilities.HostAPI, Risk: capabilities.RiskLow},
		{Key: capabilities.ExtensionsCall, Risk: capabilities.RiskHigh},
	}
	if usersRead {
		grants = append(grants, extensions.CapabilityGrant{Key: capabilities.UsersRead, Risk: capabilities.RiskMedium})
	}
	return extensions.Extension{
		ID: id, Name: id, Version: serviceE2EVersion, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: hex.EncodeToString(digest[:]), PackagePath: root, CapabilityGrants: grants,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Version: serviceE2EVersion, Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
			},
			Events: events, Services: services,
		},
	}
}

func serviceE2EManifestService(id, target string, priority int) extensions.ManifestService {
	return extensions.ManifestService{
		ID: id, ContractVersion: id + "@1", Action: "replace", TargetID: target,
		Handler: id, RequestSchema: serviceE2ERequestSchema, ResponseSchema: serviceE2EResponseSchema,
		Priority: priority,
	}
}

func serviceE2EDocument(value string) *protocolwire.TypedDocument {
	message, err := structpb.NewStruct(map[string]any{"value": value})
	if err != nil {
		panic(err)
	}
	return serviceE2ETypedDocument(serviceE2ERequestSchema, message)
}

func serviceE2EResponseDocument(values map[string]any) (*protocolwire.TypedDocument, error) {
	message, err := structpb.NewStruct(values)
	if err != nil {
		return nil, err
	}
	return serviceE2ETypedDocument(serviceE2EResponseSchema, message), nil
}

func serviceE2EMustResponseDocument(values map[string]any) *protocolwire.TypedDocument {
	document, err := serviceE2EResponseDocument(values)
	if err != nil {
		panic(err)
	}
	return document
}

func serviceE2ETypedDocument(schemaRef string, value *structpb.Struct) *protocolwire.TypedDocument {
	id, version, ok := strings.Cut(schemaRef, "@")
	if !ok {
		panic("invalid test schema " + schemaRef)
	}
	return &protocolwire.TypedDocument{SchemaId: id, SchemaVersion: version, Value: value}
}

func serviceE2EDocumentMatches(document *protocolwire.TypedDocument, schemaRef string) bool {
	id, version, ok := strings.Cut(schemaRef, "@")
	return ok && document != nil && document.GetSchemaId() == id && document.GetSchemaVersion() == version
}

func serviceE2EDocumentValues(document *protocolwire.TypedDocument) map[string]any {
	if document == nil || document.GetValue() == nil {
		return map[string]any{}
	}
	return document.GetValue().AsMap()
}
