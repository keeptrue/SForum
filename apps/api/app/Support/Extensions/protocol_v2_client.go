package extensionsruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	protocolV2Name                          = "sforum.plugin"
	hostAPIV2Version                        = "sforum.host/v2"
	hostAPIV2Contract                       = "sforum.host@2"
	hostAPIV2Legacy                         = "sforum.host-api@2"
	protocolV2LocaleNone                    = "und"
	protocolV2QueryRuntimeFeatureName       = "query.runtime"
	protocolV2QueryRuntimeFeatureVersion    = "1"
	protocolV2IdentityRuntimeFeatureName    = "identity.runtime"
	protocolV2IdentityRuntimeFeatureVersion = "1"
	protocolV2ContentRuntimeFeatureName     = "content.runtime"
	protocolV2ContentRuntimeFeatureVersion  = "1"
)

// RuntimeTrustSource resolves the live exact-artifact grant before process
// start. The wire identity is disclosure only; this host lookup is authoritative.
type RuntimeTrustSource interface {
	RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error)
}

type protocolV2ClientConfig struct {
	identity           *protocolv2.ExtensionIdentity
	authority          []*protocolv2.AuthorityGrant
	events             []extensions.ManifestEvent
	hooks              []extensions.ManifestHook
	providers          []extensions.ManifestProvider
	seo                []extensions.ManifestSEO
	jobs               []extensions.ManifestJob
	commands           []extensions.ManifestCommand
	adminSurfaces      []extensions.ManifestAdminSurface
	routes             []extensions.ManifestRoute
	guards             []extensions.ManifestGuard
	queries            []extensions.ManifestQuery
	queryResultFilters []extensions.ManifestQueryResultFilter
	content            []extensions.ManifestContent
	manifestIdentity   *extensions.ManifestIdentity
	lifecycle          *extensions.ManifestLifecycle
	token              []byte
	instance           string
	hostAPI            ProtocolV2HostRegistrar
	delegations        hostapi.ProtocolV2ActorDelegationBundleIssuer
	shortcodeQueries   hostapi.ProtocolV2ShortcodeProjectionDelegationBundleIssuer
	queryDelegations   hostapi.ProtocolV2QueryActorDelegationIssuer
	sessionDelegations hostapi.ProtocolV2SessionDelegationIssuer
	hostCommands       bool
	capabilities       capabilities.Set
	hostBrokerID       uint32
}

type protocolV2Client struct {
	ProtocolNoop
	client             pluginv2.PluginRuntimeServiceClient
	identity           *protocolv2.ExtensionIdentity
	authority          []*protocolv2.AuthorityGrant
	events             []extensions.ManifestEvent
	hooks              []extensions.ManifestHook
	providers          []extensions.ManifestProvider
	seo                []extensions.ManifestSEO
	jobs               []extensions.ManifestJob
	commands           []extensions.ManifestCommand
	adminSurfaces      []extensions.ManifestAdminSurface
	routes             []extensions.ManifestRoute
	guards             []extensions.ManifestGuard
	queries            []extensions.ManifestQuery
	queryResultFilters []extensions.ManifestQueryResultFilter
	content            []extensions.ManifestContent
	manifestIdentity   *extensions.ManifestIdentity
	lifecycle          *extensions.ManifestLifecycle
	token              []byte
	instance           string
	hostBrokerID       uint32
	delegations        hostapi.ProtocolV2ActorDelegationBundleIssuer
	shortcodeQueries   hostapi.ProtocolV2ShortcodeProjectionDelegationBundleIssuer
	queryDelegations   hostapi.ProtocolV2QueryActorDelegationIssuer
	sessionDelegations hostapi.ProtocolV2SessionDelegationIssuer
	hostCommands       bool
	capabilities       capabilities.Set
	serviceMu          sync.RWMutex
	services           []*protocolv2.ServiceDescriptor
	sequence           atomic.Uint64
}

// ProtocolV2Error preserves the stable typed error returned by a plugin.
type ProtocolV2Error struct {
	Code      protocolv2.ErrorCode
	Reason    string
	Message   string
	Retryable bool
}

func (e *ProtocolV2Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return e.Message
	}
	return e.Reason + ": " + e.Message
}

func newProtocolV2Client(client pluginv2.PluginRuntimeServiceClient, config protocolV2ClientConfig) *protocolV2Client {
	return &protocolV2Client{
		client: client, identity: cloneV2Identity(config.identity), authority: cloneV2Authority(config.authority),
		events: append([]extensions.ManifestEvent(nil), config.events...), hooks: cloneManifestHooks(config.hooks),
		providers:          append([]extensions.ManifestProvider(nil), config.providers...),
		seo:                append([]extensions.ManifestSEO(nil), config.seo...),
		jobs:               append([]extensions.ManifestJob(nil), config.jobs...),
		commands:           append([]extensions.ManifestCommand(nil), config.commands...),
		adminSurfaces:      append([]extensions.ManifestAdminSurface(nil), config.adminSurfaces...),
		routes:             cloneProtocolV2Routes(config.routes),
		guards:             cloneProtocolV2Guards(config.guards),
		queries:            append([]extensions.ManifestQuery(nil), config.queries...),
		queryResultFilters: append([]extensions.ManifestQueryResultFilter(nil), config.queryResultFilters...),
		content:            append([]extensions.ManifestContent(nil), config.content...),
		manifestIdentity:   cloneManifestIdentity(config.manifestIdentity),
		lifecycle:          cloneManifestLifecycle(config.lifecycle),
		token:              append([]byte(nil), config.token...), instance: config.instance, hostBrokerID: config.hostBrokerID,
		delegations: config.delegations, shortcodeQueries: config.shortcodeQueries,
		queryDelegations:   config.queryDelegations,
		sessionDelegations: config.sessionDelegations,
		hostCommands:       config.hostCommands,
		capabilities:       config.capabilities,
	}
}

