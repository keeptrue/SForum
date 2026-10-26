package pageviewmodels

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

var (
	ErrCorePageDataUnavailable  = errors.New("pages: core page data unavailable")
	ErrCorePageDataNotFound     = errors.New("pages: core page data not found")
	ErrCorePageDataUnauthorized = errors.New("pages: core page data requires login")
)

type CoreForumViewReader interface {
	ListCategoryGroups(context.Context, bool) ([]forum.CategoryGroup, error)
	ListTags(context.Context, bool) ([]forum.Tag, error)
	ListTopics(context.Context, forum.TopicListInput) (forum.TopicList, error)
	GetTopic(context.Context, int64) (forum.TopicDetail, error)
	GetTopicBySlug(context.Context, string) (forum.TopicDetail, error)
	GetTopicForViewer(context.Context, int64, identity.Actor) (forum.TopicDetail, error)
	GetTopicBySlugForViewer(context.Context, string, identity.Actor) (forum.TopicDetail, error)
	ListComments(context.Context, forum.CommentListInput) (forum.CommentList, error)
}

type CoreProfileViewReader interface {
	GetPublicProfile(context.Context, string) (profile.PublicProfile, error)
	GetMyProfile(context.Context, int64) (profile.PublicProfile, error)
}

type CoreNotificationViewReader interface {
	List(context.Context, notifications.ListInput) (notifications.Page, error)
	UnreadCount(context.Context, int64) (int64, error)
}

type CoreModerationViewReader interface {
	ListPending(context.Context, identity.Actor, moderation.WorkbenchListInput) (moderation.PendingList, error)
}

type CoreOptionViewReader interface {
	WebOption(context.Context, string) (string, error)
	IsFeatureEnabled(context.Context, string) (bool, error)
	ForumReadPolicySnapshot() (guestRead string, softDeleteVisibility string, revision uint64, ok bool)
}

type CoreRegistrationViewReader interface {
	RegistrationStatus(context.Context) (identity.RegistrationStatus, error)
}

type CoreSessionViewReader interface {
	ListUserSessions(context.Context, int64, string, bool, int, int) (identity.SessionListResult, error)
}

type CoreSiteChromeViewReader interface {
	ResolvePublicNavigation(context.Context, identity.Actor, string, []string) (sitechrome.ResolvedNavigation, error)
	ListPublicAnnouncements(context.Context) ([]sitechrome.Announcement, error)
}

type CoreSearchViewReader interface {
	Search(context.Context, search.SearchInput) (search.SearchResult, error)
}

type CorePageViewModelDependencies struct {
	Forum               CoreForumViewReader
	Profiles            CoreProfileViewReader
	Notifications       CoreNotificationViewReader
	NotificationTargets notifications.TargetVisibilityResolver
	Moderation          CoreModerationViewReader
	Options             CoreOptionViewReader
	Registration        CoreRegistrationViewReader
	Sessions            CoreSessionViewReader
	SiteChrome          CoreSiteChromeViewReader
	Search              CoreSearchViewReader
}

// CorePageViewModelInput contains normalized Host routing state plus the
// authoritative actor. Query values remain untrusted filters and are bounded
// again before reaching domain services.
type CorePageViewModelInput struct {
	Request          pages.CorePageViewModelRequest
	Actor            identity.Actor
	CurrentSessionID string
	Query            url.Values
}

type CorePageViewModelSource struct {
	deps CorePageViewModelDependencies
}

func NewCorePageViewModelSource(deps CorePageViewModelDependencies) *CorePageViewModelSource {
	return &CorePageViewModelSource{deps: deps}
}

