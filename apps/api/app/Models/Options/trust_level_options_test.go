package options

import (
	"testing"

	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// TestNormalizeOptionValueCoversTrustLevels 是 D-8 的回归靶子。
//
// 事故背景：`NameTrustLevels` 被塞进了社区策略的 case 组，而
// `normalizeCommunityPolicyOption` 没有它的分支 → 落到 default → 返回 ("", false)
// → `options.Update` 把**合法 JSON** 判成非法 → 静默 422。推荐默认值也存不进去，
// 因为失败发生在解码之后、写库之前，与内容无关。
//
// 三处既有测试都碰不到这里：TrustLevels 包直接调 Encode/Decode、Options 包没有
// 走 normalizeOptionValue 的用例、控制器用的是注入的 fake writer。
func TestNormalizeOptionValueCoversTrustLevels(t *testing.T) {
	valid, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	normalized, ok := normalizeOptionValue(NameTrustLevels, valid)
	if !ok {
		t.Fatal("合法 trust.levels JSON 被判非法（NameTrustLevels 没有归一化分支）")
	}
	if normalized == "" {
		t.Fatal("归一化结果为空")
	}
	// 归一化必须是幂等的：读出来再写回去要稳定。
	again, ok := normalizeOptionValue(NameTrustLevels, normalized)
	if !ok || again != normalized {
		t.Fatalf("归一化不幂等：%q → %q", normalized, again)
	}
	// 推荐默认值本身必须能存进去（D-8 的原始症状）。
	if _, ok := normalizeOptionValue(NameTrustLevels, trustLevelOptionDefault()); !ok {
		t.Fatal("推荐默认值无法通过归一化")
	}
	// 非法值仍然必须被拒。
	if _, ok := normalizeOptionValue(NameTrustLevels, `{"schemaVersion":9}`); ok {
		t.Fatal("非法配置被接受了")
	}
}

// TestEveryOptionDefinitionHasNormalizer 是全 catalog 的完整性断言。
//
// 「注册了但没有归一化分支」的洞只有遍历才能全覆盖：optionDefinitions 有它、
// 但 normalizeOptionValue 的 switch 漏一条，症状就是**合法值被静默判非法**。
func TestEveryOptionDefinitionHasNormalizer(t *testing.T) {
	// 用与生产相同的构造路径拿推荐默认值（normalizedDefaults 合并了各域 defaults）。
	defaults := normalizedDefaults(Defaults{})
	checked, skipped := 0, 0
	for _, definition := range optionDefinitions {
		value, exists := defaults[definition.name]
		if !exists {
			// 少数键的默认值来自部署配置（Defaults 结构体字段），不是内置常量；
			// 这类键无法在这里构造合法输入，计入 skipped 而不是失败。
			skipped++
			continue
		}
		if _, ok := normalizeOptionValue(definition.name, value); !ok {
			t.Fatalf("option %q 的推荐默认值无法通过 normalizeOptionValue：没有归一化分支", definition.name)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("没有遍历到任何 option 定义")
	}
	t.Logf("normalizeOptionValue 覆盖 %d 个 option 定义（%d 个默认值来自部署配置，跳过）", checked, skipped)
}