func (s *ProtocolStarter) newPluginClientConfig(
	ctx context.Context,
	extension extensions.Extension,
	cmd *exec.Cmd,
	instanceID string,
) (*plugin.ClientConfig, string, error) {
	version := extension.Manifest.Backend.ProtocolVersion
	diagnostics := pluginbootstrap.NewDiagnosticBuffer(pluginbootstrap.DefaultDiagnosticLimit)
	base := &plugin.ClientConfig{
		HandshakeConfig: pluginbootstrap.HandshakeV1(),
		Logger:          hclog.NewNullLogger(),
		Cmd:             cmd,
		Stderr:          diagnostics,
		SyncStderr:      diagnostics,
	}
	switch version {
	case 2:
		if !supportsProtocolV2HostAPI(extension.Manifest.Backend.HostAPIVersion) {
			return nil, "", fmt.Errorf("%w: host API %q", ErrUnsupportedProtocol, extension.Manifest.Backend.HostAPIVersion)
		}
		clientConfig, err := s.protocolV2ClientConfig(ctx, extension, instanceID)
		if err != nil {
			return nil, "", err
		}
		base.VersionedPlugins = map[int]plugin.PluginSet{
			pluginbootstrap.ApplicationProtocolV2: {
				pluginbootstrap.ApplicationProtocolV2Name: &protocolV2Plugin{clientConfig: &clientConfig},
			},
		}
		base.AllowedProtocols = []plugin.Protocol{plugin.ProtocolGRPC}
		base.AutoMTLS = true
		base.GRPCDialOptions = []grpc.DialOption{grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(DefaultProtocolV2MaxMessageBytes),
			grpc.MaxCallSendMsgSize(DefaultProtocolV2MaxMessageBytes),
		)}
		return base, pluginbootstrap.ApplicationProtocolV2Name, nil
	default:
		return nil, "", ErrUnsupportedProtocol
	}
}

func supportsProtocolV2HostAPI(value string) bool {
	switch strings.TrimSpace(value) {
	case hostAPIV2Contract, hostAPIV2Legacy, hostAPIV2Version:
		return true
	default:
		return false
	}
}

func (s *ProtocolStarter) protocolV2ClientConfig(
	ctx context.Context,
	extension extensions.Extension,
	runtimeInstanceID ...string,
) (protocolV2ClientConfig, error) {
	trustIdentity := extensions.RuntimeTrustIdentity{}
	var err error
	if s.trust != nil {
		trustIdentity, err = s.trust.RuntimeIdentity(ctx, extension)
	} else if extension.Source == extensions.SourceBuiltin {
		trustIdentity = extensions.RuntimeTrustIdentity{TrustGrantID: "builtin", ImpactDigest: extension.PackageDigest}
	} else {
		err = extensions.ErrTrustGrantNotFound
	}
	if err != nil {
		return protocolV2ClientConfig{}, fmt.Errorf("resolve protocol v2 trust identity: %w", err)
	}
	if trustIdentity.TrustGrantID == "" || extension.PackageDigest == "" {
		return protocolV2ClientConfig{}, fmt.Errorf("resolve protocol v2 trust identity: %w", extensions.ErrTrustGrantNotFound)
	}
	token, err := randomProtocolV2Bytes(32)
	if err != nil {
		return protocolV2ClientConfig{}, fmt.Errorf("create protocol v2 runtime token: %w", err)
	}
	instanceID := ""
	if len(runtimeInstanceID) > 1 {
		return protocolV2ClientConfig{}, ErrRuntimeAdmissionInvalid
	}
	if len(runtimeInstanceID) == 1 {
		instanceID = strings.TrimSpace(runtimeInstanceID[0])
	}
	if instanceID == "" {
		instanceID, err = newProtocolV2RuntimeInstanceID()
		if err != nil {
			return protocolV2ClientConfig{}, err
		}
	}
	if _, err := normalizeRuntimeInstanceIdentity(RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: instanceID,
	}); err != nil {
		return protocolV2ClientConfig{}, err
	}
	epoch := uint64(time.Now().UTC().UnixNano())
	if epoch == 0 {
		epoch = 1
	}
	return protocolV2ClientConfig{
		identity: &protocolv2.ExtensionIdentity{
			ExtensionId: extension.ID, ExtensionVersion: extension.Version,
			ArtifactDigest: extension.PackageDigest, TrustGrantId: trustIdentity.TrustGrantID,
			RuntimeEpoch: epoch, InstanceId: instanceID,
		},
		authority:          protocolV2Authority(extension.CapabilityGrants),
		events:             append([]extensions.ManifestEvent(nil), extension.Manifest.Events...),
		hooks:              cloneManifestHooks(extension.Manifest.Hooks),
		providers:          append([]extensions.ManifestProvider(nil), extension.Manifest.Providers...),
		seo:                append([]extensions.ManifestSEO(nil), extension.Manifest.SEO...),
		jobs:               append([]extensions.ManifestJob(nil), extension.Manifest.Jobs...),
		commands:           append([]extensions.ManifestCommand(nil), extension.Manifest.Commands...),
		adminSurfaces:      append([]extensions.ManifestAdminSurface(nil), extension.Manifest.AdminSurfaces...),
		routes:             cloneProtocolV2Routes(extension.Manifest.Routes),
		guards:             cloneProtocolV2Guards(extension.Manifest.Guards),
		queries:            append([]extensions.ManifestQuery(nil), extension.Manifest.Queries...),
		queryResultFilters: append([]extensions.ManifestQueryResultFilter(nil), extension.Manifest.QueryResultFilters...),
		content:            append([]extensions.ManifestContent(nil), extension.Manifest.Content...),
		manifestIdentity:   cloneManifestIdentity(extension.Manifest.Identity),
		lifecycle:          cloneManifestLifecycle(extension.Manifest.Lifecycle),
		token:              token,
		instance:           instanceID,
		hostAPI:            protocolV2HostRegistrarFor(s.hostAPI),
		delegations:        protocolV2ActorDelegationBundleIssuerFor(s.hostAPI),
		shortcodeQueries:   protocolV2ShortcodeProjectionDelegationIssuerFor(s.hostAPI),
		queryDelegations:   protocolV2ConsentQueryDelegationIssuerFor(s.hostAPI),
		sessionDelegations: protocolV2SessionDelegationIssuerFor(s.hostAPI),
		hostCommands:       extensionmanifest.HasDatabaseGrant(extension.Manifest.Database, extensionmanifest.DatabaseGrantHostCommands),
		capabilities:       protocolV2CapabilitySet(extension.CapabilityGrants),
	}, nil
}

