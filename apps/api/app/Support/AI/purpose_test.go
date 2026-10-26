package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func TestPurposeRegistryRejectsDuplicateAndInvalidDeclarations(t *testing.T) {
	valid := ai.PurposeDeclaration{
		ID: "core.probe", Label: ai.LocalizedPurposeLabel{Default: "探针"},
		CostClass: ai.CostClassEconomy, FailurePosture: ai.FailurePostureOpen,
		BlastRadius: ai.PurposesBlastRadiusSingle,
	}
	if _, err := ai.NewPurposeRegistry(valid, valid); !errors.Is(err, ai.ErrPurposeInvalid) {
		t.Fatalf("duplicate purpose must be refused: %v", err)
	}

	cases := map[string]ai.PurposeDeclaration{
		"missing id":          {Label: valid.Label, CostClass: valid.CostClass, FailurePosture: valid.FailurePosture, BlastRadius: valid.BlastRadius},
		"uppercase id":        {ID: "Core.Probe", Label: valid.Label, CostClass: valid.CostClass, FailurePosture: valid.FailurePosture, BlastRadius: valid.BlastRadius},
		"missing label":       {ID: "core.probe", CostClass: valid.CostClass, FailurePosture: valid.FailurePosture, BlastRadius: valid.BlastRadius},
		"unknown cost class":  {ID: "core.probe", Label: valid.Label, CostClass: "free", FailurePosture: valid.FailurePosture, BlastRadius: valid.BlastRadius},
		"unknown posture":     {ID: "core.probe", Label: valid.Label, CostClass: valid.CostClass, FailurePosture: "maybe", BlastRadius: valid.BlastRadius},
		"unknown radius":      {ID: "core.probe", Label: valid.Label, CostClass: valid.CostClass, FailurePosture: valid.FailurePosture, BlastRadius: "galaxy"},
		"invalid tool name":   {ID: "core.probe", Label: valid.Label, CostClass: valid.CostClass, FailurePosture: valid.FailurePosture, BlastRadius: valid.BlastRadius, ToolAllowlist: []string{"Forum Search"}},
		"duplicate tool name": {ID: "core.probe", Label: valid.Label, CostClass: valid.CostClass, FailurePosture: valid.FailurePosture, BlastRadius: valid.BlastRadius, ToolAllowlist: []string{"forum-search", "forum-search"}},
	}
	for name, declaration := range cases {
		if err := ai.ValidatePurposeDeclaration(declaration); err == nil {
			t.Fatalf("%s: declaration must be refused", name)
		}
	}
}

func TestBuiltinPurposesDeclareTheReplyUseCase(t *testing.T) {
	registry := ai.DefaultPurposeRegistry()
	declaration, ok := registry.Lookup(ai.CorePurposeForumReply)
	if !ok {
		t.Fatal("forum.reply must be declared")
	}
	if declaration.CostClass != ai.CostClassEconomy {
		t.Fatalf("reply cost class = %q, want economy", declaration.CostClass)
	}
	if declaration.PromptVersion != ai.CoreReplyPromptVersion {
		t.Fatalf("prompt version = %q", declaration.PromptVersion)
	}
	if len(declaration.ToolAllowlist) != len(ai.CoreReplyToolAllowlist) {
		t.Fatalf("declared tool allowlist = %+v", declaration.ToolAllowlist)
	}
	for index, name := range ai.CoreReplyToolAllowlist {
		if declaration.ToolAllowlist[index] != name {
			t.Fatalf("declared tool allowlist drifted: %+v", declaration.ToolAllowlist)
		}
	}
	if declaration.Label.Default == "" || declaration.Description.EnUS == "" {
		t.Fatalf("purpose labels must be human readable: %+v", declaration)
	}
	if _, ok := registry.Lookup("moderation.review"); ok {
		t.Fatal("the registry must not silently absorb undeclared purposes")
	}
}

