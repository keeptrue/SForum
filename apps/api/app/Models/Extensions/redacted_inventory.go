package extensions

import (
	"context"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// RedactedExtensionInventoryItem 是自动化可读的扩展清单行。
// 故意不包含 packagePath、settings、trust token、完整 Manifest 或凭证。
type RedactedExtensionInventoryItem struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	Source        string    `json:"source"`
	IsSystem      bool      `json:"isSystem"`
	PackageDigest string    `json:"packageDigest,omitempty"`
	Capabilities  []string  `json:"capabilities,omitempty"`
	RuntimeState  string    `json:"runtimeState,omitempty"`
	Protocol      string    `json:"protocolTransport,omitempty"`
	InstalledAt   time.Time `json:"installedAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ListRedactedInventory 返回去敏扩展清单，供 Host Query extensions.read 使用。
// 不检查人类 RBAC；调用方必须先通过进程能力 extensions.read。
// Safe Mode 下拒绝，避免第三方自动化读取运行态目录。
func (s *CatalogService) ListRedactedInventory(ctx context.Context) ([]RedactedExtensionInventoryItem, error) {
	if s == nil || s.store == nil {
		return nil, ErrRuntimeUnavailable
	}
	if s.safeMode {
		return nil, ErrSafeModeActive
	}
	items, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RedactedExtensionInventoryItem, 0, len(items))
	for _, item := range items {
		// 仅装饰插件运行态；主题没有 runtime 子进程。
		if item.Type == TypePlugin {
			item = s.decorateRuntime(ctx, item)
		}
		out = append(out, redactExtensionInventoryItem(item))
	}
	return out, nil
}

func redactExtensionInventoryItem(item Extension) RedactedExtensionInventoryItem {
	row := RedactedExtensionInventoryItem{
		ID:            item.ID,
		Name:          item.Name,
		Version:       item.Version,
		Type:          item.Type,
		Status:        item.Status,
		Source:        item.Source,
		IsSystem:      item.IsSystem,
		PackageDigest: item.PackageDigest,
		InstalledAt:   item.InstalledAt,
		UpdatedAt:     item.UpdatedAt,
	}
	if item.Type == TypePlugin {
		keys, _ := extensionmanifest.ResolvedCapabilities(item.Manifest)
		row.Capabilities = capabilities.NormalizeKeys(keys)
		if item.Runtime != nil {
			row.RuntimeState = item.Runtime.State
			row.Protocol = item.Runtime.ProtocolTransport
		}
	}
	return row
}
