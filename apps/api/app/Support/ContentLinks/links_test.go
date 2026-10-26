package contentlinks_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
)

func decodeTarget(t *testing.T, href string) string {
	t.Helper()
	prefix := contentlinks.InterstitialPath + "?" + contentlinks.TargetQueryKey + "="
	if !strings.HasPrefix(href, prefix) {
		t.Fatalf("href %q is not an interstitial link", href)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(href, prefix))
	if err != nil {
		t.Fatalf("decode interstitial target: %v", err)
	}
	return string(raw)
}

func TestApplyForcesNewTabAndWrapsOffsiteLinks(t *testing.T) {
	input := `<p>见 <a href="https://example.com/spec?a=1&amp;b=2" rel="ugc">规范</a> 与 <a href="/t/hello">站内帖</a>。</p>`
	got := contentlinks.Apply(input)

	if !strings.Contains(got, `<p>见 <a href="/outbound?`) {
		t.Fatalf("offsite link was not wrapped: %s", got)
	}
	if !strings.Contains(got, ` target="_blank"`) {
		t.Fatalf("expected target=_blank: %s", got)
	}
	// rel 收敛为净化器认可的固定令牌集，且 target 紧邻其前（再净化结果不变）。
	if !strings.Contains(got, `target="_blank" rel="nofollow noreferrer noopener"`) {
		t.Fatalf("expected canonical target/rel: %s", got)
	}
	if !strings.Contains(got, `<a href="/t/hello" target="_blank" rel="nofollow noreferrer noopener">站内帖</a>`) {
		t.Fatalf("internal link should keep href and gain target/rel: %s", got)
	}
	// 实体编码的 href 必须按解码后的目标编码，中转页拿到的是真实地址。
	href := strings.Split(strings.Split(got, `<a href="`)[1], `"`)[0]
	if target := decodeTarget(t, href); target != "https://example.com/spec?a=1&b=2" {
		t.Fatalf("decoded target = %q", target)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	input := `<a href="https://example.com">x</a>`
	once := contentlinks.Apply(input)
	twice := contentlinks.Apply(once)
	if once != twice {
		t.Fatalf("Apply is not idempotent:\n%s\n%s", once, twice)
	}
	if strings.Count(twice, "/outbound?") != 1 {
		t.Fatalf("expected exactly one interstitial prefix: %s", twice)
	}
}

func TestApplyLeavesUnmanagedHrefsAlone(t *testing.T) {
	cases := []string{
		`<a href="#section">锚点</a>`,
		`<a href="mailto:hi@example.com">邮件</a>`,
		`<a href="tel:+8613800000000">电话</a>`,
		`<a name="top">锚</a>`,
		`<a data-sforum-image-viewer="1" href="https://cdn.example.com/a.png"><img src="/a.png"></a>`,
		`<a href="javascript:alert(1)">坏</a>`,
	}
	for _, input := range cases {
		if got := contentlinks.Apply(input); got != input {
			t.Fatalf("Apply(%q) = %q, want unchanged", input, got)
		}
	}
}

func TestApplyHandlesProtocolRelativeAndQuotedAngleBrackets(t *testing.T) {
	protocolRelative := `<a href="//cdn.example.com/a">cdn</a>`
	got := contentlinks.Apply(protocolRelative)
	if strings.Contains(got, contentlinks.InterstitialPath) {
		t.Fatalf("protocol-relative href must not be wrapped: %s", got)
	}
	if !strings.Contains(got, `target="_blank"`) {
		t.Fatalf("protocol-relative href still needs target: %s", got)
	}

	// 属性值里的 '>' 不能提前截断标签扫描。
	tricky := `<a title="a > b" href="https://example.com/x">x</a>`
	got = contentlinks.Apply(tricky)
	if !strings.Contains(got, `title="a > b"`) {
		t.Fatalf("quoted attribute lost: %s", got)
	}
	if !strings.Contains(got, contentlinks.InterstitialPath) {
		t.Fatalf("tricky anchor was not wrapped: %s", got)
	}
}

func TestApplyKeepsNonAnchorMarkupByteStable(t *testing.T) {
	input := `<p>文本</p><abbr title="a&gt;b">x</abbr><input checked="" disabled="" type="checkbox"><img src="/a.png"/>`
	if got := contentlinks.Apply(input); got != input {
		t.Fatalf("non-anchor markup changed:\n%s\n%s", input, got)
	}
}

func TestApplyCanonicalizesRelTokens(t *testing.T) {
	// 非白名单令牌（ugc/external）净化器本来就会丢弃，这里同步收敛，
	// 保证 Apply 的输出是净化固定点（ContentRegistry SSR 校验依赖）。
	input := `<a href="https://example.com" rel="ugc external noopener">x</a>`
	got := contentlinks.Apply(input)
	if !strings.Contains(got, `target="_blank" rel="nofollow noreferrer noopener"`) {
		t.Fatalf("rel merge result = %s", got)
	}
	if strings.Contains(got, "ugc") || strings.Contains(got, "external") {
		t.Fatalf("non-allowlisted rel tokens must be dropped: %s", got)
	}
}

func TestApplyReplacesExistingTarget(t *testing.T) {
	input := `<a href="/t/1" target="_self">x</a>`
	got := contentlinks.Apply(input)
	if strings.Contains(got, "_self") {
		t.Fatalf("stale target survived: %s", got)
	}
	if !strings.Contains(got, `target="_blank"`) {
		t.Fatalf("expected forced target: %s", got)
	}
}

func TestApplyUppercaseTag(t *testing.T) {
	input := `<A HREF="/t/1">x</A>`
	got := contentlinks.Apply(input)
	if !strings.Contains(got, `target="_blank"`) {
		t.Fatalf("uppercase anchor not handled: %s", got)
	}
}

func TestInterstitialHrefAndIsOffsiteHref(t *testing.T) {
	href := contentlinks.InterstitialHref("https://example.com/中文?x=1")
	payload := strings.TrimPrefix(href, contentlinks.InterstitialPath+"?"+contentlinks.TargetQueryKey+"=")
	if strings.ContainsAny(payload, "+/=") {
		t.Fatalf("interstitial payload must be unpadded url-safe base64: %s", href)
	}
	if decodeTarget(t, href) != "https://example.com/中文?x=1" {
		t.Fatalf("round trip failed: %s", href)
	}
	for _, want := range []struct {
		href string
		want bool
	}{
		{"https://example.com", true},
		{"http://example.com/a", true},
		{"/t/1", false},
		{"#a", false},
		{"//cdn.example.com/a", false},
		{"mailto:a@b.c", false},
	} {
		if got := contentlinks.IsOffsiteHref(want.href); got != want.want {
			t.Fatalf("IsOffsiteHref(%q) = %v, want %v", want.href, got, want.want)
		}
	}
}
