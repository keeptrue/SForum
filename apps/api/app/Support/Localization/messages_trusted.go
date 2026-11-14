package localization

// trustedRuntimeMessages 是「可信运行时」错误码的第二层目录，优先级高于
// messages 主目录（见 Message 的查找顺序）。它单独成文件：主目录已接近
// 1000 行的手写文件上限，继续往里堆会把两个不同来源的文案混在一起。

var trustedRuntimeMessages = map[string]map[string]string{
	"zh-CN": {
		"extension.frontend_runtime_unavailable": "可信前端运行时暂不可用，请稍后重试。",
		"extension.frontend_digest_invalid":      "扩展前端摘要无效，请刷新扩展信息后重试。",
		"extension.frontend_package_changed":     "扩展组件文件已变化，旧授权已失效，请重新确认。",
		"extension.frontend_trust_not_found":     "该扩展没有可撤销的可信前端授权。",
		"extension.public_frontend_bridge_stale": "公开前端桥接授权已失效，请刷新页面后重试。",
	},
	"en-US": {
		"extension.frontend_runtime_unavailable": "The trusted frontend runtime is temporarily unavailable. Try again later.",
		"extension.frontend_digest_invalid":      "The extension frontend digest is invalid. Refresh the extension details and try again.",
		"extension.frontend_package_changed":     "The extension component changed, so its previous grant is no longer valid. Confirm it again.",
		"extension.frontend_trust_not_found":     "This extension has no trusted frontend grant to revoke.",
		"extension.public_frontend_bridge_stale": "The public frontend bridge grant is stale. Refresh the page and try again.",
	},
}