func (s *CorePageViewModelSource) Populate(ctx context.Context, input CorePageViewModelInput) (pages.CorePageViewModelRequest, error) {
	if s == nil || s.deps.Options == nil {
		return pages.CorePageViewModelRequest{}, ErrCorePageDataUnavailable
	}
	request := input.Request
	if err := s.requirePageFeatures(ctx, request.PageID); err != nil {
		return pages.CorePageViewModelRequest{}, err
	}
	if err := s.populateCommon(ctx, &request, input.Actor); err != nil {
		return pages.CorePageViewModelRequest{}, err
	}

	var err error
	switch request.PageID {
	case "forum.home":
		err = s.populateHome(ctx, &request, input)
	case "forum.search":
		err = s.populateSearch(ctx, &request, input)
	case "forum.category.index":
		err = s.populateCategoryIndex(ctx, &request, input)
	case "forum.category.show":
		err = s.populateCategoryShow(ctx, &request, input)
	case "forum.tag.index":
		err = s.populateTagIndex(ctx, &request, input)
	case "forum.tag.show":
		err = s.populateTagShow(ctx, &request, input)
	case "forum.topic.show":
		err = s.populateTopicDetail(ctx, &request, input)
	case "forum.topic.create":
		err = s.populateTopicCreate(ctx, &request)
	case "forum.topic.reply":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		// 主题只拿到宿主表单边界；topic 查询和评论提交继续由回复岛与 API 负责。
		request.Data.TopicReply = &themecompiler.TopicReplyPageViewModel{}
	case "forum.topic.edit":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		// 同回复页：主题只拿宿主表单边界；旧内容加载与保存权限由编辑岛与 API 负责。
		request.Data.TopicEdit = &themecompiler.TopicEditPageViewModel{}
	case "forum.profile.show":
		err = s.populateProfile(ctx, &request, input.Actor)
	case "forum.settings.profile":
		err = s.populateProfileSettings(ctx, &request, input.Actor)
	case "forum.settings.appearance":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		request.SEO.Robots = "noindex,nofollow"
		request.Data.AppearanceSettings = &themecompiler.AppearanceSettingsPageViewModel{}
	case "forum.settings.login_methods":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		request.SEO.Robots = "noindex,nofollow"
		request.Data.LoginMethodsSettings = &themecompiler.LoginMethodsSettingsPageViewModel{}
	case "forum.settings.password":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		request.SEO.Robots = "noindex,nofollow"
		request.Data.LocalPasswordSettings = &themecompiler.LocalPasswordSettingsPageViewModel{}
	case "forum.settings.security":
		err = s.populateSecuritySettings(ctx, &request, input)
	case "forum.settings.tokens":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		request.SEO.Robots = "noindex,nofollow"
		request.Data.PersonalAccessTokens = &themecompiler.PersonalAccessTokensPageViewModel{}
	case "forum.settings.notifications":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		request.SEO.Robots = "noindex,nofollow"
		request.Data.NotificationSettings = &themecompiler.NotificationSettingsPageViewModel{}
	case "forum.notifications":
		err = s.populateNotifications(ctx, &request, input)
	case "forum.notification.show":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		// 主题只渲染详情页壳；收件人校验和通知正文继续由 Host 岛的详情 API 负责。
		request.SEO.Robots = "noindex,nofollow"
		request.Data.Notifications = &themecompiler.NotificationsPageViewModel{}
	case "moderation.review":
		err = s.populateModeration(ctx, &request, input)
	case "auth.login":
		err = s.populateLogin(ctx, &request)
	case "auth.register":
		err = s.populateRegister(ctx, &request)
	case "auth.external_continuation":
		request.SEO.Robots = "noindex,nofollow"
		request.Data.ExternalContinuation = &themecompiler.ExternalAuthContinuationPageViewModel{}
	case "auth.email_verification":
		if input.Actor.ID <= 0 {
			return pages.CorePageViewModelRequest{}, ErrCorePageDataUnauthorized
		}
		request.SEO.Robots = "noindex,nofollow"
		request.Data.EmailVerification = &themecompiler.EmailVerificationPageViewModel{}
	case "auth.forgot_password":
		request.Data.ForgotPassword = &themecompiler.ForgotPasswordPageViewModel{}
	case "auth.reset_password":
		request.Data.ResetPassword = &themecompiler.ResetPasswordPageViewModel{ChallengeReady: strings.TrimSpace(input.Query.Get("token")) != ""}
	case "site.terms", "site.privacy", "site.guidelines":
		err = s.populateLegal(ctx, &request)
	case "site.outbound":
		// 中转页是重启的安全面：永不索引，且运营开关在这里透传给呈现层。
		request.SEO.Robots = "noindex,nofollow"
		enabled := true
		if value, optionErr := s.deps.Options.WebOption(ctx, options.NameForumLinksExternalInterstitial); optionErr == nil {
			enabled = parseEnabledOptionValue(value, enabled)
		}
		request.Data.Outbound = &themecompiler.OutboundPageViewModel{InterstitialEnabled: enabled}
	case "system.forbidden":
		request.SEO.Robots = "noindex,nofollow"
		request.Data.Forbidden = systemErrorViewModel(request.Locale, 403)
	case "system.not_found":
		request.SEO.Robots = "noindex,nofollow"
		request.Data.NotFound = systemErrorViewModel(request.Locale, 404)
	case "system.rate_limited":
		request.SEO.Robots = "noindex,nofollow"
		request.Data.RateLimited = systemErrorViewModel(request.Locale, 429)
	case "system.server_error":
		request.SEO.Robots = "noindex,nofollow"
		request.Data.ServerError = systemErrorViewModel(request.Locale, 500)
	case "dev.components":
		request.Data.DevelopmentComponents = &themecompiler.DevelopmentComponentsPageViewModel{Components: componentPreviews()}
	default:
		err = pages.ErrUnknownPage
	}
	if err != nil {
		return pages.CorePageViewModelRequest{}, err
	}
	return request, nil
}

