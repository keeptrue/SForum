package identity_test

import (
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// defaultInstallWhitelistKeys 是 earnable 白名单七个键的**字面**副本（J-14）。
//
// 刻意不遍历 trustlevels.EarnableWhitelist()：那是拿实现断言实现——白名单被污染
// （例如混进 user.ban）时，遍历版本会跟着一起变绿，而这份字面清单会红。
var defaultInstallWhitelistKeys = []string{
	"topic.create",
	"post.create",
	"attachment.upload",
	"topic.edit_own",
	"post.edit_own",
	"topic.delete_own",
	"post.delete_own",
}

// TestGrantSourceIsStaticForDefaultInstallWhitelistKeys 锁定默认安装下的来源解释：
// 七个白名单键对默认 member 用户是 **static**，不是 earned。
//
// 为什么这条重要：默认 earnable 是空集（D17），而 SeedMemberPermissions 已含这七键，
// 所以「TL 解锁能力」在默认安装下是零行为变化。来源若被解释成 earned，前端会告诉
// 运营「这个键是等级挣来的」——而实际上关掉信任等级它也照样生效。
func TestGrantSourceIsStaticForDefaultInstallWhitelistKeys(t *testing.T) {
	policy := trustlevels.DefaultPolicy()

	// 前提：默认策略的 earnable 是空集。前提不成立时下面的 static 断言会失去意义
	// （可能是被别的来源先命中），所以先把它钉住。
	for level := trustlevels.LevelNewcomer; level < trustlevels.LevelCount; level++ {
		if grants := policy.Earnable.Grants(level); len(grants) != 0 {
			t.Fatalf("默认策略在 TL%d 已有已挣得键 %v：默认安装前提不成立", level, grants)
		}
	}

	// 默认安装的 member 用户：静态授予来自 SeedMemberPermissions，等级已解析，
	// 没有任何 earned 独占键（earnable 为空 → 装载期合并结果为空）。
	permissions := make(map[string]bool, len(identity.SeedMemberPermissions))
	for _, key := range identity.SeedMemberPermissions {
		permissions[key] = true
	}
	actor := identity.Actor{
		ID:                 7,
		Status:             identity.UserStatusActive,
		Permissions:        permissions,
		TrustLevel:         int(trustlevels.LevelMember),
		TrustLevelResolved: true,
	}
	if len(actor.Earned) != 0 {
		t.Fatalf("默认安装的 actor 不该带 earned：%v", actor.Earned)
	}

	// 七键逐个断言字面来源。先断言 Can 为真：否则「来源是 none」和「来源写错」
	// 会混成同一个失败，读不出是哪种坏。
	for _, key := range defaultInstallWhitelistKeys {
		if !actor.Can(key) {
			t.Fatalf("默认安装的 member 用户应当能 %q（SeedMemberPermissions 含它）", key)
		}
		if origin := actor.GrantSource(key); origin != identity.PermissionOriginStatic {
			t.Fatalf("默认安装下 %q 的来源 = %q，期望 static", key, origin)
		}
	}

	// 反向对照：白名单外、默认未授予的键来源是 none，证明上面不是「一律 static」。
	if origin := actor.GrantSource("user.ban"); origin != identity.PermissionOriginNone {
		t.Fatalf("默认安装下未授予的 user.ban 来源 = %q，期望 none", origin)
	}
}
