package trustlevels_test

import (
	"reflect"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 颜色取值域 = 9 个默认 token ∪ 自定义 `#rrggbb`。
//
// 放宽的理由与图标不同：颜色不需要前端静态枚举（它是 CSS 变量/字面量，不是 class 名），
// 运营确实会想要一个品牌色。形状校验（六位 hex）负责挡住任何进不了渲染的值。
//
// 归一化是**解码期**的职责：`DecodeConfig` 先 TrimSpace + ToLower 再校验与落库，
// 这样库里永远是确定的小写形态。谓词 `IsTierColor` 只看归一化之后的输入 ——
// 同一条规则写在两处会漂移，所以这里刻意锁定「谓词不吃大写」这个事实。

func TestIsTierColorAcceptsDefaultTokens(t *testing.T) {
	for _, token := range trustlevels.TierColors() {
		if !trustlevels.IsTierColor(token) {
			t.Fatalf("默认 token %q 被拒绝了", token)
		}
	}
}

func TestIsTierColorAcceptsLowercaseHex(t *testing.T) {
	for _, hex := range []string{"#7c3aed", "#000000", "#ffffff", "#0f766e"} {
		if !trustlevels.IsTierColor(hex) {
			t.Fatalf("合法 hex %q 被拒绝了", hex)
		}
	}
}

func TestIsTierColorRejectsMalformedValues(t *testing.T) {
	rejected := []struct {
		value  string
		reason string
	}{
		{"#fff", "三位短写法必须由前端展开，后端只收六位"},
		{"#gggggg", "非 hex 字符"},
		{"red", "CSS 具名色不在取值域内"},
		{"rgb(0,0,0)", "函数式颜色无法在后台预览里静态枚举"},
		{"#7c3aed00", "八位（带 alpha）不在契约里：档位色不需要透明度"},
		{"chartreuse", "未知 token"},
		{"#7c3aed ", "谓词只吃归一化后的输入（尾随空格由解码期 TrimSpace 处理）"},
		{"", "空串是「按位次自动」的语义，不是一种颜色"},
	}
	for _, testCase := range rejected {
		if trustlevels.IsTierColor(testCase.value) {
			t.Fatalf("%q 被接受了：%s", testCase.value, testCase.reason)
		}
	}
	// 大写 hex 只差大小写：归一化在解码期，不在谓词里（两处都做会漂移）。
	if trustlevels.IsTierColor("#7C3AED") {
		t.Fatal("谓词不该接受未归一化的大写 hex：归一是 DecodeConfig 的职责")
	}
}

// TestTierColorsKeepsDefaultOrder 锁死 9 项与顺序（顺序 = 默认色序）。
//
// 第 n 项就是 rank n 的默认色（TL0 slate / TL1 teal / … / TL8 amber）。
// 少一项会让满配站点的 TL8 回退到 slate 与 TL0 撞色；改顺序等于把每档的默认色都换掉。
func TestTierColorsKeepsDefaultOrder(t *testing.T) {
	want := []string{"slate", "teal", "emerald", "sky", "indigo", "violet", "fuchsia", "rose", "amber"}
	got := trustlevels.TierColors()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("默认色序变了（顺序 = 默认配色语义）:\n got=%v\nwant=%v", got, want)
	}
	if len(want) != 9 {
		t.Fatalf("默认色序应为 9 项，实际 %d", len(want))
	}
}

func TestDefaultTierColorFollowsRank(t *testing.T) {
	want := []string{"slate", "teal", "emerald", "sky", "indigo", "violet", "fuchsia", "rose", "amber"}
	for rank, token := range want {
		if got := trustlevels.DefaultTierColor(trustlevels.Level(rank)); got != token {
			t.Fatalf("rank %d 的默认色 = %q，期望 %q", rank, got, token)
		}
	}
	// 越界 rank 回退首项：契约里 color 是 required，空串在 UI 上等于「没有颜色」。
	if got := trustlevels.DefaultTierColor(trustlevels.Level(99)); got != "slate" {
		t.Fatalf("越界 rank 的默认色 = %q，期望回退 slate", got)
	}
}

// TestDecodeConfigNormalizesCustomColor 是「放宽」的核心承诺：
// 大写与空白在**落库前**归一，运营之后读回来的是同一个值，不会出现两种形态混存。
func TestDecodeConfigNormalizesCustomColor(t *testing.T) {
	cases := map[string]string{
		"#7C3AED":   "#7c3aed",
		"#7c3aed":   "#7c3aed",
		" #7C3AED ": "#7c3aed",
	}
	for input, want := range cases {
		policy, err := trustlevels.DecodeConfig(tiersJSON(coloredTierJSON("member", input)))
		if err != nil {
			t.Fatalf("DecodeConfig(%q): %v", input, err)
		}
		tier, ok := policy.TierAt(trustlevels.LevelMember)
		if !ok || tier.Color != want {
			t.Fatalf("输入 %q 落库为 %q，期望小写归一 %q", input, tier.Color, want)
		}
		if got := policy.ColorOf(trustlevels.LevelMember); got != want {
			t.Fatalf("ColorOf(TL1) = %q，期望 %q", got, want)
		}
	}
}

// TestDecodeConfigRejectsMalformedColor 与上面成对：放宽不等于敞开。
func TestDecodeConfigRejectsMalformedColor(t *testing.T) {
	for _, color := range []string{"#fff", "#gggggg", "red", "rgb(0,0,0)", "#7c3aed00"} {
		_, err := trustlevels.DecodeConfig(tiersJSON(coloredTierJSON("member", color)))
		if err == nil {
			t.Fatalf("非法颜色 %q 被接受了", color)
		}
		details := trustlevels.ConfigErrorDetails(err)
		if len(details) != 1 || details[0].Code != trustlevels.CodeColorNotWhitelisted ||
			details[0].Field != "color" || details[0].TierID != "member" {
			t.Fatalf("颜色 %q 的明细定位不对：%+v", color, details)
		}
		// 码名保持 color_not_whitelisted（前端 i18n 稳定键）；params 回报**原始**输入，便于定位。
		if got, _ := details[0].Params["color"].(string); got != color {
			t.Fatalf("params.color = %+v，期望原样回报 %q", details[0].Params, color)
		}
	}
}