func systemErrorViewModel(locale string, statusCode int) *themecompiler.ErrorPageViewModel {
	switch statusCode {
	case 403:
		return &themecompiler.ErrorPageViewModel{
			StatusCode: statusCode,
			Title:      localizedText(locale, "这里需要更多权限", "This page needs more permission"),
			Message:    localizedText(locale, "当前请求无法继续访问。为了保护内容隐私，页面不会说明资源是否存在。", "This request cannot continue. To protect privacy, this page does not confirm whether a resource exists."),
		}
	case 404:
		return &themecompiler.ErrorPageViewModel{
			StatusCode: statusCode,
			Title:      localizedText(locale, "这个讨论暂时不在这里", "This discussion is not here"),
			Message:    localizedText(locale, "可能是链接拼写错误，或内容已被移动。你可以回到首页继续浏览社区里的最新讨论。", "The link may be misspelled, or the content may have moved. Return home to continue browsing the latest community discussions."),
		}
	case 429:
		return &themecompiler.ErrorPageViewModel{
			StatusCode: statusCode,
			Title:      localizedText(locale, "请求有点太频繁", "Too many requests"),
			Message:    localizedText(locale, "站点正在保护当前访问节奏。请按提示稍后再试，避免连续刷新。", "The site is protecting the current request rate. Please try again later as indicated, and avoid repeated refreshes."),
		}
	default:
		return &themecompiler.ErrorPageViewModel{
			StatusCode: statusCode,
			Title:      localizedText(locale, "服务正在恢复中", "The service is recovering"),
			Message:    localizedText(locale, "站点处理请求时遇到暂时问题。公开页面不会显示内部诊断细节。", "The site hit a temporary problem while processing the request. Public pages do not expose internal diagnostics."),
		}
	}
}

func (s *CorePageViewModelSource) populateCommon(ctx context.Context, request *pages.CorePageViewModelRequest, actor identity.Actor) error {
	siteName, err := s.deps.Options.WebOption(ctx, options.NameSiteName)
	if err != nil {
		return fmt.Errorf("%w: site name: %v", ErrCorePageDataUnavailable, err)
	}
	siteURL, err := s.deps.Options.WebOption(ctx, options.NameSiteURL)
	if err != nil {
		return fmt.Errorf("%w: site URL: %v", ErrCorePageDataUnavailable, err)
	}
	if strings.TrimSpace(request.SEO.Title) == "" || request.SEO.Title == request.PageID {
		request.SEO.Title = strings.TrimSpace(siteName)
	}
	request.SEO.CanonicalURL = absolutePublicURL(siteURL, request.Path)
	if request.SEO.Robots == "" {
		request.SEO.Robots = "index,follow"
	}
	if s.deps.SiteChrome == nil {
		return fmt.Errorf("%w: navigation resolver missing", ErrCorePageDataUnavailable)
	}
	resolved, navErr := s.deps.SiteChrome.ResolvePublicNavigation(ctx, actor, request.Locale, []string{sitechrome.NavigationLocationTopbar})
	if navErr != nil {
		return fmt.Errorf("%w: navigation: %v", ErrCorePageDataUnavailable, navErr)
	}
	var ok bool
	request.Navigation, ok = mapResolvedNavigation(request.Path, resolved, sitechrome.NavigationLocationTopbar)
	if !ok {
		return fmt.Errorf("%w: navigation topbar missing", ErrCorePageDataUnavailable)
	}
	return nil
}

func (s *CorePageViewModelSource) populateHome(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	return s.populateForumFeed(ctx, request, input, false)
}

func (s *CorePageViewModelSource) populateSearch(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	return s.populateForumFeed(ctx, request, input, true)
}

