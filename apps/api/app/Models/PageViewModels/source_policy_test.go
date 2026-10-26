package pageviewmodels

import (
	"context"
	"errors"
	"testing"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

type policyOptions struct {
	guestRead string
	features  map[string]bool
}

func (o policyOptions) WebOption(_ context.Context, name string) (string, error) {
	switch name {
	case options.NameSiteName:
		return "SForum", nil
	case options.NameSiteURL:
		return "https://forum.example", nil
	case options.NameSEOTopicURLMode:
		return "id_slug", nil
	default:
		return "", errors.New("unexpected option " + name)
	}
}

func (o policyOptions) IsFeatureEnabled(_ context.Context, name string) (bool, error) {
	if enabled, ok := o.features[name]; ok {
		return enabled, nil
	}
	return true, nil
}

func (o policyOptions) ForumReadPolicySnapshot() (string, string, uint64, bool) {
	return o.guestRead, "author_and_staff", 1, true
}

type policyProfiles struct {
	publicCalls int
}

func (p *policyProfiles) GetPublicProfile(context.Context, string) (profile.PublicProfile, error) {
	p.publicCalls++
	return sourceProfile(), nil
}

func (p *policyProfiles) GetMyProfile(context.Context, int64) (profile.PublicProfile, error) {
	return sourceProfile(), nil
}

type policyRegistration struct {
	calls int
}

func (r *policyRegistration) RegistrationStatus(context.Context) (identity.RegistrationStatus, error) {
	r.calls++
	return identity.RegistrationStatus{RegistrationEnabled: true}, nil
}

func TestProfileViewModelEnforcesGuestReadBeforeLoadingRecentTopics(t *testing.T) {
	profiles := &policyProfiles{}
	source := NewCorePageViewModelSource(CorePageViewModelDependencies{
		Options: policyOptions{guestRead: "login_required"}, Profiles: profiles, SiteChrome: sourceChrome{},
	})
	request := pages.CorePageViewModelRequest{
		PageID: "forum.profile.show", Locale: "en-US", Path: "/u/alice",
		RouteParams: map[string]string{"username": "alice"}, SEO: themecompiler.PageSEOView{Title: "forum.profile.show"},
	}

	if _, err := source.Populate(t.Context(), CorePageViewModelInput{Request: request}); !errors.Is(err, ErrCorePageDataUnauthorized) {
		t.Fatalf("anonymous profile error = %v", err)
	}
	if profiles.publicCalls != 0 {
		t.Fatalf("profile queried before guest-read authorization: calls=%d", profiles.publicCalls)
	}

	actor := identity.Actor{ID: 8, Status: identity.UserStatusActive}
	populated, err := source.Populate(t.Context(), CorePageViewModelInput{Request: request, Actor: actor})
	if err != nil {
		t.Fatalf("authenticated profile: %v", err)
	}
	if profiles.publicCalls != 1 || populated.Data.Profile == nil || len(populated.Data.Profile.Topics) == 0 {
		t.Fatalf("authenticated profile data = %#v, calls=%d", populated.Data.Profile, profiles.publicCalls)
	}
}

func TestRegisterViewModelHonorsCatalogFeatureRequirement(t *testing.T) {
	registration := &policyRegistration{}
	source := NewCorePageViewModelSource(CorePageViewModelDependencies{
		Options:      policyOptions{guestRead: "public", features: map[string]bool{options.NameFeatureRegistration: false}},
		Registration: registration,
	})
	_, err := source.Populate(t.Context(), CorePageViewModelInput{Request: pages.CorePageViewModelRequest{
		PageID: "auth.register", Locale: "en-US", Path: "/register", SEO: themecompiler.PageSEOView{Title: "auth.register"},
	}})
	if !errors.Is(err, ErrCorePageDataNotFound) {
		t.Fatalf("disabled registration error = %v", err)
	}
	if registration.calls != 0 {
		t.Fatalf("registration status queried after feature denial: calls=%d", registration.calls)
	}
}

func TestEmailVerificationViewModelRequiresLoginAndKeepsHostFormBoundary(t *testing.T) {
	source := NewCorePageViewModelSource(CorePageViewModelDependencies{
		Options: policyOptions{guestRead: "public"}, SiteChrome: sourceChrome{},
	})
	request := pages.CorePageViewModelRequest{
		PageID: "auth.email_verification", Locale: "en-US", Path: "/email-verification",
		SEO: themecompiler.PageSEOView{Title: "auth.email_verification"},
	}
	if _, err := source.Populate(t.Context(), CorePageViewModelInput{Request: request}); !errors.Is(err, ErrCorePageDataUnauthorized) {
		t.Fatalf("anonymous email verification error = %v", err)
	}

	populated, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: request, Actor: identity.Actor{ID: 8, Status: identity.UserStatusActive},
	})
	if err != nil {
		t.Fatal(err)
	}
	if populated.SEO.Robots != "noindex,nofollow" {
		t.Fatalf("email verification robots = %q", populated.SEO.Robots)
	}
	model, err := pages.BuildCorePageViewModel(populated)
	if err != nil {
		t.Fatal(err)
	}
	verification, ok := model.(themecompiler.EmailVerificationPageViewModel)
	if !ok || verification.Form.ComponentID != "identity.component.email_verification" || len(verification.Form.ActionRouteIDs) != 1 || verification.Form.ActionRouteIDs[0] != "core.route.identity.email_verification_request" {
		t.Fatalf("email verification form boundary = %#v", model)
	}
}

