package localization

// 扩展管理面与信任等级的对外 reason 文案（F-6）。
//
// 为什么单独成文件：messages.go 已经贴着 1000 行硬上限（arch boundary），再往里
// 加键会直接触发闸门；这些键属于「管理面运维诊断」，与用户侧文案不同域，放在这里
// 也更清楚。init 时并入主表，键集合与查询行为完全不变。
//
// 这些键此前只以常量/变量形式出现在控制器里（`fiber.NewError(..., providerSlotInvalidReason)`），
// 旧棘轮只认字面量实参，于是它们全部漏网：非 Web 客户端拿到的是裸键名。
// 覆盖它们的棘轮见 messages_coverage_test.go。
var extensionAdminMessages = map[string]map[string]string{
	"zh-CN": {
		"extension.frontend_trust_unavailable":           "前端信任确认服务暂时不可用（存储故障），请稍后重试；本次授权未生效。",
		"extension.lifecycle_operation_not_found":        "找不到该扩展生命周期操作记录，可能已过期或被清理。",
		"extensions.provider_slot_conflict":              "服务提供者选择已被其他操作更新，请刷新目录后重新核对。",
		"extensions.provider_slot_inspector_unavailable": "服务提供者检查服务暂时不可用，请稍后重试。",
		"extensions.provider_slot_invalid":               "服务提供者请求不正确：请检查契约、候选者与 revision 后重试。",
		"extensions.provider_slot_not_found":             "指定的服务槽或候选提供者不存在，请刷新后重新选择。",
		"extensions.provider_slot_stale":                 "该选择绑定的契约或候选制品已过期，请刷新后重新核对；过期选择不会自动继承到新制品。",
		"extensions.provider_slot_unavailable":           "服务提供者运行时暂时不可用，请稍后重试。",
		"extensions.query_catalog_unavailable":           "查询目录暂时不可用，请稍后重试。",
		"extensions.route_catalog_unavailable":           "路由目录暂时不可用，请稍后重试。",
		"extensions.route_provider_conflict":             "路由提供者状态已变化，请刷新后重试。",
		"extensions.route_provider_invalid":              "路由提供者请求不正确：请检查契约与候选者后重试。",
		"extensions.route_provider_not_found":            "指定的路由提供者不存在，请刷新后重新选择。",
		"extensions.route_provider_stale":                "该路由提供者选择已过期，请刷新后重新核对。",
		"site_chrome.conflict":                           "站点导航/公告/友链已被其他操作修改，请刷新后重试。",
		"trust_levels.impact_unavailable":                "信任等级影响评估暂时不可用，请稍后重试。",
		"trust_levels.reference_check_failed":            "信任等级引用检查失败，本次修改未保存，请稍后重试。",
	},
	"en-US": {
		"extension.frontend_trust_unavailable":           "The frontend trust confirmation service is temporarily unavailable (storage failure). Try again later; nothing was granted.",
		"extension.lifecycle_operation_not_found":        "That extension lifecycle operation record no longer exists; it may have expired or been cleaned up.",
		"extensions.provider_slot_conflict":              "The provider slot selection was updated by another operation. Refresh the catalog and check again.",
		"extensions.provider_slot_inspector_unavailable": "Provider slot inspection is temporarily unavailable. Try again later.",
		"extensions.provider_slot_invalid":               "The provider slot request is invalid. Check the contract, candidate, and revision, then try again.",
		"extensions.provider_slot_not_found":             "That provider slot or candidate does not exist. Refresh and choose again.",
		"extensions.provider_slot_stale":                 "The contract or candidate artifact this selection is bound to is stale. Refresh and check again; a stale selection never carries over to a new artifact.",
		"extensions.provider_slot_unavailable":           "The provider slot runtime is temporarily unavailable. Try again later.",
		"extensions.query_catalog_unavailable":           "The query catalog is temporarily unavailable. Try again later.",
		"extensions.route_catalog_unavailable":           "The route catalog is temporarily unavailable. Try again later.",
		"extensions.route_provider_conflict":             "The route provider state changed. Refresh and try again.",
		"extensions.route_provider_invalid":              "The route provider request is invalid. Check the contract and candidate, then try again.",
		"extensions.route_provider_not_found":            "That route provider does not exist. Refresh and choose again.",
		"extensions.route_provider_stale":                "This route provider selection is stale. Refresh and check again.",
		"site_chrome.conflict":                           "The site navigation, announcement, or friend-link data was changed by another operation. Refresh and try again.",
		"trust_levels.impact_unavailable":                "Trust level impact evaluation is temporarily unavailable. Try again later.",
		"trust_levels.reference_check_failed":            "The trust level reference check failed. Nothing was saved; try again later.",
	},
}

func init() {
	for locale, table := range extensionAdminMessages {
		target, ok := messages[locale]
		if !ok {
			continue
		}
		for key, value := range table {
			target[key] = value
		}
	}
}