func (s *CorePageViewModelSource) populateForumFeed(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput, searchOnly bool) error {
	if err := s.requireForumRead(input.Actor); err != nil {
		return err
	}
	if s.deps.Forum == nil {
		return ErrCorePageDataUnavailable
	}
	groups, err := s.deps.Forum.ListCategoryGroups(ctx, false)
	if err != nil {
		return err
	}
	tags, err := s.deps.Forum.ListTags(ctx, false)
	if err != nil {
		return err
	}
	mode := s.topicURLMode(ctx)
	page := boundedInt(input.Query.Get("page"), 1, 1, 200)
	query := strings.TrimSpace(input.Query.Get("q"))
	categorySlug := strings.TrimSpace(input.Query.Get("category"))
	tagSlug := strings.TrimSpace(input.Query.Get("tag"))
	model := themecompiler.HomePageViewModel{Categories: mapCategories(groups), Tags: mapTags(tags)}
	if query != "" {
		if !searchOnly {
			enabled, featureErr := s.deps.Options.IsFeatureEnabled(ctx, options.NameFeatureSearch)
			if featureErr != nil || !enabled {
				return fmt.Errorf("%w: search feature disabled or unavailable", ErrCorePageDataUnavailable)
			}
		}
		if s.deps.Search == nil {
			return ErrCorePageDataUnavailable
		}
		found, searchErr := s.deps.Search.Search(ctx, search.SearchInput{Query: query, CategorySlug: categorySlug, TagSlug: tagSlug, Page: page})
		if searchErr != nil {
			return searchErr
		}
		model.Topics = mapSearchTopics(found.Items, mode)
		model.Search = &themecompiler.SearchStateView{Query: query, Results: mapSearchResults(found.Items, mode)}
		request.Pagination = paginationView(request.Path, input.Query, found.Page, found.PerPage, found.Total)
		applyPaginatedSEO(request, input.Query, found.Page, request.Path)
	} else if !searchOnly {
		list, listErr := s.deps.Forum.ListTopics(ctx, forum.TopicListInput{Page: page, CategorySlug: categorySlug, TagSlug: tagSlug, Sort: input.Query.Get("sort")})
		if listErr != nil {
			return listErr
		}
		model.Topics = mapForumTopics(list.Items, mode)
		request.Pagination = paginationView(request.Path, input.Query, list.Page, list.PerPage, list.Total)
		applyPaginatedSEO(request, input.Query, list.Page, request.Path)
	}
	if s.deps.SiteChrome != nil {
		announcements, announcementErr := s.deps.SiteChrome.ListPublicAnnouncements(ctx)
		if announcementErr != nil {
			return announcementErr
		}
		request.Regions = mapAnnouncements(request.Locale, announcements)
	}
	if searchOnly {
		request.SEO.Title = localizedText(request.Locale, "站内搜索", "Site search")
		request.SEO.Robots = "noindex,follow"
		request.Breadcrumbs = breadcrumbs(homeLabel(request.Locale), request.SEO.Title, request.Path)
		request.Data.Search = &model
	} else {
		request.SEO.StructuredData = []themecompiler.StructuredDataView{{Kind: "WebSite", Name: request.SEO.Title, URL: request.SEO.CanonicalURL}}
		request.Data.Home = &model
	}
	return nil
}

func (s *CorePageViewModelSource) populateCategoryIndex(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	if err := s.requireForumRead(input.Actor); err != nil {
		return err
	}
	groups, err := s.forumGroups(ctx)
	if err != nil {
		return err
	}
	title := localizedText(request.Locale, "分类", "Categories")
	request.SEO.Title = title
	request.Breadcrumbs = breadcrumbs(homeLabel(request.Locale), title, request.Path)
	request.Data.CategoryIndex = &themecompiler.CategoryIndexPageViewModel{List: themecompiler.PageListView{
		Title: title, Description: safePlainHTML(""), Items: []themecompiler.TopicSummaryView{}, Taxonomy: mapCategories(groups),
	}}
	return nil
}

func (s *CorePageViewModelSource) populateCategoryShow(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	if err := s.requireForumRead(input.Actor); err != nil {
		return err
	}
	groups, err := s.forumGroups(ctx)
	if err != nil {
		return err
	}
	slug := request.RouteParams["categorySlug"]
	category, ok := findCategory(groups, slug)
	if !ok {
		return fmt.Errorf("%w: category", ErrCorePageDataNotFound)
	}
	list, err := s.deps.Forum.ListTopics(ctx, forum.TopicListInput{Page: boundedInt(input.Query.Get("page"), 1, 1, 200), CategorySlug: slug, Sort: input.Query.Get("sort")})
	if err != nil {
		return err
	}
	categoryView := mapCategory(category)
	request.SEO.Title = category.Name
	request.SEO.Description = category.Description
	request.Breadcrumbs = breadcrumbs(homeLabel(request.Locale), category.Name, request.Path)
	request.Pagination = paginationView(request.Path, input.Query, list.Page, list.PerPage, list.Total)
	applyPaginatedSEO(request, input.Query, list.Page, request.Path)
	request.Data.CategoryShow = &themecompiler.CategoryShowPageViewModel{
		Category: categoryView,
		List:     themecompiler.PageListView{Title: category.Name, Description: safePlainHTML(category.Description), Items: mapForumTopics(list.Items, s.topicURLMode(ctx))},
	}
	return nil
}

