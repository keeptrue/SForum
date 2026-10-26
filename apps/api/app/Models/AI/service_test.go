package ai_test

import (
	"context"
	"errors"
	"testing"
	"time"

	modelsai "github.com/keeptrue/sforum/apps/api/app/Models/AI"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

type stubSettings struct {
	stored    supportai.Settings
	saves     int
	resets    int
	lastActor int64
}

func (s *stubSettings) GetSettings(context.Context) (supportai.Settings, error) { return s.stored, nil }
func (s *stubSettings) SaveSettings(_ context.Context, settings supportai.Settings, actorUserID int64) (supportai.Settings, error) {
	s.saves++
	s.lastActor = actorUserID
	s.stored = settings
	return settings, nil
}
func (s *stubSettings) ResetSettings(_ context.Context, settings supportai.Settings, actorUserID int64) (supportai.Settings, error) {
	s.resets++
	s.stored = settings
	return settings, nil
}

func managerActor() identity.Actor {
	return identity.Actor{
		ID:          42,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAIManage: true},
	}
}

func memberActor() identity.Actor {
	return identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
}

func TestServiceRequiresManagePermission(t *testing.T) {
	store := &stubSettings{stored: supportai.RecommendedSettings()}
	service := modelsai.NewService(modelsai.Config{Settings: store})
	ctx := context.Background()

	if _, err := service.GetSettings(ctx, memberActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("get settings: %v", err)
	}
	if _, err := service.UpdateSettings(ctx, memberActor(), supportai.RecommendedSettings()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("update settings: %v", err)
	}
	if _, err := service.ResetSettings(ctx, memberActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("reset settings: %v", err)
	}
	if _, err := service.Usage(ctx, memberActor(), time.Now()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("usage: %v", err)
	}
	if _, err := service.Executions(ctx, memberActor(), 10); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("executions: %v", err)
	}
	if store.saves != 0 || store.resets != 0 {
		t.Fatal("denied callers must not reach the store")
	}
}

func TestServicePersistsValidatedSettingsWithTheActor(t *testing.T) {
	store := &stubSettings{stored: supportai.RecommendedSettings()}
	service := modelsai.NewService(modelsai.Config{Settings: store})
	settings := supportai.RecommendedSettings()
	settings.Enabled = true
	settings.Profiles[0].Enabled = true

	saved, err := service.UpdateSettings(context.Background(), managerActor(), settings)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if !saved.Enabled || store.lastActor != 42 {
		t.Fatalf("saved = %+v actor = %d", saved, store.lastActor)
	}
}

func TestServiceRejectsInvalidSettingsBeforeWriting(t *testing.T) {
	store := &stubSettings{stored: supportai.RecommendedSettings()}
	service := modelsai.NewService(modelsai.Config{Settings: store})
	settings := supportai.RecommendedSettings()
	settings.Enabled = true
	// 越界值才是真正非法的：天花板不可调，必须拒绝。
	settings.Gates.ExtensionDailyQuota = supportai.MaxExtensionDailyQuota + 1

	if _, err := service.UpdateSettings(context.Background(), managerActor(), settings); !errors.Is(err, supportai.ErrSettingsInvalid) {
		t.Fatalf("err = %v", err)
	}
	if store.saves != 0 {
		t.Fatal("invalid settings must not be persisted")
	}
}

func TestServiceResetRestoresRecommendedDefaults(t *testing.T) {
	enabled := supportai.RecommendedSettings()
	enabled.Enabled = true
	enabled.Profiles[0].Enabled = true
	store := &stubSettings{stored: enabled}
	service := modelsai.NewService(modelsai.Config{Settings: store})

	reset, err := service.ResetSettings(context.Background(), managerActor())
	if err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if reset.Enabled {
		t.Fatal("reset must return the disabled gateway default")
	}
	if reset.Gates.ExtensionDailyQuota != supportai.RecommendedSettings().Gates.ExtensionDailyQuota {
		t.Fatalf("quota not restored: %+v", reset.Gates)
	}
	if store.resets != 1 {
		t.Fatalf("resets = %d", store.resets)
	}
}

