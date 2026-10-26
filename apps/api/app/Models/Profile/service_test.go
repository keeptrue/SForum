package profile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

func TestServiceGetPublicProfileAggregatesData(t *testing.T) {
	commentID := int64(11)
	store := &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7, Bio: "hello"},
		stats:   ProfileStats{TopicCount: 3, CommentCount: 12},
		recent:  []forum.TopicSummary{{ID: 1, Title: "t1"}},
		activityTopics: []forum.TopicSummary{{
			ID: 2, Title: "new topic", Slug: "new-topic", Excerpt: "topic body", CreatedAt: time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC),
		}},
		comments: []ProfileCommentActivity{{
			CommentID:   commentID,
			CommentPage: 2,
			Topic:       ProfileActivityTopic{ID: 1, Title: "t1", Slug: "t1"},
			Excerpt:     "reply body",
			CreatedAt:   time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC),
		}},
	}
	service := NewService(store)

	result, err := service.GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	if result.UserID != 7 || result.Username != "alice" || result.DisplayName != "Alice" {
		t.Fatalf("unexpected user summary: %#v", result)
	}
	if result.Profile.Bio != "hello" {
		t.Fatalf("unexpected bio: %q", result.Profile.Bio)
	}
	if result.TopicCount != 3 || result.CommentCount != 12 {
		t.Fatalf("unexpected stats: %#v", result)
	}
	if len(result.RecentTopics) != 1 || result.RecentTopics[0].Title != "t1" {
		t.Fatalf("unexpected recent topics: %#v", result.RecentTopics)
	}
	if len(result.Activities) != 2 {
		t.Fatalf("unexpected activities: %#v", result.Activities)
	}
	if result.Activities[0].Kind != "comment" || result.Activities[0].CommentID == nil || *result.Activities[0].CommentID != commentID {
		t.Fatalf("expected newest comment activity first, got %#v", result.Activities[0])
	}
	if result.Activities[0].CommentPage == nil || *result.Activities[0].CommentPage != 2 {
		t.Fatalf("expected commentPage=2 on reply activity, got %#v", result.Activities[0].CommentPage)
	}
	if result.Activities[1].Kind != "topic" || result.Activities[1].Topic.Title != "new topic" {
		t.Fatalf("expected topic activity second, got %#v", result.Activities[1])
	}
}

func TestServiceGetPublicProfileRejectsEmptyUsername(t *testing.T) {
	service := NewService(&fakeStore{})
	_, err := service.GetPublicProfile(context.Background(), "   ")
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("expected ErrProfileNotFound, got %v", err)
	}
}

// 公开用户检索的三条约束都在服务层定死：查询词必须非空、条数有上限、结果里只有
// 用户名与显示名。这些测试固定的是「AI 工具拿到的是不是同一条公开读取路径」。
func TestServiceSearchPublicUsersBoundsLimitAndReturnsHits(t *testing.T) {
	store := &fakeStore{searchHits: []UserSearchHit{{UserID: 7, Username: "alice", DisplayName: "Alice"}}}
	service := NewService(store)

	result, err := service.SearchPublicUsers(context.Background(), SearchUsersInput{Query: "  alice  ", Limit: 999})
	if err != nil {
		t.Fatalf("SearchPublicUsers error: %v", err)
	}
	if store.searchQuery != "alice" || store.searchLimit != UserSearchMaxLimit {
		t.Fatalf("store input = %q/%d", store.searchQuery, store.searchLimit)
	}
	if len(result.Items) != 1 || result.Items[0].Username != "alice" {
		t.Fatalf("unexpected hits: %#v", result.Items)
	}
}

func TestServiceSearchPublicUsersAppliesDefaultLimit(t *testing.T) {
	store := &fakeStore{}
	if _, err := NewService(store).SearchPublicUsers(context.Background(), SearchUsersInput{Query: "alice"}); err != nil {
		t.Fatalf("SearchPublicUsers error: %v", err)
	}
	if store.searchLimit != UserSearchDefaultLimit {
		t.Fatalf("limit = %d, want the default", store.searchLimit)
	}
}

// 空查询等于「列出全站成员」，那是成员目录导出，不是检索——必须直接拒绝，而且不能
// 让空模式落到存储层。
func TestServiceSearchPublicUsersRejectsEmptyQuery(t *testing.T) {
	store := &fakeStore{}
	_, err := NewService(store).SearchPublicUsers(context.Background(), SearchUsersInput{Query: "   "})
	if !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("expected ErrProfileInvalid, got %v", err)
	}
	if store.searchQuery != "" {
		t.Fatalf("an invalid query must not reach the store: %q", store.searchQuery)
	}
}

