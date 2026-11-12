package trustlevels

import (
	"github.com/keeptrue/sforum/apps/api/app/Support/Palette"
)

// 本文件是档位颜色的**薄包装**：白名单与取值规则已上提到
// app/Support/Palette（徽章等级表共用同一份色板），这里只保留同名入口，
// 让 TrustLevels 内部与既有调用点（payload.go / controller.go / config.go）
// 零改动。
//
// 为什么不直接让调用点 import Palette：这些函数吃的是 `Level`，而 Palette 只认
// int（它不该知道信任等级的 rank 语义）。转换放在领域包内，依赖方向保持单向。
//
// 自定义色受格式约束（6 位 hex、小写），不敞开接受任意字符串；归一化是**解码期**
// 的职责（DecodeConfig 先 TrimSpace + ToLower 再校验），谓词只吃归一化后的输入。

// TierColors 返回颜色推荐序列（副本）。调用方不得原地修改。
func TierColors() []string {
	return palette.Tokens()
}

// IsTierColor 报告颜色值是否合法：9 个默认 token 之一，或小写 `#rrggbb`。
func IsTierColor(name string) bool {
	return palette.IsTierColor(name)
}

// DefaultTierColor 返回该 rank 的默认颜色；超出默认色序时返回白名单首项。
func DefaultTierColor(rank Level) string {
	return palette.DefaultForRank(int(rank))
}
