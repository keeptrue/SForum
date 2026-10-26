// Package useragent 提供 User-Agent 与 IP 的展示层解析，供会话目录登记设备信息。
// 仅用于展示（device_name/browser/os/ip_prefix），不参与鉴权决策。
package useragent

import (
	"strings"

	"github.com/mileusna/useragent"

	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
)

// DeviceInfo 是从请求 UA/IP 解析出的展示信息。
type DeviceInfo struct {
	DeviceName   string // 组合展示名，如 "Chrome on macOS"
	Browser      string // 浏览器名，如 "Chrome"
	OS           string // 操作系统名，如 "macOS"
	UserAgentRaw string // 截断后的原始 UA（展示/排查用，不暴露给前端）
	IPAddress    string // 规范化后的真实客户端 IP（库内存全文，不进公开设备列表 JSON）
	IPPrefix     string // 脱敏 IP 前缀，如 "1.2.3.*" / "2001:db8:1:*"
}

// Parse 解析 UA 与 IP 为展示信息。
// ua / ip 为空时各字段为空串（前端展示「未知」），不报错。
// rawUA 超过 512 字节会被截断，避免异常 UA 占满存储。
// ip 应为 clientip.FromCtx 的结果（全文）；此处再规范化并生成脱敏前缀。
func Parse(ua string, ip string) DeviceInfo {
	normalized := clientip.Normalize(ip)
	info := DeviceInfo{
		UserAgentRaw: truncate(strings.TrimSpace(ua), 512),
		IPAddress:    normalized,
		IPPrefix:     clientip.Mask(normalized),
	}
	if ua != "" {
		parsed := useragent.Parse(ua)
		info.Browser = parsed.Name
		info.OS = parsed.OS
		info.DeviceName = buildDeviceName(parsed.Name, parsed.OS)
	}
	return info
}

// buildDeviceName 构造组合展示名，如 "Chrome on macOS"。
// browser/os 任一为空时退化为非空的那一个，都空则返回空串。
func buildDeviceName(browser, os string) string {
	browser = strings.TrimSpace(browser)
	os = strings.TrimSpace(os)
	switch {
	case browser != "" && os != "":
		return browser + " on " + os
	case browser != "":
		return browser
	case os != "":
		return os
	default:
		return ""
	}
}

func truncate(value string, max int) string {
	if max <= 0 || len([]rune(value)) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}
