package profile

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

type Service struct {
	store          Store
	avatarUploader AvatarUploader
	avatarOptions  AvatarOptionResolver
	profileTabs    ProfileTabProvider
	nicknames      NicknameEditor
}

const (
	// 资料页内嵌 activities 摘要上限（向后兼容）。
	profileActivityLimit = 20
	// 分页默认/上限，与论坛浅分页习惯一致。
	profileActivityPageDefaultPerPage = 20
	profileActivityPageMaxPerPage     = 50
	profileActivityPageMaxPage        = 200
)

// WithProfileTabs 注入公开资料页扩展 tabs（F4.3）。
func (s *Service) WithProfileTabs(provider ProfileTabProvider) *Service {
	if s != nil {
		s.profileTabs = provider
	}
	return s
}

type AvatarUploader interface {
	UploadAvatar(ctx context.Context, actor identity.Actor, input attachments.UploadInput) (attachments.Attachment, error)
}

type AvatarOptionResolver interface {
	AvatarOptions(ctx context.Context) (options.AvatarOptions, error)
}

// NicknameEditor 是 Profile 对昵称域的窄依赖：策略、冷却、历史与审计都由
// Identity 实现，Profile 只负责把一次 PUT /profile 编排完。
type NicknameEditor interface {
	State(ctx context.Context, userID int64) (identity.NicknameState, error)
	Change(ctx context.Context, actor identity.Actor, displayName string) (identity.NicknameState, error)
	Availability(ctx context.Context, actor identity.Actor, value string) (identity.NicknameAvailability, error)
	PublicPreviousNames(ctx context.Context, userID int64) ([]string, error)
}

// WithNicknameEditor 注入昵称服务。未注入时昵称只读不可写，且 PUT 带
// displayName 会被拒绝——静默丢弃用户的修改比报错更糟。
func (s *Service) WithNicknameEditor(editor NicknameEditor) *Service {
	if s != nil {
		s.nicknames = editor
	}
	return s
}

type AvatarViewBuilder = avatar.ViewBuilder

func NewService(store Store) *Service {
	return &Service{store: store}
}

func NewServiceWithAvatar(store Store, uploader AvatarUploader, avatarOptions AvatarOptionResolver) *Service {
	return &Service{store: store, avatarUploader: uploader, avatarOptions: avatarOptions}
}

func NewAvatarViewBuilder(avatarOptions AvatarOptionResolver) *AvatarViewBuilder {
	return avatar.NewViewBuilder(avatarOptionResolverAdapter{inner: avatarOptions})
}

// GetPublicProfile 返回公开资料页聚合数据（匿名视角）。
//
// 保留这条签名是刻意的：PageViewModels 与 AITools 是宿主读取面，它们取到的
// 就应该是不带登录态的最宽公开视图，不该因为谁在浏览而变形。需要按访客权限
// 过滤扩展 tab 的 HTTP 入口走 GetPublicProfileForViewer。
func (s *Service) GetPublicProfile(ctx context.Context, username string) (PublicProfile, error) {
	return s.GetPublicProfileForViewer(ctx, username, identity.Actor{})
}

// GetPublicProfileForViewer 按请求者视角返回公开资料页聚合数据。
// 唯一受 viewer 影响的是扩展 tab 的可见性过滤（由 ProfileTabProvider 执行）。
func (s *Service) GetPublicProfileForViewer(ctx context.Context, username string, viewer identity.Actor) (PublicProfile, error) {
	normalized := strings.TrimSpace(username)
	if normalized == "" {
		return PublicProfile{}, ErrProfileNotFound
	}
	user, err := s.store.GetUserSummaryByUsername(ctx, normalized)
	if err != nil {
		return PublicProfile{}, err
	}
	profile, err := s.store.GetProfile(ctx, user.UserID)
	if err != nil {
		return PublicProfile{}, err
	}
	stats, err := s.store.GetProfileStats(ctx, user.UserID)
	if err != nil {
		return PublicProfile{}, err
	}
	recent, err := s.store.ListRecentTopics(ctx, user.UserID, 5)
	if err != nil {
		return PublicProfile{}, err
	}
	activities, err := s.listActivities(ctx, user.UserID, profileActivityLimit)
	if err != nil {
		return PublicProfile{}, err
	}
	profile = s.decorateProfile(ctx, user, profile)
	tabs, err := s.listProfileTabs(ctx, viewer)
	if err != nil {
		return PublicProfile{}, err
	}
	view := PublicProfile{
		UserID:        user.UserID,
		Username:      user.Username,
		DisplayName:   user.DisplayName,
		Profile:       profile,
		TopicCount:    stats.TopicCount,
		CommentCount:  stats.CommentCount,
		RecentTopics:  recent,
		Activities:    activities,
		JoinedAt:      user.JoinedAt,
		ExtensionTabs: tabs,
	}
	// 曾用名是可选公开面：运营没打开时这里什么都不返回，公开页也不会多一行。
	if s.nicknames != nil {
		if names, err := s.nicknames.PublicPreviousNames(ctx, user.UserID); err == nil {
			view.PreviousDisplayNames = names
		} else {
			slog.Warn("profile: load previous display names failed", "userId", user.UserID, "error", err)
		}
	}
	return view, nil
}

