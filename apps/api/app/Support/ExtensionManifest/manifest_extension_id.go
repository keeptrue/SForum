package extensionmanifest

// ValidExtensionID 报告 value 是否满足 manifest 的 ID 规则。
//
// Host 里接受「调用方自报扩展身份」的路由复用这里，而不是各自再写一遍 pattern：
// 两份 pattern 一旦漂移，就会出现「清单通过、路由拒绝」的半死状态。
//
// 从 manifest.go 拆出来是**棘轮**要求：`manifest.go` 是 `Support/Extensions` 一族的
// 既有 God-object 基线（720 行），本刀要为 `userFactConditions` 加字段，所以先在同一
// 包内腾出等量空间，而不是抬高基线。
func ValidExtensionID(value string) bool {
	return manifestIDPattern.MatchString(value)
}
