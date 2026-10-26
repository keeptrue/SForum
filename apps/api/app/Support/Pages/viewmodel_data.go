package pages

import themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"

// CorePageViewModelData is the closed set of Host-produced page payloads.
// Keeping one typed slot per catalog page prevents request data, domain models,
// or arbitrary maps from crossing the theme execution boundary.
type CorePageViewModelData struct {
	Home                  *themecompiler.HomePageViewModel
	Search                *themecompiler.HomePageViewModel
	CategoryIndex         *themecompiler.CategoryIndexPageViewModel
	CategoryShow          *themecompiler.CategoryShowPageViewModel
	TagIndex              *themecompiler.TagIndexPageViewModel
	TagShow               *themecompiler.TagShowPageViewModel
	TopicDetail           *themecompiler.TopicDetailPageViewModel
	TopicCreate           *themecompiler.TopicCreatePageViewModel
	TopicReply            *themecompiler.TopicReplyPageViewModel
	TopicEdit             *themecompiler.TopicEditPageViewModel
	Profile               *themecompiler.ProfilePageViewModel
	ProfileSettings       *themecompiler.ProfileSettingsPageViewModel
	AppearanceSettings    *themecompiler.AppearanceSettingsPageViewModel
	LoginMethodsSettings  *themecompiler.LoginMethodsSettingsPageViewModel
	LocalPasswordSettings *themecompiler.LocalPasswordSettingsPageViewModel
	SecuritySettings      *themecompiler.SecuritySettingsPageViewModel
	PersonalAccessTokens  *themecompiler.PersonalAccessTokensPageViewModel
	NotificationSettings  *themecompiler.NotificationSettingsPageViewModel
	Notifications         *themecompiler.NotificationsPageViewModel
	ModerationReview      *themecompiler.ModerationReviewPageViewModel
	Login                 *themecompiler.LoginPageViewModel
	Register              *themecompiler.RegisterPageViewModel
	ExternalContinuation  *themecompiler.ExternalAuthContinuationPageViewModel
	EmailVerification     *themecompiler.EmailVerificationPageViewModel
	ForgotPassword        *themecompiler.ForgotPasswordPageViewModel
	ResetPassword         *themecompiler.ResetPasswordPageViewModel
	Terms                 *themecompiler.TermsPageViewModel
	Privacy               *themecompiler.PrivacyPageViewModel
	Guidelines            *themecompiler.GuidelinesPageViewModel
	Forbidden             *themecompiler.ErrorPageViewModel
	NotFound              *themecompiler.ErrorPageViewModel
	RateLimited           *themecompiler.ErrorPageViewModel
	ServerError           *themecompiler.ErrorPageViewModel
	DevelopmentComponents *themecompiler.DevelopmentComponentsPageViewModel
	Outbound              *themecompiler.OutboundPageViewModel
}
