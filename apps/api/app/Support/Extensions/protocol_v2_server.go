package extensionsruntime

import (
	"context"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
)

const (
	DefaultProtocolV2MaxMessageBytes  = pluginv2sdk.DefaultMaxMessageBytes
	DefaultProtocolV2ConcurrentCalls  = pluginv2sdk.DefaultConcurrentCalls
	DefaultProtocolV2RequestTimeout   = pluginv2sdk.DefaultRequestTimeout
	DefaultProtocolV2HandshakeTimeout = pluginv2sdk.DefaultHandshakeTimeout

	ProtocolV2RuntimeTokenMetadataKey = pluginv2sdk.RuntimeTokenMetadataKey
)

// ProtocolV2ServerConfig is retained for Host-side compatibility. Plugin
// subprocesses own this transport contract through the public SDK.
type ProtocolV2ServerConfig = pluginv2sdk.ServeOptions

// ServeProtocolV2Plugin is retained for Host integration fixtures. Plugin
// authors should call pluginv2.Serve directly.
func ServeProtocolV2Plugin(server pluginwire.PluginRuntimeServiceServer, config ProtocolV2ServerConfig) {
	pluginv2sdk.Serve(server, config)
}

// protocolV2Plugin is the Host-side go-plugin adapter. Plugin subprocesses use
// the lightweight SDK adapter and therefore never import this Host package.
type protocolV2Plugin struct {
	plugin.NetRPCUnsupportedPlugin
	server       pluginwire.PluginRuntimeServiceServer
	clientConfig *protocolV2ClientConfig
}

func (p *protocolV2Plugin) GRPCServer(broker *plugin.GRPCBroker, server *grpc.Server) error {
	if p.server == nil {
		return fmt.Errorf("protocol v2 plugin server is required")
	}
	if binder, ok := p.server.(interface{ BindProtocolV2Broker(*plugin.GRPCBroker) }); ok {
		binder.BindProtocolV2Broker(broker)
	}
	pluginwire.RegisterPluginRuntimeServiceServer(server, p.server)
	return nil
}

func (p *protocolV2Plugin) GRPCClient(_ context.Context, broker *plugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	client := pluginwire.NewPluginRuntimeServiceClient(conn)
	if p.clientConfig == nil {
		return client, nil
	}
	config := *p.clientConfig
	if config.hostAPI != nil {
		if broker == nil {
			return nil, fmt.Errorf("protocol v2 host broker is required")
		}
		config.hostBrokerID = broker.NextId()
		binding := newProtocolV2HostBinding(config)
		go broker.AcceptAndServe(config.hostBrokerID, func(options []grpc.ServerOption) *grpc.Server {
			return protocolV2HostGRPCServer(options, config.hostAPI, binding)
		})
	}
	return newProtocolV2Client(client, config), nil
}

func normalizeProtocolV2ServerConfig(config ProtocolV2ServerConfig) ProtocolV2ServerConfig {
	if config.MaxMessageBytes <= 0 || config.MaxMessageBytes > DefaultProtocolV2MaxMessageBytes {
		config.MaxMessageBytes = DefaultProtocolV2MaxMessageBytes
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = DefaultProtocolV2ConcurrentCalls
	}
	if config.DefaultTimeout <= 0 {
		config.DefaultTimeout = DefaultProtocolV2RequestTimeout
	}
	return config
}

func protocolV2GRPCServerFactory(config ProtocolV2ServerConfig) func([]grpc.ServerOption) *grpc.Server {
	return func(options []grpc.ServerOption) *grpc.Server {
		return pluginv2sdk.NewGRPCServer(options, config, nil, nil)
	}
}

// 以下是宿主侧的 go-plugin 装配：ClientConfig 的构造与它消费的 config 生产者放在一起，
// 与 protocolV2Plugin.GRPCClient 同文件，避免「配置在 client、消费在 server」两头跑。
func (s *ProtocolStarter) newPluginClientConfig(
	ctx context.Context,
	extension extensions.Extension,
	cmd *exec.Cmd,
	instanceID string,
) (*plugin.ClientConfig, string, error) {
	diagnostics := pluginbootstrap.NewDiagnosticBuffer(pluginbootstrap.DefaultDiagnosticLimit)
	base := &plugin.ClientConfig{
		HandshakeConfig: pluginbootstrap.HandshakeV1(),
		Logger:          hclog.NewNullLogger(),
		Cmd:             cmd,
		Stderr:          diagnostics,
		SyncStderr:      diagnostics,
		SkipHostEnv:     true, // 白名单是插件 env 的唯一入口（go-plugin 默认会 append os.Environ()，见 EX2-01b）
	}
	switch extension.Manifest.Backend.ProtocolVersion {
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
