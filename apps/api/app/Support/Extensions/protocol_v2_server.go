package extensionsruntime

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-plugin"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
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
