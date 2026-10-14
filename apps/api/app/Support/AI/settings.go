package ai

import (
	"errors"
	"strings"
	"time"
)

// SettingsSchemaVersion 是站点 AI 配置的稳定身份。
const SettingsSchemaVersion = "sforum.ai.settings@1"

// 失败姿态：AI 不可用时各用途的降级方式。缺省视为 fail_open，即退回规则模式
// 并记录审计标记，而不是拦下用户请求。
const (
	FailurePostureOpen   = "fail_open"
	FailurePostureClosed = "fail_closed"
	FailurePostureSkip   = "skip_ai"
)

// 自动裁决可用的动作。删除类动作不在此列，见 ValidAutoAction。
const (
	ActionHide   = "hide"
	ActionTag    = "tag"
	ActionReject = "reject"
)

var (
	// ErrSettingsInvalid 表示站点 AI 配置不满足约束。
	ErrSettingsInvalid = errors.New("ai: settings are invalid")
)

// AutoActionSettings 控制「高置信度时自动执行动作」这一策略开关。默认关闭；
// 即使开启，动作范围也受 ValidAutoAction 限制。
type AutoActionSettings struct {
	Enabled             bool     `json:"enabled"`
	ConfidenceThreshold float64  `json:"confidenceThreshold"`
	AllowedActions      []string `json:"allowedActions"`
}

// GateSettings 是三道闸门的站点级参数。0 表示该闸门停用。
type GateSettings struct {
	RateLimitPerMinute  int   `json:"rateLimitPerMinute"`
	ExtensionDailyQuota int   `json:"extensionDailyQuota"`
	UserDailyQuota      int   `json:"userDailyQuota"`
	MonthlyBudgetMicros int64 `json:"monthlyBudgetMicros"`
	// ToolCallsPerReply 是单次回复允许的工具调用次数（步数预算）。缺省（nil）表示
	// 使用推荐默认值；显式 0 表示停用工具调用，回复退化为纯文本；上限由
	// MaxToolCallsPerReply 约束。用指针而不是整数，是为了让「升级前保存的文档」
	// 与「运营者明确关闭」是两种不同的状态——前者应当获得新能力，后者不该被覆盖。
	ToolCallsPerReply *int `json:"toolCallsPerReply,omitempty"`
}

// ResolvedToolCallsPerReply 返回实际生效的工具步数预算。
func (g GateSettings) ResolvedToolCallsPerReply() int {
	if g.ToolCallsPerReply == nil {
		return DefaultToolCallsPerReply
	}
	return *g.ToolCallsPerReply
}

func intPointer(value int) *int { return &value }

// RedactionSettings 控制提交给供应商前的内容脱敏。
type RedactionSettings struct {
	Enabled     bool `json:"enabled"`
	RedactEmail bool `json:"redactEmail"`
	RedactPhone bool `json:"redactPhone"`
	RedactIP    bool `json:"redactIp"`
}

// MultimodalSettings 控制图片理解输入。默认关闭：开启后 AI 回复会把主题与
// 被回复评论里的图片附件（最多 MaxImagesPerMessage 张、单张 MaxImageBytes 以内）
// 一并发送给模型。Core 不维护模型能力名单——运营者需要自己确认所选模型支持
// 图片理解；模型不支持时供应商侧会忽略或报错，按既有失败姿态降级，不会崩溃。
type MultimodalSettings struct {
	Enabled bool `json:"enabled"`
}

// PurposeSettings 是某个用途的站点覆盖。缺省（用途不在 map 里，或某项未设置）
// 表示沿用用途自己的声明；显式给出则以站点配置为准。
type PurposeSettings struct {
	// Tools 覆盖该用途的模型可见工具白名单。nil 表示未覆盖（使用用途声明的
	// 默认列表）；非 nil（含空列表）表示以站点配置为准。
	Tools *PurposeToolSettings `json:"tools,omitempty"`
	// FailurePosture 覆盖该用途的失败姿态。空表示未覆盖（使用用途声明 + 全局
	// 默认）。它与旧的 purposeFailurePosture 平级，后者保留给未登记用途。
	FailurePosture string `json:"failurePosture,omitempty"`
}

