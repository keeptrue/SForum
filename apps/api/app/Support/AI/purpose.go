package ai

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// 用途（purpose）是 AI 平台的结构性单位：每一次补全都属于某一个用途，用途声明
// 这次调用的形状——它花哪一档成本、失败时怎么降级、模型能看见哪些工具、它服务
// 哪种资源。这些事实此前散落在各调用方（回复生成器自带白名单常量、网关 completer
// 硬写经济档、失败姿态只存在于站点设置的一个 map 里），于是「站点上有哪些 AI
// 用途」没有一个可枚举的答案。
//
// 登记表把用途变成声明：Core 内置用途与（后续）插件贡献的用途登记在同一处，
// 运营者能一次看到全部，控制台也能按用途而不是按某一项特例来配置。
var (
	// ErrPurposeUnknown 表示请求的用途没有登记。它必须失败关闭：一个未声明的
	// 用途意味着没有人声明过它的成本等级与工具边界，放行等于让调用方自定义策略。
	ErrPurposeUnknown = errors.New("ai: purpose is unknown")
	// ErrPurposeInvalid 表示登记或使用了一条例外声明。
	ErrPurposeInvalid = errors.New("ai: purpose declaration is invalid")
)

// Core purpose identifiers.
const (
	// CorePurposeForumReply 是以机器人身份回复社区成员的用途。
	CorePurposeForumReply = "forum.reply"
	// CorePurposeForumReplyTriage 是自主参与模式的判定用途：每条新发言先由
	// 经济档模型判一次「值不值得回」，判 REPLY 才进入正式回复生成。它没有
	// 工具（判定不需要查站内资料），失败时按 closed 丢弃——判定失败的含义
	// 是「这次不回复」，fail_open 会把每次网关故障放大成一轮意外刷屏。
	CorePurposeForumReplyTriage = "forum.reply.triage"
	// 同值；保留两个名字是因为 diagnose.go 的常量先存在，登记表这里按
	// 用途命名空间的习惯再暴露一次。
	CorePurposeDiagnose = "admin.connectivity_test"
)

// CorePurposes 是 Core 内置用途声明所在的命名空间。插件贡献的用途使用自己的
// 扩展 id 作为前缀，两个命名空间不得互相占用。
const CorePurposeNamespace = "core"

// PurposeBlastRadius 描述一次用途调用失败时影响的范围。它出现在登记表与执行
// 记录里，让运营者能从「谁会被这次失败影响」的角度判断风险，而不是只看成本。
const (
	PurposesBlastRadiusSingle = "single" // 只影响触发它的那一次交互
	PurposesBlastRadiusBatch  = "batch"  // 影响一批资源或一次批量任务
	PurposesBlastRadiusSite   = "site"   // 影响全站可见内容或公共派生数据
)

// PurposeDeclaration 是一条用途声明。
//
// 它是静态事实：运行期不需要问调用方「你是什么用途」，因为用途必须由登记表
// 决定成本等级与工具边界，调用方只能请求一个已登记的 id。
type PurposeDeclaration struct {
	// ID 是稳定标识，也是执行记录与用量归属里的用途名。Core 用途使用
	// "<namespace>.<name>" 形式，例如 forum.reply。
	ID string
	// Label 是给运营者看的双语名称。控制台用它渲染用途清单。
	Label LocalizedPurposeLabel
	// Description 说明这个用途做什么、什么时候被触发。
	Description LocalizedPurposeLabel
	// CostClass 是用途声明的成本等级。用途只声明等级，具体使用哪个供应商
	// profile 由站点配置的成本等级绑定决定——调用方永远不能指定模型。
	CostClass string
	// FailurePosture 是 AI 不可用时该用途的降级方式。
	FailurePosture string
	// ToolAllowlist 是用途声明的默认模型可见工具。站点的按用途覆盖优先于它；
	// 未被覆盖时这就是生效值。空列表表示该用途不使用工具。
	ToolAllowlist []string
	// ResourceType 是该用途服务的资源种类（topic、comment 等）。它进入执行记录，
	// 让运营者能按资源反查 AI 花了什么钱。
	ResourceType string
	// PromptVersion 是产出这次结果的提示词版本。它参与缓存键与执行记录，
	// 改动提示词时必须递增，否则历史记录无法对应到产出它的那版提示词。
	PromptVersion string
	// BlastRadius 表示失败或降级时的影响范围。
	BlastRadius string
}

