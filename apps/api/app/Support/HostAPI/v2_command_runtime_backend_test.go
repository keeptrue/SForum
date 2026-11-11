package hostapi

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// TestPostgresCommandRuntimeKeepsHostCommandAuthorityBackend 是 f63ad2cf9 那个 P0 的类型快照。
//
// 事故形状：`NewPostgresProtocolV2HostCommandBackend(pool).WithTrustLevelSource(src)` 里的
// 方法来自**被嵌入的中性类型**，返回 `*PostgresProtocolV2CommandBackend`，于是引擎拿到
// 不做 host_commands 授权检查的实现 —— 声明看起来对、执行的是另一个东西。
//
// 这条断言只钉类型（快照）；真正的语义守卫是
// TestPostgresProtocolV2EntitlementCommandDeniesExtensionWithoutHostCommandsGrant。
func TestPostgresCommandRuntimeKeepsHostCommandAuthorityBackend(t *testing.T) {
	// 只为满足构造器的非 nil 校验：pgxpool 是惰性连接，这里不会真的连库。
	pool, err := pgxpool.New(context.Background(), "postgres://sforum:sforum@127.0.0.1:1/sforum?sslmode=disable")
	if err != nil {
		t.Fatalf("构造 pool 失败：%v", err)
	}
	t.Cleanup(pool.Close)
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewPostgresProtocolV2CommandRuntime(PostgresProtocolV2CommandRuntimeConfig{
		Pool:                 pool,
		ActorDelegations:     authority,
		Jobs:                 supportjobs.NewDispatcher(postgresDomainCommandJobs{}),
		Moderation:           moderation.NewPostgresStore(pool),
		AttachmentStatuses:   postgresDomainAttachmentStatusMutator{},
		AttachmentExtensions: protocolV2AttachmentExtensionStub{},
		TrustLevels:          nil,
	})
	if err != nil {
		t.Fatalf("装配 runtime 失败：%v", err)
	}
	engine := runtime.commandEngine()
	if _, ok := engine.backend.(*PostgresProtocolV2HostCommandBackend); !ok {
		t.Fatalf("Host Command engine backend = %T，期望 *hostapi.PostgresProtocolV2HostCommandBackend：\n"+
			"中性 backend 的 ResolveScope 传 requiredAuthority=\"\"，host_commands 授权闸门会静默失效（f63ad2cf9 的 P0）。",
			engine.backend)
	}
}

// TestHostCommandWrapperKeepsWrapperThroughChainedTrustLevelSource 钉住结构修复本身：
// wrapper 上的 WithTrustLevelSource 必须返回 wrapper，而不是被嵌入的中性类型。
//
// 没有这条，下一次有人把 wrapper 上的重声明删掉，上面的 runtime 断言仍然会红——
// 但红在「runtime 装配」而不是「方法提升」，排查会从错误的地方开始。
func TestHostCommandWrapperKeepsWrapperThroughChainedTrustLevelSource(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://sforum:sforum@127.0.0.1:1/sforum?sslmode=disable")
	if err != nil {
		t.Fatalf("构造 pool 失败：%v", err)
	}
	t.Cleanup(pool.Close)

	chained := NewPostgresProtocolV2HostCommandBackend(pool).WithTrustLevelSource(nil)
	if chained == nil {
		t.Fatal("链式 WithTrustLevelSource 返回 nil")
	}
	if _, ok := any(chained).(*PostgresProtocolV2HostCommandBackend); !ok {
		t.Fatalf("链式 WithTrustLevelSource 返回 %T，期望 *hostapi.PostgresProtocolV2HostCommandBackend", chained)
	}
	// 顺带证明它没有把 pool 丢掉（结构修复不该顺手改语义）。
	if chained.pool == nil {
		t.Fatal("wrapper 链式注入后 pool 丢失")
	}
}
