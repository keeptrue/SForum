package toolplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 插件贡献工具的契约边界。
//
// 插件提供的工具与 Core 内置工具走完全同一条执行路径（Orchestrator 的步数预算、
// 逐步闸门、结果截断），区别只在于：Core 工具是进程内函数调用，插件工具是一次
// Host → plugin RPC。因此插件工具必须自带 RPC 边界的约束——超时、schema 与
// 结果体积都由声明钉死，运行期不能放宽。
const (
	// AIToolSlot 是插件工具声明所在的 provider slot。它复用既有的 versioned
	// ProviderCall 传输，因此不需要新增 proto 服务。
	AIToolSlot      = "sforum.ai.tool"
	AIToolOperation = "invoke"

	AIToolRequestSchema  = "sforum.ai.tool.invoke.request@1"
	AIToolResponseSchema = "sforum.ai.tool.invoke.response@1"

	// 契约版本：参数或结果形状变更时必须递增，否则新旧插件的产物会被当成
	// 同一种能力混在同一个登记表里。
	AIToolContract = "sforum.ai.tool@1"

	// 单个插件可贡献的工具数。它与 MaxToolsPerRequest(16) 刻意取同一个量级但
	// 语义不同：一次请求最多带 16 个工具，而一个插件最多贡献 16 个工具。
	AIToolsMaximumPerExtension = 16

	// 工具名上限沿用供应商实际接受的模式，与 Core 工具名同源。
	AIToolNameMaximumLength = supportai.MaxToolNameLen
)

var (
	// ErrAIToolDeclarationInvalid 表示工具声明不满足契约。
	ErrAIToolDeclarationInvalid = errors.New("ai: tool declaration is invalid")
)

// AIToolDeclaration 是一条插件工具声明。它是清单里的静态事实：运行期不需要
// 问插件「你有哪些工具」，宿主在插件启动前就知道了。
type AIToolDeclaration struct {
	// ID 是声明的稳定标识（ExtensionID 命名空间下）。
	ID string
	// Name 是模型看到的工具名。它与 ID 分开，因为工具名的字符集受供应商约束
	// （只允许 [a-z0-9_-]，且不含点号），而 ID 是清单侧的稳定标识。
	Name string
	// Description 是给模型看的说明，决定模型什么时候调用它。
	Description string
	// Parameters 是 JSON Schema 对象（不是数组、不是裸 true）。
	Parameters json.RawMessage
	// Handler 是插件侧的可执行 handler。
	Handler   string
	TimeoutMS int
}

// AIToolCall 是一次工具调用。Arguments 是模型给出的原始 JSON 字符串——与
// Core 工具一致，解析失败属于「工具可解释的失败」，应当回给模型而不是让整次
// 补全失败。
type AIToolCall struct {
	Name      string
	Arguments string
}

// AIToolResult 是插件工具给模型的回答。
//
// IsError 为 true 时 Content 是给模型看的错误说明（参数非法、目标不存在等），
// 模型可以据此换一种方式重试；它与「调用本身失败」是两回事——后者由
// ErrAIToolUnavailable 表达，宿主只给模型一句通用说明，不让插件的内部错误串
// 借模型之口发布到社区。
type AIToolResult struct {
	Content string
	IsError bool
}