func TestServiceSearchPublicUsersRejectsOverlongQuery(t *testing.T) {
	store := &fakeStore{}
	query := strings.Repeat("a", UserSearchMaxQueryRunes+1)
	if _, err := NewService(store).SearchPublicUsers(context.Background(), SearchUsersInput{Query: query}); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("expected ErrProfileInvalid, got %v", err)
	}
}

func TestServiceSearchPublicUsersReturnsAnEmptyListNotNil(t *testing.T) {
	result, err := NewService(&fakeStore{}).SearchPublicUsers(context.Background(), SearchUsersInput{Query: "ghost"})
	if err != nil {
		t.Fatalf("SearchPublicUsers error: %v", err)
	}
	if result.Items == nil {
		t.Fatal("a miss must serialize as an empty array, not null")
	}
}

func TestServiceUpdateMyProfileRequiresActor(t *testing.T) {
	service := NewService(&fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7},
	})
	_, err := service.UpdateMyProfile(context.Background(), identity.Actor{ID: 0}, UpdateProfileInput{})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied for no actor, got %v", err)
	}
}

func TestServiceUpdateMyProfileTrimsAndPersists(t *testing.T) {
	store := &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7, Bio: "old"},
	}
	service := NewService(store)
	bio := "  new bio  "
	website := "https://example.com"
	updated, err := service.UpdateMyProfile(context.Background(), identity.Actor{ID: 7}, UpdateProfileInput{
		Bio:        &bio,
		WebsiteURL: &website,
	})
	if err != nil {
		t.Fatalf("UpdateMyProfile error: %v", err)
	}
	if updated.Profile.Bio != "new bio" {
		t.Fatalf("expected trimmed bio, got %q", updated.Profile.Bio)
	}
	if updated.Profile.WebsiteURL != "https://example.com" {
		t.Fatalf("expected website url, got %q", updated.Profile.WebsiteURL)
	}
}

func TestServiceUpdateMyProfileRejectsInvalidWebsite(t *testing.T) {
	service := NewService(&fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice"},
		profile: Profile{UserID: 7},
	})
	bad := "not-a-url"
	_, err := service.UpdateMyProfile(context.Background(), identity.Actor{ID: 7}, UpdateProfileInput{WebsiteURL: &bad})
	if !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("expected ErrProfileInvalid, got %v", err)
	}
}

func TestServiceUpdateMyProfileCannotTargetOtherUser(t *testing.T) {
	store := &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice"},
		profile: Profile{UserID: 7},
	}
	service := NewService(store)
	// actor.ID 决定写入目标；传 actor 9 但 store 只有用户 7 的资料。
	bio := "hijack"
	_, err := service.UpdateMyProfile(context.Background(), identity.Actor{ID: 9}, UpdateProfileInput{Bio: &bio})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// upsert 入参的 UserID 应为 actor.ID（9），证明只能写自己的资料。
	if store.upserted.UserID != 9 {
		t.Fatalf("expected upsert target actor id 9, got %d", store.upserted.UserID)
	}
}

func TestServiceDecoratesDefaultInitialsAvatar(t *testing.T) {
	store := &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice", Email: "alice@example.com"},
		profile: Profile{UserID: 7},
	}
	service := NewServiceWithAvatar(store, nil, newProfileAvatarOptions(nil))

	result, err := service.GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	if result.Profile.Avatar.Kind != AvatarKindInitials || result.Profile.Avatar.URL != "" {
		t.Fatalf("expected initials avatar fallback, got %#v", result.Profile.Avatar)
	}
	if result.Profile.Avatar.Alt != "Alice" {
		t.Fatalf("expected display name alt, got %q", result.Profile.Avatar.Alt)
	}
}

