package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

type protocolV2ServiceRegistry interface {
	ReplaceProtocolV2Services(string, []hostapi.ServiceRegistration) error
	PublishProtocolV2ServiceRuntime(hostapi.ServiceRuntimePublication) error
	PrepareProtocolV2ServiceRuntimeSet([]hostapi.ServiceRuntimePublication) (*hostapi.ServiceRuntimeSetTransaction, error)
	ProtocolV2ServiceRuntimeSetMatches([]hostapi.ServiceRuntimePublication) (bool, error)
	UnregisterProtocolV2Services(string)
	UnregisterProtocolV2ServiceInstance(string, string) bool
}

func (c *protocolV2Client) serviceRuntimePublication(
	extension extensions.Extension,
	registrations []hostapi.ServiceRegistration,
) (hostapi.ServiceRuntimePublication, error) {
	if c == nil || c.identity == nil {
		return hostapi.ServiceRuntimePublication{}, fmt.Errorf("protocol v2 services: exact runtime identity is unavailable")
	}
	publication := hostapi.ServiceRuntimePublication{
		ExtensionID: c.identity.GetExtensionId(), ExtensionVersion: c.identity.GetExtensionVersion(),
		ArtifactDigest: c.identity.GetArtifactDigest(), TrustGrantID: c.identity.GetTrustGrantId(),
		RuntimeEpoch: c.identity.GetRuntimeEpoch(), InstanceID: c.identity.GetInstanceId(),
		Registrations: registrations,
	}
	if publication.ExtensionID != extension.ID || publication.ArtifactDigest != extension.PackageDigest {
		return hostapi.ServiceRuntimePublication{}, fmt.Errorf("protocol v2 services: runtime identity does not match exact extension artifact")
	}
	for _, dependency := range extension.Manifest.Dependencies {
		switch dependency.Kind {
		case hostapi.ServiceDependencyRequired, hostapi.ServiceDependencyOptional:
			publication.Dependencies = append(publication.Dependencies, hostapi.ServiceDependency{
				ExtensionID: dependency.ID, Capability: dependency.Capability,
				VersionConstraint: dependency.Version, Kind: dependency.Kind,
			})
		case "provides":
			publication.Provides = append(publication.Provides, hostapi.ServiceCapability{
				ID: dependency.Capability, Version: dependency.Version,
			})
		}
	}
	return publication, nil
}

func protocolV2ServiceRegistryFor(registrar HostAPIRegistrar) protocolV2ServiceRegistry {
	if registrar == nil {
		return nil
	}
	registry, _ := registrar.(protocolV2ServiceRegistry)
	return registry
}

func (c *protocolV2Client) serviceRegistrations(extension extensions.Extension) ([]hostapi.ServiceRegistration, error) {
	c.serviceMu.RLock()
	descriptors := cloneV2Services(c.services)
	c.serviceMu.RUnlock()
	declarations := make(map[string]extensions.ManifestService, len(extension.Manifest.Services))
	for _, declaration := range extension.Manifest.Services {
		declarations[declaration.ID] = declaration
	}
	if len(descriptors) != len(declarations) {
		return nil, fmt.Errorf("protocol v2 services: handshake declares %d services, manifest declares %d", len(descriptors), len(declarations))
	}
	registrations := make([]hostapi.ServiceRegistration, 0, len(descriptors))
	seen := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		declaration, ok := declarations[descriptor.GetServiceId()]
		if !ok {
			return nil, fmt.Errorf("protocol v2 services: %q is not declared by the manifest", descriptor.GetServiceId())
		}
		if descriptor.GetRequestSchemaId() != declaration.RequestSchema || descriptor.GetResponseSchemaId() != declaration.ResponseSchema {
			return nil, fmt.Errorf("protocol v2 services: %q schema does not match the manifest", descriptor.GetServiceId())
		}
		version, err := semver.StrictNewVersion(strings.TrimSpace(descriptor.GetVersion()))
		if err != nil {
			return nil, fmt.Errorf("protocol v2 services: %q version %q is not strict SemVer", descriptor.GetServiceId(), descriptor.GetVersion())
		}
		contractMajor, err := serviceContractMajor(declaration.ContractVersion)
		if err != nil || version.Major() != contractMajor {
			return nil, fmt.Errorf("protocol v2 services: %q version %q does not match contract %q", descriptor.GetServiceId(), descriptor.GetVersion(), declaration.ContractVersion)
		}
		key := descriptor.GetServiceId() + "\x00" + descriptor.GetVersion()
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("protocol v2 services: duplicate %s@%s", descriptor.GetServiceId(), descriptor.GetVersion())
		}
		seen[key] = struct{}{}
		registrations = append(registrations, hostapi.ServiceRegistration{
			ExtensionID: extension.ID, InstanceID: c.identity.GetInstanceId(), Action: declaration.Action,
			TargetID: declaration.TargetID, Priority: declaration.Priority,
			Descriptor: proto.Clone(descriptor).(*protocolv2.ServiceDescriptor), Provider: c,
		})
	}
	return registrations, nil
}

