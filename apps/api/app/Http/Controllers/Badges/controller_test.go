package badgescontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

type fakeGate struct {
	enabled bool
	err     error
}

func (g fakeGate) BadgesFeatureEnabled(context.Context) (bool, error) { return g.enabled, g.err }

// TestMapBadgeErrorKeepsTwoForbiddenPathsApart 是本刀最容易混的一处：
// 产品开关关闭与权限不足都是 403，但语义完全不同——前者是运营决定，
// 后者是这个管理员没被授权。混用会让审查无法区分，也会让 ops 的
// 「丁打写端点必须 403 且 reason 不是 badges_disabled」断言失效。
func TestMapBadgeErrorKeepsTwoForbiddenPathsApart(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"feature disabled", badges.ErrBadgesDisabled, fiber.StatusForbidden, badges.CodeBadgesDisabled},
		{"not found", badges.ErrBadgeNotFound, fiber.StatusNotFound, badges.CodeBadgeNotFound},
		{"slug taken", badges.ErrBadgeSlugTaken, fiber.StatusConflict, badges.CodeBadgeSlugTaken},
		{"already granted", badges.ErrBadgeAlreadyGranted, fiber.StatusConflict, badges.CodeBadgeAlreadyGranted},
		// 「手动却带规则」是状态冲突，不是规则本身不合法：两码必须能分开。
		// 顺序敏感——这条 case 同时钉住「mode 冲突不会被 rule_invalid 抢走」。
		{"rule mode conflict", badges.ErrBadgeRuleModeConflict, fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleModeConflict},
		{"rule invalid", badges.ErrBadgeRuleInvalid, fiber.StatusUnprocessableEntity, badges.CodeBadgeRuleInvalid},
		{"manual only", badges.ErrBadgeManualOnly, fiber.StatusUnprocessableEntity, badges.CodeBadgeManualOnly},
	}
	for _, tc := range cases {
		mapped := mapBadgeError(tc.err)
		fiberErr, ok := mapped.(*fiber.Error)
		if !ok {
			t.Fatalf("%s: expected *fiber.Error, got %T", tc.name, mapped)
		}
		if fiberErr.Code != tc.status {
			t.Fatalf("%s: status = %d, want %d", tc.name, fiberErr.Code, tc.status)
		}
		if fiberErr.Message != tc.code {
			t.Fatalf("%s: code = %q, want %q", tc.name, fiberErr.Message, tc.code)
		}
	}
	if got := mapBadgeError(nil); got != nil {
		t.Fatalf("nil error must map to nil, got %v", got)
	}
	// 未识别错误必须原样透传（交给上层 500），不能被吞成 4xx。
	sentinel := errors.New("boom")
	if got := mapBadgeError(sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("unknown error must pass through, got %v", got)
	}
}

// TestMapBadgeErrorPutsModeConflictBeforeRuleInvalid 钉住 switch 的顺序。
//
// 单错误表测不出这条：每个夹具只包一个 sentinel。真正的风险是将来有人把两条链一起包
// （`errors.Is` 对多 %w 都成立），那时**先命中的 case 决定错误码**——顺序错了，
// 状态冲突就会被报成「请检查指标与阈值的组合」，运维会去改一个没问题的指标。
func TestMapBadgeErrorPutsModeConflictBeforeRuleInvalid(t *testing.T) {
	both := fmt.Errorf("%w: %w", badges.ErrBadgeRuleInvalid, badges.ErrBadgeRuleModeConflict)
	mapped, ok := mapBadgeError(both).(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", mapped)
	}
	if mapped.Message != badges.CodeBadgeRuleModeConflict {
		t.Fatalf("code = %q, want %q (mode conflict must win over rule invalid)",
			mapped.Message, badges.CodeBadgeRuleModeConflict)
	}
}