// 运营者填过的供应商是资产：一键重置承诺的是「恢复推荐策略」，不是「清空配置」。
func TestServiceResetKeepsProviderProfilesAndCostBindings(t *testing.T) {
	current := supportai.RecommendedSettings()
	current.Enabled = true
	current.Gates.UserDailyQuota = 999
	// 模拟运营者改过的自定义供应商：重置后必须原样保留，否则密钥引用会指向空配置
	// 而 Secret Store 里的密钥仍在，界面就会显示「未配置」。
	current.Profiles = []supportai.Profile{
		{
			ID:        "mine",
			Label:     "自建网关",
			Protocol:  supportai.ProtocolOpenAIChat,
			BaseURL:   "https://llm.example.test/v1",
			APIKeyRef: supportai.SecretReferencePrefix + "core/ai.mine.api_key",
			Model:     "local-model",
			CostClass: supportai.CostClassStandard,
			Enabled:   true,
		},
	}
	current.CostClassProfiles = map[string]string{
		supportai.CostClassEconomy:  "mine",
		supportai.CostClassStandard: "mine",
		supportai.CostClassPremium:  "mine",
	}
	store := &stubSettings{stored: current}
	service := modelsai.NewService(modelsai.Config{Settings: store})

	reset, err := service.ResetSettings(context.Background(), managerActor())
	if err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if len(reset.Profiles) != 1 || reset.Profiles[0].ID != "mine" {
		t.Fatalf("profiles were dropped by reset: %+v", reset.Profiles)
	}
	if reset.Profiles[0].APIKeyRef != current.Profiles[0].APIKeyRef {
		t.Fatalf("credential reference changed: %q", reset.Profiles[0].APIKeyRef)
	}
	if reset.CostClassProfiles[supportai.CostClassPremium] != "mine" {
		t.Fatalf("cost bindings were dropped: %+v", reset.CostClassProfiles)
	}
	// 策略字段仍然回到推荐默认：这是重置真正该做的事。
	if reset.Gates.UserDailyQuota != supportai.RecommendedSettings().Gates.UserDailyQuota {
		t.Fatalf("policy field not restored: %d", reset.Gates.UserDailyQuota)
	}
}

// 没有任何已存供应商时（首次接入），重置应给出完整的推荐默认，而不是空面板。
func TestServiceResetUsesRecommendedProfilesWhenNoneStored(t *testing.T) {
	store := &stubSettings{stored: supportai.Settings{}}
	service := modelsai.NewService(modelsai.Config{Settings: store})

	reset, err := service.ResetSettings(context.Background(), managerActor())
	if err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if len(reset.Profiles) != len(supportai.BuiltinProfiles()) {
		t.Fatalf("expected builtin profiles, got %d", len(reset.Profiles))
	}
}

func TestServiceWorksWithoutOptionalStores(t *testing.T) {
	service := modelsai.NewService(modelsai.Config{})
	ctx := context.Background()
	settings, err := service.GetSettings(ctx, managerActor())
	if err != nil || settings.Enabled {
		t.Fatalf("settings = %+v err = %v", settings, err)
	}
	usage, err := service.Usage(ctx, managerActor(), time.Now())
	if err != nil || usage.Site.DayCalls != 0 {
		t.Fatalf("usage = %+v err = %v", usage, err)
	}
	items, err := service.Executions(ctx, managerActor(), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("executions = %+v err = %v", items, err)
	}
}

// 回复用途参数的来源语义：未配置时报告「没有覆盖」，让生成器回退到用途声明；
// 配置了（哪怕为空）就以站点配置为准。这个区分是插件工具暴露策略的全部依据。
func TestServiceReplyPurposeParametersReportOverrideState(t *testing.T) {
	store := &stubSettings{stored: supportai.RecommendedSettings().Normalized()}
	service := modelsai.NewService(modelsai.Config{Settings: store})
	ctx := context.Background()

	parameters, err := service.ReplyPurposeParameters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if parameters.ToolsOverride || len(parameters.ToolsAllowed) != len(supportai.CoreReplyToolAllowlist) {
		t.Fatalf("unconfigured parameters = %#v", parameters)
	}
	if parameters.CostClass != supportai.CostClassEconomy || parameters.PromptVersion != supportai.CoreReplyPromptVersion {
		t.Fatalf("declared parameters = %#v", parameters)
	}

	settings := supportai.RecommendedSettings().Normalized()
	settings.Purposes = map[string]supportai.PurposeSettings{
		supportai.CorePurposeForumReply: {Tools: &supportai.PurposeToolSettings{
			Allowed: []string{"forum-search", "vendor-plugin-tool"},
		}},
	}
	store.stored = settings
	parameters, err = service.ReplyPurposeParameters(ctx)
	if err != nil || !parameters.ToolsOverride || len(parameters.ToolsAllowed) != 2 || parameters.ToolsAllowed[1] != "vendor-plugin-tool" {
		t.Fatalf("configured parameters = %#v err=%v", parameters, err)
	}

	settings.Purposes = map[string]supportai.PurposeSettings{
		supportai.CorePurposeForumReply: {Tools: &supportai.PurposeToolSettings{Allowed: []string{}}},
	}
	store.stored = settings
	parameters, err = service.ReplyPurposeParameters(ctx)
	if err != nil || !parameters.ToolsOverride || len(parameters.ToolsAllowed) != 0 {
		t.Fatalf("explicitly empty allowlist = %#v err=%v", parameters, err)
	}
}