// B3：两条派生用途的声明逐格对照任务书那张表。
//
// 它同时是「能过 PurposeRegistry.Register 校验」的证据：DefaultPurposeRegistry
// 在构造时逐条调用 Register，任何一条不合法都会让 MustBuiltinPurposeRegistry
// panic——而 prodUpdate 的调用点遍布装配路径，那会是启动失败。这里按字段断言，
// 是因为「注册成功」只证明声明合法，不证明声明**正确**。
func TestBuiltinPurposesDeclareDerivedContentPurposes(t *testing.T) {
	registry := ai.DefaultPurposeRegistry()

	cases := []struct {
		id            string
		label         string
		labelEn       string
		tools         []string
		promptVersion string
	}{
		{
			id:            "forum.digest",
			label:         "话题蒸馏",
			labelEn:       "Topic digest",
			tools:         []string{"forum-topic-read", "forum-search"},
			promptVersion: "forum-digest@1",
		},
		{
			id:            "forum.tour",
			label:         "长楼导览",
			labelEn:       "Long-thread tour",
			tools:         []string{"forum-topic-read", "forum-topic-list"},
			promptVersion: "forum-tour@1",
		},
	}
	for _, want := range cases {
		declaration, ok := registry.Lookup(want.id)
		if !ok {
			t.Fatalf("%s must be declared (B3 needs a second outlet for compute)", want.id)
		}
		// 成本等级：两条都是 economy（任务书那张表）。
		if declaration.CostClass != ai.CostClassEconomy {
			t.Fatalf("%s cost class = %q, want economy", want.id, declaration.CostClass)
		}
		// 失败姿态：与 forum.reply 相同，不发明新的降级语义（易错点 3）。
		reply, ok := registry.Lookup(ai.CorePurposeForumReply)
		if !ok {
			t.Fatal("forum.reply must be declared")
		}
		if declaration.FailurePosture != reply.FailurePosture {
			t.Fatalf("%s failure posture = %q, want the same as forum.reply (%q)",
				want.id, declaration.FailurePosture, reply.FailurePosture)
		}
		// 工具白名单：逐项相同，且顺序一致（顺序会进请求，稳定顺序让 diff 可读）。
		if len(declaration.ToolAllowlist) != len(want.tools) {
			t.Fatalf("%s tools = %+v, want %+v", want.id, declaration.ToolAllowlist, want.tools)
		}
		for index, name := range want.tools {
			if declaration.ToolAllowlist[index] != name {
				t.Fatalf("%s tools = %+v, want %+v", want.id, declaration.ToolAllowlist, want.tools)
			}
			// 白名单里的每个名字都必须是 Host 工具登记表里真实存在的工具：
			// 声明一个不存在的工具，模型会把它当成可用能力反复尝试。
			if !ai.ValidToolName(name) {
				t.Fatalf("%s declares an invalid tool name %q", want.id, name)
			}
		}
		if declaration.ResourceType != "topic" {
			t.Fatalf("%s resource type = %q, want topic", want.id, declaration.ResourceType)
		}
		if declaration.PromptVersion != want.promptVersion {
			t.Fatalf("%s prompt version = %q, want %q", want.id, declaration.PromptVersion, want.promptVersion)
		}
		if declaration.BlastRadius != ai.PurposesBlastRadiusSingle {
			t.Fatalf("%s blast radius = %q, want single", want.id, declaration.BlastRadius)
		}
		// 双语标签都必须非空：Register 的校验要求 Default 非空，EnUS 缺失会让
		// 英文控制台显示中文（回退），运营者看不出是两个不同用途之外的别的信号。
		if declaration.Label.Default != want.label || declaration.Label.EnUS != want.labelEn {
			t.Fatalf("%s label = %+v", want.id, declaration.Label)
		}
		if declaration.Description.Default == "" || declaration.Description.EnUS == "" {
			t.Fatalf("%s description must be bilingual: %+v", want.id, declaration.Description)
		}
	}

	// 两条派生用途的 id 是常量导出的，且与登记表里的键一致。
	if ai.CorePurposeForumDigest != "forum.digest" || ai.CorePurposeForumTour != "forum.tour" {
		t.Fatalf("purpose constants drifted: %q %q", ai.CorePurposeForumDigest, ai.CorePurposeForumTour)
	}
	// 提示词版本常量与声明一致：分成两处写死就是漂移的开始。
	if ai.CoreDigestPromptVersion != "forum-digest@1" || ai.CoreTourPromptVersion != "forum-tour@1" {
		t.Fatalf("prompt version constants drifted: %q %q", ai.CoreDigestPromptVersion, ai.CoreTourPromptVersion)
	}

	// 枚举顺序稳定（按 id 排序），控制台清单不会每次刷新换序。
	purposes := registry.Purposes()
	for index := 1; index < len(purposes); index++ {
		if purposes[index-1].ID >= purposes[index].ID {
			t.Fatalf("purposes must be sorted by id: %q then %q", purposes[index-1].ID, purposes[index].ID)
		}
	}
}