func newProtocolV2RuntimeInstanceID() (string, error) {
	instanceBytes, err := randomProtocolV2Bytes(16)
	if err != nil {
		return "", fmt.Errorf("create protocol v2 instance id: %w", err)
	}
	return hex.EncodeToString(instanceBytes), nil
}

func (c *protocolV2Client) Handshake(ctx context.Context) error {
	ctx, cancel := protocolV2Deadline(ctx, DefaultProtocolV2HandshakeTimeout)
	defer cancel()
	hostFeatures := protocolV2HostFeatures(c.queries, c.queryResultFilters, c.manifestIdentity, c.content)
	response, err := c.client.Handshake(ctx, protocolV2HandshakeRequest(ctx, c, hostFeatures))
	if err != nil {
		return err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return err
	}
	selected := response.GetSelectedProtocol()
	if selected.GetProtocol() != protocolV2Name || selected.GetMajor() != 2 ||
		selected.GetMinMinor() != 0 || selected.GetMaxMinor() != 0 {
		return &ProtocolV2Error{Code: protocolv2.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH, Reason: "protocol.version_mismatch", Message: "Plugin selected an incompatible protocol version."}
	}
	if err := validateProtocolV2SelectedFeatures(hostFeatures, response.GetSelectedFeatures()); err != nil {
		return err
	}
	if protocolV2RequiresQueryRuntime(c.queries, c.queryResultFilters) &&
		!protocolV2SelectedFeature(
			response.GetSelectedFeatures(),
			protocolV2QueryRuntimeFeatureName,
			protocolV2QueryRuntimeFeatureVersion,
		) {
		return &ProtocolV2Error{
			Code: protocolv2.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH, Reason: "query_runtime.feature_required",
			Message: "Plugin did not select the required query.runtime@1 feature.",
		}
	}
	if protocolV2RequiresIdentityRuntime(c.manifestIdentity) &&
		!protocolV2SelectedFeature(
			response.GetSelectedFeatures(),
			protocolV2IdentityRuntimeFeatureName,
			protocolV2IdentityRuntimeFeatureVersion,
		) {
		return &ProtocolV2Error{
			Code: protocolv2.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH, Reason: "identity_runtime.feature_required",
			Message: "Plugin did not select the required identity.runtime@1 feature.",
		}
	}
	c.serviceMu.Lock()
	c.services = cloneV2Services(response.GetServices())
	c.serviceMu.Unlock()
	return nil
}

