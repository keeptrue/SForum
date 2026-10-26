package extensionsruntime_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

const (
	eventsCommandFixtureID    = "sforum.events-command-reference"
	eventsCommandFixtureHook  = "sforum.events-command-reference.hook.emit"
	eventsCommandFixtureName  = "sforum.events-command-reference.emit-requested"
	eventsCommandFixtureGrant = int64(1301)

	eventsCommandCustomEvent = "sforum.events-command-reference.custom-requested"
)

// M1.5 的端到端验证：真实子进程通过 SDK 发布事件，宿主事件总线是唯一出口。
//
// 前两个用例证明「声明过的事件真的能发布、并且真的投递到自己的 listener」；
// 后两个用例证明命名空间归属只有 Host 的已验证身份能判定——插件自己把名字填成
// 别的插件或 Core 的，命令层直接拒绝。
func TestEventsCommandReferencePluginPublishesThroughRealBroker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping events command reference plugin subprocess build in short mode")
	}
	extension := buildReferenceFixtureExtension(t, "sforum-events-command-reference", eventsCommandFixtureID, eventsCommandFixtureGrant, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool := newMigratedFixtureDatabase(t, ctx, "events_command_reference")
	grantID := seedReferenceArtifact(t, ctx, pool, &extension)

	// 命令目录必须在插件 broker 之前冻结，而事件总线依赖 Manager，因此这里用一个
	// 可后填的发布目标——与生产的 deferredEventPublisher 同一手法。
	publisher := &fixtureEventPublisher{}
	authority, err := hostapi.NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	commandRuntime, err := hostapi.NewPostgresProtocolV2CommandRuntime(hostapi.PostgresProtocolV2CommandRuntimeConfig{
		Pool: pool, ActorDelegations: authority,
		Jobs:                 supportjobs.NewDispatcher(aiCommandFixtureRiverClient{}),
		Moderation:           moderation.NewPostgresStore(pool),
		AttachmentStatuses:   protocolV2RouteAttachmentMutator{},
		AttachmentExtensions: protocolV2RouteAttachmentMutator{},
		Events:               publisher,
	})
	if err != nil {
		t.Fatalf("build events command runtime: %v", err)
	}
	hostGateway, _ := newProtocolV2HostGateway()
	if err := hostGateway.BindProtocolV2CommandRuntime(commandRuntime); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hostGateway.Close() })
	hostGateway.BindCommandCapabilitySource(&aiCommandFixtureCapabilities{
		extensionID: extension.ID,
		keys:        resolvedAIFixtureCapabilities(extension.Manifest),
	})

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: strconv.FormatInt(grantID, 10), ImpactDigest: extension.PackageDigest,
		}},
		HostAPI: hostGateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	publisher.Set(manager)
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatalf("start events command fixture: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	invoke := func(name, kind, idempotencyKey string) extensionsruntime.HookResult {
		t.Helper()
		return starter.InvokeHook(ctx, extension, extensionsruntime.HookInput{
			DeclarationID: eventsCommandFixtureHook, Name: eventsCommandFixtureName, Kind: "observe",
			ContractVersion: eventsCommandFixtureHook + "@1",
			InputSchema:     "sforum.events-command-reference.hook.emit.input@1",
			ResultSchema:    "sforum.events-command-reference.hook.emit.result@1",
			FailurePolicy:   "fail_closed", Timeout: 5 * time.Second,
			Payload: map[string]any{
				"name": name, "kind": kind, "payload": map[string]any{"topicId": "42"},
				"idempotencyKey": idempotencyKey,
			},
		})
	}

	// 1) 自己命名空间下、清单里声明过的事件：命令提交，并且投递成功（fixture 自己
	//    就是那个 listener，投递失败会让这次发布报告失败）。
	accepted := invoke(eventsCommandCustomEvent, "observe", "events-1")
	if !accepted.OK {
		t.Fatalf("plugin-namespaced publish = %#v", accepted)
	}
	if state := stringResultValue(accepted, "state"); state != "COMMAND_STATE_COMMITTED" {
		t.Fatalf("state = %q, want COMMITTED; result=%#v", state, accepted)
	}
	if got := stringResultValue(accepted, "name"); got != eventsCommandCustomEvent {
		t.Fatalf("published name = %q", got)
	}
	if delivered := stringResultValue(accepted, "delivered"); delivered != "1" {
		t.Fatalf("delivered = %q, want 1", delivered)
	}
	if failed := stringResultValue(accepted, "failed"); failed != "0" {
		t.Fatalf("failed = %q, want 0", failed)
	}

	// 2) 另一个插件的命名空间：命令回执必须被拒，原因码稳定可读。
	foreign := invoke("other.plugin.some-event", "observe", "events-2")
	if state := stringResultValue(foreign, "state"); state != "COMMAND_STATE_ROLLED_BACK" {
		t.Fatalf("foreign namespace state = %q; result=%#v", state, foreign)
	}
	if reason := stringResultValue(foreign, "reason"); reason != "events.namespace_forbidden" {
		t.Fatalf("foreign namespace reason = %q", reason)
	}

	// 3) Core 自己的事件名同样不可发布：插件的命名空间与 Core 目录是两份边界。
	core := invoke("topic.created", "observe", "events-3")
	if state := stringResultValue(core, "state"); state != "COMMAND_STATE_ROLLED_BACK" {
		t.Fatalf("Core-owned name state = %q; result=%#v", state, core)
	}
	if reason := stringResultValue(core, "reason"); reason != "events.namespace_forbidden" {
		t.Fatalf("Core-owned name reason = %q", reason)
	}
}

// fixtureEventPublisher 是测试里的可后填事件发布目标：它把命令层需要的窄接口
// 转发给随后建立的 Manager 的事件总线，未回填时显式报告不可用。
type fixtureEventPublisher struct {
	mu      sync.RWMutex
	manager *extensionsruntime.Manager
}

func (p *fixtureEventPublisher) Set(manager *extensionsruntime.Manager) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.manager = manager
	p.mu.Unlock()
}

func (p *fixtureEventPublisher) EmitFromExtension(
	ctx context.Context,
	extensionID string,
	envelope appevents.Envelope,
) appevents.Result {
	if p == nil {
		return appevents.Result{OK: false, Reason: "events.publisher_unavailable"}
	}
	p.mu.RLock()
	manager := p.manager
	p.mu.RUnlock()
	if manager == nil {
		return appevents.Result{OK: false, Reason: "events.publisher_unavailable"}
	}
	return extensionsruntime.NewExtensionEventPublisher(manager).EmitFromExtension(ctx, extensionID, envelope)
}
