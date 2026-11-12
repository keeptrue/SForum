package extensionmanifest

import (
	"strings"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
)

// 能力投影：把清单转成 capabilities 包的解析输入。
//
// 单独一个文件不是为了行数好看，而是这组函数只依赖「清单的结构」而不依赖
// 「清单的校验规则」——前者随产品能力增长，后者随契约收紧，混在一个文件里
// 每次加能力都要动 700 行的大文件（manifest.go 有行数上限基线）。
// CapabilityResolveInput 将 manifest 转为 capabilities 解析输入（F2.1）。
func CapabilityResolveInput(manifest Manifest) capabilities.ResolveInput {
	slots := make([]string, 0, len(manifest.Providers))
	for _, provider := range manifest.Providers {
		slots = append(slots, provider.Slot)
	}
	return capabilities.ResolveInput{
		Explicit:      manifest.Capabilities,
		HasJobs:       len(manifest.Jobs) > 0,
		HasSettings:   len(manifest.Settings) > 0,
		ProviderSlots: slots,
		HasBackend:    strings.TrimSpace(manifest.Backend.Entry) != "",
	}
}

// ResolvedCapabilities 返回有效能力 key 与 implied 标记。
func ResolvedCapabilities(manifest Manifest) (keys []string, implied map[string]bool) {
	return capabilities.Resolve(CapabilityResolveInput(manifest))
}

// CapabilityGrants 返回启用审查用的能力列表。
func CapabilityGrants(manifest Manifest) []capabilities.Grant {
	return capabilities.GrantsFor(CapabilityResolveInput(manifest))
}