// validateProtocolV2SelectedFeatures rejects capabilities the plugin claims
// without an exact Host offer. Selected feature names are unique so a response
// cannot make version choice ambiguous.
func validateProtocolV2SelectedFeatures(
	offered []*protocolv2.ProtocolFeature,
	selected []*protocolv2.ProtocolFeature,
) error {
	offeredVersions := make(map[string]string, len(offered))
	for _, feature := range offered {
		if feature != nil {
			offeredVersions[feature.GetName()] = feature.GetVersion()
		}
	}
	seen := make(map[string]struct{}, len(selected))
	for _, feature := range selected {
		if feature == nil || feature.GetName() == "" || feature.GetVersion() == "" ||
			strings.TrimSpace(feature.GetName()) != feature.GetName() ||
			strings.TrimSpace(feature.GetVersion()) != feature.GetVersion() {
			return protocolV2FeatureMismatch("protocol.feature_invalid", "Plugin selected an invalid protocol feature.")
		}
		if _, duplicate := seen[feature.GetName()]; duplicate {
			return protocolV2FeatureMismatch("protocol.feature_duplicate", "Plugin selected a protocol feature more than once.")
		}
		seen[feature.GetName()] = struct{}{}
		version, offeredByHost := offeredVersions[feature.GetName()]
		if !offeredByHost || version != feature.GetVersion() {
			return protocolV2FeatureMismatch("protocol.feature_mismatch", "Plugin selected a feature the Host did not offer.")
		}
	}
	return nil
}

func protocolV2SelectedFeature(features []*protocolv2.ProtocolFeature, name, version string) bool {
	for _, feature := range features {
		if feature != nil && feature.GetName() == name && feature.GetVersion() == version {
			return true
		}
	}
	return false
}

func protocolV2FeatureMismatch(reason, message string) error {
	return &ProtocolV2Error{
		Code:   protocolv2.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH,
		Reason: reason, Message: message,
	}
}

func protocolV2RequiresQueryRuntime(
	queries []extensions.ManifestQuery,
	filters []extensions.ManifestQueryResultFilter,
) bool {
	if len(filters) > 0 {
		return true
	}
	for _, query := range queries {
		if strings.TrimSpace(query.Handler) != "" {
			return true
		}
	}
	return false
}

func protocolV2RequiresIdentityRuntime(identity *extensions.ManifestIdentity) bool {
	if identity == nil {
		return false
	}
	for _, provider := range identity.Providers {
		if len(provider.Operations) > 0 {
			return true
		}
	}
	return false
}

func protocolV2HostRegistrarFor(registrar HostAPIRegistrar) ProtocolV2HostRegistrar {
	if registrar == nil {
		return nil
	}
	result, _ := registrar.(ProtocolV2HostRegistrar)
	return result
}

func (c *protocolV2Client) Readiness(ctx context.Context) error {
	ctx, cancel := protocolV2Deadline(ctx, DefaultProtocolV2HandshakeTimeout)
	defer cancel()
	response, err := c.client.Readiness(ctx, &protocolv2.ReadinessRequest{Context: c.requestContext(ctx, "readiness")})
	if err != nil {
		return err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return err
	}
	if !response.GetReady() {
		return &ProtocolV2Error{Code: protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, Reason: "protocol.not_ready", Message: "Plugin is not ready."}
	}
	return nil
}

func (c *protocolV2Client) Health() (PluginHealth, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultProtocolV2HandshakeTimeout)
	defer cancel()
	response, err := c.client.Health(ctx, &protocolv2.HealthRequest{Context: c.requestContext(ctx, "health")})
	if err != nil {
		return PluginHealth{}, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return PluginHealth{}, err
	}
	return PluginHealth{OK: response.GetHealthy()}, nil
}

func (*protocolV2Client) RouteTarget() (PluginRouteTarget, error) {
	// V2 routes are served by the Route Registry over gRPC; no loopback URL is trusted.
	return PluginRouteTarget{}, nil
}

func (c *protocolV2Client) InvokeHook(input PluginHookRequest) (PluginHookResponse, error) {
	return c.InvokeHookContext(context.Background(), input)
}