func TestPurposeResolverAppliesSiteOverridesOverDeclarations(t *testing.T) {
	registry, err := ai.NewPurposeRegistry(ai.PurposeDeclaration{
		ID: "core.probe", Label: ai.LocalizedPurposeLabel{Default: "探针"},
		CostClass: ai.CostClassStandard, FailurePosture: ai.FailurePostureOpen,
		BlastRadius: ai.PurposesBlastRadiusSingle, ToolAllowlist: []string{"forum-search"},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := ai.RecommendedSettings().Normalized()

	resolver := ai.NewPurposeResolver(registry, func() ai.Settings { return settings })
	resolution, err := resolver.Resolve("core.probe")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.ToolsOverridden || resolution.FailurePostureOverridden {
		t.Fatalf("unconfigured resolution = %#v", resolution)
	}
	if len(resolution.ToolsAllowed) != 1 || resolution.ToolsAllowed[0] != "forum-search" {
		t.Fatalf("declared tools = %+v", resolution.ToolsAllowed)
	}
	if resolution.FailurePosture != ai.FailurePostureOpen {
		t.Fatalf("declared posture = %q", resolution.FailurePosture)
	}

	settings.Purposes = map[string]ai.PurposeSettings{
		"core.probe": {
			Tools:          &ai.PurposeToolSettings{Allowed: []string{"vendor-plugin-tool"}},
			FailurePosture: ai.FailurePostureClosed,
		},
	}
	resolution, err = resolver.Resolve("core.probe")
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.ToolsOverridden || !resolution.FailurePostureOverridden {
		t.Fatalf("configured resolution = %#v", resolution)
	}
	if len(resolution.ToolsAllowed) != 1 || resolution.ToolsAllowed[0] != "vendor-plugin-tool" {
		t.Fatalf("overridden tools = %+v", resolution.ToolsAllowed)
	}
	if resolution.FailurePosture != ai.FailurePostureClosed {
		t.Fatalf("overridden posture = %q", resolution.FailurePosture)
	}

	// 显式空列表是有效配置：该用途不允许调用任何工具。
	settings.Purposes["core.probe"] = ai.PurposeSettings{Tools: &ai.PurposeToolSettings{Allowed: []string{}}}
	resolution, err = resolver.Resolve("core.probe")
	if err != nil || !resolution.ToolsOverridden || len(resolution.ToolsAllowed) != 0 {
		t.Fatalf("empty override = %#v err=%v", resolution, err)
	}

	if _, err := resolver.Resolve("core.unknown"); !errors.Is(err, ai.ErrPurposeUnknown) {
		t.Fatalf("undeclared purpose must be refused: %v", err)
	}
}

func TestPurposeResolverWithoutRegistryRefusesEveryPurpose(t *testing.T) {
	resolver := ai.NewPurposeResolver(nil, nil)
	if _, err := resolver.Resolve(ai.CorePurposeForumReply); !errors.Is(err, ai.ErrPurposeUnknown) {
		t.Fatalf("nil registry must refuse: %v", err)
	}
}

// 站点配置里的旧版 reply.tools 会被迁移进按用途配置：升级前的运营者意图不变，
// 而新配置只有一条路径。
func TestSettingsMigrateLegacyReplyToolsIntoPurposes(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.Reply.Tools = &ai.ReplyToolSettings{Allowed: []string{"forum-search", "vendor-plugin-tool"}}
	normalized := settings.Normalized()

	configured, ok := normalized.Purposes[ai.CorePurposeForumReply]
	if !ok || configured.Tools == nil {
		t.Fatalf("legacy reply.tools was not migrated: %#v", normalized.Purposes)
	}
	if len(configured.Tools.Allowed) != 2 || configured.Tools.Allowed[1] != "vendor-plugin-tool" {
		t.Fatalf("migrated allowlist = %+v", configured.Tools.Allowed)
	}

	// 已经用新路径配置过的站点不会被旧字段覆盖回去。
	settings.Purposes = map[string]ai.PurposeSettings{
		ai.CorePurposeForumReply: {Tools: &ai.PurposeToolSettings{Allowed: []string{"time-now"}}},
	}
	normalized = settings.Normalized()
	if got := normalized.Purposes[ai.CorePurposeForumReply].Tools.Allowed; len(got) != 1 || got[0] != "time-now" {
		t.Fatalf("purposes must win over the legacy field: %+v", got)
	}

	// 什么都不覆盖的条目会被丢弃，不留一个假的「已自定义」。
	settings.Reply.Tools = nil
	settings.Purposes = map[string]ai.PurposeSettings{ai.CorePurposeForumReply: {}}
	if normalized = settings.Normalized(); normalized.Purposes != nil {
		t.Fatalf("empty purpose overrides must be dropped: %#v", normalized.Purposes)
	}
}

func TestSettingsValidateRejectsMalformedPurposeOverrides(t *testing.T) {
	base := ai.RecommendedSettings().Normalized()

	badID := base
	badID.Purposes = map[string]ai.PurposeSettings{"Forum Reply": {Tools: &ai.PurposeToolSettings{Allowed: []string{"forum-search"}}}}
	if err := badID.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
		t.Fatalf("malformed purpose id must be refused: %v", err)
	}

	badTools := base
	badTools.Purposes = map[string]ai.PurposeSettings{
		ai.CorePurposeForumReply: {Tools: &ai.PurposeToolSettings{Allowed: []string{"Forum Search"}}},
	}
	if err := badTools.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
		t.Fatalf("malformed tool name must be refused: %v", err)
	}

	duplicateTools := base
	duplicateTools.Purposes = map[string]ai.PurposeSettings{
		ai.CorePurposeForumReply: {Tools: &ai.PurposeToolSettings{Allowed: []string{"time-now", "time-now"}}},
	}
	if err := duplicateTools.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
		t.Fatalf("duplicate tool name must be refused: %v", err)
	}

	badPosture := base
	badPosture.Purposes = map[string]ai.PurposeSettings{ai.CorePurposeForumReply: {FailurePosture: "explode"}}
	if err := badPosture.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
		t.Fatalf("malformed posture must be refused: %v", err)
	}

	// 未登记的用途是合法配置：用途登记表会随版本与插件变化，保存时把它钉死会让
	// 「先写策略、后上用途」变成不可能。
	unregistered := base
	unregistered.Purposes = map[string]ai.PurposeSettings{
		"vendor.summarize": {Tools: &ai.PurposeToolSettings{Allowed: []string{"forum-search"}}},
	}
	if err := unregistered.Validate(); err != nil {
		t.Fatalf("unregistered purpose overrides must be accepted: %v", err)
	}
}

