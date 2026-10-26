package extensions

import (
	"context"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// AuthProviderPackageCatalog 从 Host 扩展包目录构建 auth provider 只读 discovery。
// trust/enable 权威仍属既有 lifecycle；本结构不写 Registry。
type AuthProviderPackageCatalog struct {
	// ListPackages 返回已安装/暂存的扩展（含 builtin SyncBuiltins 结果）。
	// 必须是 Host 内部只读路径，不要求 extension.view 权限。
	ListPackages func(ctx context.Context) ([]Extension, error)
	// HasLiveGrant 可选：exact-artifact 可执行信任查询。
	// nil 时 Trusted 仅在 StatusEnabled 时为 true（启用隐含已 trust）。
	HasLiveGrant func(ctx context.Context, extensionID, version, packageDigest string) (bool, error)
}

// ListAuthProviderCandidates 扫描包目录中 kind=auth 的 Identity provider 声明。
func (c AuthProviderPackageCatalog) ListAuthProviderCandidates(ctx context.Context) ([]identity.AuthProviderPackageCandidate, error) {
	if c.ListPackages == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	items, err := c.ListPackages(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]identity.AuthProviderPackageCandidate, 0)
	for _, item := range items {
		if item.Type != TypePlugin {
			continue
		}
		declared := item.Manifest.Identity
		if declared == nil || len(declared.Providers) == 0 {
			continue
		}
		version := strings.TrimSpace(item.Version)
		digest := strings.ToLower(strings.TrimSpace(item.PackageDigest))
		versionID := item.ActiveVersionID
		// 优先 staged 版本元数据（SyncBuiltins 后常见：installed + staged，尚未 enable）。
		if item.StagedVersion != nil {
			if version == "" {
				version = strings.TrimSpace(item.StagedVersion.Version)
			}
			if digest == "" {
				digest = strings.ToLower(strings.TrimSpace(item.StagedVersion.PackageDigest))
			}
			if versionID == 0 {
				versionID = item.StagedVersion.ID
			}
			// staged 清单可能比活动 tip 更新；discovery 展示 staged identity 声明。
			if item.Status != StatusEnabled {
				declared = stagedIdentityOr(item.StagedVersion.Manifest.Identity, declared)
			}
		}
		enabled := item.Status == StatusEnabled
		trusted := enabled
		if c.HasLiveGrant != nil && digest != "" && version != "" {
			if granted, grantErr := c.HasLiveGrant(ctx, item.ID, version, digest); grantErr == nil {
				trusted = granted || enabled
			}
		}
		for _, provider := range declared.Providers {
			if strings.ToLower(strings.TrimSpace(provider.Kind)) != "auth" {
				continue
			}
			ops := make([]string, 0, len(provider.Operations))
			for _, op := range provider.Operations {
				name := strings.ToLower(strings.TrimSpace(op.Name))
				if name != "" {
					ops = append(ops, name)
				}
			}
			label, locales := resolveManifestProviderLabel(provider.Label)
			candidate := identity.NormalizeAuthProviderPackageCandidate(identity.AuthProviderPackageCandidate{
				ProviderID:            provider.ID,
				Kind:                  provider.Kind,
				ContractVersion:       provider.ContractVersion,
				Priority:              provider.Priority,
				Operations:            ops,
				OwnerExtensionID:      item.ID,
				OwnerExtensionVersion: version,
				OwnerPackageDigest:    digest,
				VersionID:             versionID,
				Label:                 label,
				LabelLocales:          locales,
				Icon:                  strings.TrimSpace(provider.Icon),
				Trusted:               trusted,
				Enabled:               enabled,
				Status:                item.Status,
				Source:                item.Source,
			})
			if candidate.ProviderID == "" || candidate.OwnerExtensionID == "" {
				continue
			}
			out = append(out, candidate)
		}
	}
	return out, nil
}

func stagedIdentityOr(staged, fallback *extensionmanifest.ManifestIdentity) *extensionmanifest.ManifestIdentity {
	if staged != nil && len(staged.Providers) > 0 {
		return staged
	}
	return fallback
}

func resolveManifestProviderLabel(label *extensionmanifest.LocalizedText) (string, map[string]string) {
	if label == nil || label.IsEmpty() {
		return "", nil
	}
	locales := map[string]string{}
	for k, v := range label.ByLocale {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			locales[k] = v
		}
	}
	defaultLabel := strings.TrimSpace(label.Default)
	if defaultLabel == "" {
		defaultLabel = label.Resolve("en-US")
	}
	if defaultLabel != "" {
		if strings.TrimSpace(locales["en-US"]) == "" {
			locales["en-US"] = defaultLabel
		}
	}
	if len(locales) == 0 {
		locales = nil
	}
	return defaultLabel, locales
}

// ListPackagesWithoutActor 是 Host 内部包目录读取（无权限门控）。
// 仅供 admin identity provider discovery 使用。
func (s *CatalogService) ListPackagesWithoutActor(ctx context.Context) ([]Extension, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.List(ctx)
}

// NewAuthProviderPackageCatalog 从 Service + 可选 trust 服务构造 discovery 源。
func NewAuthProviderPackageCatalog(service *Service, trust *ExecutableTrustService) AuthProviderPackageCatalog {
	catalog := AuthProviderPackageCatalog{}
	if service != nil {
		catalog.ListPackages = service.ListPackagesWithoutActor
	}
	if trust != nil && service != nil {
		catalog.HasLiveGrant = func(ctx context.Context, extensionID, version, packageDigest string) (bool, error) {
			item, err := service.store.Get(ctx, normalizeID(extensionID))
			if err != nil {
				// 包不存在：discovery 不 fail closed 整个列表，该候选视为未信任。
				return false, nil
			}
			// 用候选 digest/version 对齐 trust 检查（staged 可能尚未 active）。
			if dig := strings.ToLower(strings.TrimSpace(packageDigest)); dig != "" {
				item.PackageDigest = dig
			}
			if ver := strings.TrimSpace(version); ver != "" {
				item.Version = ver
			}
			return trust.TrustedArtifact(ctx, item)
		}
	}
	return catalog
}