func (s *CorePageViewModelSource) populateTagIndex(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	if err := s.requireForumRead(input.Actor); err != nil {
		return err
	}
	if err := s.requirePublicTags(ctx); err != nil {
		return err
	}
	if s.deps.Forum == nil {
		return ErrCorePageDataUnavailable
	}
	tags, err := s.deps.Forum.ListTags(ctx, false)
	if err != nil {
		return err
	}
	title := localizedText(request.Locale, "标签", "Tags")
	request.SEO.Title = title
	request.Breadcrumbs = breadcrumbs(homeLabel(request.Locale), title, request.Path)
	request.Data.TagIndex = &themecompiler.TagIndexPageViewModel{List: themecompiler.PageListView{
		Title: title, Description: safePlainHTML(""), Items: []themecompiler.TopicSummaryView{}, Taxonomy: mapTags(tags),
	}}
	return nil
}

func (s *CorePageViewModelSource) populateTagShow(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	if err := s.requireForumRead(input.Actor); err != nil {
		return err
	}
	if err := s.requirePublicTags(ctx); err != nil {
		return err
	}
	if s.deps.Forum == nil {
		return ErrCorePageDataUnavailable
	}
	tags, err := s.deps.Forum.ListTags(ctx, false)
	if err != nil {
		return err
	}
	slug := request.RouteParams["tagSlug"]
	tag, ok := findTag(tags, slug)
	if !ok {
		return fmt.Errorf("%w: tag", ErrCorePageDataNotFound)
	}
	list, err := s.deps.Forum.ListTopics(ctx, forum.TopicListInput{Page: boundedInt(input.Query.Get("page"), 1, 1, 200), TagSlug: slug, Sort: input.Query.Get("sort")})
	if err != nil {
		return err
	}
	request.SEO.Title = tag.Name
	request.SEO.Description = tag.Description
	request.Breadcrumbs = breadcrumbs(homeLabel(request.Locale), tag.Name, request.Path)
	request.Pagination = paginationView(request.Path, input.Query, list.Page, list.PerPage, list.Total)
	applyPaginatedSEO(request, input.Query, list.Page, request.Path)
	request.Data.TagShow = &themecompiler.TagShowPageViewModel{
		Tag:  mapTag(tag),
		List: themecompiler.PageListView{Title: tag.Name, Description: safePlainHTML(tag.Description), Items: mapForumTopics(list.Items, s.topicURLMode(ctx))},
	}
	return nil
}

func (s *CorePageViewModelSource) populateTopicDetail(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	if err := s.requireForumRead(input.Actor); err != nil {
		return err
	}
	if s.deps.Forum == nil {
		return ErrCorePageDataUnavailable
	}
	ctx = forum.WithPublicRenderLocale(ctx, request.Locale)
	topic, err := s.resolveTopic(ctx, request.RouteParams["path"], input.Actor)
	if err != nil {
		if errors.Is(err, forum.ErrTopicNotFound) {
			return fmt.Errorf("%w: topic", ErrCorePageDataNotFound)
		}
		return err
	}
	comments, err := s.deps.Forum.ListComments(ctx, forum.CommentListInput{
		TopicID: topic.ID, View: "tree", Page: boundedInt(input.Query.Get("page"), 1, 1, 200), Viewer: input.Actor,
	})
	if err != nil {
		return err
	}
	mode := s.topicURLMode(ctx)
	request.Pagination = paginationView(request.Path, input.Query, comments.Page, comments.PerPage, comments.Total)
	applyPaginatedSEO(request, input.Query, comments.Page, localizedTopicPath(request.Path, topicPublicPath(topic.ID, topic.Slug, mode)))
	request.SEO.Title = topic.Title
	request.SEO.Description = topic.Content.Excerpt
	request.SEO.StructuredData = []themecompiler.StructuredDataView{{
		Kind: "DiscussionForumPosting", ID: strconv.FormatInt(topic.ID, 10), Name: topic.Title,
		URL: request.SEO.CanonicalURL, Description: topic.Content.Excerpt,
		DateCreated: formatViewTime(topic.CreatedAt), DateUpdated: formatViewTime(topic.UpdatedAt),
	}}
	request.Breadcrumbs = []themecompiler.BreadcrumbItem{{Label: topic.CategoryName, URL: "/c/" + url.PathEscape(topic.CategorySlug)}, {Label: topic.Title, URL: request.Path}}
	request.Data.TopicDetail = &themecompiler.TopicDetailPageViewModel{
		Topic: mapForumTopic(topic.TopicSummary, mode),
		Body:  themecompiler.NewSafeHTMLFromSanitized(topic.Content.HTMLContent), Comments: mapComments(comments.Items),
	}
	return nil
}

