package toolplugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 插件工具执行器：把 Host 的工具接口接到「一次 Host → plugin RPC」上。
//
// 这样接的原因是不让 Orchestrator 知道插件的存在。步数预算、逐步闸门、结果
// 截断、失败姿态都在编排层，插件工具与 Core 工具走同一条路径；差别只在于
// Invoke 的实现是进程内调用还是一次 RPC。若让编排层区分两者，它就会长出两套
// 预算逻辑，而运营者在控制台看到的「每次回复最多 3 次工具调用」也会有两种含义。

// PluginToolInvoker 是运行期的最小面：给定精确插件与冻结声明，执行一次调用。
//
// 请求/响应类型在本包内本地声明，而不是直接 import 扩展运行时：后者反向依赖
// Support/AI（命令层），会形成环。本地类型只承载派发必需的字段，由适配层在
// 边界上做一次翻译——这样 AI 包始终只知道契约，不知道传输。
type PluginToolInvoker interface {
	InvokeVersionedAITool(context.Context, extensions.Extension, PluginToolCall) (PluginToolResult, error)
}

// PluginToolCall 是一次派发的输入。字段与运行时的派发请求一一对应，只是去掉了
// 传输层类型。
type PluginToolCall struct {
	DeclarationID   string
	ContractVersion string
	Handler         string
	Timeout         time.Duration
	Name            string
	Arguments       string
	// Locale / TopicID / ViewerUserID 只在清单的 aiTools[].context 声明过时才有值。
	// 未声明的项保持零值，适配层据此决定是否写进请求文档。
	Locale       string
	TopicID      int64
	ViewerUserID int64
}

// PluginToolResult 是派发结果。IsError 表示「工具可解释的失败」，会原样给模型。
type PluginToolResult struct {
	Content string
	IsError bool
}

// PluginTool 是登记在宿主 ToolRegistry 里的插件工具实现。
type PluginTool struct {
	extension   extensions.Extension
	declaration extensions.ManifestAITool
	invoker     PluginToolInvoker
}

// NewPluginTool 构造一个插件工具。声明必须在调用前完成契约校验：这里的失败
// 只表示装配错误，不是用户输入错误。
func NewPluginTool(
	extension extensions.Extension,
	declaration extensions.ManifestAITool,
	invoker PluginToolInvoker,
) (*PluginTool, error) {
	if invoker == nil || declaration.ID == "" || declaration.Name == "" {
		return nil, fmt.Errorf("%w: invoker and declaration are required", ErrAIToolDeclarationInvalid)
	}
	if err := ValidateAIToolDeclaration(extension.ID, declaration); err != nil {
		return nil, err
	}
	return &PluginTool{extension: extension, declaration: declaration, invoker: invoker}, nil
}

// Definition 给模型看的声明。Parameters 原样透传：宿主不改写插件的 schema，
// 否则模型看到的参数与插件实际收到的参数会对不上。
func (t *PluginTool) Definition() supportai.ToolDefinition {
	if t == nil {
		return supportai.ToolDefinition{}
	}
	return supportai.ToolDefinition{
		Name:        t.declaration.Name,
		Description: t.declaration.Description,
		Parameters:  t.declaration.Parameters,
	}
}

// Origin 声明该工具来自哪个扩展。控制台据此把插件工具与 Core 内置工具分开，
// 运营者不用靠工具名的形状去猜。
func (t *PluginTool) Origin() supportai.ToolOrigin {
	if t == nil {
		return supportai.ToolOrigin{}
	}
	return supportai.ToolOrigin{
		Kind:        "plugin",
		ExtensionID: t.extension.ID,
		Version:     t.extension.Version,
	}
}

// DisplayDescriptions 透传清单里声明的界面文案（locale -> 文本）。
//
// 它只服务控制台：模型收到的仍是 Definition().Description。返回副本，调用方
// 改到它不会污染下一次枚举，也不会改到声明本身。
func (t *PluginTool) DisplayDescriptions() map[string]string {
	if t == nil || len(t.declaration.DescriptionI18n) == 0 {
		return nil
	}
	copied := make(map[string]string, len(t.declaration.DescriptionI18n))
	for locale, text := range t.declaration.DescriptionI18n {
		copied[locale] = text
	}
	return copied
}

// TimeoutMS 是声明里的单次调用超时上限。控制台展示与派发读取同一个值，因此
// 「界面说最多等多久」与「实际等多久」不会分叉。
func (t *PluginTool) TimeoutMS() int {
	if t == nil {
		return 0
	}
	return t.declaration.TimeoutMS
}

// SettingsPath 返回该插件的管理设置页路径；插件没有设置页时返回空串。
//
// 路径不含 /control-panel 前缀，由前端用 adminRoutes.path 拼；与邮件设置页
// 打开提供商设置的方式一致。只有真的声明了设置页才返回，避免把运营者送到 404。
func (t *PluginTool) SettingsPath() string {
	if t == nil || !extensionHasSettingsPage(t.extension) {
		return ""
	}
	return "/extensions/" + t.extension.ID + "/pages/settings"
}