// SearchPublicUsers 按昵称/用户名检索可公开访问的活跃用户。
//
// 它是一条公开展示面的只读入口，因此三条约束在这里定死，调用方不必重复判断：
// 查询词必须非空（空查询等于「导出成员目录」）、结果只含用户名与显示名、条数有上
// 限。不可用的输入返回 ErrProfileInvalid，而不是一个空列表——空列表的含义是
// 「没有这个用户」，两者不该混。
func (s *Service) SearchPublicUsers(ctx context.Context, input SearchUsersInput) (UserSearchResult, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" || utf8.RuneCountInString(query) > UserSearchMaxQueryRunes {
		return UserSearchResult{}, ErrProfileInvalid
	}
	if s == nil || s.store == nil {
		return UserSearchResult{}, ErrProfileInvalid
	}
	limit := input.Limit
	if limit <= 0 {
		limit = UserSearchDefaultLimit
	}
	if limit > UserSearchMaxLimit {
		limit = UserSearchMaxLimit
	}
	items, err := s.store.SearchPublicUsers(ctx, query, limit)
	if err != nil {
		return UserSearchResult{}, err
	}
	if items == nil {
		items = []UserSearchHit{}
	}
	return UserSearchResult{Items: items}, nil
}

// ListPublicActivities 按 kind 分页返回公开主题或回复活动。
func (s *Service) ListPublicActivities(ctx context.Context, input ListActivitiesInput) (ActivityPage, error) {
	normalized := strings.TrimSpace(input.Username)
	if normalized == "" {
		return ActivityPage{}, ErrProfileNotFound
	}
	kind := strings.TrimSpace(strings.ToLower(input.Kind))
	if kind == "" {
		kind = ActivityKindTopic
	}
	if kind != ActivityKindTopic && kind != ActivityKindComment {
		return ActivityPage{}, ErrProfileInvalid
	}
	page, perPage := normalizeActivityPage(input.Page, input.PerPage)
	user, err := s.store.GetUserSummaryByUsername(ctx, normalized)
	if err != nil {
		return ActivityPage{}, err
	}
	stats, err := s.store.GetProfileStats(ctx, user.UserID)
	if err != nil {
		return ActivityPage{}, err
	}
	offset := (page - 1) * perPage
	var (
		items []ProfileActivity
		total int64
	)
	switch kind {
	case ActivityKindTopic:
		total = stats.TopicCount
		topics, listErr := s.store.ListActivityTopics(ctx, user.UserID, perPage, offset)
		if listErr != nil {
			return ActivityPage{}, listErr
		}
		items = make([]ProfileActivity, 0, len(topics))
		for _, topic := range topics {
			items = append(items, ProfileActivity{
				Kind:      ActivityKindTopic,
				Topic:     activityTopicFromSummary(topic),
				Excerpt:   topic.Excerpt,
				CreatedAt: topic.CreatedAt,
			})
		}
	case ActivityKindComment:
		total = stats.CommentCount
		comments, listErr := s.store.ListActivityComments(ctx, user.UserID, perPage, offset)
		if listErr != nil {
			return ActivityPage{}, listErr
		}
		items = make([]ProfileActivity, 0, len(comments))
		for _, comment := range comments {
			items = append(items, commentActivity(comment))
		}
	}
	return ActivityPage{
		Items:   items,
		Page:    page,
		PerPage: perPage,
		Total:   total,
		HasMore: int64(page*perPage) < total,
		Kind:    kind,
	}, nil
}