// TestRequireFeatureReturnsDisabledCode 断言开关关闭时抛的是 badges_disabled，
// 且与权限拒绝的 code 不同。
func TestRequireFeatureReturnsDisabledCode(t *testing.T) {
	controller := &Controller{gate: fakeGate{enabled: false}}
	err := controller.requireFeature(context.Background())
	fiberErr, ok := err.(*fiber.Error)
	if !ok || fiberErr.Code != fiber.StatusForbidden || fiberErr.Message != badges.CodeBadgesDisabled {
		t.Fatalf("disabled gate = %v, want 403 %s", err, badges.CodeBadgesDisabled)
	}
	if fiberErr.Message == "auth.required" {
		t.Fatal("feature-disabled 403 must not be confused with permission 403")
	}

	enabled := &Controller{gate: fakeGate{enabled: true}}
	if err := enabled.requireFeature(context.Background()); err != nil {
		t.Fatalf("enabled gate returned %v", err)
	}
	// 未装配 gate 时默认放行（本地/测试装配的默认形状）。
	if err := (&Controller{}).requireFeature(context.Background()); err != nil {
		t.Fatalf("nil gate must not block: %v", err)
	}
}

// TestProfileBadgeItemOmitsAbsentFields 固定资料页扁平契约：
// 未获得的自动徽章给 current+threshold、不给 grantedAt；已获得只给 grantedAt；
// 手动徽章两者都不给。
func TestProfileBadgeItemOmitsAbsentFields(t *testing.T) {
	grantedAt := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	threshold := int64(7)
	metric := badges.MetricDaysVisited

	// 未获得的自动徽章。
	auto := toProfileBadgeItem(badges.UserBadge{
		Badge: badges.PublicBadge{
			Slug: "regular-visitor", Name: "常客", Tier: badges.TierBronze,
			Icon: "i-lucide-calendar", Position: 0, GrantMode: badges.GrantAutomatic,
			Metric: &metric, Threshold: &threshold,
		},
		Progress: badges.BadgeProgress{Current: 0, Threshold: 7},
	})
	if auto.Earned || auto.GrantedAt != nil {
		t.Fatalf("unearned item must not carry grantedAt: %+v", auto)
	}
	if auto.Current == nil || *auto.Current != 0 {
		t.Fatalf("current=0 must still be emitted (pointer, not omitempty): %+v", auto.Current)
	}
	if auto.Threshold == nil || *auto.Threshold != 7 {
		t.Fatalf("threshold missing: %+v", auto.Threshold)
	}

	// 已获得：不给进度条数据。
	earned := toProfileBadgeItem(badges.UserBadge{
		Badge: badges.PublicBadge{
			Slug: "first-topic", Name: "初次发帖", Tier: badges.TierBronze,
			Icon: "i-lucide-sprout", GrantMode: badges.GrantAutomatic,
		},
		Earned:    true,
		GrantedAt: &grantedAt,
		Progress:  badges.BadgeProgress{Current: 1, Threshold: 1},
	})
	if !earned.Earned || earned.GrantedAt == nil {
		t.Fatalf("earned item must carry grantedAt: %+v", earned)
	}
	if earned.Current != nil || earned.Threshold != nil {
		t.Fatalf("earned item must not carry progress fields: %+v", earned)
	}

	// 手动徽章未获得：没有阈值，两个字段都不给。
	manual := toProfileBadgeItem(badges.UserBadge{
		Badge: badges.PublicBadge{Slug: "quality-answer", GrantMode: badges.GrantManual},
	})
	if manual.Current != nil || manual.Threshold != nil {
		t.Fatalf("manual unearned badge must not carry progress: %+v", manual)
	}
}

