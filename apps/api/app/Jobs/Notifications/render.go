package notificationjobs

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

func renderDelivery(delivery notifications.MailDelivery) extensionsruntime.MailProviderRequest {
	data := map[string]string{}
	_ = json.Unmarshal(delivery.TemplateData, &data)
	subject, text, htmlBody := renderMailTemplate(delivery.TemplateKey, data)
	return extensionsruntime.MailProviderRequest{
		DeliveryID: deliveryID(delivery.ID), CorrelationID: delivery.CorrelationID, To: []string{delivery.Recipient},
		Subject: subject, TextBody: text, HTMLBody: htmlBody,
	}
}

func deliveryID(id int64) string { return fmt.Sprint(id) }

func renderMailTemplate(key string, data map[string]string) (subject, text, htmlBody string) {
	// Existing admin and historical deliveries retain their stored copy exactly.
	if subject = strings.TrimSpace(data["subject"]); subject != "" {
		return subject, data["textBody"], data["htmlBody"]
	}
	locale := data["locale"]
	english := strings.EqualFold(locale, "en-US") || strings.EqualFold(locale, "en")
	siteName := fallback(data["siteName"], "SForum")
	brand := mailBrandFromData(data, siteName)
	name := fallback(data["username"], fallback(data["recipientName"], "there"))

	var eyebrow, title, intro, action, actionURL, note, detailTitle, detail string
	var list []string
	switch key {
	case "identity.email_verification":
		actionURL = data["verifyUrl"]
		if english {
			subject = fmt.Sprintf("Verify your %s email", siteName)
			eyebrow, title = "Account security", "Verify your email"
			intro = fmt.Sprintf("%s, confirm this email address to finish securing your account.", name)
			action = "Verify email"
			detailTitle, detail = "This link expires in 24 hours", "If you did not create this account, you can safely ignore this email."
			note = "For your security, do not forward this verification link."
		} else {
			subject = fmt.Sprintf("验证你的 %s 邮箱", siteName)
			eyebrow, title = "账号安全", "验证你的邮箱"
			intro = fmt.Sprintf("%s，请确认这个邮箱地址以完成账号安全验证。", name)
			action = "验证邮箱"
			detailTitle, detail = "链接将在 24 小时后失效", "如果不是你创建了这个账号，可以忽略这封邮件。"
			note = "出于安全考虑，请不要把验证链接转发给其他人。"
		}
	case "identity.password_reset":
		actionURL = data["resetUrl"]
		if english {
			subject = fmt.Sprintf("Reset your %s password", siteName)
			eyebrow, title = "Account security", "Reset your password"
			intro = fmt.Sprintf("%s, we received a request to reset the password for your account.", name)
			action = "Reset password"
			detailTitle, detail = "This link expires soon", "If you did not request this, you can safely ignore this email. Your password will not change."
			note = "For your security, do not forward this reset link."
		} else {
			subject = fmt.Sprintf("重置你的 %s 密码", siteName)
			eyebrow, title = "账号安全", "重置你的密码"
			intro = fmt.Sprintf("%s，我们收到了这个账号的密码重置请求。", name)
			action = "设置新密码"
			detailTitle, detail = "链接将在 30 分钟后失效", "如果不是你发起的请求，可以忽略这封邮件，账号密码不会发生变化。"
			note = "出于安全考虑，请不要把重置链接转发给其他人。"
		}
	case "identity.welcome":
		actionURL = data["siteUrl"]
		if english {
			subject = fmt.Sprintf("Welcome to %s, %s", siteName, name)
			eyebrow, title = "Welcome", fmt.Sprintf("Welcome to %s, %s", siteName, name)
			intro = "Your account is ready. Start by finding discussions that matter to you."
			action, note = "Explore the forum", fmt.Sprintf("You received this email because you just joined %s.", siteName)
			list = []string{"Complete your profile", "Browse a category that interests you", "Read the community guidelines"}
		} else {
			subject = fmt.Sprintf("欢迎来到 %s，%s", siteName, name)
			eyebrow, title = "欢迎加入", fmt.Sprintf("%s，欢迎来到 %s", name, siteName)
			intro = "这里按话题组织讨论，也认真对待每一条回复。你的账号已经准备好了。"
			action, note = "开始逛逛", fmt.Sprintf("收到这封邮件是因为你刚刚注册了 %s。", siteName)
			list = []string{"补充头像和个人简介", "从感兴趣的分类开始浏览", "阅读社区规范，了解讨论边界"}
		}
	case "forum.reply", "forum.mention", "forum.moderation_pending", "forum.moderation_approved", "forum.moderation_rejected":
		return renderForumTemplate(key, data, english, siteName, name, brand)
	default:
		if english {
			subject, eyebrow, title, intro, note = fmt.Sprintf("[%s] Notification", siteName), "Notification", "You have a new notification", "Open the forum to see the latest activity.", "This email was sent by your notification settings."
		} else {
			subject, eyebrow, title, intro, note = fmt.Sprintf("[%s] 通知", siteName), "社区通知", "你有一条新通知", "打开论坛查看最新动态。", "这封邮件由你的通知偏好触发。"
		}
	}
	text = plainMail(title, intro, list, action, actionURL, detailTitle, detail, note, siteName)
	htmlBody = transactionalHTML(locale, brand, eyebrow, title, intro, list, action, actionURL, detailTitle, detail, note)
	return subject, text, htmlBody
}

