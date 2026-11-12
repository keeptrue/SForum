package badgescontroller

import (
	"errors"
	"testing"

	"github.com/gofiber/fiber/v3"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// TestMapBadgeErrorProjectsFactDenial 断言事实上报拒绝的传输投影。
//
// 这条存在的理由就是 §13#19：新错误码如果没有 case，会落到 `mapBadgeError` 的
// default → 500，而「插件参数写错」被报成「宿主坏了」是最误导人的一种失败。
// 同时它也证明 `ErrBadgeFactWriteDenied` 这个 sentinel **有真实生产者**
// （`badges.NewBadgeFactWriteDenied`），不是一个永远为假的映射分支。
func TestMapBadgeErrorProjectsFactDenial(t *testing.T) {
	err := badges.NewBadgeFactWriteDenied("fact_key_not_declared")
	if !errors.Is(err, badges.ErrBadgeFactWriteDenied) {
		t.Fatal("拒绝错误必须能 errors.Is 到 sentinel（HTTP 映射的依据）")
	}
	if reason := badges.BadgeFactWriteDenialReason(err); reason != "fact_key_not_declared" {
		t.Fatalf("reason 投影 = %q", reason)
	}

	mapped := mapBadgeError(err)
	var fiberErr *fiber.Error
	if !errors.As(mapped, &fiberErr) {
		t.Fatalf("必须映射成 fiber.Error，得到 %T", mapped)
	}
	if fiberErr.Code != fiber.StatusForbidden {
		t.Fatalf("status = %d，want 403", fiberErr.Code)
	}
	if fiberErr.Message != badges.CodeBadgeFactWriteDenied {
		t.Fatalf("code = %q，want %q", fiberErr.Message, badges.CodeBadgeFactWriteDenied)
	}
	if badges.CodeBadgeFactWriteDenied != "forum.badge_fact_write_denied" {
		t.Fatalf("顶层码字面量被改动：%q", badges.CodeBadgeFactWriteDenied)
	}

	// 未知错误按存储失败兜底，且仍然取得到顶层码（不会退化成 500 的裸错误）。
	unknown := errors.New("something else")
	if reason := badges.BadgeFactWriteDenialReason(unknown); reason != "fact_write_failed" {
		t.Fatalf("未知错误 reason = %q，want fact_write_failed", reason)
	}
}
