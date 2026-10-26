package extensionscontroller

import (
	"path"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
)

const (
	assetInspectorDefaultLimit = 50
	assetInspectorMaximumLimit = 200
	assetInspectorUnavailable  = "extensions.asset_inspector_unavailable"
	assetInspectorInvalid      = "extensions.asset_inspector_invalid"
	// assetInspectorSchemaVersion 是 admin 检查器响应契约版本，与注册表内部
	// schemaVersion 解耦，便于后续扩展字段而不强制改写 live registry 图。
	assetInspectorSchemaVersion = "sforum.asset-inspector@1"
)

// AssetInspectorSnapshot 是 Asset Registry 的脱敏 admin 视图。
// 绝不返回绝对文件系统路径或 ImpactDigest 等信任影响字段。
type AssetInspectorSnapshot struct {
	SchemaVersion    string                      `json:"schemaVersion"`
	Revision         uint64                      `json:"revision"`
	Digest           string                      `json:"digest"`
	PublicationCount int                         `json:"publicationCount"`
	AssetCount       int                         `json:"assetCount"`
	Publications     []AssetInspectorPublication `json:"publications"`
}

// AssetInspectorPublication 描述一个扩展制品的已发布资源集合（已脱敏）。
type AssetInspectorPublication struct {
	ExtensionID      string                 `json:"extensionId"`
	ExtensionVersion string                 `json:"extensionVersion"`
	PackageDigest    string                 `json:"packageDigest"`
	OwnerKind        string                 `json:"ownerKind"`
	Assets           []AssetInspectorHandle `json:"assets"`
}

// AssetInspectorHandle 暴露资源句柄的加载元数据；Path 仅在包相对路径时保留。
type AssetInspectorHandle struct {
	Handle    string   `json:"handle"`
	Type      string   `json:"type"`
	Path      string   `json:"path,omitempty"`
	Module    bool     `json:"module,omitempty"`
	Loading   string   `json:"loading,omitempty"`
	Integrity string   `json:"integrity,omitempty"`
	CSP       []string `json:"csp,omitempty"`
	Scope     []string `json:"scope,omitempty"`
}

func (h *Controller) inspectAsset(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.assetRegistry == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, assetInspectorUnavailable)
	}
	limit, err := parseInspectorLimit(c.Query("limit"), assetInspectorDefaultLimit, assetInspectorMaximumLimit)
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, assetInspectorInvalid)
	}
	return apphttp.OK(c, buildAssetInspectorSnapshot(h.assetRegistry.Snapshot(), limit))
}

// buildAssetInspectorSnapshot 将 live Snapshot 投影为 admin 脱敏视图。
// publicationCount/assetCount 始终反映全量图；publications 按 extensionId 排序后截断。
func buildAssetInspectorSnapshot(snapshot assetregistry.Snapshot, limit int) AssetInspectorSnapshot {
	publications := make([]AssetInspectorPublication, 0, len(snapshot.Publications))
	for _, publication := range snapshot.Publications {
		handles := make([]AssetInspectorHandle, 0, len(publication.Assets))
		for _, asset := range publication.Assets {
			handles = append(handles, AssetInspectorHandle{
				Handle:    asset.Handle,
				Type:      asset.Type,
				Path:      packageRelativePathOnly(asset.Path),
				Module:    asset.Module,
				Loading:   asset.Loading,
				Integrity: asset.Integrity,
				CSP:       append([]string(nil), asset.CSP...),
				Scope:     append([]string(nil), asset.Scope...),
			})
		}
		publications = append(publications, AssetInspectorPublication{
			ExtensionID:      publication.Artifact.ExtensionID,
			ExtensionVersion: publication.Artifact.ExtensionVersion,
			PackageDigest:    publication.Artifact.PackageDigest,
			OwnerKind:        publication.Artifact.OwnerKind,
			Assets:           handles,
		})
	}
	// 按 extensionId 稳定排序后再截断，避免 map 遍历顺序漂移。
	sort.Slice(publications, func(i, j int) bool {
		if publications[i].ExtensionID == publications[j].ExtensionID {
			return publications[i].PackageDigest < publications[j].PackageDigest
		}
		return publications[i].ExtensionID < publications[j].ExtensionID
	})
	if limit > 0 && len(publications) > limit {
		publications = publications[:limit]
	}
	return AssetInspectorSnapshot{
		SchemaVersion:    assetInspectorSchemaVersion,
		Revision:         snapshot.Revision,
		Digest:           snapshot.Digest,
		PublicationCount: len(snapshot.Publications),
		AssetCount:       len(snapshot.Assets),
		Publications:     publications,
	}
}

// packageRelativePathOnly 仅保留包内相对路径；绝对路径、盘符路径、反斜杠与
// 越界片段一律丢弃，防止宿主安装根目录泄露到 admin 检查器。
func packageRelativePathOnly(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || strings.Contains(value, "\\") ||
		strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return ""
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return value
}
