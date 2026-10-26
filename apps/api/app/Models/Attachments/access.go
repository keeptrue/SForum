package attachments

import (
	"context"
	"errors"
	"strings"

	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// CodeGuestLoginRequired 与论坛游客阅读策略对齐：匿名访问受保护附件内容。
const CodeGuestLoginRequired = "forum.guest_login_required"

// ErrGuestLoginRequired 论坛 guest.read=login_required 时匿名访问帖子媒体。
var ErrGuestLoginRequired = errors.New("attachments: guest login required")

// 显式站点公开用途：头像、SEO/品牌图；不受 forum.guest.read 限制。
const (
	ResourceTypeUser = "user"
	ResourceTypeSEO  = "seo"
	ResourceTypeSite = "site"

	ContextAvatar         = "avatar"
	ContextLogo           = "logo"
	ContextFavicon        = "favicon"
	ContextAppleTouchIcon = "apple-touch-icon"
)

// isSitePublicReference 是否为站点自有的公开资产引用（SEO、头像、品牌图）。
func isSitePublicReference(ref AttachmentReference) bool {
	switch strings.TrimSpace(ref.ResourceType) {
	case ResourceTypeSEO:
		return true
	case ResourceTypeUser:
		return strings.TrimSpace(ref.Context) == ContextAvatar
	case ResourceTypeSite:
		ctx := strings.TrimSpace(ref.Context)
		return ctx == ContextLogo || ctx == ContextFavicon || ctx == ContextAppleTouchIcon || ctx == "brand" || ctx == "chrome"
	default:
		return false
	}
}

// isAnonymousReadableReference 判定该引用是否让附件对匿名读者可读。
//
// 这一支与「是否返回存储永久 URL」是两个不同的问题，必须分开判：
// 扩展引用的图片要对所有访客可见，但绝不能因此把存储 PublicURL 泄漏到
// 管理列表与附件 API。见 isPermanentURLReference 的说明。
func isAnonymousReadableReference(ref ReferenceAccess) bool {
	if isSitePublicReference(ref.AttachmentReference) {
		return true
	}
	if strings.TrimSpace(ref.ResourceType) != extensionreference.ResourceType {
		return false
	}
	// content_type 是读时重查的，不做缓存：MIME 不合格就按不可读处理。
	return extensionreference.IsContentType(ref.ContentType)
}

// isPermanentURLReference 判定该引用是否允许返回存储的永久 URL 而不是代理路径。
//
// 只有站点自有资产（品牌图、头像、SEO 图）走永久 URL。扩展引用刻意不加入：
// 已认领的图标继续返回稳定的 /media/attachments/<publicId> 代理地址，
// 否则管理列表与附件 API 会把对象存储直链暴露给插件与运营。
func isPermanentURLReference(ref ReferenceAccess) bool {
	return isSitePublicReference(ref.AttachmentReference)
}

func isForumReference(resourceType string) bool {
	switch strings.TrimSpace(resourceType) {
	case "topic", "comment", "post":
		return true
	default:
		return false
	}
}

func canModerateAttachmentReference(actor identity.Actor) bool {
	return actor.Can(identity.PermissionAttachmentManage) || actor.Can(identity.PermissionModerationReview)
}

func canViewForumReference(actor identity.Actor, ref ReferenceAccess) bool {
	if !ref.Exists || ref.CategoryVisibility != "public" {
		return canModerateAttachmentReference(actor)
	}
	if ref.TopicStatus != "active" && ref.TopicStatus != "locked" {
		if ref.TopicStatus == "pending" && actor.ID > 0 && actor.ID == ref.AuthorUserID {
			return true
		}
		return canModerateAttachmentReference(actor)
	}
	switch ref.ResourceStatus {
	case "active", "locked":
		return true
	case "pending":
		return (actor.ID > 0 && actor.ID == ref.AuthorUserID) || canModerateAttachmentReference(actor)
	default:
		return canModerateAttachmentReference(actor)
	}
}

func (s *Service) guestReadLoginRequired(ctx context.Context) bool {
	if s == nil || s.options == nil {
		return false
	}
	value, err := s.options.WebOption(ctx, options.NameForumGuestRead)
	if err != nil {
		// 读配置失败：保守按 login_required，避免误公开。
		return true
	}
	return strings.TrimSpace(value) == "login_required"
}

// authorizeAttachmentView 在 canViewAttachment 之上叠加论坛游客读策略。
func (s *Service) authorizeAttachmentView(ctx context.Context, actor identity.Actor, attachment Attachment) error {
	if !canViewAttachment(actor, attachment) {
		return identity.ErrPermissionDenied
	}
	if actor.Can(identity.PermissionAttachmentManage) ||
		attachment.Status != StatusActive || attachment.Visibility != VisibilityPublic {
		return nil
	}
	refs, err := s.store.ListReferenceAccess(ctx, attachment.ID)
	if err != nil {
		return identity.ErrPermissionDenied
	}
	ownerUserID := int64(0)
	if attachment.Owner != nil {
		ownerUserID = attachment.Owner.ID
	}
	return AuthorizeReadGuardSubject(actor, ReadGuardSubject{
		PublicID: attachment.PublicID, OwnerUserID: ownerUserID,
		Status: attachment.Status, Visibility: attachment.Visibility,
		Exists: true, References: refs,
	}, s.guestReadLoginRequired(ctx))
}

// AuthorizeReadGuardSubject keeps core and trusted replacement reads on the
// same attachment/reference policy without exposing storage internals.
func AuthorizeReadGuardSubject(actor identity.Actor, subject ReadGuardSubject, guestLoginRequired bool) error {
	if !subject.Exists || strings.TrimSpace(subject.PublicID) == "" {
		return ErrAttachmentNotFound
	}
	if actor.Can(identity.PermissionAttachmentManage) {
		return nil
	}
	if subject.Status != StatusActive {
		return identity.ErrPermissionDenied
	}
	if subject.Visibility != VisibilityPublic {
		if actor.IsActive() && subject.OwnerUserID > 0 && actor.ID == subject.OwnerUserID {
			return nil
		}
		return identity.ErrPermissionDenied
	}
	if len(subject.References) == 0 {
		if actor.IsActive() && subject.OwnerUserID > 0 && actor.ID == subject.OwnerUserID {
			return nil
		}
		return identity.ErrPermissionDenied
	}
	for _, ref := range subject.References {
		if isAnonymousReadableReference(ref) {
			return nil
		}
		if !isForumReference(ref.ResourceType) || !canViewForumReference(actor, ref) {
			continue
		}
		if !actor.IsActive() && guestLoginRequired {
			return ErrGuestLoginRequired
		}
		return nil
	}
	return identity.ErrPermissionDenied
}

// MediaAttachmentURLPath 是正文、普通附件与扩展公开资产共用的稳定媒体地址；
// Nuxt 在内部代理到带会话授权的 display 变体路由，变体不可用时由附件服务回退原文件。
//
// 与宿主域名无关，也不复用 decorateURL：扩展数据里绝不能出现对象存储直链，
// 换域名或换存储后端时插件侧存下来的 URL 才不会烂掉。
func MediaAttachmentURLPath(publicID string) string {
	return "/media/attachments/" + publicID
}

func mediaAttachmentURLPath(publicID string) string {
	return MediaAttachmentURLPath(publicID)
}

// shouldProxyAuthorizedURL 只有站点自有资产可返回永久 URL；论坛、扩展与未引用媒体始终走代理。
func (s *Service) shouldProxyAuthorizedURL(ctx context.Context, attachment Attachment) bool {
	refs, err := s.store.ListReferenceAccess(ctx, attachment.ID)
	if err != nil {
		return true
	}
	for _, ref := range refs {
		if isPermanentURLReference(ref) {
			return false
		}
	}
	return true
}

func (s *Service) decorateURL(ctx context.Context, attachment Attachment) Attachment {
	if s.shouldProxyAuthorizedURL(ctx, attachment) {
		attachment.URL = mediaAttachmentURLPath(attachment.PublicID)
		return attachment
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		attachment.URL = mediaAttachmentURLPath(attachment.PublicID)
		return attachment
	}
	adapter, err := s.adapterForSettings(ctx, settings, attachment.Provider)
	if err == nil {
		// 远程 provider 若仅有永久公网 URL，在需授权时仍回退代理（上面已处理）。
		// 此处 public 模式可直接用 PublicURL；有 SignedURL 能力时优先短时签名。
		if attachment.Visibility == VisibilityPrivate {
			if signed, signErr := adapter.SignedURL(ctx, attachment.ObjectKey, defaultSignedURLTTL); signErr == nil && signed != "" {
				attachment.URL = signed
			}
		} else {
			attachment.URL = adapter.PublicURL(attachment.ObjectKey)
		}
	}
	if attachment.URL == "" {
		attachment.URL = mediaAttachmentURLPath(attachment.PublicID)
	}
	return attachment
}