func (s *CorePageViewModelSource) populateTopicCreate(ctx context.Context, request *pages.CorePageViewModelRequest) error {
	groups, err := s.forumGroups(ctx)
	if err != nil {
		return err
	}
	tags, err := s.deps.Forum.ListTags(ctx, false)
	if err != nil {
		return err
	}
	request.Data.TopicCreate = &themecompiler.TopicCreatePageViewModel{Categories: mapCategories(groups), Tags: mapTags(tags)}
	return nil
}

func (s *CorePageViewModelSource) populateProfile(ctx context.Context, request *pages.CorePageViewModelRequest, actor identity.Actor) error {
	if err := s.requireForumRead(actor); err != nil {
		return err
	}
	if s.deps.Profiles == nil {
		return ErrCorePageDataUnavailable
	}
	item, err := s.deps.Profiles.GetPublicProfile(ctx, request.RouteParams["username"])
	if err != nil {
		if errors.Is(err, profile.ErrProfileNotFound) {
			return fmt.Errorf("%w: profile", ErrCorePageDataNotFound)
		}
		return err
	}
	user, bio, topics := mapPublicProfile(item, s.topicURLMode(ctx))
	request.SEO.Title = firstNonempty(user.DisplayName, user.Username)
	request.SEO.Description = item.Profile.Bio
	request.SEO.StructuredData = []themecompiler.StructuredDataView{{Kind: "Person", ID: strconv.FormatInt(user.ID, 10), Name: request.SEO.Title, URL: request.SEO.CanonicalURL, Description: item.Profile.Bio}}
	request.Breadcrumbs = breadcrumbs(homeLabel(request.Locale), request.SEO.Title, request.Path)
	request.Data.Profile = &themecompiler.ProfilePageViewModel{Profile: user, Bio: bio, Topics: topics}
	return nil
}

func (s *CorePageViewModelSource) populateProfileSettings(ctx context.Context, request *pages.CorePageViewModelRequest, actor identity.Actor) error {
	if s.deps.Profiles == nil || actor.ID <= 0 {
		return ErrCorePageDataUnauthorized
	}
	item, err := s.deps.Profiles.GetMyProfile(ctx, actor.ID)
	if err != nil {
		return err
	}
	user, _, _ := mapPublicProfile(item, s.topicURLMode(ctx))
	request.SEO.Robots = "noindex,nofollow"
	request.Data.ProfileSettings = &themecompiler.ProfileSettingsPageViewModel{Profile: user}
	return nil
}

func (s *CorePageViewModelSource) populateSecuritySettings(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	if s.deps.Sessions == nil || input.Actor.ID <= 0 {
		return ErrCorePageDataUnauthorized
	}
	list, err := s.deps.Sessions.ListUserSessions(ctx, input.Actor.ID, input.CurrentSessionID, false, 1, 20)
	if err != nil {
		return err
	}
	devices := make([]themecompiler.SecurityDeviceView, 0, len(list.Items))
	for _, item := range list.Items {
		devices = append(devices, themecompiler.SecurityDeviceView{Label: item.DeviceName, Current: item.IsCurrent, LastSeenAt: formatViewTime(item.LastSeenAt)})
	}
	request.SEO.Robots = "noindex,nofollow"
	request.Data.SecuritySettings = &themecompiler.SecuritySettingsPageViewModel{CredentialConfigured: true, Devices: devices}
	return nil
}

func (s *CorePageViewModelSource) populateNotifications(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	if s.deps.Notifications == nil || input.Actor.ID <= 0 {
		return ErrCorePageDataUnauthorized
	}
	page, err := s.deps.Notifications.List(ctx, notifications.ListInput{
		RecipientUserID: input.Actor.ID, Limit: boundedInt(input.Query.Get("limit"), 20, 1, 100), BeforeID: boundedInt64(input.Query.Get("beforeId"), 0, 0),
	})
	if err != nil {
		return err
	}
	page, err = notifications.ResolveSafeTargets(ctx, s.deps.NotificationTargets, input.Actor.ID, page)
	if err != nil {
		return err
	}
	request.SEO.Robots = "noindex,nofollow"
	request.Data.Notifications = &themecompiler.NotificationsPageViewModel{Items: mapNotifications(page.Items)}
	return nil
}

func (s *CorePageViewModelSource) populateModeration(ctx context.Context, request *pages.CorePageViewModelRequest, input CorePageViewModelInput) error {
	if s.deps.Moderation == nil || input.Actor.ID <= 0 {
		return ErrCorePageDataUnauthorized
	}
	list, err := s.deps.Moderation.ListPending(ctx, input.Actor, moderation.WorkbenchListInput{
		TargetType: input.Query.Get("targetType"), Page: boundedInt(input.Query.Get("page"), 1, 1, 200), PerPage: boundedInt(input.Query.Get("perPage"), 20, 1, 100),
	})
	if err != nil {
		return err
	}
	request.SEO.Robots = "noindex,nofollow"
	request.Pagination = paginationView(request.Path, input.Query, list.Page, list.PerPage, list.Total)
	request.Data.ModerationReview = &themecompiler.ModerationReviewPageViewModel{Items: mapModerationItems(list.Items)}
	return nil
}