func normalizeActivityPage(page, perPage int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if page > profileActivityPageMaxPage {
		page = profileActivityPageMaxPage
	}
	if perPage <= 0 {
		perPage = profileActivityPageDefaultPerPage
	}
	if perPage > profileActivityPageMaxPerPage {
		perPage = profileActivityPageMaxPerPage
	}
	return page, perPage
}

func (s *Service) listActivities(ctx context.Context, userID int64, limit int) ([]ProfileActivity, error) {
	if limit <= 0 || limit > profileActivityLimit {
		limit = profileActivityLimit
	}
	topics, err := s.store.ListRecentActivityTopics(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	comments, err := s.store.ListRecentComments(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	items := make([]ProfileActivity, 0, len(topics)+len(comments))
	for _, topic := range topics {
		items = append(items, ProfileActivity{
			Kind:      ActivityKindTopic,
			Topic:     activityTopicFromSummary(topic),
			Excerpt:   topic.Excerpt,
			CreatedAt: topic.CreatedAt,
		})
	}
	for _, comment := range comments {
		items = append(items, commentActivity(comment))
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].Kind < items[j].Kind
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func activityTopicFromSummary(topic forum.TopicSummary) ProfileActivityTopic {
	return ProfileActivityTopic{
		ID:             topic.ID,
		Slug:           topic.Slug,
		Title:          topic.Title,
		Status:         topic.Status,
		CategorySlug:   topic.CategorySlug,
		CategoryName:   topic.CategoryName,
		CommentCount:   topic.CommentCount,
		CreatedAt:      topic.CreatedAt,
		UpdatedAt:      topic.UpdatedAt,
		LastActivityAt: topic.LastActivityAt,
	}
}

// commentActivity 把 store 层回复活动转为公开时间线条目，附带主题评论分页页码供深链。
func commentActivity(comment ProfileCommentActivity) ProfileActivity {
	commentID := comment.CommentID
	page := comment.CommentPage
	if page <= 0 {
		page = 1
	}
	return ProfileActivity{
		Kind:        ActivityKindComment,
		Topic:       comment.Topic,
		CommentID:   &commentID,
		CommentPage: &page,
		Excerpt:     comment.Excerpt,
		CreatedAt:   comment.CreatedAt,
	}
}

func (s *Service) listProfileTabs(ctx context.Context, viewer identity.Actor) ([]ProfileExtensionTab, error) {
	if s == nil || s.profileTabs == nil {
		return nil, nil
	}
	return s.profileTabs.ProfileTabs(ctx, viewer)
}

// GetMyProfile 读取当前用户可编辑的资料。
func (s *Service) GetMyProfile(ctx context.Context, userID int64) (PublicProfile, error) {
	if userID <= 0 {
		return PublicProfile{}, identity.ErrPermissionDenied
	}
	user, err := s.store.GetUserSummaryByID(ctx, userID)
	if err != nil {
		return PublicProfile{}, err
	}
	profile, err := s.store.GetProfile(ctx, userID)
	if err != nil {
		return PublicProfile{}, err
	}
	return s.myProfileView(ctx, user, profile), nil
}

// UpdateMyProfile 更新当前用户自己的资料。仅限当前用户操作自己。
//
// 顺序：先校验资料字段（无写入）→ 改昵称 → 写 user_profiles。昵称的策略与
// 冷却拒绝必须发生在任何写入之前，否则一次「昵称太短」会顺手把简介存了。
// 昵称已提交而资料写失败是罕见情况，重试即可；反向排序的副作用更糟。
func (s *Service) UpdateMyProfile(ctx context.Context, actor identity.Actor, input UpdateProfileInput) (PublicProfile, error) {
	if actor.ID <= 0 {
		return PublicProfile{}, identity.ErrPermissionDenied
	}
	normalized, err := normalizeUpdateProfileInput(input)
	if err != nil {
		return PublicProfile{}, err
	}
	if input.DisplayName != nil {
		if s.nicknames == nil {
			return PublicProfile{}, identity.ErrNicknameDisabled
		}
		if _, err := s.nicknames.Change(ctx, actor, *input.DisplayName); err != nil {
			return PublicProfile{}, err
		}
	}
	existing, err := s.store.GetProfile(ctx, actor.ID)
	if err != nil {
		return PublicProfile{}, err
	}
	// 合并：nil 字段保留原值。
	merged := Profile{
		UserID:             actor.ID,
		Bio:                existing.Bio,
		Signature:          existing.Signature,
		Location:           existing.Location,
		WebsiteURL:         existing.WebsiteURL,
		AvatarAttachmentID: existing.AvatarAttachmentID,
		CreatedAt:          existing.CreatedAt,
	}
	if normalized.Bio != nil {
		merged.Bio = *normalized.Bio
	}
	if normalized.Signature != nil {
		merged.Signature = *normalized.Signature
	}
	if normalized.Location != nil {
		merged.Location = *normalized.Location
	}
	if normalized.WebsiteURL != nil {
		merged.WebsiteURL = *normalized.WebsiteURL
	}
	updated, err := s.store.UpsertProfile(ctx, merged)
	if err != nil {
		return PublicProfile{}, err
	}
	if normalized.AvatarAttachmentID != nil {
		updated, err = s.store.SetAvatarAttachment(ctx, actor.ID, normalized.AvatarAttachmentID, actor.ID)
		if err != nil {
			return PublicProfile{}, err
		}
	}
	user, err := s.store.GetUserSummaryByID(ctx, actor.ID)
	if err != nil {
		return PublicProfile{}, err
	}
	return s.myProfileView(ctx, user, updated), nil
}

// CheckNicknameAvailability 是改名前的预检；未注入昵称服务时直接拒绝。
func (s *Service) CheckNicknameAvailability(ctx context.Context, actor identity.Actor, value string) (NicknameAvailability, error) {
	if s == nil || s.nicknames == nil {
		return NicknameAvailability{}, identity.ErrNicknameDisabled
	}
	return s.nicknames.Availability(ctx, actor, value)
}

// myProfileView 组装自助资料视图。昵称状态读取失败只降级隐藏字段，
// 不让整页资料因为一个策略缓存问题读不出来。
func (s *Service) myProfileView(ctx context.Context, user UserProfileSummary, profile Profile) PublicProfile {
	view := PublicProfile{
		UserID:      user.UserID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Profile:     s.decorateProfile(ctx, user, profile),
		JoinedAt:    user.JoinedAt,
	}
	if s.nicknames != nil {
		if state, err := s.nicknames.State(ctx, user.UserID); err == nil {
			view.Nickname = &state
		} else {
			slog.Warn("profile: load nickname state failed", "userId", user.UserID, "error", err)
		}
	}
	return view
}

func (s *Service) UploadAvatar(ctx context.Context, actor identity.Actor, input attachments.UploadInput) (Profile, error) {
	if actor.ID <= 0 || !actor.IsActive() {
		return Profile{}, identity.ErrPermissionDenied
	}
	if !actor.Can(identity.PermissionAttachmentUpload) {
		return Profile{}, identity.ErrPermissionDenied
	}
	avatarOptions, err := s.resolveAvatarOptions(ctx)
	if err != nil {
		return Profile{}, err
	}
	if !avatarOptions.AllowUpload {
		return Profile{}, ErrAvatarUploadDisabled
	}
	if s.avatarUploader == nil {
		return Profile{}, ErrAvatarUploadDisabled
	}
	attachment, err := s.avatarUploader.UploadAvatar(ctx, actor, input)
	if err != nil {
		return Profile{}, err
	}
	updated, err := s.store.SetAvatarAttachment(ctx, actor.ID, &attachment.ID, actor.ID)
	if err != nil {
		return Profile{}, err
	}
	user, err := s.store.GetUserSummaryByID(ctx, actor.ID)
	if err != nil {
		return updated, nil
	}
	return s.decorateProfile(ctx, user, updated), nil
}

func (s *Service) DeleteAvatar(ctx context.Context, actor identity.Actor) (Profile, error) {
	if actor.ID <= 0 || !actor.IsActive() {
		return Profile{}, identity.ErrPermissionDenied
	}
	updated, err := s.store.SetAvatarAttachment(ctx, actor.ID, nil, actor.ID)
	if err != nil {
		return Profile{}, err
	}
	user, err := s.store.GetUserSummaryByID(ctx, actor.ID)
	if err != nil {
		return updated, nil
	}
	return s.decorateProfile(ctx, user, updated), nil
}

func (s *Service) decorateProfile(ctx context.Context, user UserProfileSummary, profile Profile) Profile {
	profile.Avatar = NewAvatarViewBuilder(s.avatarOptions).AvatarView(ctx, AvatarUser{
		UserID:      user.UserID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Email:       user.Email,
	}, s.avatarSource(ctx, profile))
	return profile
}

func (s *Service) avatarSource(ctx context.Context, profile Profile) AvatarSource {
	source := AvatarSource{AttachmentID: profile.AvatarAttachmentID}
	if profile.AvatarAttachmentID == nil || *profile.AvatarAttachmentID <= 0 {
		return source
	}
	if attachment, err := s.store.GetAvatarAttachment(ctx, *profile.AvatarAttachmentID); err == nil {
		source.Attachment = &attachment
	}
	return source
}

func (s *Service) resolveAvatarOptions(ctx context.Context) (options.AvatarOptions, error) {
	if s.avatarOptions == nil {
		return defaultAvatarOptions(), nil
	}
	return s.avatarOptions.AvatarOptions(ctx)
}

type avatarOptionResolverAdapter struct {
	inner AvatarOptionResolver
}

func (r avatarOptionResolverAdapter) AvatarOptions(ctx context.Context) (avatar.Options, error) {
	if r.inner == nil {
		return avatar.DefaultOptions(), nil
	}
	resolved, err := r.inner.AvatarOptions(ctx)
	if err != nil {
		return avatar.Options{}, err
	}
	return avatarOptionsFromRuntime(resolved), nil
}

func avatarOptionsFromRuntime(input options.AvatarOptions) avatar.Options {
	return avatar.Options{
		AllowUpload:           input.AllowUpload,
		DefaultProvider:       input.DefaultProvider,
		GravatarBaseURL:       input.GravatarBaseURL,
		GravatarHashAlgorithm: input.GravatarHashAlgorithm,
		DefaultStaticURL:      input.DefaultStaticURL,
		MaxSizeKB:             input.MaxSizeKB,
		MaxDimension:          input.MaxDimension,
		AllowGIF:              input.AllowGIF,
		CompressEnabled:       input.CompressEnabled,
		TargetDimension:       input.TargetDimension,
		CompressQuality:       input.CompressQuality,
	}
}

func defaultAvatarOptions() options.AvatarOptions {
	defaults := avatar.DefaultOptions()
	return options.AvatarOptions{
		AllowUpload:           defaults.AllowUpload,
		DefaultProvider:       defaults.DefaultProvider,
		GravatarBaseURL:       defaults.GravatarBaseURL,
		GravatarHashAlgorithm: defaults.GravatarHashAlgorithm,
		DefaultStaticURL:      defaults.DefaultStaticURL,
		MaxSizeKB:             defaults.MaxSizeKB,
		MaxDimension:          defaults.MaxDimension,
		AllowGIF:              defaults.AllowGIF,
		CompressEnabled:       defaults.CompressEnabled,
		TargetDimension:       defaults.TargetDimension,
		CompressQuality:       defaults.CompressQuality,
	}
}

// normalizeUpdateProfileInput 规范化并校验输入字段。
func normalizeUpdateProfileInput(input UpdateProfileInput) (NormalizeProfile, error) {
	result := NormalizeProfile{
		Bio:                trimPtr(input.Bio, maxBioLength),
		Signature:          trimPtr(input.Signature, maxSignatureLength),
		Location:           trimPtr(input.Location, maxLocationLength),
		WebsiteURL:         trimPtr(input.WebsiteURL, maxWebsiteLength),
		AvatarAttachmentID: input.AvatarAttachmentID,
	}
	if result.Bio != nil && len(*result.Bio) > maxBioLength {
		return NormalizeProfile{}, ErrProfileInvalid
	}
	if result.Signature != nil && len(*result.Signature) > maxSignatureLength {
		return NormalizeProfile{}, ErrProfileInvalid
	}
	if result.Location != nil && len(*result.Location) > maxLocationLength {
		return NormalizeProfile{}, ErrProfileInvalid
	}
	if result.WebsiteURL != nil {
		url := *result.WebsiteURL
		if url != "" && !isValidWebsiteURL(url) {
			return NormalizeProfile{}, ErrProfileInvalid
		}
	}
	if result.AvatarAttachmentID != nil && *result.AvatarAttachmentID <= 0 {
		return NormalizeProfile{}, ErrProfileInvalid
	}
	return result, nil
}

// trimPtr 裁剪空白并校验长度上限；nil 保持 nil。
func trimPtr(value *string, _ int) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

// isValidWebsiteURL 做最简单的 URL 形态校验，避免过度校验。
func isValidWebsiteURL(value string) bool {
	if len(value) > maxWebsiteLength {
		return false
	}
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// 保留 forum 包引用，避免未来扩展公开资料时的引用丢失（近期主题等依赖 forum 类型）。
var _ = forum.TopicSummary{}