func (c *protocolV2Client) InvokeHookContext(parent context.Context, input PluginHookRequest) (PluginHookResponse, error) {
	declaration, err := c.eventDeclaration(input.DeclarationID, input.Name, input.Kind)
	if err != nil {
		return PluginHookResponse{}, err
	}
	if input.ContractVersion != "" && input.ContractVersion != declaration.ContractVersion {
		return PluginHookResponse{}, fmt.Errorf("protocol v2 hook %q contract does not match %q", declaration.ID, input.ContractVersion)
	}
	timeout := DefaultProtocolV2RequestTimeout
	if input.TimeoutMS > 0 {
		timeout = time.Duration(input.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := protocolV2Deadline(parent, timeout)
	defer cancel()
	payloadSchemaID, payloadSchemaVersion, err := protocolV2SchemaRef(declaration.InputSchema)
	if err != nil {
		return PluginHookResponse{}, err
	}
	payload, err := protocolV2Document(payloadSchemaID, payloadSchemaVersion, input.Payload)
	if err != nil {
		return PluginHookResponse{}, err
	}
	response, err := c.client.InvokeHook(ctx, &pluginv2.HookRequest{
		Context: c.requestContext(ctx, input.CorrelationID), HookId: declaration.ID,
		HookName: declaration.Name, HookKind: declaration.Kind, ContractVersion: declaration.ContractVersion,
		DeliveryId: strconv.FormatInt(input.DeliveryID, 10), Payload: payload,
		MutableFields: append([]string(nil), input.PatchFields...),
	})
	if err != nil {
		if ctx.Err() != nil {
			return PluginHookResponse{}, ctx.Err()
		}
		return PluginHookResponse{}, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return PluginHookResponse{}, err
	}
	if declaration.ResultSchema != "" {
		if err := validateProtocolV2DocumentRef(response.GetResult(), declaration.ResultSchema, "hook result"); err != nil {
			return PluginHookResponse{}, err
		}
	} else if response.GetResult() != nil {
		return PluginHookResponse{}, fmt.Errorf("protocol v2 hook %q returned an undeclared result", declaration.Name)
	}
	if response.GetPatch() != nil {
		patchSchema, err := protocolV2PatchSchemaRef(declaration.ResultSchema)
		if err != nil {
			return PluginHookResponse{}, err
		}
		if err := validateProtocolV2DocumentRef(response.GetPatch(), patchSchema, "hook patch"); err != nil {
			return PluginHookResponse{}, err
		}
	}
	result := PluginHookResponse{OK: response.GetAccepted(), Result: protocolV2Values(response.GetResult())}
	if values := protocolV2Values(response.GetResult()); len(values) > 0 {
		result.Reason = stringValue(values, "reason")
		result.Message = stringValue(values, "message")
	}
	if patch := response.GetPatch().GetValue(); patch != nil {
		result.Patch = patch.AsMap()
	}
	return result, nil
}

func (c *protocolV2Client) ExecutePluginJob(parent context.Context, invocation supportjobs.PluginJobInvocation) error {
	if c == nil || c.client == nil || c.identity == nil {
		return extensions.ErrRuntimeUnavailable
	}
	if parent == nil {
		return fmt.Errorf("%w: caller context is required", supportjobs.ErrPluginJobRuntimeStale)
	}
	contract := invocation.Contract
	if contract.ExtensionID != c.identity.GetExtensionId() || contract.ExtensionVersion != c.identity.GetExtensionVersion() ||
		contract.ArtifactDigest != c.identity.GetArtifactDigest() || invocation.TrustGrantID != c.identity.GetTrustGrantId() {
		return supportjobs.ErrPluginJobRuntimeStale
	}
	frozenContract, err := extensions.PluginJobContractForExtension(extensions.Extension{
		ID: c.identity.GetExtensionId(), Version: c.identity.GetExtensionVersion(), PackageDigest: c.identity.GetArtifactDigest(),
		Manifest: extensions.Manifest{Jobs: append([]extensions.ManifestJob(nil), c.jobs...)},
	}, contract.JobName)
	if err != nil || !frozenContract.Equal(contract) {
		return supportjobs.ErrPluginJobRuntimeStale
	}
	payload, err := protocolV2Document(contract.PayloadSchemaID, contract.PayloadSchemaVersion, invocation.Payload)
	if err != nil {
		return err
	}
	ctx, cancel := protocolV2Deadline(parent, DefaultProtocolV2RequestTimeout)
	defer cancel()
	attempt := uint32(0)
	if invocation.Attempt > 0 {
		attempt = uint32(invocation.Attempt)
	}
	requestContext := c.requestContext(ctx, strconv.FormatInt(invocation.JobID, 10))
	stream, err := c.client.ExecuteJob(ctx, &pluginv2.JobRequest{
		Context: requestContext,
		JobId:   strconv.FormatInt(invocation.JobID, 10), JobKind: contract.JobName,
		PayloadVersion: contract.PayloadSchemaVersion, Attempt: attempt, Payload: payload,
	})
	if err != nil {
		return err
	}
	validator := pluginJobProgressValidator{jobID: strconv.FormatInt(invocation.JobID, 10), requestContext: requestContext}
	var terminalError error
	for {
		update, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				if validator.terminal {
					return terminalError
				}
				return &ProtocolV2Error{
					Code: protocolv2.ErrorCode_ERROR_CODE_INTERNAL, Reason: "runtime.job_terminal_missing",
					Message: "Plugin job stream ended without a terminal progress state.", Retryable: true,
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return recvErr
		}
		remoteError, err := validator.accept(update)
		if err != nil {
			return err
		}
		if remoteError != nil {
			terminalError = remoteError
		}
	}
}

func (c *protocolV2Client) invokePluginCommand(
	parent context.Context,
	contract PluginCommandContract,
	input map[string]any,
) (map[string]any, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return nil, extensions.ErrRuntimeUnavailable
	}
	if contract.ExtensionID != c.identity.GetExtensionId() || contract.ExtensionVersion != c.identity.GetExtensionVersion() ||
		contract.ArtifactDigest != c.identity.GetArtifactDigest() || contract.InstanceID != c.identity.GetInstanceId() {
		return nil, ErrPluginCommandRuntimeStale
	}
	if err := validateFrozenPluginCommand(contract, c.commands); err != nil {
		return nil, err
	}
	schemaID, schemaVersion, err := protocolV2SchemaRef(contract.InputSchema)
	if err != nil {
		return nil, err
	}
	document, err := protocolV2Document(schemaID, schemaVersion, input)
	if err != nil {
		return nil, err
	}
	ctx, cancel := protocolV2Deadline(parent, contract.Timeout)
	defer cancel()
	response, err := c.client.InvokeCommand(ctx, &pluginv2.CommandInvocationRequest{
		Context: c.requestContext(ctx, contract.ID), CommandId: contract.ID,
		ContractVersion: contract.ContractVersion, Handler: contract.Handler, Input: document,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return nil, err
	}
	if err := validateProtocolV2DocumentRef(response.GetResult(), contract.ResultSchema, "plugin command result"); err != nil {
		return nil, err
	}
	return protocolV2Values(response.GetResult()), nil
}

func (c *protocolV2Client) invokeAdminSurface(
	parent context.Context,
	contract AdminSurfaceContract,
	input map[string]any,
	actor *ProtocolV2InvocationActor,
	idempotencyKey string,
) (map[string]any, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return nil, extensions.ErrRuntimeUnavailable
	}
	propsSchema, resultSchema := adminSurfaceContractSchemaReferences(contract)
	if contract.ExtensionID != c.identity.GetExtensionId() || contract.ExtensionVersion != c.identity.GetExtensionVersion() ||
		contract.ArtifactDigest != c.identity.GetArtifactDigest() || contract.InstanceID != c.identity.GetInstanceId() ||
		contract.Handler == "" || propsSchema == "" || resultSchema == "" {
		return nil, ErrAdminSurfaceRuntimeStale
	}
	if err := validateFrozenAdminSurface(contract, c.adminSurfaces); err != nil {
		return nil, err
	}
	schemaID, schemaVersion, err := protocolV2SchemaRef(propsSchema)
	if err != nil {
		return nil, err
	}
	document, err := protocolV2Document(schemaID, schemaVersion, input)
	if err != nil {
		return nil, err
	}
	ctx, cancel := protocolV2Deadline(parent, DefaultProtocolV2RequestTimeout)
	defer cancel()
	requestContext, retireSessionDelegation, err := c.invocationRequestContext(ctx, contract.ID, actor, idempotencyKey)
	if err != nil {
		return nil, err
	}
	defer retireSessionDelegation()
	response, err := c.client.InvokeHook(ctx, &pluginv2.HookRequest{
		Context: requestContext, HookId: contract.ID,
		HookName: contract.Handler, HookKind: "admin_surface", ContractVersion: contract.ContractVersion,
		DeliveryId: contract.ID, Payload: document,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return nil, err
	}
	if !response.GetAccepted() || response.GetPatch() != nil {
		return nil, ErrAdminSurfaceRuntimeStale
	}
	if err := validateProtocolV2DocumentRef(response.GetResult(), resultSchema, "admin surface result"); err != nil {
		return nil, err
	}
	return protocolV2Values(response.GetResult()), nil
}

type pluginJobProgressValidator struct {
	jobID          string
	requestContext *protocolv2.RequestContext
	lastState      protocolv2.ProgressState
	completed      uint32
	total          uint32
	seen           bool
	terminal       bool
}

func (v *pluginJobProgressValidator) accept(update *protocolv2.ProgressUpdate) (error, error) {
	invalid := func(format string, args ...any) (error, error) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPluginJobStream, fmt.Sprintf(format, args...))
	}
	if update == nil {
		return invalid("job %q returned a nil update", v.jobID)
	}
	if v.terminal {
		return invalid("job %q emitted progress after its terminal update", v.jobID)
	}
	if update.GetStepId() != v.jobID {
		return invalid("expected job %q, got %q", v.jobID, update.GetStepId())
	}
	response := update.GetContext()
	if response == nil || response.GetRequestId() != v.requestContext.GetRequestId() ||
		!proto.Equal(response.GetExtension(), v.requestContext.GetExtension()) ||
		!proto.Equal(response.GetTrace(), v.requestContext.GetTrace()) ||
		response.GetServerTime() == nil || !response.GetServerTime().IsValid() {
		return invalid("progress response context does not match the exact runtime request")
	}
	state := update.GetState()
	if !validLifecycleProgressState(state) {
		return invalid("job %q returned state %s", v.jobID, state)
	}
	if state == protocolv2.ProgressState_PROGRESS_STATE_PLANNED && v.seen && v.lastState != protocolv2.ProgressState_PROGRESS_STATE_PLANNED {
		return invalid("job %q regressed to planned", v.jobID)
	}
	if update.GetCompletedUnits() < v.completed || update.GetTotalUnits() < v.total ||
		(update.GetTotalUnits() > 0 && update.GetCompletedUnits() > update.GetTotalUnits()) {
		return invalid("job %q progress counters are invalid", v.jobID)
	}
	terminal := isLifecycleTerminal(state)
	if state == protocolv2.ProgressState_PROGRESS_STATE_SUCCEEDED && update.GetTotalUnits() > 0 && update.GetCompletedUnits() != update.GetTotalUnits() {
		return invalid("successful job %q did not complete every unit", v.jobID)
	}
	detail := update.GetError()
	hasError := detail != nil && detail.GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNSPECIFIED
	if state == protocolv2.ProgressState_PROGRESS_STATE_FAILED || state == protocolv2.ProgressState_PROGRESS_STATE_CANCELLED {
		if !hasError || strings.TrimSpace(detail.GetReason()) == "" {
			return invalid("terminal job %q has no typed error", v.jobID)
		}
		if (state == protocolv2.ProgressState_PROGRESS_STATE_CANCELLED && detail.GetCode() != protocolv2.ErrorCode_ERROR_CODE_CANCELLED) ||
			(state == protocolv2.ProgressState_PROGRESS_STATE_FAILED && detail.GetCode() == protocolv2.ErrorCode_ERROR_CODE_CANCELLED) {
			return invalid("terminal job %q state and error code disagree", v.jobID)
		}
		if retry := detail.GetRetryAfter(); retry != nil && !retry.IsValid() {
			return invalid("terminal job %q has an invalid retry time", v.jobID)
		}
	} else if hasError {
		return invalid("non-failed job %q returned an error", v.jobID)
	}
	if update.GetResult() != nil {
		return invalid("job %q returned a result without a declared result schema", v.jobID)
	}
	v.seen = true
	v.lastState = state
	v.completed = update.GetCompletedUnits()
	v.total = update.GetTotalUnits()
	v.terminal = terminal
	if state == protocolv2.ProgressState_PROGRESS_STATE_FAILED || state == protocolv2.ProgressState_PROGRESS_STATE_CANCELLED {
		return protocolV2Error(detail), nil
	}
	return nil, nil
}