func (s *CorePageViewModelSource) populateLogin(ctx context.Context, request *pages.CorePageViewModelRequest) error {
	status, err := s.registrationStatus(ctx)
	if err != nil {
		return err
	}
	request.SEO.Robots = "noindex,follow"
	request.Data.Login = &themecompiler.LoginPageViewModel{RegistrationEnabled: status.RegistrationEnabled, RecoveryEnabled: true}
	return nil
}

func (s *CorePageViewModelSource) populateRegister(ctx context.Context, request *pages.CorePageViewModelRequest) error {
	status, err := s.registrationStatus(ctx)
	if err != nil {
		return err
	}
	request.SEO.Robots = "noindex,nofollow"
	request.Data.Register = &themecompiler.RegisterPageViewModel{RegistrationEnabled: status.RegistrationEnabled}
	return nil
}

func (s *CorePageViewModelSource) populateLegal(ctx context.Context, request *pages.CorePageViewModelRequest) error {
	var key, title string
	switch request.PageID {
	case "site.terms":
		key, title = legalOptionName("terms", request.Locale), localizedText(request.Locale, "服务条款", "Terms of Service")
	case "site.privacy":
		key, title = legalOptionName("privacy", request.Locale), localizedText(request.Locale, "隐私政策", "Privacy Policy")
	default:
		key, title = legalOptionName("guidelines", request.Locale), localizedText(request.Locale, "社区指南", "Community Guidelines")
	}
	body, err := s.deps.Options.WebOption(ctx, key)
	if err != nil {
		return err
	}
	safe := themecompiler.NewSafeHTMLFromSanitized("")
	if strings.TrimSpace(body) != "" {
		rendered, renderErr := forum.RenderContent(forum.ContentInput{RawContent: body, SourceFormat: forum.SourceFormatMarkdown, EditorType: forum.EditorTypeMarkdown})
		if renderErr != nil {
			return renderErr
		}
		safe = themecompiler.NewSafeHTMLFromSanitized(rendered.HTMLContent)
	}
	request.SEO.Title = title
	request.Breadcrumbs = breadcrumbs(homeLabel(request.Locale), title, request.Path)
	switch request.PageID {
	case "site.terms":
		request.Data.Terms = &themecompiler.TermsPageViewModel{Content: safe}
	case "site.privacy":
		request.Data.Privacy = &themecompiler.PrivacyPageViewModel{Content: safe}
	default:
		request.Data.Guidelines = &themecompiler.GuidelinesPageViewModel{Content: safe}
	}
	return nil
}

func (s *CorePageViewModelSource) requireForumRead(actor identity.Actor) error {
	guestRead, _, _, ok := s.deps.Options.ForumReadPolicySnapshot()
	if !ok {
		return ErrCorePageDataUnavailable
	}
	if guestRead == "login_required" && actor.ID <= 0 {
		return ErrCorePageDataUnauthorized
	}
	return nil
}

func (s *CorePageViewModelSource) requirePageFeatures(ctx context.Context, pageID string) error {
	page, ok := pages.Find(pageID)
	if !ok {
		return pages.ErrUnknownPage
	}
	for _, feature := range page.RequiresFeatures {
		enabled, err := s.deps.Options.IsFeatureEnabled(ctx, feature)
		if err != nil {
			return fmt.Errorf("%w: required feature %s: %v", ErrCorePageDataUnavailable, feature, err)
		}
		if !enabled {
			return fmt.Errorf("%w: required feature %s disabled", ErrCorePageDataNotFound, feature)
		}
	}
	return nil
}

func (s *CorePageViewModelSource) requirePublicTags(ctx context.Context) error {
	value, err := s.deps.Options.WebOption(ctx, options.NameForumTagPublicPages)
	if err != nil {
		return err
	}
	if !enabledOption(value) {
		return fmt.Errorf("%w: public tags disabled", ErrCorePageDataNotFound)
	}
	return nil
}

func (s *CorePageViewModelSource) forumGroups(ctx context.Context) ([]forum.CategoryGroup, error) {
	if s.deps.Forum == nil {
		return nil, ErrCorePageDataUnavailable
	}
	return s.deps.Forum.ListCategoryGroups(ctx, false)
}

func (s *CorePageViewModelSource) registrationStatus(ctx context.Context) (identity.RegistrationStatus, error) {
	if s.deps.Registration == nil {
		return identity.RegistrationStatus{}, ErrCorePageDataUnavailable
	}
	return s.deps.Registration.RegistrationStatus(ctx)
}

