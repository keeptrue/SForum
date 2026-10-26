package extensions

import (
	"context"
	"log/slog"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// PublicSurfaceRevisionBumper 在扩展设置变更影响公开贡献面时递增宿主 revision。
// 通常由 options.Service 实现；未注入时跳过（测试兼容）。
type PublicSurfaceRevisionBumper interface {
	BumpPublicSurfaceRevision(ctx context.Context) (int64, error)
}

// WithPublicSurfaceRevisionBumper 注入公开前端贡献面 revision bump。
func WithPublicSurfaceRevisionBumper(bumper PublicSurfaceRevisionBumper) ServiceOption {
	return func(s *Service) {
		s.publicSurfaceRevision = bumper
	}
}

// 公开 forum 贡献点：设置变更后会影响帖子页/导航等匿名 SSR 内容。
var publicForumContributionPoints = map[string]struct{}{
	extensionmanifest.PointForumTopicBadges:     {},
	extensionmanifest.PointForumTopicSidebar:    {},
	extensionmanifest.PointForumTopicListBadges: {},
	extensionmanifest.PointForumNavItems:        {},
}

// ManifestAffectsPublicSurface 判定该扩展设置变更是否应 bump public_surface_revision。
// 条件（任一）：存在非空 enabledBySetting 的贡献；或存在公开 forum 贡献点。
func ManifestAffectsPublicSurface(manifest Manifest) bool {
	for _, contribution := range normalizeManifest(manifest).Contributions {
		if strings.TrimSpace(contribution.EnabledBySetting) != "" {
			return true
		}
		if _, ok := publicForumContributionPoints[strings.TrimSpace(contribution.Point)]; ok {
			return true
		}
	}
	return false
}

func maybeBumpPublicSurfaceRevision(s *Service, ctx context.Context, extension Extension) {
	if s == nil || s.publicSurfaceRevision == nil {
		return
	}
	if !ManifestAffectsPublicSurface(extension.Manifest) {
		return
	}
	if _, err := s.publicSurfaceRevision.BumpPublicSurfaceRevision(ctx); err != nil {
		// 设置已保存成功；revision bump 失败只记日志，避免回滚运营配置。
		slog.Warn("public surface revision bump failed after extension settings change",
			"extensionId", extension.ID,
			"err", err,
		)
	}
}
