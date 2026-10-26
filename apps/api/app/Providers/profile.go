package providers

import (
	"github.com/gofiber/fiber/v3"

	profilecontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Profile"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type ProfileProvider struct {
	controller *profilecontroller.Controller
	service    *profile.Service
}

func NewProfileProvider(store profile.Store, users identity.ActorStore, sessions *authsession.Manager, optionsService *options.Service) *ProfileProvider {
	service := profile.NewService(store)
	return &ProfileProvider{
		controller: profilecontroller.NewController(service, users, sessions, optionsService),
		service:    service,
	}
}

// WithNicknameEditor 注入昵称服务（Identity 实现）。不注入时昵称只读，
// 带 displayName 的 PUT 会被拒绝。
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
	service := profile.NewServiceWithAvatar(store, uploader, optionsService)
	if tabs != nil {
		service.WithProfileTabs(tabs)
	}
	return &ProfileProvider{
		controller: profilecontroller.NewController(service, users, sessions, optionsService),
		service:    service,
	}
}

func (p *ProfileProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