func (s *CorePageViewModelSource) topicURLMode(ctx context.Context) string {
	value, err := s.deps.Options.WebOption(ctx, options.NameSEOTopicURLMode)
	if err != nil {
		return "id_slug"
	}
	return value
}

func (s *CorePageViewModelSource) resolveTopic(ctx context.Context, rawPath string, viewer identity.Actor) (forum.TopicDetail, error) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return forum.TopicDetail{}, forum.ErrTopicNotFound
	}
	if id, err := strconv.ParseInt(parts[0], 10, 64); err == nil && id > 0 {
		return s.deps.Forum.GetTopicForViewer(ctx, id, viewer)
	}
	return s.deps.Forum.GetTopicBySlugForViewer(ctx, parts[0], viewer)
}

func paginationView(path string, query url.Values, page, perPage int, total int64) *themecompiler.PaginationView {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 20
	}
	result := &themecompiler.PaginationView{Page: page, PageSize: perPage, Total: total}
	if page > 1 {
		result.PreviousURL = pageURL(path, query, page-1)
	}
	if int64(page*perPage) < total {
		result.NextURL = pageURL(path, query, page+1)
	}
	return result
}

func applyPaginatedSEO(request *pages.CorePageViewModelRequest, query url.Values, page int, canonicalPath string) {
	canonicalQuery := make(url.Values)
	if page > 1 {
		canonicalQuery.Set("page", strconv.Itoa(page))
	}
	if encoded := canonicalQuery.Encode(); encoded != "" {
		canonicalPath += "?" + encoded
	}
	request.SEO.CanonicalURL = absolutePublicURL(request.SEO.CanonicalURL, canonicalPath)
	for key := range query {
		if key != "page" {
			request.SEO.Robots = "noindex,follow"
			break
		}
	}
}

func localizedTopicPath(requestPath, topicPath string) string {
	if index := strings.Index(strings.ToLower(requestPath), "/t/"); index >= 0 {
		return strings.TrimRight(requestPath[:index], "/") + topicPath
	}
	return topicPath
}

func pageURL(path string, input url.Values, page int) string {
	query := make(url.Values, len(input))
	for key, values := range input {
		query[key] = append([]string(nil), values...)
	}
	if page <= 1 {
		query.Del("page")
	} else {
		query.Set("page", strconv.Itoa(page))
	}
	if encoded := query.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func absolutePublicURL(base, path string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	reference, err := url.Parse(path)
	if err != nil {
		return ""
	}
	return parsed.ResolveReference(reference).String()
}

func breadcrumbs(siteName, label, path string) []themecompiler.BreadcrumbItem {
	items := []themecompiler.BreadcrumbItem{{Label: siteName, URL: "/"}}
	if path != "/" {
		items = append(items, themecompiler.BreadcrumbItem{Label: label, URL: path})
	}
	return items
}

func findCategory(groups []forum.CategoryGroup, slug string) (forum.Category, bool) {
	for _, group := range groups {
		if group.Visibility == "hidden" {
			continue
		}
		for _, item := range group.Categories {
			if item.Slug == slug && item.Visibility != "hidden" {
				return item, true
			}
		}
	}
	return forum.Category{}, false
}

func findTag(items []forum.Tag, slug string) (forum.Tag, bool) {
	for _, item := range items {
		if item.Slug == slug && item.Status == forum.TagStatusActive {
			return item, true
		}
	}
	return forum.Tag{}, false
}

func boundedInt(raw string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minimum {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func boundedInt64(raw string, fallback, minimum int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < minimum {
		return fallback
	}
	return value
}

func enabledOption(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enabled", "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func legalOptionName(kind, locale string) string {
	english := strings.HasPrefix(strings.ToLower(locale), "en")
	switch kind {
	case "terms":
		if english {
			return options.NameLegalTermsBodyENUS
		}
		return options.NameLegalTermsBodyZHCN
	case "privacy":
		if english {
			return options.NameLegalPrivacyBodyENUS
		}
		return options.NameLegalPrivacyBodyZHCN
	default:
		if english {
			return options.NameLegalGuidelinesBodyENUS
		}
		return options.NameLegalGuidelinesBodyZHCN
	}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func homeLabel(locale string) string {
	return localizedText(locale, "首页", "Home")
}

// parseEnabledOptionValue 解析 options 的 enabled/disabled 文本；空或未知值回退 fallback。
// 不复用 Options 的未导出归一化，避免把选项内部表示泄漏成 PageViewModels 的依赖。
func parseEnabledOptionValue(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enabled", "true", "1", "on":
		return true
	case "disabled", "false", "0", "off":
		return false
	default:
		return fallback
	}
}