func TestServiceDecoratesGravatarAvatarWithConfiguredHash(t *testing.T) {
	store := &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice", Email: " Alice@Example.COM "},
		profile: Profile{UserID: 7},
	}
	service := NewServiceWithAvatar(store, nil, newProfileAvatarOptions(map[string]string{
		options.NameAvatarDefaultProvider:       options.AvatarProviderGravatar,
		options.NameAvatarGravatarBaseURL:       "https://avatar.example.com/avatar/",
		options.NameAvatarGravatarHashAlgorithm: options.AvatarHashSHA256,
	}))

	result, err := service.GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	sum := sha256.Sum256([]byte("alice@example.com"))
	expectedURL := "https://avatar.example.com/avatar/" + hex.EncodeToString(sum[:])
	if result.Profile.Avatar.Kind != AvatarKindGravatar || result.Profile.Avatar.URL != expectedURL {
		t.Fatalf("expected gravatar URL %q, got %#v", expectedURL, result.Profile.Avatar)
	}
}

func TestAvatarBuilderPrefersUploadedAttachment(t *testing.T) {
	builder := NewAvatarViewBuilder(newProfileAvatarOptions(nil))
	avatarID := int64(88)
	view := builder.AvatarView(context.Background(), AvatarUser{
		UserID:      7,
		Username:    "alice",
		DisplayName: "Alice",
		Email:       "alice@example.com",
	}, AvatarSource{
		AttachmentID: &avatarID,
		Attachment: &AvatarAttachment{
			ID:       avatarID,
			PublicID: "avatar-public",
			Status:   attachments.StatusActive,
		},
	})
	if view.Kind != AvatarKindUploaded || view.URL != "/media/avatars/avatar-public" {
		t.Fatalf("expected uploaded avatar URL, got %#v", view)
	}
	if view.AttachmentID == nil || *view.AttachmentID != avatarID {
		t.Fatalf("expected uploaded avatar attachment id %d, got %#v", avatarID, view.AttachmentID)
	}
	if view.Alt != "Alice" {
		t.Fatalf("expected display-name alt, got %q", view.Alt)
	}
}

func TestServiceUploadAvatarRequiresPermissionAndAvatarSwitch(t *testing.T) {
	store := &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7},
	}
	uploader := &fakeAvatarUploader{attachment: attachments.Attachment{ID: 88, PublicID: "avatar-public"}}
	service := NewServiceWithAvatar(store, uploader, newProfileAvatarOptions(nil))

	_, err := service.UploadAvatar(context.Background(), identity.Actor{ID: 7, Status: identity.UserStatusActive}, attachments.UploadInput{
		OriginalName: "avatar.jpg",
		ContentType:  "image/jpeg",
		SizeBytes:    4,
		File:         newProfileReadSeekCloser("data"),
	})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied without attachment.upload, got %v", err)
	}
	if uploader.called {
		t.Fatalf("uploader should not run when actor lacks permission")
	}

	disabled := NewServiceWithAvatar(store, uploader, newProfileAvatarOptions(map[string]string{
		options.NameAvatarAllowUpload: "disabled",
	}))
	_, err = disabled.UploadAvatar(context.Background(), uploadProfileActor(), attachments.UploadInput{
		OriginalName: "avatar.jpg",
		ContentType:  "image/jpeg",
		SizeBytes:    4,
		File:         newProfileReadSeekCloser("data"),
	})
	if !errors.Is(err, ErrAvatarUploadDisabled) {
		t.Fatalf("expected avatar upload disabled, got %v", err)
	}
}

func TestServiceUploadAvatarSetsProfileReference(t *testing.T) {
	avatarID := int64(88)
	store := &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7},
		avatarAttachment: AvatarAttachment{
			ID:          avatarID,
			PublicID:    "avatar-public",
			OwnerUserID: 7,
			ContentType: "image/jpeg",
			Status:      "active",
		},
	}
	uploader := &fakeAvatarUploader{attachment: attachments.Attachment{ID: avatarID, PublicID: "avatar-public"}}
	service := NewServiceWithAvatar(store, uploader, newProfileAvatarOptions(nil))

	updated, err := service.UploadAvatar(context.Background(), uploadProfileActor(), attachments.UploadInput{
		OriginalName: "avatar.jpg",
		ContentType:  "image/jpeg",
		SizeBytes:    4,
		File:         newProfileReadSeekCloser("data"),
	})
	if err != nil {
		t.Fatalf("UploadAvatar returned error: %v", err)
	}
	if store.setAvatarAttachmentID == nil || *store.setAvatarAttachmentID != avatarID || store.setAvatarActorID != 7 {
		t.Fatalf("expected avatar reference for attachment 88 by actor 7, got id=%v actor=%d", store.setAvatarAttachmentID, store.setAvatarActorID)
	}
	if updated.Avatar.Kind != AvatarKindUploaded || updated.Avatar.AttachmentID == nil || *updated.Avatar.AttachmentID != avatarID {
		t.Fatalf("expected uploaded avatar view, got %#v", updated.Avatar)
	}
}