func (c *protocolV2Client) eventDeclaration(id, name, kind string) (extensions.ManifestEvent, error) {
	name = strings.TrimSpace(name)
	kind = strings.TrimSpace(kind)
	id = strings.TrimSpace(id)
	for _, hook := range c.hooks {
		if (id == "" || hook.ID == id) && hook.Name == name && hook.Kind == kind {
			if hook.ID == "" || hook.ContractVersion == "" || hook.InputSchema == "" ||
				(hook.Kind != "observe" && hook.ResultSchema == "") {
				return extensions.ManifestEvent{}, fmt.Errorf("protocol v2 hook %q has an incomplete contract", name)
			}
			return extensions.ManifestEvent{
				ID: hook.ID, ContractVersion: hook.ContractVersion, Name: hook.Name, Kind: hook.Kind,
				Handler: hook.Handler, InputSchema: hook.InputSchema, ResultSchema: hook.ResultSchema,
				Priority: hook.Priority, TimeoutMS: hook.TimeoutMS,
			}, nil
		}
	}
	for _, event := range c.events {
		if event.Name == name && event.Kind == kind {
			if event.ID == "" || event.ContractVersion == "" || event.InputSchema == "" || event.ResultSchema == "" {
				return extensions.ManifestEvent{}, fmt.Errorf("protocol v2 event %q has an incomplete contract", name)
			}
			return event, nil
		}
	}
	return extensions.ManifestEvent{}, fmt.Errorf("protocol v2 event %q kind %q is not declared by the manifest", name, kind)
}