func renderForumTemplate(key string, data map[string]string, english bool, siteName, name string, brand renderedMailBrand) (subject, text, htmlBody string) {
	var eyebrow, title, intro, note, action, actionURL string
	if english {
		switch key {
		case "forum.reply":
			if data["alsoMentioned"] == "true" {
				subject, eyebrow, title, intro = fmt.Sprintf("[%s] New reply", siteName), "New reply", "Someone replied and mentioned you", fmt.Sprintf("%s, someone replied to your content and mentioned you.", name)
			} else {
				subject, eyebrow, title, intro = fmt.Sprintf("[%s] New reply", siteName), "New reply", "Someone continued the discussion", fmt.Sprintf("%s, someone replied to your content.", name)
			}
		case "forum.mention":
			subject, eyebrow, title, intro = fmt.Sprintf("[%s] You were mentioned", siteName), "Mention", "You were mentioned in a discussion", fmt.Sprintf("%s, someone mentioned you in a post.", name)
		case "forum.moderation_pending":
			subject, eyebrow, title, intro = fmt.Sprintf("[%s] Content awaiting review", siteName), "Moderation queue", "New content needs review", fmt.Sprintf("%s, new content has entered the moderation queue.", name)
			action = "Open moderation queue"
		case "forum.moderation_approved":
			subject, eyebrow, title, intro = fmt.Sprintf("[%s] Content approved", siteName), "Moderation result", "Your content is now public", fmt.Sprintf("%s, your content passed moderation and is visible to members.", name)
		default:
			subject, eyebrow, title, intro = fmt.Sprintf("[%s] Content needs changes", siteName), "Moderation result", "Your content needs an update", fmt.Sprintf("%s, your content was not published. Review the moderation note and submit it again.", name)
		}
		note = "This email was sent by your notification settings."
	} else {
		switch key {
		case "forum.reply":
			if data["alsoMentioned"] == "true" {
				subject, eyebrow, title, intro = fmt.Sprintf("[%s] 你收到了新回复", siteName), "新回复", "有人回复并提到了你", fmt.Sprintf("%s，有人回复了你的内容并提到了你。", name)
			} else {
				subject, eyebrow, title, intro = fmt.Sprintf("[%s] 你收到了新回复", siteName), "新回复", "有人继续了这场讨论", fmt.Sprintf("%s，有人回复了你的内容。", name)
			}
		case "forum.mention":
			subject, eyebrow, title, intro = fmt.Sprintf("[%s] 你被提及了", siteName), "提及", "有人在讨论中提到了你", fmt.Sprintf("%s，有人在内容中提到了你。", name)
		case "forum.moderation_pending":
			subject, eyebrow, title, intro = fmt.Sprintf("[%s] 有新内容等待审核", siteName), "审核队列", "有新内容需要处理", fmt.Sprintf("%s，审核队列中出现了新的待处理内容。", name)
			action = "打开审核队列"
		case "forum.moderation_approved":
			subject, eyebrow, title, intro = fmt.Sprintf("[%s] 内容已通过审核", siteName), "审核结果", "你的内容已经公开", fmt.Sprintf("%s，你提交的内容已通过审核，现在其他成员可以看到了。", name)
		default:
			subject, eyebrow, title, intro = fmt.Sprintf("[%s] 内容还需要调整", siteName), "审核结果", "这篇内容还需要一点调整", fmt.Sprintf("%s，你提交的内容暂未公开。请查看审核说明后再次提交。", name)
		}
		note = "这封邮件由你的通知偏好触发。"
	}
	if action != "" && strings.HasPrefix(data["reviewPath"], "/") {
		actionURL = strings.TrimRight(data["siteUrl"], "/") + data["reviewPath"]
	}
	text = plainMail(title, intro, nil, action, actionURL, "", data["reviewNote"], note, siteName)
	htmlBody = transactionalHTML(data["locale"], brand, eyebrow, title, intro, nil, action, actionURL, "", data["reviewNote"], note)
	return subject, text, htmlBody
}

