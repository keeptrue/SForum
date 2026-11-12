// Package capabilities 定义插件 Host 能力目录与运行时授权检查（F2.1）。
//
// 能力与 RBAC permission 不同：permission 约束用户/管理员；capability 约束
// 插件子进程可调用的宿主能力（出站网络、入队、读用户等）。
package capabilities

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// 稳定 capability key。新增时同步 Catalog、OpenAPI 与知识库。
const (
	NetOutbound      = "net.outbound"
	JobsEnqueue      = "jobs.enqueue"
	SettingsOwn      = "settings.own"
	PermissionsCheck = "permissions.check"
	UsersRead        = "users.read"
	// UsersEmailRead 允许插件通过「能力 + actor 委派」门控的同意式查询读取
	// **调用者本人**的主邮箱。它永远不进入稳定视图或公开查询，也永远不授予
	// 读取其他用户邮箱的权力；每次读取都由宿主审计。
	UsersEmailRead = "users.email.read"
	AuditAppend    = "audit.append"
	HostAPI        = "host.api"
	// AIInvoke 允许插件经宿主 AI 网关发起补全：成本记账、限流与审计都归宿主。
	AIInvoke = "ai.invoke"
	// EventsEmit 允许插件向宿主事件通道发布自身命名空间下的事件。
	EventsEmit = "events.emit"
	// BadgesFactWrite 允许插件通过 Host 命令上报用户维度的徽章事实计数。
	// 它不授予任何表访问：写入只有一个入口（host 命令），宿主负责取整、边界与审计。
	BadgesFactWrite = "badges.fact.write"
	// Trusted automation: process capabilities, never human RBAC or PAT scopes.
	ExtensionsRead   = "extensions.read"
	ExtensionsCall   = "extensions.call"
	ExtensionsManage = "extensions.manage"
)

// Risk 等级：admin 启用审查 UI 按此排序与着色。
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// ErrUnknown 声明了目录外的 capability。
var ErrUnknown = errors.New("capabilities: unknown capability")

// ErrDenied 运行时缺少授权。
var ErrDenied = errors.New("capabilities: denied")

// Definition 是目录中的一条能力。
type Definition struct {
	Key         string `json:"key"`
	Risk        string `json:"risk"`
	LabelZH     string `json:"labelZh"`
	LabelEN     string `json:"labelEn"`
	Description string `json:"description"`
}

// Grant 是解析后的能力（含是否由宿主推断）。
type Grant struct {
	Key         string `json:"key"`
	Risk        string `json:"risk"`
	LabelZH     string `json:"labelZh"`
	LabelEN     string `json:"labelEn"`
	Description string `json:"description"`
	Implied     bool   `json:"implied,omitempty"`
}

var catalog = []Definition{
	{
		Key:         HostAPI,
		Risk:        RiskLow,
		LabelZH:     "调用宿主 API",
		LabelEN:     "Call Host API",
		Description: "Use the versioned host surface (sforum.host/v1) for permission checks, settings, jobs, and audit.",
	},
	{
		Key:         SettingsOwn,
		Risk:        RiskLow,
		LabelZH:     "读写自身设置",
		LabelEN:     "Own extension settings",
		Description: "Read and write settings belonging only to this extension.",
	},
	{
		Key:         PermissionsCheck,
		Risk:        RiskLow,
		LabelZH:     "校验用户权限",
		LabelEN:     "Check user permissions",
		Description: "Ask the host whether an actor holds a permission key; cannot grant permissions.",
	},
	{
		Key:         JobsEnqueue,
		Risk:        RiskMedium,
		LabelZH:     "入队后台任务",
		LabelEN:     "Enqueue background jobs",
		Description: "Enqueue River jobs for job kinds declared in the plugin manifest only.",
	},
	{
		Key:         AuditAppend,
		Risk:        RiskMedium,
		LabelZH:     "追加审计记录",
		LabelEN:     "Append audit events",
		Description: "Append structured audit events under this extension's namespace.",
	},
	{
		Key:         NetOutbound,
		Risk:        RiskHigh,
		LabelZH:     "出站网络",
		LabelEN:     "Outbound network",
		Description: "Open outbound network connections (HTTP, SMTP, etc.) from the plugin process or via Host helpers.",
	},
	{
		Key:         UsersRead,
		Risk:        RiskHigh,
		LabelZH:     "读取用户信息",
		LabelEN:     "Read user profiles",
		Description: "Read safe, non-secret user fields through the Host API.",
	},
	{
		Key:         UsersEmailRead,
		Risk:        RiskHigh,
		LabelZH:     "读取本人邮箱地址",
		LabelEN:     "Read own email address",
		Description: "Read the signed-in actor's own primary email address through the capability- and delegation-gated consent query. Never grants access to other users, never enters stable views, and every read is audited.",
	},
	{
		Key:         ExtensionsRead,
		Risk:        RiskMedium,
		LabelZH:     "读取扩展清单",
		LabelEN:     "Read extension inventory",
		Description: "Read a redacted extension inventory and public runtime/contract state. Never exposes secrets, trust tokens, package paths, or credentials.",
	},
	{
		Key:         ExtensionsCall,
		Risk:        RiskMedium,
		LabelZH:     "调用扩展服务",
		LabelEN:     "Call extension services",
		Description: "Invoke declared plugin services or providers through Host Service Discovery with live exact-artifact admission.",
	},
	{
		Key:         ExtensionsManage,
		Risk:        RiskHigh,
		LabelZH:     "管理受信任扩展",
		LabelEN:     "Manage trusted extensions",
		Description: "Perform allowlisted settings update/reset/action and disable of already-trusted non-system non-self plugins via Host Commands. Never replaces super_admin trust confirmation.",
	},
	{
		Key:         AIInvoke,
		Risk:        RiskMedium,
		LabelZH:     "调用宿主 AI 网关",
		LabelEN:     "Invoke Host AI gateway",
		Description: "Send AI completion requests through the Host gateway so cost accounting, rate limits, and audit stay host-owned. Subject and cost gates still apply per request.",
	},
	{
		Key:         EventsEmit,
		Risk:        RiskLow,
		LabelZH:     "发布自身命名空间事件",
		LabelEN:     "Emit own-namespace events",
		Description: "Publish events strictly under this extension's namespace through the Host event channel; core event names and other plugins' namespaces stay closed.",
	},
	{
		// 启用确认弹窗只渲染 label：这一行就是运营判断风险时读到的全部文案，
		// 因此必须自解释成「写入用户维度的插件事实计数」，而不是笼统的「徽章」。
		Key:         BadgesFactWrite,
		Risk:        RiskMedium,
		LabelZH:     "写入插件事实计数",
		LabelEN:     "Write plugin fact counters",
		Description: "Report per-user badge fact counters only through the Host badges.fact.record command. The Host clamps deltas, enforces bounds, and audits every write; it never grants direct access to badge or counter tables.",
	},
}