// 回复的输出预算来自该用途成本等级绑定 profile 的 Defaults.MaxTokens：推理类模型
// 会先把预算花在思考上，写死的小值会让它永远写不出正文。
func TestServiceReplyPurposeParametersCarryProfileOutputBudget(t *testing.T) {
	settings := supportai.RecommendedSettings().Normalized()
	for index := range settings.Profiles {
		if settings.Profiles[index].ID == "deepseek" {
			settings.Profiles[index].Enabled = true
			settings.Profiles[index].Defaults.MaxTokens = 4_096
		}
	}
	store := &stubSettings{stored: settings}
	service := modelsai.NewService(modelsai.Config{Settings: store})

	parameters, err := service.ReplyPurposeParameters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if parameters.MaxTokens != 4_096 {
		t.Fatalf("max tokens = %d, want the bound profile's value", parameters.MaxTokens)
	}
}

// 解析不出可用 profile 时返回 0：由生成器回落到内置下限，而不是让预算变成 0
// （那会让请求在契约校验就被拒，把一个配置问题伪装成请求格式问题）。
func TestServiceReplyPurposeParametersReportNoBudgetWithoutProfile(t *testing.T) {
	settings := supportai.RecommendedSettings().Normalized()
	// 推荐默认里所有 profile 都是关闭的，因此经济档解析不出可用 profile。
	store := &stubSettings{stored: settings}
	service := modelsai.NewService(modelsai.Config{Settings: store})

	parameters, err := service.ReplyPurposeParameters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if parameters.MaxTokens != 0 {
		t.Fatalf("max tokens = %d, want 0 when no profile resolves", parameters.MaxTokens)
	}
}

// 控制台按用途枚举 AI 行为：站内已声明的用途与当前生效参数必须能一次拿到，
// 而不是让运营者从代码或特例字段里推断。
func TestServicePurposeDetailsCarryDeclaredAndEffectiveParameters(t *testing.T) {
	settings := supportai.RecommendedSettings().Normalized()
	settings.Purposes = map[string]supportai.PurposeSettings{
		supportai.CorePurposeForumReply: {
			Tools:          &supportai.PurposeToolSettings{Allowed: []string{"forum-search"}},
			FailurePosture: supportai.FailurePostureClosed,
		},
	}
	service := modelsai.NewService(modelsai.Config{Settings: &stubSettings{stored: settings}})

	details, err := service.PurposeDetails(context.Background(), managerActor())
	if err != nil {
		t.Fatal(err)
	}
	// 登记表现在含 forum.reply 与 admin.connectivity_test 两条 Core 用途；
	// 本测试关心的是「声明 + 生效覆盖」能否同时被枚举，所以按 id 取出
	// forum.reply 而不是假设目录只有一条。
	var replyDetail *supportai.ValidatedPurpose
	for i := range details {
		if details[i].Declaration.ID == supportai.CorePurposeForumReply {
			replyDetail = &details[i]
			break
		}
	}
	if replyDetail == nil {
		t.Fatalf("purpose catalog missing forum.reply: %+v", details)
	}
	if !replyDetail.ToolsOverridden || len(replyDetail.ToolsAllowed) != 1 || replyDetail.ToolsAllowed[0] != "forum-search" {
		t.Fatalf("effective tools = %+v", replyDetail)
	}
	if len(replyDetail.Declaration.ToolAllowlist) != len(supportai.CoreReplyToolAllowlist) {
		t.Fatalf("declared tools must stay visible next to the override: %+v", replyDetail.Declaration.ToolAllowlist)
	}
	if !replyDetail.FailurePostureOverridden || replyDetail.FailurePosture != supportai.FailurePostureClosed {
		t.Fatalf("effective posture = %+v", replyDetail)
	}

	if _, err := service.PurposeDetails(context.Background(), memberActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("purpose catalog requires ai.manage: %v", err)
	}
}

type stubToolCatalog struct {
	tools []supportai.ToolAvailability
}

func (s stubToolCatalog) Available() []supportai.ToolAvailability { return s.tools }

// 工具目录只对 ai.manage 可见；未注入目录时返回空清单而不是报错——控制台会
// 退化成手动输入工具名，那是一个更笨但可用的界面。
func TestAvailableToolsRequiresManagePermissionAndUsesCatalog(t *testing.T) {
	catalog := stubToolCatalog{tools: []supportai.ToolAvailability{{
		Name: "web-search", Description: "检索站外互联网。",
		Origin: supportai.ToolOrigin{Kind: "plugin", ExtensionID: "sforum.web-search", Version: "1.0.0"},
	}}}
	service := modelsai.NewService(modelsai.Config{}).WithToolCatalog(catalog)

	if _, err := service.AvailableTools(identity.Actor{ID: 1, Status: identity.UserStatusActive}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("non-manager must be denied, got %v", err)
	}
	tools, err := service.AvailableTools(managerActor())
	if err != nil || len(tools) != 1 || tools[0].Origin.ExtensionID != "sforum.web-search" {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}

	empty := modelsai.NewService(modelsai.Config{})
	if got, err := empty.AvailableTools(managerActor()); err != nil || got != nil {
		t.Fatalf("missing catalog must report no tools, got %#v err=%v", got, err)
	}
}