func serviceContractMajor(contract string) (uint64, error) {
	_, raw, ok := strings.Cut(strings.TrimSpace(contract), "@")
	if !ok || raw == "" || strings.Contains(raw, "@") {
		return 0, fmt.Errorf("invalid service contract %q", contract)
	}
	return strconv.ParseUint(raw, 10, 64)
}

func (c *protocolV2Client) Invoke(
	ctx context.Context,
	caller *protocolv2.RequestContext,
	serviceID, version, operation string,
	input *protocolv2.TypedDocument,
) (*protocolv2.TypedDocument, *protocolv2.ErrorDetail, error) {
	callCtx, cancel := protocolV2Deadline(ctx, DefaultProtocolV2RequestTimeout)
	defer cancel()
	response, err := c.client.InvokeService(callCtx, &pluginv2.ServiceRequest{
		Context: c.forwardedServiceContext(callCtx, caller), ServiceId: serviceID,
		ServiceVersion: version, Operation: operation, Input: input,
	})
	if err != nil {
		return nil, nil, err
	}
	return response.GetOutput(), response.GetError(), nil
}

func (c *protocolV2Client) Stream(
	ctx context.Context,
	caller *protocolv2.RequestContext,
	serviceID, version, operation string,
	stream hostapi.ServiceBidiStream,
) (*protocolv2.ErrorDetail, error) {
	callCtx, cancel := protocolV2Deadline(ctx, DefaultProtocolV2RequestTimeout)
	defer cancel()
	provider, err := c.client.StreamService(callCtx)
	if err != nil {
		return nil, err
	}
	if err := provider.Send(&pluginv2.ServiceStreamFrame{Frame: &pluginv2.ServiceStreamFrame_Open{Open: &pluginv2.ServiceStreamOpen{
		Context: c.forwardedServiceContext(callCtx, caller), ServiceId: serviceID,
		ServiceVersion: version, Operation: operation, IdleTimeout: durationpb.New(DefaultProtocolV2RequestTimeout),
	}}}); err != nil {
		return nil, err
	}

	type relayResult struct {
		detail *protocolv2.ErrorDetail
		err    error
		input  bool
	}
	results := make(chan relayResult, 2)
	go func() {
		for {
			message, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				results <- relayResult{input: true, err: provider.CloseSend()}
				return
			}
			if err != nil {
				results <- relayResult{input: true, err: err}
				return
			}
			if err := provider.Send(&pluginv2.ServiceStreamFrame{Frame: &pluginv2.ServiceStreamFrame_Message{Message: message}}); err != nil {
				results <- relayResult{input: true, err: err}
				return
			}
		}
	}()
	go func() {
		for {
			frame, err := provider.Recv()
			if errors.Is(err, io.EOF) {
				results <- relayResult{}
				return
			}
			if err != nil {
				results <- relayResult{err: err}
				return
			}
			switch value := frame.GetFrame().(type) {
			case *pluginv2.ServiceStreamFrame_Message:
				if err := stream.Send(value.Message); err != nil {
					results <- relayResult{err: err}
					return
				}
			case *pluginv2.ServiceStreamFrame_Error:
				results <- relayResult{detail: value.Error}
				return
			default:
				results <- relayResult{err: fmt.Errorf("protocol v2 service stream returned an invalid frame")}
				return
			}
		}
	}()

	inputDone := false
	for {
		select {
		case result := <-results:
			if result.input && result.err == nil {
				inputDone = true
				continue
			}
			if !result.input || result.err != nil {
				return result.detail, result.err
			}
		case <-callCtx.Done():
			return nil, callCtx.Err()
		}
		if inputDone {
			continue
		}
	}
}

func (c *protocolV2Client) forwardedServiceContext(ctx context.Context, caller *protocolv2.RequestContext) *protocolv2.RequestContext {
	result := c.requestContext(ctx, "service")
	if caller == nil {
		return result
	}
	if caller.GetTrace() != nil {
		result.Trace = proto.Clone(caller.GetTrace()).(*protocolv2.TraceContext)
	}
	if caller.GetLocale() != "" {
		result.Locale = caller.GetLocale()
	}
	result.IdempotencyKey = caller.GetIdempotencyKey()
	return result
}

func cloneV2Services(values []*protocolv2.ServiceDescriptor) []*protocolv2.ServiceDescriptor {
	result := make([]*protocolv2.ServiceDescriptor, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, proto.Clone(value).(*protocolv2.ServiceDescriptor))
		}
	}
	return result
}

var _ hostapi.ServiceProvider = (*protocolV2Client)(nil)
var _ hostapi.ServiceStreamingProvider = (*protocolV2Client)(nil)