func plainMail(title, intro string, list []string, action, actionURL, detailTitle, detail, note, siteName string) string {
	lines := []string{title, "", intro}
	for _, item := range list {
		lines = append(lines, "- "+item)
	}
	if actionURL != "" {
		lines = append(lines, "", action+": "+actionURL)
	}
	if detailTitle != "" {
		lines = append(lines, "", detailTitle, detail)
	}
	return strings.Join(append(lines, "", note, siteName), "\n")
}

type renderedMailBrand struct {
	siteName, logoURL, iconURL, mark string
	accent, soft, softBorder         string
}

func mailBrandFromData(data map[string]string, siteName string) renderedMailBrand {
	brand := renderedMailBrand{
		siteName:   siteName,
		logoURL:    safeMailImageURL(data["brandLogoUrl"]),
		iconURL:    safeMailImageURL(data["brandIconUrl"]),
		mark:       fallback(data["brandMark"], firstMailRune(siteName)),
		accent:     safeMailColor(data["brandAccent"], "#0f766e"),
		soft:       safeMailColor(data["brandAccentSoft"], "#e6f4f1"),
		softBorder: safeMailColor(data["brandAccentSoftBorder"], "#b8ded8"),
	}
	return brand
}

func transactionalHTML(locale string, brand renderedMailBrand, eyebrow, title, intro string, list []string, action, actionURL, detailTitle, detail, note string) string {
	escape := html.EscapeString
	listHTML := ""
	if len(list) > 0 {
		items := make([]string, 0, len(list))
		for index, item := range list {
			items = append(items, fmt.Sprintf(`<tr><td style="width:30px;padding:0 10px 13px 0;color:#0f766e;font-size:13px;font-weight:800;vertical-align:top;">%02d</td><td style="padding:0 0 13px;color:#25252b;font-size:14px;line-height:1.55;">%s</td></tr>`, index+1, escape(item)))
		}
		listHTML = `<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="margin:22px 0 4px;border-collapse:collapse;">` + strings.Join(items, "") + `</table>`
	}
	actionHTML := ""
	if action != "" && actionURL != "" {
		actionHTML = fmt.Sprintf(`<table role="presentation" cellspacing="0" cellpadding="0" style="margin-top:26px;"><tr><td style="border-radius:4px;background:%s;"><a href="%s" style="display:inline-block;padding:12px 19px;color:#fff;font-size:14px;line-height:1;font-weight:700;text-decoration:none;">%s</a></td></tr></table>`, escape(brand.accent), escape(actionURL), escape(action))
	}
	detailHTML := ""
	if detailTitle != "" || detail != "" {
		detailHTML = fmt.Sprintf(`<div style="margin:24px 0 0;padding-top:20px;border-top:1px solid #e9e8ed;"><p style="margin:0 0 6px;color:#25252b;font-size:14px;line-height:1.5;font-weight:700;">%s</p><p style="margin:0;color:#676773;font-size:13px;line-height:1.7;">%s</p></div>`, escape(detailTitle), escape(detail))
	}
	if locale == "" {
		locale = "zh-CN"
	}
	return fmt.Sprintf(`<!doctype html><html lang="%s"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"></head><body style="margin:0;padding:0;background:#f5f5f8;color:#25252b;font-family:Inter,'Noto Sans SC','PingFang SC','Microsoft YaHei',Arial,sans-serif;"><table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="width:100%%;background:#f5f5f8;"><tr><td align="center" style="padding:0 24px;"><table role="presentation" width="640" cellspacing="0" cellpadding="0" style="width:640px;max-width:100%%;"><tr><td style="padding:36px 4px 20px;font-size:15px;font-weight:700;">%s</td></tr><tr><td style="border:1px solid #e9e8ed;border-radius:4px;background:#fff;overflow:hidden;"><div style="height:3px;background:%s;"></div><div style="padding:38px 42px 34px;"><span style="display:inline-block;margin:0 0 20px;padding:5px 8px;border:1px solid %s;border-radius:3px;background:%s;color:%s;font-size:11px;line-height:1;font-weight:700;">%s</span><h1 style="margin:0 0 14px;color:#25252b;font-size:26px;line-height:1.3;">%s</h1><p style="margin:0;color:#676773;font-size:15px;line-height:1.75;">%s</p>%s%s%s%s</div></td></tr><tr><td style="padding:22px 6px 38px;color:#8b8b96;font-size:11.5px;line-height:1.65;"><p style="margin:0 0 5px;">%s</p><p style="margin:0;">%s</p></td></tr></table></td></tr></table></body></html>`, escape(locale), mailBrandHeader(brand), escape(brand.accent), escape(brand.softBorder), escape(brand.soft), escape(brand.accent), escape(eyebrow), escape(title), escape(intro), listHTML, actionHTML, detailHTML, "", escape(note), escape(brand.siteName))
}