func uploadProfileActor() identity.Actor {
	return identity.Actor{
		ID:          7,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentUpload: true},
	}
}

type fakeStore struct {
	user                  UserProfileSummary
	profile               Profile
	stats                 ProfileStats
	recent                []forum.TopicSummary
	activityTopics        []forum.TopicSummary
	comments              []ProfileCommentActivity
	searchHits            []UserSearchHit
	searchQuery           string
	searchLimit           int
	upserted              Profile
	avatarAttachment      AvatarAttachment
	setAvatarAttachmentID *int64
	setAvatarActorID      int64
	err                   error
}

func (s *fakeStore) GetProfile(context.Context, int64) (Profile, error) {
	return s.profile, nil
}

func (s *fakeStore) UpsertProfile(_ context.Context, input Profile) (Profile, error) {
	s.upserted = input
	return input, nil
}

func (s *fakeStore) SetAvatarAttachment(_ context.Context, userID int64, attachmentID *int64, actorUserID int64) (Profile, error) {
	s.setAvatarAttachmentID = attachmentID
	s.setAvatarActorID = actorUserID
	s.profile.UserID = userID
	s.profile.AvatarAttachmentID = attachmentID
	return s.profile, nil
}

func (s *fakeStore) GetAvatarAttachment(_ context.Context, attachmentID int64) (AvatarAttachment, error) {
	if s.avatarAttachment.ID == attachmentID {
		return s.avatarAttachment, nil
	}
	return AvatarAttachment{}, ErrProfileInvalid
}

func (s *fakeStore) GetUserSummaryByUsername(context.Context, string) (UserProfileSummary, error) {
	if s.err != nil {
		return UserProfileSummary{}, s.err
	}
	return s.user, nil
}

func (s *fakeStore) GetUserSummaryByID(context.Context, int64) (UserProfileSummary, error) {
	return s.user, nil
}

func (s *fakeStore) GetProfileStats(context.Context, int64) (ProfileStats, error) {
	return s.stats, nil
}

func (s *fakeStore) SearchPublicUsers(_ context.Context, query string, limit int) ([]UserSearchHit, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.searchQuery = query
	s.searchLimit = limit
	return s.searchHits, nil
}

func (s *fakeStore) ListRecentTopics(context.Context, int64, int) ([]forum.TopicSummary, error) {
	return s.recent, nil
}

func (s *fakeStore) ListRecentActivityTopics(context.Context, int64, int) ([]forum.TopicSummary, error) {
	return s.activityTopics, nil
}

func (s *fakeStore) ListActivityTopics(_ context.Context, _ int64, limit, offset int) ([]forum.TopicSummary, error) {
	return slicePage(s.activityTopics, limit, offset), nil
}

func (s *fakeStore) ListRecentComments(context.Context, int64, int) ([]ProfileCommentActivity, error) {
	return s.comments, nil
}

func (s *fakeStore) ListActivityComments(_ context.Context, _ int64, limit, offset int) ([]ProfileCommentActivity, error) {
	return slicePage(s.comments, limit, offset), nil
}

