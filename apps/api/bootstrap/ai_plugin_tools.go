package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	toolplugin "github.com/keeptrue/sforum/apps/api/app/Support/AI/toolplugin"
)

// 具体传输适配器（toolplugin.PluginToolInvoker → 扩展运行时的 versioned
// ProviderCall）放在 api_assembly_domains.go：那里是已获准 import 扩展运行时的
// 装配文件，本文件保持只依赖契约与登记表，不新增对 legacy 运行时的 import。
//
// 登记时机（2026-10-19 修订）：登记表不再只是启动期快照。期望集每次成功应用之后
// 都会重建「Core 工具 + 当前已启用插件工具」并原子换表（见 aiToolRefresher 与
// aiToolRefreshApplier），因此后台的启用/停用/升级不再需要重启 API 进程。装配期
// 那一次登记保留：它覆盖第一个期望集应用之前的时间窗，也让没有发布事件的部署
// （单测、Safe Mode）行为与从前一致。

// pluginAIToolExtensionLister 是刷新时重新读取扩展清单的最小面。用窄接口而不是
// 具体 store：本文件只关心「现在有哪些扩展」，不关心它来自 Postgres 还是测试
// double。
type pluginAIToolExtensionLister interface {
	List(context.Context) ([]extensions.Extension, error)
}

// aiToolRefreshTimeout 限制一次登记表重建的时长。重建是期望集应用成功之后的
// 附加动作：它慢或失败都不该拖住协调器的收敛节奏（心跳在另一个 goroutine，
// 但同一个 run loop 还要处理后续发布）。超时后保留旧表并告警。
const aiToolRefreshTimeout = 5 * time.Second

// buildPluginAITools 把「已启用插件声明的 AI 工具」构建成宿主工具值集合。
//
// 单条声明非法不阻断整体构建：清单在安装时已经校验过，这里出现非法声明意味着
// 数据库里的历史产物与当前校验规则不一致，跳过它并继续比让整张表建不起来更有
// 价值——但会返回错误集合，由调用方决定是否记日志。
func buildPluginAITools(
	invoker toolplugin.PluginToolInvoker,
	items []extensions.Extension,
) ([]supportai.Tool, []error) {
	if invoker == nil {
		return nil, nil
	}
	pluginRegistry := toolplugin.NewPluginToolRegistry()
	var failures []error
	for _, item := range items {
		if item.Type != extensions.TypePlugin || len(item.Manifest.AITools) == 0 {
			continue
		}
		// 停用的插件没有运行时可派发：登记它只会造出「看得见、调不动」的工具，
		// 并在控制台里把运营者引到一个必然失败的白名单条目上。
		if item.Status != extensions.StatusEnabled {
			continue
		}
		extension := item
		for _, declaration := range extension.Manifest.AITools {
			tool, err := toolplugin.NewPluginTool(extension, declaration, invoker)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s/%s: %w", extension.ID, declaration.ID, err))
				continue
			}
			if err := pluginRegistry.Register(tool); err != nil {
				failures = append(failures, fmt.Errorf("%s/%s: %w", extension.ID, declaration.ID, err))
			}
		}
	}
	registered := pluginRegistry.Tools()
	tools := make([]supportai.Tool, 0, len(registered))
	for _, tool := range registered {
		tools = append(tools, tool)
	}
	return tools, failures
}

// mergeAIToolSets 把插件工具并进 Core 工具集合，同名时保留先出现的一方。
//
// Core 工具先入，因此插件永远覆盖不了 Core 行为——否则一个插件就能把
// forum-search 换成自己的实现，而运营者完全看不出来。这条规则与
// PluginToolRegistry.Apply 一致；区别只是这里发生在换表之前，因此「Core + 插件」
// 能在一次 Replace 里原子生效，中途不会被读到半张表。
func mergeAIToolSets(core []supportai.Tool, plugins []supportai.Tool) []supportai.Tool {
	merged := make([]supportai.Tool, 0, len(core)+len(plugins))
	seen := make(map[string]bool, len(core)+len(plugins))
	for _, group := range [][]supportai.Tool{core, plugins} {
		for _, tool := range group {
			if tool == nil {
				continue
			}
			name := tool.Definition().Name
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			merged = append(merged, tool)
		}
	}
	return merged
}