// extensionHasSettingsPage 判断扩展是否真的有设置页：声明了 settings 文档，或
// 声明了一个 path=/settings 的管理页。两者任一成立，动态管理页路由才存在。
func extensionHasSettingsPage(extension extensions.Extension) bool {
	manifest := extension.Manifest
	if len(manifest.Settings) > 0 || manifest.SettingsDocument.Explicit {
		return true
	}
	for _, page := range extensionmanifest.EffectiveAdminPages(manifest) {
		if extensionmanifest.NormalizeRoutePath(page.Path) == "/settings" {
			return true
		}
	}
	return false
}

// Invoke 执行一次工具调用。
//
// 超时来自声明且不可协商：调用时若插件进程慢，这里返回的错误只会被编排层记为
// 「工具执行失败」并给模型一句通用说明，不会把插件的内部错误串带进社区发布物。
func (t *PluginTool) Invoke(ctx context.Context, call supportai.ToolCall, env supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.invoker == nil {
		return supportai.ToolResult{}, errors.New("ai: plugin tool is not wired")
	}
	// 名字必须匹配声明：模型可能凭空捏造一个名字，也可能在多个插件贡献同名
	// 工具时张冠李戴。宿主只对声明过的组合发起调用。
	if call.Name != t.declaration.Name {
		return supportai.ToolResult{Content: "工具 " + call.Name + " 不可用。", IsError: true}, nil
	}
	timeout := time.Duration(t.declaration.TimeoutMS) * time.Millisecond
	dispatch := PluginToolCall{
		DeclarationID:   t.declaration.ID,
		ContractVersion: t.declaration.ContractVersion,
		Handler:         t.declaration.Handler,
		Timeout:         timeout,
		Name:            t.declaration.Name,
		Arguments:       call.Arguments,
	}
	// 只填清单声明过的事实：默认集合只有 locale，viewer 需要作者显式声明才披露。
	if t.declaration.DeclaresAIToolContext(extensionmanifest.AIToolContextLocale) {
		dispatch.Locale = env.Locale
	}
	if t.declaration.DeclaresAIToolContext(extensionmanifest.AIToolContextTopic) {
		dispatch.TopicID = env.TopicID
	}
	if t.declaration.DeclaresAIToolContext(extensionmanifest.AIToolContextViewer) {
		dispatch.ViewerUserID = env.ViewerUserID
	}
	response, err := t.invoker.InvokeVersionedAITool(ctx, t.extension, dispatch)
	if err != nil {
		// 调用本身的失败：返回 error，由编排层统一处理（记日志 + 给模型通用
		// 说明）。刻意不把 err 的文案放进 ToolResult——插件的内部错误串不该
		// 借模型之口发布到社区。
		return supportai.ToolResult{}, err
	}
	return supportai.ToolResult{Content: response.Content, IsError: response.IsError}, nil
}

// PluginToolRegistry 收集插件贡献的工具，并把它们注册进宿主的 ToolRegistry。
//
// 注册表是「站内有哪些 AI 工具」的唯一可枚举答案：Core 内置工具与插件工具登记
// 在同一处，因此运营者可以一次看到全部。
type PluginToolRegistry struct {
	tools map[string]*PluginTool
}

func NewPluginToolRegistry() *PluginToolRegistry {
	return &PluginToolRegistry{tools: map[string]*PluginTool{}}
}

// Register 登记一个插件工具。同名工具拒绝后注册者：模型看到的工具名必须唯一，
// 否则一次调用该派给谁就是不确定的。
func (r *PluginToolRegistry) Register(tool *PluginTool) error {
	if r == nil || tool == nil {
		return supportai.ErrToolLoopUnavailable
	}
	name := tool.Definition().Name
	if !supportai.ValidToolName(name) {
		return fmt.Errorf("%w: invalid tool name %q", ErrAIToolDeclarationInvalid, name)
	}
	if r.tools == nil {
		r.tools = map[string]*PluginTool{}
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("%w: duplicate tool %s", ErrAIToolDeclarationInvalid, name)
	}
	r.tools[name] = tool
	return nil
}

// Tools 返回全部已登记工具，按名字排序以保证枚举顺序稳定。
func (r *PluginToolRegistry) Tools() []*PluginTool {
	if r == nil || len(r.tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*PluginTool, 0, len(names))
	for _, name := range names {
		out = append(out, r.tools[name])
	}
	return out
}

// Apply 把插件工具合并进宿主工具登记表。
//
// 已存在的名字被跳过而不报错：Core 内置工具先注册，插件不得覆盖它们。这条规则
// 与「插件不得替换 Core 行为」是同一条边界——否则一个插件就能把 forum-search
// 换成自己的实现，而运营者完全看不出来。
func (r *PluginToolRegistry) Apply(registry *supportai.ToolRegistry) error {
	if r == nil || registry == nil {
		return supportai.ErrToolLoopUnavailable
	}
	for _, tool := range r.Tools() {
		if _, exists := registry.Lookup(tool.Definition().Name); exists {
			continue
		}
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}
