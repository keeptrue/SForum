package notificationjobs

import (
	"encoding/json"
	"strings"
	"testing"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
)

func TestRenderDeliveryUsesStoredPasswordResetContent(t *testing.T) {
	data, _ := json.Marshal(map[string]string{"subject": "Reset", "textBody": "secret link"})
	request := renderDelivery(notifications.MailDelivery{ID: 1, Recipient: "u@example.com", TemplateKey: "identity.password_reset", TemplateData: data})
	if request.Subject != "Reset" || request.TextBody != "secret link" {
		t.Fatalf("unexpected request: %#v", request)
	}
}
func TestRenderDeliveryCreatesLocalizedForumMail(t *testing.T) {
	request := renderDelivery(notifications.MailDelivery{ID: 2, Recipient: "u@example.com", TemplateKey: "forum.mention", TemplateData: json.RawMessage(`{"topicId":42}`)})
	if !strings.Contains(request.Subject, "提及") || !strings.Contains(request.TextBody, "提到了") || !strings.Contains(request.HTMLBody, "邮件由你的通知偏好") {
		t.Fatalf("unexpected request: %#v", request)
	}
}

func TestRenderDeliveryCreatesPendingModerationMailWithReviewTarget(t *testing.T) {
	request := renderDelivery(notifications.MailDelivery{ID: 9, Recipient: "reviewer@example.com", TemplateKey: "forum.moderation_pending", TemplateData: json.RawMessage(`{"locale":"zh-CN","recipientName":"审核员","siteName":"SForum","siteUrl":"https://forum.test/","reviewPath":"/moderation?source=pre_publish&reviewType=topic&reviewId=42"}`)})
	wantURL := "https://forum.test/moderation?source=pre_publish&amp;reviewType=topic&amp;reviewId=42"
	if !strings.Contains(request.Subject, "有新内容等待审核") || !strings.Contains(request.TextBody, "打开审核队列: https://forum.test/moderation?source=pre_publish&reviewType=topic&reviewId=42") || !strings.Contains(request.HTMLBody, wantURL) {
		t.Fatalf("unexpected pending moderation mail: %#v", request)
	}
}

func TestRenderDeliveryRendersEnglishPasswordResetTemplate(t *testing.T) {
	request := renderDelivery(notifications.MailDelivery{ID: 3, Recipient: "u@example.com", TemplateKey: "identity.password_reset", TemplateData: json.RawMessage(`{"locale":"en-US","username":"<Alex>","resetUrl":"https://forum.test/reset?token=secret","siteName":"Forum"}`)})
	if !strings.Contains(request.Subject, "Reset your Forum password") || strings.Contains(request.TextBody, "重置") || !strings.Contains(request.HTMLBody, "&lt;Alex&gt;") || !strings.Contains(request.HTMLBody, "Reset password") {
		t.Fatalf("unexpected localized reset template: %#v", request)
	}
}

func TestRenderDeliveryRendersEmailVerificationTemplate(t *testing.T) {
	request := renderDelivery(notifications.MailDelivery{ID: 8, Recipient: "u@example.com", TemplateKey: "identity.email_verification", TemplateData: json.RawMessage(`{"locale":"en-US","username":"Alex","verifyUrl":"https://forum.test/api/v1/auth/email-verification/confirm?token=opaque","siteName":"Forum"}`)})
	if request.Subject != "Verify your Forum email" || !strings.Contains(request.TextBody, "Verify email: https://forum.test") || !strings.Contains(request.HTMLBody, "Verify email") {
		t.Fatalf("unexpected email verification mail: %#v", request)
	}
}

func TestRenderDeliveryRendersWelcomeTemplate(t *testing.T) {
	request := renderDelivery(notifications.MailDelivery{ID: 4, Recipient: "u@example.com", TemplateKey: "identity.welcome", TemplateData: json.RawMessage(`{"locale":"zh-CN","username":"林墨","siteName":"SForum","siteUrl":"https://forum.test"}`)})
	if !strings.Contains(request.Subject, "欢迎来到 SForum") || !strings.Contains(request.HTMLBody, "开始逛逛") || !strings.Contains(request.TextBody, "补充头像") {
		t.Fatalf("unexpected welcome template: %#v", request)
	}
}

func TestRenderDeliveryUsesQueuedBrandLogoAndAppearance(t *testing.T) {
	request := renderDelivery(notifications.MailDelivery{ID: 5, Recipient: "u@example.com", TemplateKey: "identity.password_reset", TemplateData: json.RawMessage(`{
"locale":"zh-CN","username":"蓝海","resetUrl":"https://forum.test/reset","siteName":"蓝色论坛",
"brandLogoUrl":"https://forum.test/brand/logo.png","brandAccent":"#2563eb","brandAccentSoft":"#eff6ff","brandAccentSoftBorder":"#b9d1ff"}`)})
	if !strings.Contains(request.HTMLBody, `src="https://forum.test/brand/logo.png"`) || !strings.Contains(request.HTMLBody, `background:#2563eb`) || strings.Contains(request.HTMLBody, `>S</span>`) {
		t.Fatalf("expected queued logo and blue theme, got %#v", request)
	}
}

func TestRenderDeliveryUsesSiteMarkWhenNoImageIsConfigured(t *testing.T) {
	request := renderDelivery(notifications.MailDelivery{ID: 6, Recipient: "u@example.com", TemplateKey: "identity.welcome", TemplateData: json.RawMessage(`{
"locale":"zh-CN","username":"用户","siteName":"大佬论坛","siteUrl":"https://forum.test","brandMark":"大","brandAccent":"#7c3aed"}`)})
	if !strings.Contains(request.HTMLBody, `>大</span>`) || strings.Contains(request.HTMLBody, `>S</span>`) || !strings.Contains(request.HTMLBody, `background:#7c3aed`) {
		t.Fatalf("expected site mark and violet theme, got %#v", request)
	}
}