var catalogByKey map[string]Definition

func init() {
	catalogByKey = make(map[string]Definition, len(catalog))
	for _, def := range catalog {
		catalogByKey[def.Key] = def
	}
}

// Catalog 返回完整能力目录（稳定顺序）。
func Catalog() []Definition {
	out := make([]Definition, len(catalog))
	copy(out, catalog)
	return out
}

// Known 判断 key 是否在目录中。
func Known(key string) bool {
	_, ok := catalogByKey[strings.TrimSpace(key)]
	return ok
}

// Find 查找单条定义。
func Find(key string) (Definition, bool) {
	def, ok := catalogByKey[strings.TrimSpace(key)]
	return def, ok
}

// ValidateKeys 校验声明列表：去重、必须已知。
func ValidateKeys(keys []string) error {
	seen := make(map[string]struct{}, len(keys))
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" {
			return fmt.Errorf("%w: empty key", ErrUnknown)
		}
		if !Known(key) {
			return fmt.Errorf("%w: %s", ErrUnknown, key)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate %s", ErrUnknown, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// NormalizeKeys 去空白、去重、稳定排序。
func NormalizeKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Set 是启用后授予的能力集合，供运行时 O(1) 检查。
type Set map[string]struct{}

// NewSet 从 key 列表构建集合。
func NewSet(keys []string) Set {
	set := make(Set, len(keys))
	for _, key := range NormalizeKeys(keys) {
		set[key] = struct{}{}
	}
	return set
}

// Has 是否包含能力。
func (s Set) Has(key string) bool {
	if s == nil {
		return false
	}
	_, ok := s[strings.TrimSpace(key)]
	return ok
}

// Require 缺少能力时返回 ErrDenied。
func (s Set) Require(key string) error {
	if s.Has(key) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrDenied, key)
}

// Keys 返回排序后的 key 列表。
func (s Set) Keys() []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, 0, len(s))
	for key := range s {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// GrantsFromKeys 将 key 列表展开为带文案的 Grant（未知 key 跳过）。
func GrantsFromKeys(keys []string, implied map[string]bool) []Grant {
	keys = NormalizeKeys(keys)
	out := make([]Grant, 0, len(keys))
	for _, key := range keys {
		def, ok := Find(key)
		if !ok {
			continue
		}
		out = append(out, Grant{
			Key:         def.Key,
			Risk:        def.Risk,
			LabelZH:     def.LabelZH,
			LabelEN:     def.LabelEN,
			Description: def.Description,
			Implied:     implied[key],
		})
	}
	// 高风险优先展示，便于启用审查。
	sort.SliceStable(out, func(i, j int) bool {
		return riskRank(out[i].Risk) > riskRank(out[j].Risk)
	})
	return out
}

func riskRank(risk string) int {
	switch risk {
	case RiskHigh:
		return 3
	case RiskMedium:
		return 2
	case RiskLow:
		return 1
	default:
		return 0
	}
}

// RequiresConfirmation 是否需要运营在启用时显式确认（有任意能力即确认）。
func RequiresConfirmation(keys []string) bool {
	return len(NormalizeKeys(keys)) > 0
}
