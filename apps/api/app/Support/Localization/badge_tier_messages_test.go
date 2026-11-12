package localization

import "testing"

// 徽章等级表（202611120001）引入的五个错误码的显式双语文案覆盖。
//
// 为什么不只依赖 locales_test.go 的两条自动发现测试：
//   - TestAPIErrorCodesHaveLocalizedMessages 走 Message()，目标语言缺键时会回退到
//     zh-CN，所以「只加了中文」的半成品照样绿；
//   - TestAPIErrorCodesAreLocalizedInEveryLocale 直接查目录，能抓到半成品，但它只
//     报告「发现到的码」。这条测试把「本次新增的五个码」写成显式清单，让删除或改名
//     在测试里立刻可见——自动发现只能证明「没漏」，证明不了「还在」。
func TestBadgeTierErrorCodesAreLocalized(t *testing.T) {
	codes := []string{
		"forum.badge_tier_not_found",
		"forum.badge_tier_key_taken",
		"forum.badge_tier_invalid",
		"forum.badge_tier_in_use",
		"forum.badge_grant_batch_invalid",
	}
	locales := []string{"zh-CN", "en-US"}
	for _, code := range codes {
		for _, locale := range locales {
			catalog, ok := messages[locale]
			if !ok {
				t.Fatalf("locale %s has no catalog", locale)
			}
			message, present := catalog[code]
			if !present {
				t.Fatalf("%s: missing message in %s", code, locale)
			}
			if message == "" || message == code {
				t.Fatalf("%s: message in %s is empty or falls back to the key (%q)", code, locale, message)
			}
		}
		// 中英两份文案必须不同：相同通常意味着复制粘贴后忘了翻译。
		if messages["zh-CN"][code] == messages["en-US"][code] {
			t.Fatalf("%s: zh-CN and en-US messages are identical (%q)", code, messages["zh-CN"][code])
		}
	}
}