func slicePage[T any](items []T, limit, offset int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if limit <= 0 || end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

func TestServiceListPublicActivitiesPaginatesByKind(t *testing.T) {
	topics := make([]forum.TopicSummary, 0, 25)
	for i := 1; i <= 25; i++ {
		topics = append(topics, forum.TopicSummary{
			ID:        int64(i),
			Title:     "topic",
			Slug:      "t",
			CreatedAt: time.Date(2026, 7, 23, 0, 0, i, 0, time.UTC),
		})
	}
	store := &fakeStore{
		user:           UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile:        Profile{UserID: 7},
		stats:          ProfileStats{TopicCount: 25, CommentCount: 2},
		activityTopics: topics,
		comments: []ProfileCommentActivity{
			{CommentID: 1, CommentPage: 1, Topic: ProfileActivityTopic{ID: 1, Title: "t1"}, CreatedAt: time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC)},
			{CommentID: 2, CommentPage: 3, Topic: ProfileActivityTopic{ID: 2, Title: "t2"}, CreatedAt: time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)},
		},
	}
	service := NewService(store)

	page1, err := service.ListPublicActivities(context.Background(), ListActivitiesInput{
		Username: "alice",
		Kind:     ActivityKindTopic,
		Page:     1,
		PerPage:  10,
	})
	if err != nil {
		t.Fatalf("ListPublicActivities page1: %v", err)
	}
	if page1.Page != 1 || page1.PerPage != 10 || page1.Total != 25 || !page1.HasMore || len(page1.Items) != 10 {
		t.Fatalf("unexpected page1: %#v", page1)
	}
	if page1.Items[0].Kind != ActivityKindTopic || page1.Items[0].Topic.ID != 1 {
		t.Fatalf("unexpected first item: %#v", page1.Items[0])
	}

	page3, err := service.ListPublicActivities(context.Background(), ListActivitiesInput{
		Username: "alice",
		Kind:     ActivityKindTopic,
		Page:     3,
		PerPage:  10,
	})
	if err != nil {
		t.Fatalf("ListPublicActivities page3: %v", err)
	}
	if page3.HasMore || len(page3.Items) != 5 || page3.Items[0].Topic.ID != 21 {
		t.Fatalf("unexpected page3: %#v", page3)
	}

	comments, err := service.ListPublicActivities(context.Background(), ListActivitiesInput{
		Username: "alice",
		Kind:     ActivityKindComment,
		Page:     1,
		PerPage:  20,
	})
	if err != nil {
		t.Fatalf("ListPublicActivities comments: %v", err)
	}
	if comments.Total != 2 || comments.HasMore || len(comments.Items) != 2 || comments.Items[0].Kind != ActivityKindComment {
		t.Fatalf("unexpected comments page: %#v", comments)
	}
	if comments.Items[0].CommentPage == nil || *comments.Items[0].CommentPage != 1 {
		t.Fatalf("expected first comment page 1, got %#v", comments.Items[0].CommentPage)
	}
	if comments.Items[1].CommentPage == nil || *comments.Items[1].CommentPage != 3 {
		t.Fatalf("expected second comment page 3, got %#v", comments.Items[1].CommentPage)
	}

	_, err = service.ListPublicActivities(context.Background(), ListActivitiesInput{
		Username: "alice",
		Kind:     "likes",
	})
	if !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("expected ErrProfileInvalid for bad kind, got %v", err)
	}
}

type fakeAvatarUploader struct {
	called     bool
	attachment attachments.Attachment
	err        error
}

func (u *fakeAvatarUploader) UploadAvatar(_ context.Context, _ identity.Actor, _ attachments.UploadInput) (attachments.Attachment, error) {
	u.called = true
	if u.err != nil {
		return attachments.Attachment{}, u.err
	}
	return u.attachment, nil
}

type profileReadSeekCloser struct {
	*strings.Reader
}

func newProfileReadSeekCloser(value string) *profileReadSeekCloser {
	return &profileReadSeekCloser{Reader: strings.NewReader(value)}
}

func (r *profileReadSeekCloser) Close() error { return nil }

type fakeProfileOptionStore struct {
	items map[string]string
}

func newProfileAvatarOptions(values map[string]string) *options.Service {
	return options.NewServiceWithCacheTTL(&fakeProfileOptionStore{items: values}, time.Minute)
}

func (s *fakeProfileOptionStore) List(context.Context) ([]options.Option, error) {
	items := make([]options.Option, 0, len(s.items))
	for name, value := range s.items {
		items = append(items, options.Option{Name: name, Value: value})
	}
	return items, nil
}

func (s *fakeProfileOptionStore) InsertMissing(_ context.Context, input options.UpdateInput) error {
	if s.items == nil {
		s.items = map[string]string{}
	}
	if _, ok := s.items[input.Name]; !ok {
		s.items[input.Name] = input.Value
	}
	return nil
}

func (s *fakeProfileOptionStore) Upsert(_ context.Context, input options.UpdateInput) (options.Option, error) {
	if s.items == nil {
		s.items = map[string]string{}
	}
	s.items[input.Name] = input.Value
	return options.Option{Name: input.Name, Value: input.Value}, nil
}

type fakeNicknameEditor struct {
	state        identity.NicknameState
	previous     []string
	availability identity.NicknameAvailability
	changes      []string
	err          error
}

func (f *fakeNicknameEditor) State(context.Context, int64) (identity.NicknameState, error) {
	return f.state, f.err
}

