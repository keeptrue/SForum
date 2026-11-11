package options

import (
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// NameTrustLevels 是信任等级阈值策略的 option 键。
//
// 用**单个 JSON 值**而不是四档 × 七指标 + 限额的扁平键（D19）：扁平键要过 options
// 的 5 道注册，漏第 5 道会静默丢弃；单键只有一处解码，严格性由
// trustlevels.DecodeConfig 保证（未知字段拒绝、schema 版本、缺档拒绝）。
// 先例是 seo.footer_links。
const NameTrustLevels = "trust.levels"

// trustLevelOptionDefault 是推荐默认值（四档阈值 + 分档限额 + 空 earnable）。
//
// 空 earnable 是刻意的（D17）：默认成员角色已含全部非管理内容键，默认解锁现有键
// 是零行为变化的假功能。运营在后台勾选后才真正生效。
func trustLevelOptionDefault() string {
	encoded, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		// 内置默认值编码失败属于编程错误：宁可空串（解码时回落到同一份 DefaultPolicy），
		// 也不要在启动期 panic。
		return ""
	}
	return encoded
}

// normalizeTrustLevelsOption 归一化 trust.levels：严格解码后重新编码成规范形状。
//
// 解码失败返回 false，上游保留旧值——与其它 option 的「非法值不生效」语义一致。
// 注意这里**不**做部分修正：坏配置必须整体拒绝，否则运营会看到「保存成功但某档没生效」。
//
// 这是 v1→v2 的**唯一**升级入口（D-N13）：存量值是 v1，运营保存一次就落成 v2。
// 读路径（WebOption / trustlevels.DecodeConfig）绝不回写库值——否则「谁先读谁决定
// 数据形状」，灰度与回滚都无法预测。将来要加别的写路径，必须复用这个归一化函数，
// 不要各自写一份 EncodeConfig。
func normalizeTrustLevelsOption(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return trustLevelOptionDefault(), true
	}
	policy, err := trustlevels.DecodeConfig(trimmed)
	if err != nil {
		return "", false
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		return "", false
	}
	return encoded, true
}

// TrustManagePermission 返回后台写阈值所需的权限键。
//
// 只用于目录声明；实际裁决仍然发生在领域层（Actor.Can）。
func TrustManagePermission() string { return identity.PermissionTrustManage }
