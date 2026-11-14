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
	if got := policy.Earnable.All(policy.LastRank()); len(got) != 0 {
		t.Fatalf("默认 earnable 必须为空集，得到 %v", got)
	}
	for rank := trustlevels.LevelNewcomer; rank <= policy.LastRank(); rank++ {
		if got := policy.Earnable.Grants(rank); len(got) != 0 {
			t.Fatalf("默认 TL%d 解锁了 %v", rank, got)
		}
	}
}

// TestEligiblePermissionsIsLiteralWhitelist 是 P0-1 的回归靶子：
// 白名单必须与**字面**七键集合相等，而不是「用同一个分类谓词自证」。
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

// TestEarnableValidateRejectsDangerousKeys 是 D6 + D15 的**新落点**：
//
// M4a 之前「危险键」是在解码期由 `Earnable.Validate` 硬拒的。D24 修订后解码期只判
// 结构（空键），安全边界拆成两处，所以这条测试也拆成两处一起断言——否则「解码期不拒」
// 看上去就像安全边界消失了：
//   - 写期：`ValidateEarnableDeclared` 对任何未声明键（管理类/父键/不存在的键）报
//     `earnable_not_declared`；
//   - 运行期：`EarnableRegistry.FilterGranted` 把非 valid 键全部筛掉（绝不授予）。
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
	}
	registry := trustlevels.DefaultEarnableRegistry()
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// 空键仍然由结构校验拒（它与「谁声明了」无关）。
			earnable := trustlevels.Earnable{trustlevels.Level(2): {testCase.key}}
			if len(trustlevels.ValidateEarnableDeclared([]string{testCase.key}, registry)) == 0 {
				t.Fatalf("写期校验接受了危险键 %q", testCase.key)
			}
			// 运行期也绝不授予。
			if granted := registry.FilterGranted([]string{testCase.key}); len(granted) != 0 {
				t.Fatalf("运行期授予了危险键 %q", testCase.key)
			}
			// 配置本身仍可解码（D24：解码保留，否则插件停用即全站掉默认策略）。
			if err := earnable.Validate(); err != nil {
				t.Fatalf("解码期不该因 %q 拒绝整份配置：%v", testCase.key, err)
			}
		})
	}
	// 结构性判据仍在：空键必须拒。
	if err := (trustlevels.Earnable{trustlevels.Level(2): {""}}).Validate(); err == nil {
		t.Fatal("空键必须由结构校验拒绝")
	}
	// 反向证明：合法内容键必须通过，否则上面对照组没有意义（T10）。
	valid := trustlevels.Earnable{trustlevels.Level(2): {"topic.create", "attachment.upload"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() 拒绝了合法键：%v", err)
	}
	if len(trustlevels.ValidateEarnableDeclared([]string{"topic.create"}, registry)) != 0 {
		t.Fatal("核心白名单键不该被判未声明")
	}
}

