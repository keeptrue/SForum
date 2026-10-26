package pluginv2

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/go-plugin"
	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// DefaultMaxMessageBytes 是 Protocol V2 单条 gRPC 消息上限。它与 Host 侧
	// ai.MaxTotalInputBytes 同源：AI 补全请求经插件传输时，Host 允许的输入规模
	// 不能大于传输层能收下的规模，否则大上下文请求会先死在 gRPC 上。
	// 16MiB 给 JSON/proto 序列化留出余量（正文 + 转义 + 副本），可承载
	// ai.MaxTotalInputBytes 的 12MiB 正文。
	DefaultMaxMessageBytes  = 16 << 20
	DefaultConcurrentCalls  = 16
	DefaultRequestTimeout   = 5 * time.Second
	DefaultHandshakeTimeout = 5 * time.Second

	RuntimeTokenMetadataKey = "x-sforum-runtime-token-bin"
)

// ServeOptions controls process-local transport safety limits.
type ServeOptions struct {
	MaxMessageBytes int
	MaxConcurrent   int
	DefaultTimeout  time.Duration
}

// Serve runs one Protocol V2-only HashiCorp go-plugin subprocess.
func Serve(server pluginwire.PluginRuntimeServiceServer, options ...ServeOptions) {
	if server == nil {
		panic("protocol v2 plugin server is required")
	}
	config := ServeOptions{}
	if len(options) > 0 {
		config = options[0]
	}
	config = normalizeServeOptions(config)
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pluginbootstrap.HandshakeV1(),
		VersionedPlugins: map[int]plugin.PluginSet{
			pluginbootstrap.ApplicationProtocolV2: {
				pluginbootstrap.ApplicationProtocolV2Name: &runtimePlugin{server: server},
			},
		},
		GRPCServer: func(options []grpc.ServerOption) *grpc.Server {
			return NewGRPCServer(options, config, nil, nil)
		},
	})
}

type runtimePlugin struct {
	plugin.NetRPCUnsupportedPlugin
	server pluginwire.PluginRuntimeServiceServer
}

func (p *runtimePlugin) GRPCServer(broker *plugin.GRPCBroker, server *grpc.Server) error {
	if p.server == nil {
		return fmt.Errorf("protocol v2 plugin server is required")
	}
	if binder, ok := p.server.(interface{ BindProtocolV2Broker(*plugin.GRPCBroker) }); ok {
		binder.BindProtocolV2Broker(broker)
	}
	pluginwire.RegisterPluginRuntimeServiceServer(server, p.server)
	return nil
}

func (p *runtimePlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	return pluginwire.NewPluginRuntimeServiceClient(conn), nil
}

// NewGRPCServer creates a Protocol V2 gRPC server with the shared message,
// concurrency, and deadline limits. Host authentication interceptors run
// before the transport limits when supplied.
func NewGRPCServer(
	options []grpc.ServerOption,
	config ServeOptions,
	unaryInterceptors []grpc.UnaryServerInterceptor,
	streamInterceptors []grpc.StreamServerInterceptor,
) *grpc.Server {
	config = normalizeServeOptions(config)
	semaphore := make(chan struct{}, config.MaxConcurrent)
	unaryInterceptors = append(unaryInterceptors, unaryLimitInterceptor(config.DefaultTimeout, semaphore))
	streamInterceptors = append(streamInterceptors, streamLimitInterceptor(config.DefaultTimeout, semaphore))
	options = append(options,
		grpc.MaxRecvMsgSize(config.MaxMessageBytes),
		grpc.MaxSendMsgSize(config.MaxMessageBytes),
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
		grpc.ChainStreamInterceptor(streamInterceptors...),
	)
	return grpc.NewServer(options...)
}

func normalizeServeOptions(config ServeOptions) ServeOptions {
	if config.MaxMessageBytes <= 0 || config.MaxMessageBytes > DefaultMaxMessageBytes {
		config.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = DefaultConcurrentCalls
	}
	if config.DefaultTimeout <= 0 {
		config.DefaultTimeout = DefaultRequestTimeout
	}
	return config
}

func unaryLimitInterceptor(timeout time.Duration, semaphore chan struct{}) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		callCtx, cancel := withDefaultDeadline(ctx, timeout)
		defer cancel()
		if err := acquire(callCtx, semaphore); err != nil {
			return nil, err
		}
		defer func() { <-semaphore }()
		return handler(callCtx, request)
	}
}

func streamLimitInterceptor(timeout time.Duration, semaphore chan struct{}) grpc.StreamServerInterceptor {
	return func(server any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, cancel := withDefaultDeadline(stream.Context(), timeout)
		defer cancel()
		if err := acquire(ctx, semaphore); err != nil {
			return err
		}
		defer func() { <-semaphore }()
		return handler(server, &limitedServerStream{ServerStream: stream, ctx: ctx})
	}
}

type limitedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *limitedServerStream) Context() context.Context { return s.ctx }

func withDefaultDeadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok || timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func acquire(ctx context.Context, semaphore chan struct{}) error {
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return status.Error(codes.ResourceExhausted, "protocol v2 concurrency limit reached before deadline")
	}
}
