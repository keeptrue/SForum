package extensionmanifest

// isThemeManifestSupported 报告主题清单是否只声明了主题能承载的字段。
//
// 主题的公开页面贡献只来自 theme.json；管理端设置使用 Settings Document。
//
// plugin-only 家族的 kind 判据**不在这里**：它在 `validatePluginOnlyFamilies`，调用点
// 在 `validateManifest` 里、本函数之前（2026-11-14 审查：本函数先返回会让清单里的
// seo / identity / permissionDefinitions 三行永不可达）。下面保留这三项作为「清单被
// 误删」时的兜底——它不决定首个报错文本，只保证仍然拒。
//
// 从 manifest.go 拆出来是文件体积门禁的要求（该文件有 720 行的历史上限），顺带把
// 「主题能承载什么」这一件事收进一个文件。
func isThemeManifestSupported(manifest Manifest) bool {
	// 主题的公开页面贡献只来自 theme.json；管理端设置使用 Settings Document。
	if manifest.Backend != (ManifestBackend{}) ||
		len(manifest.Permissions) != 0 ||
		len(manifest.Capabilities) != 0 ||
		len(manifest.Migrations) != 0 ||
		len(manifest.Routes) != 0 ||
		len(manifest.Hooks) != 0 ||
		len(manifest.Events) != 0 ||
		len(manifest.Jobs) != 0 ||
		len(manifest.Providers) != 0 ||
		len(manifest.Contributions) != 0 {
		return false
	}
	if len(manifest.Guards) != 0 || len(manifest.Schedules) != 0 ||
		len(manifest.Content) != 0 || len(manifest.Editor) != 0 || len(manifest.Entities) != 0 ||
		manifest.Database != nil || len(manifest.Cache) != 0 || len(manifest.SEO) != 0 ||
		len(manifest.Services) != 0 || len(manifest.Commands) != 0 ||
		len(manifest.AdminSurfaces) != 0 || len(manifest.Queries) != 0 || len(manifest.QueryResultFilters) != 0 ||
		manifest.Identity != nil || len(manifest.PermissionDefinitions) != 0 ||
		len(manifest.Media) != 0 || manifest.Lifecycle != nil || len(manifest.OpenAPI) != 0 {
		return false
	}
	return true
}
