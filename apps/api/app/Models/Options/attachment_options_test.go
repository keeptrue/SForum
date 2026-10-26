package options

import (
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// TestNormalizeAttachmentMIMETypesRejectsActiveContent 验证主动内容类型被 denylist 硬封禁，
// 防止运营者通过允许列表放开 HTML/SVG/JS 等形成存储型 XSS。
func TestNormalizeAttachmentMIMETypesRejectsActiveContent(t *testing.T) {
	for _, mime := range []string{
		"text/html",
		"text/xml",
		"application/xhtml+xml",
		"application/xml",
		"image/svg+xml",
		"application/javascript",
		"text/javascript",
		"application/ecmascript",
		"text/ecmascript",
		// 大小写不敏感。
		"TEXT/HTML",
		"Image/SVG+XML",
	} {
		if _, ok := normalizeAttachmentMIMETypes(mime); ok {
			t.Fatalf("expected active content MIME %q to be rejected, but it was accepted", mime)
		}
	}
}

// TestNormalizeAttachmentMIMETypesAcceptsSafeAndWildcard 验证安全类型与类型通配符仍被接受。
func TestNormalizeAttachmentMIMETypesAcceptsSafeAndWildcard(t *testing.T) {
	cases := map[string]string{
		"image/jpeg":                 "image/jpeg",
		"image/png,image/webp":       "image/png,image/webp",
		"application/pdf,text/plain": "application/pdf,text/plain",
		"image/*":                    "image/*",
		"image/jpeg, image/png":      "image/jpeg,image/png", // 去空白
	}
	for input, expected := range cases {
		got, ok := normalizeAttachmentMIMETypes(input)
		if !ok {
			t.Fatalf("expected %q to be accepted, but it was rejected", input)
		}
		if got != expected {
			t.Fatalf("normalize %q = %q, want %q", input, got, expected)
		}
	}
}

// TestNormalizeAttachmentMIMETypesRejectsStarStar 全通配 */* 过于宽泛，配置层拒绝。
func TestNormalizeAttachmentMIMETypesRejectsStarStar(t *testing.T) {
	if _, ok := normalizeAttachmentMIMETypes("*/*"); ok {
		t.Fatal("expected */* to be rejected")
	}
	if _, ok := normalizeAttachmentMIMETypes("image/png,*/*"); ok {
		t.Fatal("expected list containing */* to be rejected")
	}
}

// TestNormalizeAttachmentMIMETypesRejectsActiveContentAmongList 验证
// 混合列表中只要含一个主动内容类型即整体拒绝（原子性），避免静默丢弃危险项后放行其余。
func TestNormalizeAttachmentMIMETypesRejectsActiveContentAmongList(t *testing.T) {
	if _, ok := normalizeAttachmentMIMETypes("image/png,text/html"); ok {
		t.Fatal("expected mixed list containing text/html to be rejected entirely")
	}
}

// TestIsAttachmentActiveContentType 验证 content 响应层判定函数语义一致。
func TestIsAttachmentActiveContentType(t *testing.T) {
	for _, mime := range []string{"text/html", "image/svg+xml", "application/javascript"} {
		if !IsAttachmentActiveContentType(mime) {
			t.Fatalf("expected %q to be active content", mime)
		}
	}
	if IsAttachmentActiveContentType("image/png") {
		t.Fatal("expected image/png to NOT be active content")
	}
	if !IsAttachmentActiveContentType("  TEXT/HTML  ") {
		t.Fatal("expected whitespace/case-insensitive text/html to be active content")
	}
}

// TestAttachmentExtensionReferenceLimitOption 覆盖新选项的默认值、边界与回落：
// 它决定单个扩展最多能认领多少张存活附件（公开图标等）。
func TestAttachmentExtensionReferenceLimitOption(t *testing.T) {
	defaults := map[string]string{}
	mergeAttachmentDefaults(defaults)
	if got := defaults[NameAttachmentExtensionReferenceLimit]; got != "500" {
		t.Fatalf("default = %q", got)
	}

	for _, test := range []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "1", want: "1", ok: true},
		{input: "500", want: "500", ok: true},
		{input: " 42 ", want: "42", ok: true},
		{input: "10000", want: "10000", ok: true},
		{input: "0", ok: false},
		{input: "10001", ok: false},
		{input: "-1", ok: false},
		{input: "abc", ok: false},
		{input: "", ok: false},
	} {
		got, ok := normalizeOptionValue(NameAttachmentExtensionReferenceLimit, test.input)
		if ok != test.ok {
			t.Fatalf("normalize %q ok = %t, want %t", test.input, ok, test.ok)
		}
		if ok && got != test.want {
			t.Fatalf("normalize %q = %q, want %q", test.input, got, test.want)
		}
	}

	// 非法值在写入路径上回落默认值，而不是让整次附件设置保存失败。
	values := map[string]string{NameAttachmentExtensionReferenceLimit: "not-a-number"}
	coerceAttachmentOptions(values, defaults)
	if got := values[NameAttachmentExtensionReferenceLimit]; got != "500" {
		t.Fatalf("coerced = %q", got)
	}
	if !isValidAttachmentOptions(values) {
		t.Fatal("coerced attachment options must validate")
	}
}

// TestAttachmentExtensionReferenceLimitOptionIsRegistered 确认新选项真的进了
// 选项目录（否则运营无法通过 web-options 调整，也无法被 InternalValues 读出）。
func TestAttachmentExtensionReferenceLimitOptionIsRegistered(t *testing.T) {
	definition, ok := optionDefinitionFor(NameAttachmentExtensionReferenceLimit)
	if !ok {
		t.Fatal("option is not registered in the catalog")
	}
	if definition.managePermission != identity.PermissionAttachmentSettings {
		t.Fatalf("manage permission = %q", definition.managePermission)
	}
	names := attachmentOptionNames()
	for _, name := range names {
		if name == NameAttachmentExtensionReferenceLimit {
			return
		}
	}
	t.Fatalf("option missing from attachment option names: %v", names)
}