func cloneManifestHooks(items []extensions.ManifestHook) []extensions.ManifestHook {
	result := make([]extensions.ManifestHook, len(items))
	for index, item := range items {
		result[index] = cloneManifestHook(item)
	}
	return result
}

func (c *protocolV2Client) ProviderProbe(input ProviderProbeRequest) (ProviderProbeResponse, error) {
	response, err := c.providerCall(input.Slot, "probe", map[string]any{})
	if err != nil {
		return ProviderProbeResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return ProviderProbeResponse{
		OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"),
		Message: stringValue(values, "message"), Details: stringMapValue(values, "details"),
		Suggestions: append([]string(nil), response.GetSuggestions()...),
	}, nil
}

func (c *protocolV2Client) SendMail(input MailProviderRequest) (MailProviderResponse, error) {
	response, err := c.providerCall("mail.provider", "send", map[string]any{
		"deliveryId": input.DeliveryID, "correlationId": input.CorrelationID,
		"fromAddress": input.FromAddress, "fromName": input.FromName, "to": input.To,
		"subject": input.Subject, "textBody": input.TextBody, "htmlBody": input.HTMLBody,
	})
	if err != nil {
		return MailProviderResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return MailProviderResponse{
		OK: booleanValue(values, "ok"), Classification: stringValue(values, "classification"),
		Reason: stringValue(values, "reason"), Message: stringValue(values, "message"),
	}, nil
}

func (c *protocolV2Client) InvokeVersionedSEO(
	parent context.Context,
	input VersionedSEORequest,
) (VersionedSEOResponse, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return VersionedSEOResponse{}, extensions.ErrRuntimeUnavailable
	}
	var declaration *extensions.ManifestSEO
	for index := range c.seo {
		candidate := &c.seo[index]
		if candidate.ID == input.DeclarationID && candidate.ContractVersion == input.ContractVersion &&
			candidate.Handler == input.Handler {
			declaration = candidate
			break
		}
	}
	if declaration == nil {
		return VersionedSEOResponse{}, fmt.Errorf("protocol v2 SEO declaration %q is not frozen", input.DeclarationID)
	}
	declaredTimeout := time.Duration(declaration.TimeoutMS) * time.Millisecond
	if input.Timeout != declaredTimeout || declaredTimeout <= 0 || declaredTimeout > DefaultProtocolV2RequestTimeout {
		return VersionedSEOResponse{}, fmt.Errorf("protocol v2 SEO declaration %q timeout drifted", input.DeclarationID)
	}
	requestSchemaID, requestSchemaVersion, err := protocolV2SchemaRef(ProtocolV2SEORequestSchema)
	if err != nil {
		return VersionedSEOResponse{}, err
	}
	document, err := protocolV2Document(requestSchemaID, requestSchemaVersion, input.Input)
	if err != nil {
		return VersionedSEOResponse{}, err
	}
	ctx, cancel := protocolV2Deadline(parent, declaredTimeout)
	defer cancel()
	response, err := c.client.ProviderCall(ctx, &pluginv2.ProviderCallRequest{
		Context: c.requestContext(ctx, input.DeclarationID),
		SlotId:  ProtocolV2SEOProviderSlot, Operation: ProtocolV2SEOProviderOperation,
		ContractVersion: input.ContractVersion, DeclarationId: input.DeclarationID, Input: document,
	})
	if err != nil {
		switch status.Code(err) {
		case codes.DeadlineExceeded:
			return VersionedSEOResponse{}, context.DeadlineExceeded
		case codes.Canceled:
			return VersionedSEOResponse{}, context.Canceled
		}
		if ctx.Err() != nil {
			return VersionedSEOResponse{}, ctx.Err()
		}
		return VersionedSEOResponse{}, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return VersionedSEOResponse{}, err
	}
	if err := validateProtocolV2DocumentRef(response.GetOutput(), ProtocolV2SEOResponseSchema, "SEO provider output"); err != nil {
		return VersionedSEOResponse{}, err
	}
	return VersionedSEOResponse{Output: protocolV2Values(response.GetOutput())}, nil
}

func (c *protocolV2Client) providerCall(slot, operation string, input map[string]any) (*pluginv2.ProviderCallResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultProtocolV2RequestTimeout)
	defer cancel()
	document, err := protocolV2Document("sforum.provider."+slot+"."+operation, "1", input)
	if err != nil {
		return nil, err
	}
	response, err := c.client.ProviderCall(ctx, &pluginv2.ProviderCallRequest{
		Context: c.requestContext(ctx, operation), SlotId: slot, Operation: operation,
		ContractVersion: "1", Input: document,
	})
	if err != nil {
		return nil, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *protocolV2Client) requestContext(ctx context.Context, correlation string) *protocolv2.RequestContext {
	requestID := c.instance + "-" + strconv.FormatUint(c.sequence.Add(1), 10)
	if correlation == "" {
		correlation = requestID
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().UTC().Add(DefaultProtocolV2RequestTimeout)
	}
	return &protocolv2.RequestContext{
		RequestId:        requestID,
		Trace:            &protocolv2.TraceContext{TraceId: correlation},
		Locale:           protocolV2LocaleNone,
		Deadline:         timestamppb.New(deadline),
		Extension:        cloneV2Identity(c.identity),
		GrantedAuthority: cloneV2Authority(c.authority),
	}
}

func protocolV2Document(schemaID, version string, values map[string]any) (*protocolv2.TypedDocument, error) {
	// Host 业务 payload 常含 []string、具名 struct（如 forum.ContentInput）。
	// structpb.NewStruct 只接受 JSON 友好类型；先 JSON round-trip 再编码。
	normalized, err := cloneHookDocument(values)
	if err != nil {
		return nil, fmt.Errorf("encode %s: normalize payload: %w", schemaID, err)
	}
	value, err := structpb.NewStruct(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", schemaID, err)
	}
	return &protocolv2.TypedDocument{SchemaId: schemaID, SchemaVersion: version, Value: value}, nil
}

func protocolV2Values(document *protocolv2.TypedDocument) map[string]any {
	if document == nil || document.GetValue() == nil {
		return map[string]any{}
	}
	return document.GetValue().AsMap()
}

func protocolV2Error(detail *protocolv2.ErrorDetail) error {
	if detail == nil || detail.GetCode() == protocolv2.ErrorCode_ERROR_CODE_UNSPECIFIED {
		return nil
	}
	return &ProtocolV2Error{Code: detail.GetCode(), Reason: detail.GetReason(), Message: detail.GetMessage(), Retryable: detail.GetRetryable()}
}

func protocolV2Deadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parentDeadline, ok := ctx.Deadline(); ok && time.Until(parentDeadline) <= timeout {
		// Do not race a second timer against an earlier Host-owned deadline. The
		// parent cause may carry ForceDrain or a domain-specific budget sentinel.
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func randomProtocolV2Bytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	return value, nil
}

func booleanValue(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func stringMapValue(values map[string]any, key string) map[string]string {
	raw, _ := values[key].(map[string]any)
	result := make(map[string]string, len(raw))
	for name, value := range raw {
		if text, ok := value.(string); ok {
			result[name] = text
		}
	}
	return result
}

var _ PluginProtocol = (*protocolV2Client)(nil)
var _ pluginHookContextInvoker = (*protocolV2Client)(nil)
var _ error = (*ProtocolV2Error)(nil)