// LocalizedPurposeLabel 是用途名称/说明的双语文本。它是值类型且不可为空：
// 运营者看到的清单里不该有「一个没有名字的用途」。
type LocalizedPurposeLabel struct {
	Default string `json:"default"`
	EnUS    string `json:"en-US,omitempty"`
}

// Localized 返回请求语言下的文本，缺失时回退到默认文本。
func (l LocalizedPurposeLabel) Localized(locale string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "en") && strings.TrimSpace(l.EnUS) != "" {
		return l.EnUS
	}
	return l.Default
}

// PurposeRegistry 是站内用途的唯一可枚举答案。
//
// 登记表在装配期构造，之后只读——运行期增删用途会让同一次请求里的工具边界
// 与成本等级变来变去，而模型可能已经基于旧声明发起了调用。
type PurposeRegistry struct {
	purposes map[string]PurposeDeclaration
}

// NewPurposeRegistry 以给定声明构造登记表。重复 id 或非法声明直接失败：
// 装配错误必须在启动期可见，而不是在第一次调用时变成一次静默降级。
func NewPurposeRegistry(declarations ...PurposeDeclaration) (*PurposeRegistry, error) {
	registry := &PurposeRegistry{purposes: make(map[string]PurposeDeclaration, len(declarations))}
	for _, declaration := range declarations {
		if err := registry.Register(declaration); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// Register 登记一条用途声明。同名用途拒绝后注册者：模型看到的成本等级与工具
// 边界必须唯一，否则「这次调用花哪一档」就成了不确定的。
func (r *PurposeRegistry) Register(declaration PurposeDeclaration) error {
	if r == nil {
		return fmt.Errorf("%w: registry is not constructed", ErrPurposeInvalid)
	}
	if err := ValidatePurposeDeclaration(declaration); err != nil {
		return err
	}
	id := strings.TrimSpace(declaration.ID)
	if _, exists := r.purposes[id]; exists {
		return fmt.Errorf("%w: duplicate purpose %s", ErrPurposeInvalid, id)
	}
	declaration.ID = id
	declaration.ToolAllowlist = append([]string(nil), declaration.ToolAllowlist...)
	r.purposes[id] = declaration
	return nil
}

// Lookup 按 id 取一条声明。
func (r *PurposeRegistry) Lookup(id string) (PurposeDeclaration, bool) {
	if r == nil {
		return PurposeDeclaration{}, false
	}
	declaration, ok := r.purposes[strings.TrimSpace(id)]
	return declaration, ok
}

// Purposes 返回全部声明，按 id 排序以保证枚举顺序稳定。
func (r *PurposeRegistry) Purposes() []PurposeDeclaration {
	if r == nil || len(r.purposes) == 0 {
		return nil
	}
	out := make([]PurposeDeclaration, 0, len(r.purposes))
	for _, declaration := range r.purposes {
		out = append(out, declaration)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ValidatePurposeDeclaration 校验一条用途声明。调用方（网关命令、插件用途
// 贡献）与登记表共用它，保证「能通过校验的声明」与「能登记的用途」是同一集合。
func ValidatePurposeDeclaration(declaration PurposeDeclaration) error {
	id := strings.TrimSpace(declaration.ID)
	if id == "" || id != declaration.ID || len(id) > MaxPurposeIDLen || !validPurposeID(id) {
		return fmt.Errorf("%w: invalid purpose id", ErrPurposeInvalid)
	}
	if strings.TrimSpace(declaration.Label.Default) == "" {
		return fmt.Errorf("%w: a purpose needs a human label", ErrPurposeInvalid)
	}
	if !ValidCostClass(declaration.CostClass) {
		return fmt.Errorf("%w: invalid cost class %q", ErrPurposeInvalid, declaration.CostClass)
	}
	if !ValidFailurePosture(declaration.FailurePosture) {
		return fmt.Errorf("%w: invalid failure posture %q", ErrPurposeInvalid, declaration.FailurePosture)
	}
	if !ValidBlastRadius(declaration.BlastRadius) {
		return fmt.Errorf("%w: invalid blast radius %q", ErrPurposeInvalid, declaration.BlastRadius)
	}
	if len(declaration.ToolAllowlist) > MaxToolsPerRequest {
		return fmt.Errorf("%w: tool allowlist exceeds one request budget", ErrPurposeInvalid)
	}
	seen := make(map[string]bool, len(declaration.ToolAllowlist))
	for _, name := range declaration.ToolAllowlist {
		name = strings.TrimSpace(name)
		if !ValidToolName(name) || seen[name] {
			return fmt.Errorf("%w: invalid tool name %q", ErrPurposeInvalid, name)
		}
		seen[name] = true
	}
	return nil
}

// ValidBlastRadius 限制登记表接受的影响范围取值。
func ValidBlastRadius(radius string) bool {
	switch radius {
	case PurposesBlastRadiusSingle, PurposesBlastRadiusBatch, PurposesBlastRadiusSite:
		return true
	default:
		return false
	}
}

func validPurposeID(id string) bool {
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// BuiltinPurposes 返回 Core 内置用途。回复是第一个也是当前唯一的消费方；
// 内容用途（摘要/翻译/摘要信）落地时在这里增加条目，而不是在各调用方加特例。
func BuiltinPurposes() []PurposeDeclaration {
	return []PurposeDeclaration{
		{
			ID: CorePurposeForumReply,
			Label: LocalizedPurposeLabel{
				Default: "AI 回复", EnUS: "AI reply",
			},
			Description: LocalizedPurposeLabel{
				Default: "以机器人身份回复社区成员并引用站内资料。",
				EnUS:    "Answer a community member as the bot, citing in-site material.",
			},
			CostClass:      CostClassEconomy,
			FailurePosture: FailurePostureOpen,
			ToolAllowlist:  append([]string(nil), CoreReplyToolAllowlist...),
			ResourceType:   "topic",
			PromptVersion:  CoreReplyPromptVersion,
			BlastRadius:    PurposesBlastRadiusSingle,
		},
		{
			ID: CorePurposeForumReplyTriage,
			Label: LocalizedPurposeLabel{
				Default: "AI 回复判定", EnUS: "AI reply triage",
			},
			Description: LocalizedPurposeLabel{
				Default: "自主参与模式下判断一条新发言是否值得 AI 回复；不产生任何用户可见输出。",
				EnUS:    "Decide whether a new post deserves an AI reply in autonomous mode; produces no user-visible output.",
			},
			CostClass:      CostClassEconomy,
			FailurePosture: FailurePostureClosed,
			ToolAllowlist:  nil,
			ResourceType:   "topic",
			PromptVersion:  CoreTriagePromptVersion,
			BlastRadius:    PurposesBlastRadiusSingle,
		},
		{
			ID: CorePurposeDiagnose,
			Label: LocalizedPurposeLabel{
				Default: "连通性探测", EnUS: "Connectivity probe",
			},
			Description: LocalizedPurposeLabel{
				Default: "控制台发起的最小真实调用，验证当前供应商与密钥可用。",
				EnUS:    "Minimal real call from the admin console to verify provider and credential health.",
			},
			// 探测刻意走最便宜的等级；失败必须显式报错（fail_closed），否则
			// 运营者会把「供应商不可用」误读成「连接正常」。
			CostClass:      CostClassEconomy,
			FailurePosture: FailurePostureClosed,
			ToolAllowlist:  nil,
			ResourceType:   "diagnose",
			PromptVersion:  DiagnosePromptVersion,
			BlastRadius:    PurposesBlastRadiusSingle,
		},
	}
}

// BuiltinPurposeRegistry 构造 Core 内置用途登记表。声明是编译期常量，因此
// 构造失败只可能是代码缺陷——装配路径应当把错误当成启动失败，而不是忽略它。
func BuiltinPurposeRegistry() (*PurposeRegistry, error) {
	return NewPurposeRegistry(BuiltinPurposes()...)
}

// MustBuiltinPurposeRegistry 用于装配期：内置用途不合法属于编程错误。
func MustBuiltinPurposeRegistry() *PurposeRegistry {
	registry, err := BuiltinPurposeRegistry()
	if err != nil {
		panic("ai: built-in purpose registry is invalid: " + err.Error())
	}
	return registry
}

var (
	defaultPurposeRegistryOnce sync.Once
	defaultPurposeRegistry     *PurposeRegistry
)

// DefaultPurposeRegistry 返回 Core 内置用途的单例登记表。
//
// 它是「没被显式注入时的默认登记表」：调用方（生成器的回退路径、控制台枚举）
// 拿到的必须是同一份声明集合，否则「回复用途的成本等级是什么」在进程内会有两个
// 答案。声明是编译期常量，因此构造失败属于编程错误，这里 panic 而不是返回错误。
func DefaultPurposeRegistry() *PurposeRegistry {
	defaultPurposeRegistryOnce.Do(func() {
		defaultPurposeRegistry = MustBuiltinPurposeRegistry()
	})
	return defaultPurposeRegistry
}