// ValidateAIToolDeclaration 校验一条声明。它同时被清单校验（作者侧）与登记表
// 注册（运行期）调用，保证「能通过校验的清单」与「能注册的工具」是同一集合。
func ValidateAIToolDeclaration(extensionID string, declaration extensionmanifest.ManifestAITool) error {
	if declaration.Name == "" || declaration.Handler == "" || strings.TrimSpace(declaration.Description) == "" {
		return ErrAIToolDeclarationInvalid
	}
	// Name 必须匹配供应商实际接受的模式；点号不在允许集合内。
	if !supportai.ValidToolName(declaration.Name) {
		return fmt.Errorf("%w: invalid tool name %q", ErrAIToolDeclarationInvalid, declaration.Name)
	}
	if len(strings.TrimSpace(declaration.Description)) > supportai.MaxToolDescriptionBytes {
		return fmt.Errorf("%w: description too long", ErrAIToolDeclarationInvalid)
	}
	// 界面文案是可选声明，但一旦出现就必须与清单侧同源校验：运行期登记表是
	// 第二个入口，不能因为清单在安装时校验过就默认这里的值一定合法（数据库里
	// 可能躺着旧规则下的历史产物）。
	if err := validateToolDescriptionI18n(declaration.DescriptionI18n); err != nil {
		return err
	}
	// Parameters 是 JSON Schema 对象。Core 不解释它的语义，但必须保证它是合法
	// JSON 且是对象——供应商会在请求层直接拒绝其它形状。
	if err := validateToolParameters(declaration.Parameters); err != nil {
		return err
	}
	if declaration.TimeoutMS <= 0 || declaration.TimeoutMS > extensionmanifest.AIToolMaximumTimeoutMS {
		return fmt.Errorf("%w: timeout must be 1..%d", ErrAIToolDeclarationInvalid, extensionmanifest.AIToolMaximumTimeoutMS)
	}
	// Handler 与 ID 必须落在该插件自己的命名空间下：一个插件不得注册另一个
	// 插件的 handler，也不得占用它没有声明过的 ID。
	if !extensionmanifest.IsOwnedID(extensionID, declaration.Handler) ||
		!extensionmanifest.IsOwnedID(extensionID, declaration.ID) {
		return fmt.Errorf("%w: id and handler must be namespaced under %q", ErrAIToolDeclarationInvalid, extensionID)
	}
	return nil
}

// validateToolParameters 只校验形状：合法 JSON、且顶层是对象。
// 语义（properties/required 是否自洽）由供应商在使用时判定，宿主不做第二次
// 解释——否则插件和宿主会对同一份 schema 产生两套不同的理解。
func validateToolParameters(raw json.RawMessage) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return fmt.Errorf("%w: parameters are required", ErrAIToolDeclarationInvalid)
	}
	if len(trimmed) > supportai.MaxToolSchemaBytes {
		return fmt.Errorf("%w: parameters too large", ErrAIToolDeclarationInvalid)
	}
	if !json.Valid([]byte(trimmed)) {
		return fmt.Errorf("%w: parameters are not valid JSON", ErrAIToolDeclarationInvalid)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil || probe == nil {
		return fmt.Errorf("%w: parameters must be a JSON object", ErrAIToolDeclarationInvalid)
	}
	return nil
}

// aiToolDescriptionLocalesMaximum 与清单 schema 的 maxProperties 对齐：界面文案
// 不是数据面，语言种数只需要覆盖控制台的实际语言，超出只会撑大载荷。
const aiToolDescriptionLocalesMaximum = 8

// validateToolDescriptionI18n 校验可选的界面文案。字段缺失是合法状态（旧插件），
// 一旦出现则每条都必须可用：空文案会让控制台显示一个空行，超长文案会把
// GET /admin/ai/settings 撑大。
func validateToolDescriptionI18n(descriptions map[string]string) error {
	if len(descriptions) == 0 {
		return nil
	}
	if len(descriptions) > aiToolDescriptionLocalesMaximum {
		return fmt.Errorf("%w: too many display locales", ErrAIToolDeclarationInvalid)
	}
	for locale, text := range descriptions {
		if strings.TrimSpace(locale) == "" || locale != strings.TrimSpace(locale) {
			return fmt.Errorf("%w: invalid display locale %q", ErrAIToolDeclarationInvalid, locale)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return fmt.Errorf("%w: empty display description for %q", ErrAIToolDeclarationInvalid, locale)
		}
		if len(trimmed) > supportai.MaxToolDescriptionBytes {
			return fmt.Errorf("%w: display description too long", ErrAIToolDeclarationInvalid)
		}
	}
	return nil
}

// AIToolDescriptor 是登记表的可枚举条目。它比声明多一个 ExtensionID，因为
// 登记表是全局的：同名工具在不同插件下必须能被区分。
type AIToolDescriptor struct {
	ExtensionID string
	Version     string
	Digest      string
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolDefinition 转成给模型看的声明。
func (d AIToolDescriptor) ToolDefinition() supportai.ToolDefinition {
	name := d.Name
	if name == "" {
		return supportai.ToolDefinition{}
	}
	// Core 工具名是全局唯一的，插件工具名则可能在多个插件之间重复。冲突时
	// 登记表会拒绝后注册者（见 ToolRegistry 的注册规则），因此这里不做改写：
	// 让模型看到的名字与插件声明的名字完全一致，避免提示词与产物对不上。
	return supportai.ToolDefinition{
		Name:        name,
		Description: d.Description,
		Parameters:  d.Parameters,
	}
}