func (f *fakeNicknameEditor) Change(_ context.Context, _ identity.Actor, displayName string) (identity.NicknameState, error) {
	f.changes = append(f.changes, displayName)
	if f.err != nil {
		return identity.NicknameState{}, f.err
	}
	return f.state, nil
}

func (f *fakeNicknameEditor) Availability(context.Context, identity.Actor, string) (identity.NicknameAvailability, error) {
	return f.availability, f.err
}

func (f *fakeNicknameEditor) PublicPreviousNames(context.Context, int64) ([]string, error) {
	return f.previous, f.err
}

func newNicknameProfileService(editor *fakeNicknameEditor) (*Service, *fakeStore) {
	store := &fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7},
	}
	return NewService(store).WithNicknameEditor(editor), store
}

// 未注入昵称服务时必须拒绝写入，而不是静默丢弃用户的修改。
func TestServiceUpdateMyProfileRejectsNicknameWithoutEditor(t *testing.T) {
	service := NewService(&fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7},
	})
	name := "Alicia"
	_, err := service.UpdateMyProfile(context.Background(), identity.Actor{ID: 7}, UpdateProfileInput{DisplayName: &name})
	if !errors.Is(err, identity.ErrNicknameDisabled) {
		t.Fatalf("expected nickname disabled, got %v", err)
	}
}

func TestServiceUpdateMyProfileChangesNicknameAndReturnsState(t *testing.T) {
	editor := &fakeNicknameEditor{state: identity.NicknameState{DisplayName: "Alicia", CanChange: false}}
	service, _ := newNicknameProfileService(editor)

	name := "Alicia"
	updated, err := service.UpdateMyProfile(context.Background(), identity.Actor{ID: 7}, UpdateProfileInput{DisplayName: &name})
	if err != nil {
		t.Fatalf("UpdateMyProfile error: %v", err)
	}
	if len(editor.changes) != 1 || editor.changes[0] != "Alicia" {
		t.Fatalf("nickname changes = %v", editor.changes)
	}
	if updated.Nickname == nil || updated.Nickname.DisplayName != "Alicia" {
		t.Fatalf("nickname state = %+v", updated.Nickname)
	}
}

func TestServiceGetMyProfileIncludesNicknameState(t *testing.T) {
	editor := &fakeNicknameEditor{state: identity.NicknameState{DisplayName: "Alice", CanChange: true, Duplicates: 2}}
	service, _ := newNicknameProfileService(editor)

	result, err := service.GetMyProfile(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetMyProfile error: %v", err)
	}
	if result.Nickname == nil || result.Nickname.Duplicates != 2 || !result.Nickname.CanChange {
		t.Fatalf("nickname state = %+v", result.Nickname)
	}
}

// 公开资料页只在昵称服务返回曾用名时才带这个字段。
func TestServicePublicProfileIncludesPreviousDisplayNames(t *testing.T) {
	editor := &fakeNicknameEditor{previous: []string{"Old Alice"}}
	service, _ := newNicknameProfileService(editor)

	result, err := service.GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	if len(result.PreviousDisplayNames) != 1 || result.PreviousDisplayNames[0] != "Old Alice" {
		t.Fatalf("previous display names = %v", result.PreviousDisplayNames)
	}
}

func TestServiceCheckNicknameAvailability(t *testing.T) {
	editor := &fakeNicknameEditor{availability: identity.NicknameAvailability{Available: true, Duplicates: 3}}
	service, _ := newNicknameProfileService(editor)

	result, err := service.CheckNicknameAvailability(context.Background(), identity.Actor{ID: 7}, "Alicia")
	if err != nil {
		t.Fatalf("CheckNicknameAvailability error: %v", err)
	}
	if !result.Available || result.Duplicates != 3 {
		t.Fatalf("availability = %+v", result)
	}
}

// profileTabProviderStub 记录 Service 传下来的 viewer，并按接口约定过滤：
// tab.Permission == "" || viewer.Can(tab.Permission)。
// 过滤规则本身由 providers 包针对真实 provider 覆盖；这里的职责是证明 Service
// 确实把请求者视角交到了 provider 手上（而不是永远传零值）。
type profileTabProviderStub struct {
	tabs   []ProfileExtensionTab
	viewer identity.Actor
	calls  int
	err    error
}