// TestCacheHeadersSeparatePublicAndAdmin 断言公开端点可共享缓存、后台端点禁止缓存。
func TestCacheHeadersSeparatePublicAndAdmin(t *testing.T) {
	app := fiber.New()
	app.Get("/public", func(c fiber.Ctx) error {
		setPublicCacheable(c)
		return c.SendStatus(fiber.StatusOK)
	})
	app.Get("/admin", func(c fiber.Ctx) error {
		setAdminNoStore(c)
		return c.SendStatus(fiber.StatusOK)
	})

	publicResp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/public", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := publicResp.Header.Get(fiber.HeaderCacheControl); got != "public, max-age=30" {
		t.Fatalf("public cache-control = %q", got)
	}

	adminResp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/admin", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := adminResp.Header.Get(fiber.HeaderCacheControl); got != "private, no-store" {
		t.Fatalf("admin cache-control = %q", got)
	}
}

// TestMapBadgeErrorRuleCodes 是 M1.4-08 的映射层断言：五个规则错误码必须都映射成
// **422**，不能落到 default（那是 500）。
//
// 为什么不造 HTTP 装配：这五个码是服务层语义，HTTP 只做映射；给 admin 写路径造一套
// 新装配测的是「装配对不对」而不是「码对不对」，长期维护成本还高。服务层断言
// （`TestRuleErrorCodesAtServiceLayer`）+ 这条映射断言 = 完整链路。
func TestMapBadgeErrorRuleCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		// 必须包真正的 *ruleengine.Error：`CodeOf` 走 errors.As，拼字符串是取不到码的。
		{"too_complex", fmt.Errorf("%w: %w", badges.ErrBadgeRuleInvalid, &ruleengine.Error{Code: ruleengine.CodeTooDeep, Path: "root", Detail: "depth"}), badges.CodeBadgeRuleTooComplex},
		{"unknown_condition", fmt.Errorf("%w: %w", badges.ErrBadgeRuleInvalid, &ruleengine.Error{Code: ruleengine.CodeUnknownCondition, Path: "root.children[0]", Detail: "unknown"}), badges.CodeBadgeRuleUnknownCond},
		{"cycle", fmt.Errorf("%w: a -> b -> a", badges.ErrBadgeRuleCycle), badges.CodeBadgeRuleCycle},
		{"reference_invalid", fmt.Errorf("%w: category gone", badges.ErrBadgeRuleReferenceInvalid), badges.CodeBadgeRuleReferenceInvalid},
		{"backfill_unavailable", badges.ErrBadgeBackfillUnavailable, badges.CodeBadgeBackfillUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := mapBadgeError(tc.err)
			var fiberErr *fiber.Error
			if !errors.As(mapped, &fiberErr) {
				t.Fatalf("必须映射成 fiber.Error，得到 %T", mapped)
			}
			if fiberErr.Code != fiber.StatusUnprocessableEntity {
				t.Fatalf("%s 必须映射成 422，得到 %d", tc.name, fiberErr.Code)
			}
			if fiberErr.Message != tc.code {
				t.Fatalf("%s 错误码 = %q，want %q", tc.name, fiberErr.Message, tc.code)
			}
		})
	}
}

// TestRecomputeResponseCarriesAllCounters 是 M1.4-21 的映射层回归。
//
// 事故形态：handler 里内联的 map 字面量只写了 granted / refreshed，`queued` 被丢掉，
// 而 OpenAPI 的 `BadgeRecomputeResult.required` 要求三个键。契约与实现不一致，
// 但功能路径（重算确实跑了）完全正常——只有返回结构不对。
//
// 断言三件事：三个键都在、值映射正确、**没有多余键**（多键同样是契约漂移）。
func TestRecomputeResponseCarriesAllCounters(t *testing.T) {
	raw, err := json.Marshal(toRecomputeResponse(badges.RecomputeResult{Granted: 1, Refreshed: 2, Queued: 3}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]int64
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]int64{"granted": 1, "refreshed": 2, "queued": 3}
	if len(got) != len(want) {
		t.Fatalf("键集合 = %v，want %v（多键或少键都是契约漂移）", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %d，want %d（完整响应 %v）", key, got[key], value, got)
		}
	}
}
