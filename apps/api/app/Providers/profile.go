package providers

import (
	"github.com/gofiber/fiber/v3"

	"context"
	profilecontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Profile"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type ProfileProvider struct {
	controller *profilecontroller.Controller
	service    *profile.Service
}

// trustTierCatalog 从 trust.levels 单键解出档位身份（id + 双语名）。
//
// 适配器留在 providers 层：Profile 只认最小接口 TrustTierCatalog，不知道配置存在哪、
// 也不知道它是怎么编码的。解码失败返回 false（资料页退回内置 id），不报错——
// 资料页不该因为等级配置写坏而整页失败。
type trustTierCatalog struct {
	options *options.Service
}

func (c trustTierCatalog) TierIdentity(ctx context.Context, rank int) (profile.TierIdentity, bool) {
	if c.options == nil {
		return profile.TierIdentity{}, false
	}
	raw, err := c.options.WebOption(ctx, options.NameTrustLevels)
	if err != nil {
		return profile.TierIdentity{}, false
	}
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		return profile.TierIdentity{}, false
	}
	level := trustlevels.Level(rank)
	id := policy.TierID(level)
	if id == "" {
		return profile.TierIdentity{}, false
	}
	return profile.TierIdentity{ID: id, Name: policy.DisplayNameOf(level)}, true
}

func NewProfileProvider(store profile.Store, users identity.ActorStore, sessions *authsession.Manager, optionsService *options.Service) *ProfileProvider {
	service := profile.NewService(store).WithTrustTierCatalog(trustTierCatalog{options: optionsService})
	return &ProfileProvider{
		controller: profilecontroller.NewController(service, users, sessions, optionsService),
		service:    service,
	}
}

// WithBookmarkLister 注入收藏名单读取面（Forum 实现，经 providers 适配）。
func (p *ProfileProvider) WithBookmarkLister(lister profile.BookmarkLister) *ProfileProvider {
	if p != nil && p.service != nil {
		p.service.WithBookmarkLister(lister)
	}
	return p
}

// WithNicknameEditor 注入昵称服务（Identity 实现）。不注入时昵称只读，
// 带 displayName 的 PUT 会被拒绝。
// WithTrustLevelReader 注入信任等级读取（公开资料页等级卡）。
//
// 未注入时资料页不带等级卡，与信任等级上线之前逐字节等价。
func (p *ProfileProvider) WithTrustLevelReader(reader profile.TrustLevelReader) *ProfileProvider {
	if p != nil && p.service != nil {
		p.service.WithTrustLevelReader(reader)
	}
	return p
}

// WithActivityCounters 注入公开行为计数（获赞 / 访问天数）。
//
// 适配器留在装配层：Profile 只认最小接口，Badges 的 SQL 不在 Profile 里复制。
// 未注入时两个计数为 0，与本次接线之前逐字节等价。
func (p *ProfileProvider) WithActivityCounters(reader profile.ActivityCountersReader) *ProfileProvider {
	if p != nil && p.service != nil {
		p.service.WithActivityCounters(reader)
	}
	return p
}

func (p *ProfileProvider) WithNicknameEditor(editor profile.NicknameEditor) *ProfileProvider {
	if p != nil && p.service != nil {
		p.service.WithNicknameEditor(editor)
	}
	return p
}

func NewProfileProviderWithAvatar(store profile.Store, users identity.ActorStore, sessions *authsession.Manager, uploader *attachments.Service, optionsService *options.Service) *ProfileProvider {
	return NewProfileProviderWithAvatarAndTabs(store, users, sessions, uploader, optionsService, nil)
}

// NewProfileProviderWithAvatarAndTabs 注入公开资料扩展 tabs（F4.3）。
func NewProfileProviderWithAvatarAndTabs(store profile.Store, users identity.ActorStore, sessions *authsession.Manager, uploader *attachments.Service, optionsService *options.Service, tabs profile.ProfileTabProvider) *ProfileProvider {
	service := profile.NewServiceWithAvatar(store, uploader, optionsService).
		WithTrustTierCatalog(trustTierCatalog{options: optionsService})
	if tabs != nil {
		service.WithProfileTabs(tabs)
	}
	return &ProfileProvider{
		controller: profilecontroller.NewController(service, users, sessions, optionsService),
		service:    service,
	}
}

// WithSettingsSurface 注入插件设置面（个人设置页 tab + 字段值读写）。
// 三个参数同源装配：字段目录来自 provider 投影，读写由同一个 meta 适配器实现，
// 避免「能看见的字段」与「能写的字段」来自两处判断。
func (p *ProfileProvider) WithSettingsSurface(
	sections profile.SettingsSectionProvider,
	meta profile.SettingsMetaReader,
	writer profile.SettingsMetaWriter,
) *ProfileProvider {
	if p != nil && p.service != nil {
		p.service.WithSettingsSections(sections).WithSettingsMeta(meta, writer)
	}
	return p
}

func (p *ProfileProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