// PurposeToolSettings 是某个用途的工具白名单覆盖。
type PurposeToolSettings struct {
	Allowed []string `json:"allowed"`
}

// ToolAllowlist 返回该用途的白名单覆盖。第二个返回值为假表示「未覆盖」，
// 调用方应回退到用途自己的声明；返回真时（包括空列表）以站点配置为准。
func (p PurposeSettings) ToolAllowlist() ([]string, bool) {
	if p.Tools == nil {
		return nil, false
	}
	if p.Tools.Allowed == nil {
		// {"tools":{}} 视为显式「不给任何工具」：字段存在即表示运营者接管了这一项。
		return []string{}, true
	}
	names := make([]string, 0, len(p.Tools.Allowed))
	for _, name := range p.Tools.Allowed {
		names = append(names, strings.TrimSpace(name))
	}
	return names, true
}

// Settings 是站点 AI 配置。它覆盖策略层（全部可配）与受限层（可配但有硬
// 天花板）。边界层（记账、trace、密钥处理、排序规则、契约版本）不在其中，
// 也不可由插件或后台改变。
type Settings struct {
	SchemaVersion string `json:"schemaVersion"`
	Enabled       bool   `json:"enabled"`
	// Profiles 是 provider 端点清单。密钥只以 Secret Store 引用形式出现。
	Profiles []Profile `json:"profiles"`
	// CostClassProfiles 把成本等级映射到 profile id。用途只声明成本等级，
	// 由配置决定实际使用哪个模型。
	CostClassProfiles map[string]string `json:"costClassProfiles"`
	// PurposeFailurePosture 覆盖单个用途的失败姿态；缺省为 fail_open。它是
	// 未登记/插件用途的通用出口；已登记用途请优先使用 Purposes 的按用途配置。
	PurposeFailurePosture map[string]string `json:"purposeFailurePosture"`
	// Purposes 是按用途的站点覆盖。键是用途 id；未列出的用途沿用自身声明。
	// 它是「回复特例」的替代品：回复工具的覆盖现在写在 Purposes["forum.reply"]
	// 下，与其它用途走同一条路径。
	Purposes map[string]PurposeSettings `json:"purposes,omitempty"`
	// Reply 是 AI 回复的可配置部分（提示词与旧版工具覆盖）。零值即使用内置默认。
	Reply      ReplySettings      `json:"reply"`
	AutoAction AutoActionSettings `json:"autoAction"`
	Gates      GateSettings       `json:"gates"`
	Redaction  RedactionSettings  `json:"redaction"`
	// Multimodal 是图片理解开关。零值即关闭，升级前的旧配置文档天然兼容。
	Multimodal MultimodalSettings `json:"multimodal"`
	// Revision 每次保存递增，用于在执行记录中快照生效配置版本。
	Revision        int64     `json:"revision"`
	UpdatedByUserID *int64    `json:"updatedByUserId,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
}

// RecommendedSettings 是开箱可用且保守的默认值：总开关关闭、不做任何自动
// 裁决、每插件配额很小、站点预算开启。运营者必须主动调高才会产生规模用量。
func RecommendedSettings() Settings {
	return Settings{
		SchemaVersion: SettingsSchemaVersion,
		Enabled:       false,
		Profiles:      BuiltinProfiles(),
		CostClassProfiles: map[string]string{
			CostClassEconomy:  "deepseek",
			CostClassStandard: "deepseek",
			CostClassPremium:  "deepseek",
		},
		PurposeFailurePosture: map[string]string{},
		AutoAction: AutoActionSettings{
			Enabled:             false,
			ConfidenceThreshold: 0.9,
			AllowedActions:      []string{ActionHide, ActionTag},
		},
		Gates: GateSettings{
			RateLimitPerMinute:  60,
			ExtensionDailyQuota: 500,
			UserDailyQuota:      20,
			MonthlyBudgetMicros: 20 * MicroPerUnit,
			ToolCallsPerReply:   intPointer(DefaultToolCallsPerReply),
		},
		Redaction: RedactionSettings{
			Enabled:     true,
			RedactEmail: true,
			RedactPhone: true,
			RedactIP:    true,
		},
	}
}

func ValidFailurePosture(posture string) bool {
	switch posture {
	case FailurePostureOpen, FailurePostureClosed, FailurePostureSkip:
		return true
	default:
		return false
	}
}

// ValidAutoAction 限制自动裁决可用的动作集合。数据销毁类动作（删除）永不进入
// 白名单，无论开关如何配置；这是硬边界，不是可调项。
func ValidAutoAction(action string) bool {
	switch action {
	case ActionHide, ActionTag, ActionReject:
		return true
	default:
		return false
	}
}

// Normalized 去空白并把 nil map 归一为空 map，避免读路径反复判空。
func (s Settings) Normalized() Settings {
	s.SchemaVersion = SettingsSchemaVersion
	if s.Profiles == nil {
		s.Profiles = []Profile{}
	}
	profiles := make([]Profile, 0, len(s.Profiles))
	for _, profile := range s.Profiles {
		profiles = append(profiles, profile.Normalized())
	}
	s.Profiles = profiles
	if s.CostClassProfiles == nil {
		s.CostClassProfiles = map[string]string{}
	}
	if s.PurposeFailurePosture == nil {
		s.PurposeFailurePosture = map[string]string{}
	}
	if s.AutoAction.AllowedActions == nil {
		s.AutoAction.AllowedActions = []string{}
	}
	if s.Reply.Tools != nil {
		allowed := make([]string, 0, len(s.Reply.Tools.Allowed))
		for _, name := range s.Reply.Tools.Allowed {
			allowed = append(allowed, strings.TrimSpace(name))
		}
		s.Reply.Tools = &ReplyToolSettings{Allowed: allowed}
	}
	// TriggerMode 存储时即归一：未知值落回空串（读路径再按 mention 处理），
	// 避免脏值在配置文档里长期存活。
	if s.Reply.TriggerMode != "" && !ValidTriggerMode(s.Reply.TriggerMode) {
		s.Reply.TriggerMode = ""
	}
	s.migrateLegacyReplyTools()
	s.normalizePurposes()
	return s
}

// migrateLegacyReplyTools 把旧版 reply.tools 覆盖搬进按用途的配置里。
//
// 旧字段只在「按用途配置里还没有回复用途」时生效：升级前的站点有这份配置，
// 迁移后运营者的意图不变；已经用新路径配置过的站点不会被旧字段覆盖回去。
// 旧字段本身保留（不清空），这样回滚到旧版本不会丢失配置。
func (s *Settings) migrateLegacyReplyTools() {
	if s.Reply.Tools == nil {
		return
	}
	if s.Purposes == nil {
		s.Purposes = map[string]PurposeSettings{}
	}
	if _, exists := s.Purposes[CorePurposeForumReply]; exists {
		return
	}
	s.Purposes[CorePurposeForumReply] = PurposeSettings{
		Tools: &PurposeToolSettings{Allowed: append([]string(nil), s.Reply.Tools.Allowed...)},
	}
}

// normalizePurposes 去空白并丢弃「空覆盖」条目：一条什么都不覆盖的用途配置
// 与没有这条配置等价，留着只会让控制台显示一个假的「已自定义」。
func (s *Settings) normalizePurposes() {
	if len(s.Purposes) == 0 {
		s.Purposes = nil
		return
	}
	normalized := make(map[string]PurposeSettings, len(s.Purposes))
	for id, purpose := range s.Purposes {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		entry := PurposeSettings{
			FailurePosture: strings.TrimSpace(purpose.FailurePosture),
		}
		if purpose.Tools != nil {
			allowed := make([]string, 0, len(purpose.Tools.Allowed))
			for _, name := range purpose.Tools.Allowed {
				allowed = append(allowed, strings.TrimSpace(name))
			}
			entry.Tools = &PurposeToolSettings{Allowed: allowed}
		}
		if entry.Tools == nil && entry.FailurePosture == "" {
			continue
		}
		normalized[id] = entry
	}
	if len(normalized) == 0 {
		s.Purposes = nil
		return
	}
	s.Purposes = normalized
}

// Validate 覆盖策略层与受限层。越界值直接拒绝，而不是截断到边界，避免静默
// 改变运营者的意图。
// validateReplyTools 校验回复工具白名单的形状。语义（这个工具是否真的存在）不在这里
// 判定：登记表会随插件启停变化，配置保存时把它钉死会让「先配策略、后装插件」变成
// 不可能；运行期由登记表跳过未注册的名字。
//
// 它同时服务于旧版 reply.tools 与按用途配置（Purposes[...].tools）：两处都是同一
// 种「名称列表」形状，用同一条规则校验，避免两条路径出现不同口径。
func validateToolNameList(allowed []string) error {
	if len(allowed) == 0 {
		return nil
	}
	if len(allowed) > MaxToolsPerRequest {
		return ErrSettingsInvalid
	}
	seen := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		name = strings.TrimSpace(name)
		if !ValidToolName(name) || seen[name] {
			return ErrSettingsInvalid
		}
		seen[name] = true
	}
	return nil
}

func (s Settings) validateReplyTools() error {
	if s.Reply.Tools == nil {
		return nil
	}
	return validateToolNameList(s.Reply.Tools.Allowed)
}

// validatePurposes 校验按用途配置的形状。与工具白名单同理，这里刻意不要求用途
// 「已登记」：用途登记表会随版本与（后续的）插件用途变化，保存时把它钉死会让
// 「先写策略、后上用途」变成不可能；运行期由 resolver 跳过未登记的用途并回退到
// 自身默认值。
func (s Settings) validatePurposes() error {
	if len(s.Purposes) == 0 {
		return nil
	}
	for id, purpose := range s.Purposes {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > MaxPurposeIDLen || !validPurposeID(id) {
			return ErrSettingsInvalid
		}
		if purpose.Tools != nil {
			if err := validateToolNameList(purpose.Tools.Allowed); err != nil {
				return err
			}
		}
		if posture := strings.TrimSpace(purpose.FailurePosture); posture != "" && !ValidFailurePosture(posture) {
			return ErrSettingsInvalid
		}
	}
	return nil
}

func (s Settings) Validate() error {
	if err := s.validateReplyTools(); err != nil {
		return err
	}
	if err := s.validatePurposes(); err != nil {
		return err
	}
	if len(s.Profiles) > MaxProfiles {
		return ErrSettingsInvalid
	}
	seen := make(map[string]bool, len(s.Profiles))
	for _, profile := range s.Profiles {
		// id 是所有引用的键：成本等级绑定与密钥引用都靠它，所以任何时候都必须合法。
		id := strings.TrimSpace(profile.ID)
		if !validProfileID(id) || len(id) > MaxProfileIDLen {
			return ErrSettingsInvalid
		}
		if seen[id] {
			return ErrSettingsInvalid
		}
		seen[id] = true
		if !profile.Enabled {
			// 未启用的配置允许不完整：运营者常常先把自定义提供商存成草稿、
			// 补全后再启用，而草稿不参与运行，不该阻止保存。
			continue
		}
		if err := profile.Validate(); err != nil {
			return ErrSettingsInvalid
		}
	}
	// 同样刻意不拒绝「总开关开启但没有任何可用 profile」：缺供应商的后果由
	// Warnings 提示，运行时会走失败姿态并留下 trace，而不是把人卡在保存上。
	for class, ref := range s.CostClassProfiles {
		if !ValidCostClass(class) {
			return ErrSettingsInvalid
		}
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if !seen[ref] {
			return ErrSettingsInvalid
		}
	}
	for purpose, posture := range s.PurposeFailurePosture {
		if strings.TrimSpace(purpose) == "" || !ValidFailurePosture(posture) {
			return ErrSettingsInvalid
		}
	}
	if s.AutoAction.ConfidenceThreshold < 0 || s.AutoAction.ConfidenceThreshold > 1 {
		return ErrSettingsInvalid
	}
	for _, action := range s.AutoAction.AllowedActions {
		if !ValidAutoAction(action) {
			return ErrSettingsInvalid
		}
	}
	if s.AutoAction.Enabled && len(s.AutoAction.AllowedActions) == 0 {
		return ErrSettingsInvalid
	}
	if err := s.Gates.Validate(); err != nil {
		return err
	}
	return nil
}

func (g GateSettings) Validate() error {
	if g.RateLimitPerMinute < 0 || g.RateLimitPerMinute > MaxRateLimitPerMinute {
		return ErrSettingsInvalid
	}
	if g.ExtensionDailyQuota < 0 || g.ExtensionDailyQuota > MaxExtensionDailyQuota {
		return ErrSettingsInvalid
	}
	if g.UserDailyQuota < 0 || g.UserDailyQuota > MaxUserDailyQuota {
		return ErrSettingsInvalid
	}
	if g.MonthlyBudgetMicros < 0 || g.MonthlyBudgetMicros > MaxMonthlyBudgetMicros {
		return ErrSettingsInvalid
	}
	if g.ToolCallsPerReply != nil && (*g.ToolCallsPerReply < 0 || *g.ToolCallsPerReply > MaxToolCallsPerReply) {
		return ErrSettingsInvalid
	}
	return nil
}

// Profile 按 id 查找。
func (s Settings) Profile(id string) (Profile, bool) {
	id = strings.TrimSpace(id)
	for _, profile := range s.Profiles {
		if strings.TrimSpace(profile.ID) == id {
			return profile, true
		}
	}
	return Profile{}, false
}

// ResolveProfile 选取本次调用使用的 profile。显式 profileRef 优先；否则按成本
// 等级取绑定；两者都不可用时返回 ErrProfileUnavailable，由网关按失败姿态处理。
func (s Settings) ResolveProfile(costClass, profileRef string) (Profile, error) {
	ref := strings.TrimSpace(profileRef)
	if ref == "" {
		ref = strings.TrimSpace(s.CostClassProfiles[strings.TrimSpace(costClass)])
	}
	if ref == "" {
		return Profile{}, ErrProfileUnavailable
	}
	profile, ok := s.Profile(ref)
	if !ok || !profile.Enabled {
		return Profile{}, ErrProfileUnavailable
	}
	return profile, nil
}

// FailurePosture 返回用途的失败姿态，缺省 fail_open。
func (s Settings) FailurePosture(purpose string) string {
	posture := strings.TrimSpace(s.PurposeFailurePosture[strings.TrimSpace(purpose)])
	if ValidFailurePosture(posture) {
		return posture
	}
	return FailurePostureOpen
}

// Warnings 返回非阻断的配置提示，供 Admin 面板显示。它们不阻止保存。
func (s Settings) Warnings() []string {
	out := []string{}
	if !s.Enabled {
		return out
	}
	enabled := 0
	priced := false
	for _, profile := range s.Profiles {
		if !profile.Enabled {
			continue
		}
		enabled++
		if profile.Price.Configured() {
			priced = true
		}
	}
	if enabled == 0 {
		out = append(out, "gateway_enabled_without_enabled_profile:no_call_will_succeed")
	}
	if !priced {
		out = append(out, "no_enabled_profile_has_price:budget_gate_inactive")
	}
	if s.Gates.MonthlyBudgetMicros <= 0 {
		out = append(out, "monthly_budget_is_zero:budget_gate_inactive")
	}
	for _, class := range []string{CostClassEconomy, CostClassStandard, CostClassPremium} {
		if strings.TrimSpace(s.CostClassProfiles[class]) == "" {
			out = append(out, "cost_class_unbound:"+class)
		}
	}
	return out
}