func (p *profileTabProviderStub) ProfileTabs(_ context.Context, viewer identity.Actor) ([]ProfileExtensionTab, error) {
	p.calls++
	p.viewer = viewer
	if p.err != nil {
		return nil, p.err
	}
	visible := make([]ProfileExtensionTab, 0, len(p.tabs))
	for _, tab := range p.tabs {
		if tab.Permission == "" || viewer.Can(tab.Permission) {
			visible = append(visible, tab)
		}
	}
	return visible, nil
}

func newProfileTabTestService(provider *profileTabProviderStub) *Service {
	return NewService(&fakeStore{
		user:    UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: Profile{UserID: 7},
	}).WithProfileTabs(provider)
}

func gatedProfileTabs() []ProfileExtensionTab {
	return []ProfileExtensionTab{
		{ID: "public", Kind: "hostLink", URL: "/tags"},
		{ID: "gated", Kind: "hostLink", URL: "/moderation", Permission: "demo.moderate"},
		{ID: "widget", Kind: "widget", ComponentID: "demo.stats"},
	}
}

// GetPublicProfile 的签名保留是有语义的：宿主读取面（PageViewModels / AITools）
// 永远拿匿名视角，扩展 tab 过滤不能因为「谁在读」而漂移。
func TestServiceGetPublicProfileKeepsAnonymousViewer(t *testing.T) {
	provider := &profileTabProviderStub{tabs: gatedProfileTabs()}
	service := newProfileTabTestService(provider)

	result, err := service.GetPublicProfile(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetPublicProfile error: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if provider.viewer.ID != 0 || provider.viewer.Status != "" || len(provider.viewer.Permissions) != 0 {
		t.Fatalf("GetPublicProfile must pass a zero-value actor, got %#v", provider.viewer)
	}
	if len(result.ExtensionTabs) != 2 || result.ExtensionTabs[0].ID != "public" || result.ExtensionTabs[1].ID != "widget" {
		t.Fatalf("anonymous tabs = %#v, want public + widget", result.ExtensionTabs)
	}
}

func TestServiceGetPublicProfileForViewerFiltersTabsByPermission(t *testing.T) {
	provider := &profileTabProviderStub{tabs: gatedProfileTabs()}
	service := newProfileTabTestService(provider)

	anonymous, err := service.GetPublicProfileForViewer(context.Background(), "alice", identity.Actor{})
	if err != nil {
		t.Fatalf("anonymous GetPublicProfileForViewer: %v", err)
	}
	if len(anonymous.ExtensionTabs) != 2 {
		t.Fatalf("anonymous tabs = %#v, want the two ungated tabs", anonymous.ExtensionTabs)
	}

	// 活跃但缺权限键：受限 tab 不出现。
	member := identity.Actor{ID: 5, Status: identity.UserStatusActive, Permissions: map[string]bool{"other.permission": true}}
	memberProfile, err := service.GetPublicProfileForViewer(context.Background(), "alice", member)
	if err != nil {
		t.Fatalf("member GetPublicProfileForViewer: %v", err)
	}
	if len(memberProfile.ExtensionTabs) != 2 {
		t.Fatalf("member tabs = %#v, want the two ungated tabs", memberProfile.ExtensionTabs)
	}

	// 有权限键：受限 tab 出现，且 viewer 原样到达 provider。
	moderator := identity.Actor{ID: 6, Status: identity.UserStatusActive, Permissions: map[string]bool{"demo.moderate": true}}
	moderatorProfile, err := service.GetPublicProfileForViewer(context.Background(), "alice", moderator)
	if err != nil {
		t.Fatalf("moderator GetPublicProfileForViewer: %v", err)
	}
	if len(moderatorProfile.ExtensionTabs) != 3 || moderatorProfile.ExtensionTabs[1].ID != "gated" {
		t.Fatalf("moderator tabs = %#v, want gated tab included", moderatorProfile.ExtensionTabs)
	}
	if provider.viewer.ID != moderator.ID || !provider.viewer.Can("demo.moderate") {
		t.Fatalf("provider received viewer %#v, want moderator %#v", provider.viewer, moderator)
	}
}

func TestServiceGetPublicProfilePropagatesTabProviderError(t *testing.T) {
	expected := errors.New("contributions unavailable")
	service := newProfileTabTestService(&profileTabProviderStub{err: expected})

	if _, err := service.GetPublicProfileForViewer(context.Background(), "alice", identity.Actor{}); !errors.Is(err, expected) {
		t.Fatalf("expected provider error, got %v", err)
	}
}