func mailBrandHeader(brand renderedMailBrand) string {
	escape := html.EscapeString
	if brand.logoURL != "" {
		return fmt.Sprintf(`<img src="%s" alt="%s" style="display:inline-block;height:28px;max-width:180px;vertical-align:middle;object-fit:contain;object-position:left;">`, escape(brand.logoURL), escape(brand.siteName))
	}
	if brand.iconURL != "" {
		return fmt.Sprintf(`<img src="%s" alt="" style="display:inline-block;width:28px;height:28px;border-radius:5px;vertical-align:middle;object-fit:contain;"><span style="padding-left:10px;vertical-align:middle;">%s</span>`, escape(brand.iconURL), escape(brand.siteName))
	}
	return fmt.Sprintf(`<span style="display:inline-block;width:28px;height:28px;border-radius:5px;background:%s;color:#fff;line-height:28px;text-align:center;">%s</span><span style="padding-left:10px;vertical-align:middle;">%s</span>`, escape(brand.accent), escape(brand.mark), escape(brand.siteName))
}

func safeMailImageURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return parsed.String()
}

func safeMailColor(value, fallbackColor string) string {
	value = strings.TrimSpace(value)
	if len(value) == 7 && value[0] == '#' {
		for _, char := range value[1:] {
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
				return fallbackColor
			}
		}
		return strings.ToLower(value)
	}
	return fallbackColor
}

func firstMailRune(value string) string {
	for _, runeValue := range strings.TrimSpace(value) {
		return string(runeValue)
	}
	return "S"
}

func fallback(value, alternative string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return alternative
}
