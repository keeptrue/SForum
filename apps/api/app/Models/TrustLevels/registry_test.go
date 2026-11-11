package trustlevels_test

import (
	"slices"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// TestDefaultEarnableIsEmpty 锁定 D17：默认 earnable 为空集。
//
// `SeedMemberPermissions` 已经给了默认成员全部非管理内容键，所以「TL2 解锁
// topic.create」对默认安装是零行为变化的假功能。默认空集也让 F 组（关闭开关 =
// 本刀之前行为）天然成立。
func TestDefaultEarnableIsEmpty(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	if got := policy.Earnable.All(); len(got) != 0 {
		t.Fatalf("默认 earnable 必须为空集，得到 %v", got)
	}
	for level := trustlevels.LevelNewcomer; level < trustlevels.LevelCount; level++ {
		if got := policy.Earnable.Grants(level); len(got) != 0 {
			t.Fatalf("默认 TL%d 解锁了 %v", level, got)
		}
	}
}

// TestEligiblePermissionsIsLiteralWhitelist 是 P0-1 的回归靶子：
// 白名单必须与**字面**七键集合相等，而不是「用同一个分类谓词自证」。
//
// 事故背景：EligiblePermissions 曾经是负面过滤，而分类谓词的后缀表是 ".override"（点），
// 键名是 user.permission_override（下划线）→ 直接泄漏一个提权键，且旧测试用同一个
// 谓词断言，等于没测。
func TestEligiblePermissionsIsLiteralWhitelist(t *testing.T) {
	want := []string{
		"topic.create",
		"post.create",
		"attachment.upload",
		"topic.edit_own",
		"post.edit_own",
		"topic.delete_own",
		"post.delete_own",
	}
	got := trustlevels.EligiblePermissions()
	if !slices.Equal(got, want) {
		t.Fatalf("白名单 = %v，期望字面集合 %v", got, want)
	}
	// 独立断言（不复用分类谓词）：这些键一个都不能出现在白名单里。
	for _, forbidden := range []string{
		"user.permission_override", "post.edit_any", "topic.edit_any",
		"topic.lock", "topic.pin", "moderation.review", "settings.manage",
	} {
		if slices.Contains(got, forbidden) {
			t.Fatalf("白名单泄漏了 %q", forbidden)
		}
	}
}

// TestEarnableValidateRejectsDangerousKeys 是 D6 + D15 的配置面：
// 任何一条危险键都必须被拒绝，而不是被静默忽略（T16 的坑）。
func TestEarnableValidateRejectsDangerousKeys(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"管理类", "user.ban"},
		{"管理类后缀", "moderation.review"},
		{"父权限键", "settings.manage"},
		{"父权限键（extension）", "extension.manage"},
		{"父权限键（user）", "user.manage"},
		{"目录里不存在", "topic.create.nonexistent"},
		{"空键", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			earnable := trustlevels.Earnable{
				trustlevels.LevelRegular: {testCase.key},
			}
			if err := earnable.Validate(); err == nil {
				t.Fatalf("Validate() 接受了危险键 %q", testCase.key)
			}
		})
	}

	// 反向证明：合法内容键必须通过，否则上面对照组没有意义（T10）。
	valid := trustlevels.Earnable{trustlevels.LevelRegular: {"topic.create", "attachment.upload"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() 拒绝了合法键：%v", err)
	}
}

// TestEarnableValidateCoversWholeCatalog 把 D6/D15/D24 做成遍历**整个权限目录**的
// 结构断言：白名单内的键必须通过，白名单外的一切键（含管理类、父键、edit_any）
// 必须被拒绝。
func TestEarnableValidateCoversWholeCatalog(t *testing.T) {
	checked := 0
	for _, permission := range identity.SeedPermissions {
		earnable := trustlevels.Earnable{trustlevels.LevelRegular: {permission.Key}}
		err := earnable.Validate()
		whitelisted := trustlevels.IsWhitelisted(permission.Key)
		if whitelisted && err != nil {
			t.Fatalf("白名单键 %q 被 Validate 拒绝了：%v", permission.Key, err)
		}
		if !whitelisted && err == nil {
			t.Fatalf("白名单外的键 %q 被 Validate 接受了", permission.Key)
		}
		checked++
	}
	if checked != len(identity.SeedPermissions) || checked == 0 {
		t.Fatalf("遍历了 %d 个权限，目录有 %d 个", checked, len(identity.SeedPermissions))
	}
}

// TestEarnableGrantsCumulative 锁定「高等级包含低等级」的累计语义。
func TestEarnableGrantsCumulative(t *testing.T) {
	earnable := trustlevels.Earnable{
		trustlevels.LevelMember:  {"post.create"},
		trustlevels.LevelRegular: {"topic.create"},
	}
	if got := earnable.Grants(trustlevels.LevelNewcomer); got != nil {
		t.Fatalf("TL0 不应有已挣得，得到 %v", got)
	}
	if got := earnable.Grants(trustlevels.LevelMember); !slices.Equal(got, []string{"post.create"}) {
		t.Fatalf("TL1 = %v", got)
	}
	if got := earnable.Grants(trustlevels.LevelRegular); !slices.Equal(got, []string{"post.create", "topic.create"}) {
		t.Fatalf("TL2 = %v", got)
	}
	if got := earnable.Grants(trustlevels.Level(99)); got != nil {
		t.Fatalf("越界等级解锁了 %v", got)
	}
	if level, ok := earnable.UnlockLevel("topic.create"); !ok || level != trustlevels.LevelRegular {
		t.Fatalf("UnlockLevel = %v,%v", level, ok)
	}
	if !earnable.IsEarnable("post.create") || earnable.IsEarnable("user.ban") {
		t.Fatal("IsEarnable 判定错误")
	}
}

// TestLevelSlugIsNotRoleKey 锁定 D21 的命名：等级标识不能与角色 key 混用。
func TestLevelSlugIsNotRoleKey(t *testing.T) {
	if got := trustlevels.LevelMember.Slug(); got != "member" {
		t.Fatalf("TL1 的 slug 期望 member，得到 %q", got)
	}
	if got := trustlevels.Level(99).Slug(); got != "" {
		t.Fatalf("越界等级 slug 应为空，得到 %q", got)
	}
}