func TestNotFoundViewModelIsNeverIndexable(t *testing.T) {
	source := NewCorePageViewModelSource(CorePageViewModelDependencies{Options: policyOptions{guestRead: "public"}, SiteChrome: sourceChrome{}})
	populated, err := source.Populate(t.Context(), CorePageViewModelInput{Request: pages.CorePageViewModelRequest{
		PageID: "system.not_found", Locale: "en-US", Path: "/missing", SEO: themecompiler.PageSEOView{Title: "system.not_found"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if populated.SEO.Robots != "noindex,nofollow" {
		t.Fatalf("not-found robots = %q", populated.SEO.Robots)
	}
}

func TestPublicTopicNotFoundDoesNotRevealVisibilityToPrivilegedActors(t *testing.T) {
	source := newTestSource(&sourceForum{topicErr: forum.ErrTopicNotFound}, defaultSourceOptions("public"))
	request := pages.CorePageViewModelRequest{
		PageID: "forum.topic.show", Locale: "en-US", Path: "/t/42/hidden",
		RouteParams: map[string]string{"path": "42/hidden"}, SEO: themecompiler.PageSEOView{Title: "forum.topic.show"},
	}
	actors := map[string]identity.Actor{
		"guest":       {},
		"super_admin": {ID: 1, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}},
	}
	for name, actor := range actors {
		t.Run(name, func(t *testing.T) {
			_, err := source.Populate(t.Context(), CorePageViewModelInput{Request: request, Actor: actor})
			if !errors.Is(err, ErrCorePageDataNotFound) {
				t.Fatalf("public hidden topic error = %v", err)
			}
		})
	}
}

func TestTopicReplyViewModelRequiresLoginAndKeepsHostFormBoundary(t *testing.T) {
	source := NewCorePageViewModelSource(CorePageViewModelDependencies{Options: policyOptions{guestRead: "public"}, SiteChrome: sourceChrome{}})
	request := pages.CorePageViewModelRequest{
		PageID: "forum.topic.reply", Locale: "en-US", Path: "/topics/reply",
		SEO: themecompiler.PageSEOView{Title: "forum.topic.reply"},
	}
	if _, err := source.Populate(t.Context(), CorePageViewModelInput{Request: request}); !errors.Is(err, ErrCorePageDataUnauthorized) {
		t.Fatalf("anonymous reply error = %v", err)
	}

	populated, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: request, Actor: identity.Actor{ID: 8, Status: identity.UserStatusActive},
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := pages.BuildCorePageViewModel(populated)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := model.(themecompiler.TopicReplyPageViewModel)
	if !ok || reply.Form.ComponentID != "forum.component.topic_reply" || len(reply.Form.ActionRouteIDs) != 1 || reply.Form.ActionRouteIDs[0] != "core.route.forum.create_comment" {
		t.Fatalf("reply form boundary = %#v", model)
	}
}

func TestTopicEditViewModelRequiresLoginAndKeepsHostFormBoundary(t *testing.T) {
	source := NewCorePageViewModelSource(CorePageViewModelDependencies{Options: policyOptions{guestRead: "public"}, SiteChrome: sourceChrome{}})
	request := pages.CorePageViewModelRequest{
		PageID: "forum.topic.edit", Locale: "en-US", Path: "/topics/42/edit",
		RouteParams: map[string]string{"topicId": "42"},
		SEO:         themecompiler.PageSEOView{Title: "forum.topic.edit"},
	}
	if _, err := source.Populate(t.Context(), CorePageViewModelInput{Request: request}); !errors.Is(err, ErrCorePageDataUnauthorized) {
		t.Fatalf("anonymous edit error = %v", err)
	}

	populated, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: request, Actor: identity.Actor{ID: 8, Status: identity.UserStatusActive},
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := pages.BuildCorePageViewModel(populated)
	if err != nil {
		t.Fatal(err)
	}
	edit, ok := model.(themecompiler.TopicEditPageViewModel)
	if !ok || edit.Form.ComponentID != "forum.component.topic_editor" || len(edit.Form.ActionRouteIDs) != 1 || edit.Form.ActionRouteIDs[0] != "core.route.forum.update_topic" {
		t.Fatalf("edit form boundary = %#v", model)
	}
}