func TestEarnableValidateCoversWholeCatalog(t *testing.T) {
	checked := 0
	registry := trustlevels.DefaultEarnableRegistry()
	for _, permission := range identity.SeedPermissions {
		whitelisted := trustlevels.IsWhitelisted(permission.Key)
		// 写期：白名单外的键一个都不能过。
		rejected := len(trustlevels.ValidateEarnableDeclared([]string{permission.Key}, registry)) > 0
		if whitelisted == rejected {
			t.Fatalf("键 %q：白名单=%v 但写期拒绝=%v（两者必须相反）", permission.Key, whitelisted, rejected)
		}
		// 运行期：只有白名单键会被授予。
		granted := len(registry.FilterGranted([]string{permission.Key})) > 0
		if granted != whitelisted {
			t.Fatalf("键 %q：可授予=%v 与白名单=%v 不一致", permission.Key, granted, whitelisted)
		}
		// 解码期：一律可解码（不因未知键整份失败）。
		if err := (trustlevels.Earnable{trustlevels.Level(2): {permission.Key}}).Validate(); err != nil {
			t.Fatalf("键 %q 让整份配置解码失败：%v", permission.Key, err)
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
		trustlevels.Level(1): {"post.create"},
		trustlevels.Level(2): {"topic.create"},
	}
	if got := earnable.Grants(trustlevels.LevelNewcomer); got != nil {
		t.Fatalf("TL0 不应有已挣得，得到 %v", got)
	}
	if got := earnable.Grants(trustlevels.Level(1)); !slices.Equal(got, []string{"post.create"}) {
		t.Fatalf("TL1 = %v", got)
	}
	if got := earnable.Grants(trustlevels.Level(2)); !slices.Equal(got, []string{"post.create", "topic.create"}) {
		t.Fatalf("TL2 = %v", got)
	}
	if got := earnable.Grants(trustlevels.Level(99)); got != nil {
		t.Fatalf("越界等级解锁了 %v", got)
	}
	if level, ok := earnable.UnlockLevel("topic.create"); !ok || level != trustlevels.Level(2) {
		t.Fatalf("UnlockLevel = %v,%v", level, ok)
	}
	if !earnable.IsEarnable("post.create") || earnable.IsEarnable("user.ban") {
		t.Fatal("IsEarnable 判定错误")
	}
}

// TestEarnableAllFollowsLastRank 是「All() 把 TL4 当顶」这个静默坑的回归靶子：
// 上界必须由调用方（policy.LastRank）给出，第 5–8 档配置的键不能从 All() 里消失。
func TestEarnableAllFollowsLastRank(t *testing.T) {
	earnable := trustlevels.Earnable{
		trustlevels.Level(2): {"post.create"},
		trustlevels.Level(6): {"topic.create"},
		trustlevels.Level(8): {"attachment.upload"},
	}
	all := earnable.All(8)
	if !slices.Contains(all, "topic.create") || !slices.Contains(all, "attachment.upload") {
		t.Fatalf("All(8) 丢了高档位配置：%v", all)
	}
	// 旧行为（写死 TL4）的等价断言：只到 4 时高档位的键确实不可见——这正是要修的病，
	// 所以断言两边都必须成立，证明上界来源真的起作用。
	truncated := earnable.All(4)
	if slices.Contains(truncated, "topic.create") || slices.Contains(truncated, "attachment.upload") {
		t.Fatalf("All(4) 不应包含第 5 档以上的键：%v", truncated)
	}
}

// TestBuiltinTierIDsAreNotRoleKeys 锁定 D21 的命名：等级标识不能与角色 key 混用。
//
// 内置档位的 id 与 v1 的 levelSlug 逐字相同，所以存量站点与前端 i18n 都不用改。
func TestBuiltinTierIDsAreNotRoleKeys(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	if got := policy.TierID(trustlevels.LevelMember); got != "member" {
		t.Fatalf("TL1 的身份期望 member，得到 %q", got)
	}
	if got := trustlevels.BuiltinTierID(trustlevels.LevelNewcomer); got != trustlevels.NewcomerID {
		t.Fatalf("TL0 的身份 = %q", got)
	}
	if got := trustlevels.BuiltinTierID(trustlevels.Level(99)); got != "" {
		t.Fatalf("越界档位 id 应为空，得到 %q", got)
	}
	if got := policy.TierID(trustlevels.Level(99)); got != "" {
		t.Fatalf("越界 rank 的 TierID 应为空，得到 %q", got)
	}
	if !trustlevels.IsBuiltinTierID("leader") || trustlevels.IsBuiltinTierID("tier_6") {
		t.Fatal("IsBuiltinTierID 判定错误")
	}
}

// TestNextTierSeqCountsDeletedTierIDs 是「id 永不复用」在物理删除下的靶子：
// 4 档内置 + 删过 tier_7，下一个新档必须是 tier_8，而不是回头重用 tier_6。
func TestNextTierSeqCountsDeletedTierIDs(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` + tierJSON("member") + `,` + tierJSON("resident") + `,` +
			tierJSON("veteran") + `,` + tierJSON("leader") + `],"deletedTierIds":["tier_7"]}`)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := policy.NextTierSeq(); got != 8 {
		t.Fatalf("NextTierSeq = %d，期望 8（墓碑 tier_7 必须计入）", got)
	}
	// 空墓碑时仍是基准 5 + 1。
	if got := trustlevels.DefaultPolicy().NextTierSeq(); got != 6 {
		t.Fatalf("默认策略的 NextTierSeq = %d，期望 6", got)
	}
}

// TestColorOfDefaultsAndOverrides 锁定有效色的解析顺序：TL0 默认、空值跟随位次、
// 显式覆盖优先、越界回退首项。
func TestColorOfDefaultsAndOverrides(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	cases := []struct {
		rank trustlevels.Level
		want string
	}{
		{trustlevels.LevelNewcomer, "slate"},
		{trustlevels.LevelMember, "teal"},
		{2, "emerald"},
		{policy.LastRank(), "indigo"},
		{trustlevels.MaxLevel + 1, "slate"},
	}
	for _, testCase := range cases {
		if got := policy.ColorOf(testCase.rank); got != testCase.want {
			t.Fatalf("ColorOf(TL%d) = %q，期望 %q", testCase.rank, got, testCase.want)
		}
	}

	overridden, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` +
			`{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","color":"rose","requirements":{},"limits":{},"earnable":[]},` +
			tierJSON("resident") + `]}`)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := overridden.ColorOf(trustlevels.LevelMember); got != "rose" {
		t.Fatalf("显式覆盖没生效：ColorOf(TL1) = %q", got)
	}
	// TL2 没配颜色 → 仍按位次取默认色序，而不是继承 TL1 的覆盖。
	if got := overridden.ColorOf(2); got != "emerald" {
		t.Fatalf("ColorOf(TL2) = %q，期望默认色序 emerald", got)
	}

	// 满配 8 档：顶档必须拿到 amber（设计稿的「终点色」），而不是回退成 TL0 的 slate。
	// DefaultTierColor 负责这条对应关系，ColorOf 只是把它按 rank 取出来。
	full, err := trustlevels.DecodeConfig(newTiersJSON(trustlevels.MaxTiers))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := full.LastRank(); got != trustlevels.MaxTiers {
		t.Fatalf("前置条件失败：满配 LastRank = %d", got)
	}
	if got := full.ColorOf(trustlevels.MaxLevel); got != "amber" {
		t.Fatalf("满配顶档 ColorOf(TL%d) = %q，期望 amber", trustlevels.MaxLevel, got)
	}
	if got := trustlevels.DefaultTierColor(trustlevels.MaxLevel); got != "amber" {
		t.Fatalf("DefaultTierColor(TL%d) = %q，期望 amber", trustlevels.MaxLevel, got)
	}
}

// TestDeletedTierIDsDoNotAffectRank 锁定墓碑不参与判定：rank、LastRank、TierID 都不变。
func TestDeletedTierIDsDoNotAffectRank(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` + tierJSON("member") + `,` + tierJSON("resident") + `,` +
			tierJSON("veteran") + `,` + tierJSON("leader") + `],"deletedTierIds":["tier_6","tier_9"]}`)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := policy.LastRank(); got != 4 {
		t.Fatalf("LastRank = %d，期望 4（墓碑不占号）", got)
	}
	if id := policy.TierID(trustlevels.LevelMember); id != "member" {
		t.Fatalf("TierID(TL1) = %q，期望 member", id)
	}
	if _, ok := policy.TierAt(5); ok {
		t.Fatal("TierAt(5) 不该命中：墓碑不是档位")
	}
}