// registerPluginAITools 把已启用插件声明的 AI 工具登记进宿主工具登记表。
//
// 它在装配期调用一次；运行期的重建走 aiToolRefresher（两者共用
// buildPluginAITools，因此「能登记的集合」只有一种解释）。登记不等于暴露：模型
// 在请求里看到的工具由用途自己的白名单决定（RunInput.ToolNames），Core 用途的
// 白名单不含插件工具，因此这里登记的结果不会改变任何现有对话的行为。
func registerPluginAITools(
	registry *supportai.ToolRegistry,
	invoker toolplugin.PluginToolInvoker,
	items []extensions.Extension,
) []error {
	if registry == nil || invoker == nil {
		return nil
	}
	tools, failures := buildPluginAITools(invoker, items)
	if err := registry.Replace(mergeAIToolSets(registry.Tools(), tools)); err != nil {
		failures = append(failures, err)
	}
	return failures
}

// aiToolRefresher 重建「Core 工具 + 当前已启用插件工具」并原子换表。
//
// 它回答的问题只有一个：**此刻**站内有哪些 AI 工具。Core 工具是进程内对象、
// 无状态，装配期取一份快照后原样复用；插件工具每次都按最新的扩展清单重建，
// 因此启用/停用/升级会自然反映进去，不需要重启进程。
type aiToolRefresher struct {
	registry *supportai.ToolRegistry
	core     []supportai.Tool
	invoker  toolplugin.PluginToolInvoker
	store    pluginAIToolExtensionLister
	logger   *slog.Logger
}

// Refresh 重新读取扩展清单并换表。失败时返回错误且**不动**现有登记表：调用方
// 继续用旧表服务请求，「换表失败」不该退化成「站内没有工具」。
func (r *aiToolRefresher) Refresh(ctx context.Context) error {
	if r == nil || r.registry == nil || r.store == nil || r.invoker == nil {
		return nil
	}
	items, err := r.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list extensions for ai tool refresh: %w", err)
	}
	pluginTools, failures := buildPluginAITools(r.invoker, items)
	if r.logger != nil {
		for _, failure := range failures {
			r.logger.Warn("plugin AI tool declaration skipped during refresh", "error", failure)
		}
	}
	if err := r.registry.Replace(mergeAIToolSets(r.core, pluginTools)); err != nil {
		return fmt.Errorf("replace ai tool registry: %w", err)
	}
	return nil
}

// aiToolRefreshHook 是「插件运行时期望集已应用」到「AI 工具登记表重建」之间的
// 桥。
//
// 它必须早于协调器构造（协调器在扩展装配阶段启动），而登记表要到 AI 域装配才
// 存在，因此这是一个可延迟装配的槽位：未装配时是 no-op——那种情况下装配期那次
// 登记就是当下状态的答案，没有需要补的东西。
type aiToolRefreshHook struct {
	mu      sync.RWMutex
	refresh func(context.Context) error
	logger  *slog.Logger
}

// Arm 装配刷新实现。重复装配以最后一次为准（进程内只有一个 AI 登记表）。
func (h *aiToolRefreshHook) Arm(refresh func(context.Context) error, logger *slog.Logger) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.refresh = refresh
	h.logger = logger
	h.mu.Unlock()
}

// AfterApply 在一次期望集应用成功后触发重建。
//
// 它刻意不返回错误：运行时集合已经应用成功，登记表重建是派生动作，失败只降级为
// 「控制台里少了一个工具名 + 一条告警」，绝不能让协调器因此判定本轮收敛失败。
// 刷新上下文与原上下文解耦（WithoutCancel + 自己的超时）：一次发布被取消不该
// 让登记表停在旧状态，而慢刷新也不该无限占用 apply 循环。
func (h *aiToolRefreshHook) AfterApply(ctx context.Context, publication extensions.PluginRuntimePublication) {
	if h == nil {
		return
	}
	h.mu.RLock()
	refresh := h.refresh
	logger := h.logger
	h.mu.RUnlock()
	if refresh == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aiToolRefreshTimeout)
	defer cancel()
	if err := refresh(refreshCtx); err != nil && logger != nil {
		logger.Warn("AI tool registry refresh failed; keeping the previous registry",
			"error", err, "revision", publication.Revision, "reason", string(publication.Reason))
	}
}
