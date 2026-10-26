package providers

import (
	"context"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	aicontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/AI"
	modelsai "github.com/keeptrue/sforum/apps/api/app/Models/AI"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

// identityStore 是 AI 控制台需要的身份能力：读取当前操作者，以及解析充当 AI
// 助手的机器人账号。两者都由 identity.PostgresStore 提供。
type identityStore interface {
	identity.ActorStore
	modelsai.BotAccountReader
}

type AIProvider struct {
	controller *aicontroller.Controller
	service    *modelsai.Service
}

// AIOutboundOptions 是 AI 出站策略。两项都由部署环境决定，不由后台配置决定：
// 它们属于 SSRF 边界，运营者不该在控制台里把它关掉。
type AIOutboundOptions struct {
	// AllowHTTP 允许 http:// 供应商地址（非生产）。
	AllowHTTP bool
	// AllowPrivate 放行解析到私网/回环地址的端点，供本机或内网模型服务使用。
	// 只应在非生产环境显式打开：它等于允许服务端读取内网，而模型列表探测会把
	// 远端响应的模型标识回传，因此放行就是一条真实的内网读取路径。
	AllowPrivate bool
}

// NewAIProvider 装配 AI 网关：配置与用量存储、Secret Store 凭证解析、受控出站
// 执行，以及权限感知的服务层。网关默认关闭：未显式启用前不产生任何出站调用。
//
// allowHTTP 沿用仓库其它出站路径的同一约定（非生产允许 http，生产只允许 https）：
// 供应商地址由运营者在后台填写，本地反代或内网网关常常只有 http。私网、回环与保留
// 地址默认仍被拒绝，需要 outbound.AllowPrivate 显式放行。
func NewAIProvider(pool *pgxpool.Pool, secrets *secretstore.Service, users identityStore, sessions *authsession.Manager, outbound AIOutboundOptions, cacheStore cache.Cache, defaultLocale func(context.Context) (string, error)) *AIProvider {
	store := supportai.NewPostgresStore(pool)
	// 同一个受控出站实例同时服务补全与只读探测：SSRF 校验、超时与响应体上限
	// 只有一处实现，探测不会成为绕过它们的旁路。
	invoker := supportai.NewHTTPInvoker(supportai.HTTPInvokerOptions{
		AllowHTTP:    outbound.AllowHTTP,
		AllowPrivate: outbound.AllowPrivate,
	})
	resolver := supportai.NewSecretStoreResolver(secrets)
	// 用途登记表是进程级只读声明：网关用它拒绝未声明的用途，服务层用它解析生效
	// 参数并供控制台枚举。两处必须是同一实例，否则「网关认识的用途」与「控制台
	// 列出的用途」会出现两个答案。
	purposes := supportai.DefaultPurposeRegistry()
	gateway := supportai.NewGateway(supportai.GatewayConfig{
		Settings: store,
		Usage:    store,
		Traces:   store,
		Creds:    resolver,
		Invoker:  invoker,
		Cache:    supportai.NewResultCache(cacheStore),
		Purposes: purposes,
	})
	service := modelsai.NewService(modelsai.Config{
		Settings:         store,
		Purposes:         purposes,
		Gateway:          gateway,
		Usage:            store,
		Traces:           store,
		ReplyOutcomes:    store,
		Credentials:      supportai.NewCredentialWriter(secrets),
		CredentialReader: resolver,
		Models:           supportai.NewHTTPModelLister(invoker),
		BotAccounts:      users,
		DefaultLocale:    defaultLocale,
	})
	return &AIProvider{controller: aicontroller.NewController(service, users, sessions, secrets), service: service}
}

// Service 暴露服务层，供其他 Provider 把自己的 AI 用途接到同一网关上。
func (p *AIProvider) Service() *modelsai.Service { return p.service }

// Gateway 暴露底层网关，供回复生成等用途复用同一个装配好的实例。
func (p *AIProvider) Gateway() *supportai.Gateway {
	if p == nil || p.service == nil {
		return nil
	}
	return p.service.Gateway()
}

func (p *AIProvider) RegisterRoutes(api fiber.Router) { p.controller.RegisterRoutes(api) }