// 未声明的用途必须被网关拒绝，且不产生任何出站调用。
func TestGatewayRejectsUndeclaredPurposeWithoutTouchingProvider(t *testing.T) {
	settings := enabledSettings()
	gateway, _, traces, invoker, _ := newGateway(t, settings)
	request := validRequest()
	request.Purpose = "vendor.custom-purpose"

	_, err := gateway.Execute(context.Background(), request)
	var unavailable *ai.UnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, ai.ErrPurposeUnknown) {
		t.Fatalf("err = %v", err)
	}
	if unavailable.Reason != ai.GateReasonPurposeUnknown {
		t.Fatalf("reason = %q", unavailable.Reason)
	}
	if invoker.calls != 0 {
		t.Fatal("an undeclared purpose must not reach the provider")
	}
	if len(traces.records) != 1 || traces.records[0].Status != ai.ExecutionStatusDenied ||
		traces.records[0].GateReason != ai.GateReasonPurposeUnknown {
		t.Fatalf("traces = %+v", traces.records)
	}
	if !strings.Contains(traces.records[0].ErrorSummary, "vendor.custom-purpose") {
		t.Fatalf("trace must name the refused purpose: %q", traces.records[0].ErrorSummary)
	}
}

// 站点总开关关闭时，「为什么没有出站」的答案必须仍然只有一个：gateway_disabled。
func TestGatewayDisabledWinsOverPurposeValidation(t *testing.T) {
	settings := ai.RecommendedSettings()
	gateway, _, _, invoker, _ := newGateway(t, settings)
	request := validRequest()
	request.Purpose = "vendor.custom-purpose"

	_, err := gateway.Execute(context.Background(), request)
	var unavailable *ai.UnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, ai.ErrGatewayDisabled) {
		t.Fatalf("err = %v", err)
	}
	if invoker.calls != 0 {
		t.Fatal("disabled gateway must not call the provider")
	}
}
