package forum

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

func TestForumUserSummaryCarriesAvatarView(t *testing.T) {
	user := UserSummary{
		ID:          7,
		Username:    "alice",
		DisplayName: "Alice",
		Avatar:      avatar.View{Kind: avatar.KindInitials, Alt: "Alice"},
	}
	if user.Avatar.Kind != avatar.KindInitials || user.Avatar.Alt != "Alice" {
		t.Fatalf("expected avatar view on forum user summary, got %#v", user.Avatar)
	}
}

func TestServiceCreateTopicRendersSharedPostContent(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}

	topic, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		CategorySlug: "general",
		Title:        "  第一篇帖子  ",
		Content: ContentInput{
			RawContent:   "正文 **加粗** <script>alert(1)</script>",
			SourceFormat: SourceFormatMarkdown,
			EditorType:   EditorTypeMarkdown,
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}

	if topic.ID == 0 || topic.Content.ID == 0 {
		t.Fatalf("expected topic and shared post content ids, got %#v", topic)
	}
	if store.createdTopic.AuthorUserID != actor.ID {
		t.Fatalf("expected author %d, got %d", actor.ID, store.createdTopic.AuthorUserID)
	}
	if store.createdTopic.Content.RawContent != "正文 **加粗** <script>alert(1)</script>" {
		t.Fatalf("expected raw content to be preserved, got %q", store.createdTopic.Content.RawContent)
	}
	if store.createdTopic.Content.HTMLContent == "" || containsUnsafeHTML(store.createdTopic.Content.HTMLContent) {
		t.Fatalf("expected safe rendered HTML, got %q", store.createdTopic.Content.HTMLContent)
	}
	if store.createdTopic.Slug == "" {
		t.Fatal("expected generated slug")
	}
}

// 验证 slug 全局唯一化：当目标 slug 已被占用时，自动追加 -2/-3 后缀。
func TestServiceCreateTopicDeduplicatesSlugOnCollision(t *testing.T) {
	store := newServiceFakeStore()
	// 预置占用：hello-world 与 hello-world-2 均已存在，期望最终得到 hello-world-3。
	store.existingSlugs = map[string]bool{"hello-world": true, "hello-world-2": true}
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}

	topic, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		CategorySlug: "general",
		Title:        "Hello World",
		Content: ContentInput{
			RawContent:   "正文",
			SourceFormat: SourceFormatMarkdown,
			EditorType:   EditorTypeMarkdown,
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}
	if topic.Slug != "hello-world-3" {
		t.Fatalf("expected deduplicated slug hello-world-3, got %q", topic.Slug)
	}

	// 无冲突时直接使用原始 slug。
	store2 := newServiceFakeStore()
	service2 := NewService(ServiceConfig{Store: store2})
	topic2, err := service2.CreateTopic(context.Background(), actor, CreateTopicInput{
		CategorySlug: "general",
		Title:        "Hello World",
		Content:      ContentInput{RawContent: "正文", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}
	if topic2.Slug != "hello-world" {
		t.Fatalf("expected slug hello-world when no collision, got %q", topic2.Slug)
	}
}

func TestServiceCreateTopicAppliesBeforeCreateFilterAndEmitsCreatedEvent(t *testing.T) {
	store := newServiceFakeStore()
	publisher := &fakeEventPublisher{results: map[string]appevents.Result{
		appevents.TopicBeforeCreate: {
			OK: true,
			Patch: map[string]any{
				"title":        "Patched title",
				"categorySlug": "general",
			},
		},
	}}
	service := NewService(ServiceConfig{Store: store, Publisher: publisher})
	actor := identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}

	topic, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		Title: "Original title",
		Content: ContentInput{
			RawContent:   "正文",
			SourceFormat: SourceFormatMarkdown,
			EditorType:   EditorTypeMarkdown,
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}
	if topic.Title != "Patched title" || store.createdTopic.CategorySlug != "general" {
		t.Fatalf("expected patched topic input, got topic=%#v record=%#v", topic, store.createdTopic)
	}
	if !publisher.seen(appevents.TopicBeforeCreate) || !publisher.seen(appevents.TopicCreated) {
		t.Fatalf("expected before/create events, got %#v", publisher.names)
	}
}

func TestServiceCreateTopicUsesConfiguredDefaultCategory(t *testing.T) {
	store := newServiceFakeStore()
	settings := testForumSettings()
	settings.DefaultCategorySlug = "support"
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	actor := topicCreator()

	_, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		Title:   "默认分类",
		Content: validMarkdownContent("正文"),
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}
	if store.createdTopic.CategorySlug != "support" {
		t.Fatalf("expected configured default category, got %q", store.createdTopic.CategorySlug)
	}
}

func TestServiceCreateTopicNormalizesAndDeduplicatesTagSlugs(t *testing.T) {
	store := newServiceFakeStore()
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})
	actor := topicCreator()

	_, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		CategorySlug: "general",
		Title:        "标签规范化",
		TagSlugs:     []string{" Go ", "go", "Nuxt-UI", "", "nuxt-ui"},
		Content:      validMarkdownContent("正文"),
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}
	if !stringSlicesEqual(store.createdTopic.TagSlugs, []string{"go", "nuxt-ui"}) {
		t.Fatalf("expected normalized tag slugs on create record, got %#v", store.createdTopic.TagSlugs)
	}
	if store.createdTopic.TagCreationMode != TagCreationModeControlled {
		t.Fatalf("expected tag creation mode on create record, got %q", store.createdTopic.TagCreationMode)
	}
	if store.resolveTagsCalled {
		t.Fatal("service should defer tag resolution to the store create transaction")
	}
}

func TestServiceCreateTopicAllowsChineseTagSlugs(t *testing.T) {
	store := newServiceFakeStore()
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})
	actor := topicCreator()

	_, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		CategorySlug: "general",
		Title:        "中文标签",
		TagSlugs:     []string{" 中文标签 ", "中文标签", "Nuxt-UI"},
		Content:      validMarkdownContent("正文"),
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}
	if !stringSlicesEqual(store.createdTopic.TagSlugs, []string{"中文标签", "nuxt-ui"}) {
		t.Fatalf("expected normalized Chinese tag slugs on create record, got %#v", store.createdTopic.TagSlugs)
	}
}

func TestServiceCreateTopicControlledModeRejectsUnknownTags(t *testing.T) {
	store := newServiceFakeStore()
	store.resolveTagsErr = ErrInvalidTag
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})
	actor := topicCreator()

	_, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		CategorySlug: "general",
		Title:        "受控标签",
		TagSlugs:     []string{"unknown"},
		Content:      validMarkdownContent("正文"),
	})
	if !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("expected ErrInvalidTag, got %v", err)
	}
	if store.createdTopic.Title != "" {
		t.Fatalf("topic should not be created after invalid tag, got %#v", store.createdTopic)
	}
}

func TestServiceCreateTopicAllowsReviewAndOpenTagCreationModes(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		status string
	}{
		{name: "review", mode: TagCreationModeReview, status: TagStatusPending},
		{name: "open", mode: TagCreationModeOpen, status: TagStatusActive},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newServiceFakeStore()
			store.resolvedTags = []TopicTagSummary{{ID: 2, Slug: "new-tag", Name: "new tag", Status: tc.status}}
			settings := testForumSettings()
			settings.TagCreationMode = tc.mode
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})

			topic, err := service.CreateTopic(context.Background(), topicCreator(), CreateTopicInput{
				CategorySlug: "general",
				Title:        "标签模式",
				TagSlugs:     []string{"new-tag"},
				Content:      validMarkdownContent("正文"),
			})
			if err != nil {
				t.Fatalf("CreateTopic returned error: %v", err)
			}
			if store.createdTopic.TagCreationMode != tc.mode {
				t.Fatalf("expected creation mode %q on create record, got %q", tc.mode, store.createdTopic.TagCreationMode)
			}
			if len(topic.Tags) != 1 || topic.Tags[0].Status != tc.status {
				t.Fatalf("expected resolved tag status %q, got %#v", tc.status, topic.Tags)
			}
		})
	}
}

func TestServiceCreateTopicRejectsTooManyTags(t *testing.T) {
	settings := testForumSettings()
	settings.TagMaxPerTopic = 1
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})

	_, err := service.CreateTopic(context.Background(), topicCreator(), CreateTopicInput{
		CategorySlug: "general",
		Title:        "太多标签",
		TagSlugs:     []string{"one", "two"},
		Content:      validMarkdownContent("正文"),
	})
	if !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("expected ErrInvalidTag, got %v", err)
	}
	if store.resolveTagsCalled {
		t.Fatal("tag resolution should not run after max tag validation fails")
	}
}

func TestServiceForumPaginationSettingsValidationAndPermission(t *testing.T) {
	settings := testForumSettings()
	manager := &fakeSettingsManager{settings: settings}
	service := NewService(ServiceConfig{Store: newServiceFakeStore(), Settings: manager, Publisher: nil})
	categoryActor := identity.Actor{Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionCategoryManage: true}}
	settingsActor := identity.Actor{Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsManage: true}}

	topicsPerPage := 30
	if _, err := service.UpdateForumSettings(context.Background(), categoryActor, UpdateForumSettingsInput{TopicsPerPage: &topicsPerPage}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("category manager pagination update error = %v, want permission denied", err)
	}
	if _, err := service.UpdateForumSettings(context.Background(), settingsActor, UpdateForumSettingsInput{TopicsPerPage: &topicsPerPage}); err != nil {
		t.Fatalf("settings manager pagination update: %v", err)
	}
	if manager.updated.TopicsPerPage == nil || *manager.updated.TopicsPerPage != 30 {
		t.Fatalf("updated topicsPerPage = %#v", manager.updated.TopicsPerPage)
	}

	for _, value := range []int{0, 101} {
		if _, err := service.UpdateForumSettings(context.Background(), settingsActor, UpdateForumSettingsInput{CommentsPerPage: &value}); !errors.Is(err, ErrInvalidSettings) {
			t.Fatalf("commentsPerPage=%d error = %v, want invalid settings", value, err)
		}
	}
	if _, err := service.ResetForumSettings(context.Background(), settingsActor); err != nil {
		t.Fatalf("settings manager reset: %v", err)
	}
}

func TestServiceUsesConfiguredPaginationDefaultsAndExplicitOverrides(t *testing.T) {
	settings := testForumSettings()
	settings.TopicsPerPage = 30
	settings.CommentsPerPage = 40
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})

	if _, err := service.ListTopics(context.Background(), TopicListInput{}); err != nil {
		t.Fatalf("ListTopics default: %v", err)
	}
	if store.listTopicsInput.PerPage != 30 {
		t.Fatalf("topic perPage = %d, want 30", store.listTopicsInput.PerPage)
	}
	if _, err := service.ListTopics(context.Background(), TopicListInput{PerPage: 12}); err != nil {
		t.Fatalf("ListTopics explicit: %v", err)
	}
	if store.listTopicsInput.PerPage != 12 {
		t.Fatalf("explicit topic perPage = %d, want 12", store.listTopicsInput.PerPage)
	}

	if _, err := service.ListComments(context.Background(), CommentListInput{TopicID: 10}); err != nil {
		t.Fatalf("ListComments default: %v", err)
	}
	if store.listCommentsInput.PerPage != 40 {
		t.Fatalf("comment perPage = %d, want 40", store.listCommentsInput.PerPage)
	}
	if _, err := service.ListComments(context.Background(), CommentListInput{TopicID: 10, PerPage: 150}); err != nil {
		t.Fatalf("ListComments explicit: %v", err)
	}
	if store.listCommentsInput.PerPage != 100 {
		t.Fatalf("clamped comment perPage = %d, want 100", store.listCommentsInput.PerPage)
	}
}

func TestServiceCreateTopicBeforeCreateCanPatchTagSlugs(t *testing.T) {
	store := newServiceFakeStore()
	publisher := &fakeEventPublisher{results: map[string]appevents.Result{
		appevents.TopicBeforeCreate: {
			OK:    true,
			Patch: map[string]any{"tagSlugs": []string{"patched-tag"}},
		},
	}}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})

	_, err := service.CreateTopic(context.Background(), topicCreator(), CreateTopicInput{
		CategorySlug: "general",
		Title:        "事件补丁",
		TagSlugs:     []string{"original"},
		Content:      validMarkdownContent("正文"),
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}
	if !stringSlicesEqual(store.createdTopic.TagSlugs, []string{"patched-tag"}) {
		t.Fatalf("expected patched tag slugs, got %#v", store.createdTopic.TagSlugs)
	}
	if store.resolveTagsCalled {
		t.Fatal("service should not resolve patched tag slugs before CreateTopic")
	}
}

func TestServiceCreateTopicCreatedEventIncludesTagSlugs(t *testing.T) {
	store := newServiceFakeStore()
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})

	_, err := service.CreateTopic(context.Background(), topicCreator(), CreateTopicInput{
		CategorySlug: "general",
		Title:        "创建事件",
		TagSlugs:     []string{"go", "nuxt"},
		Content:      validMarkdownContent("正文"),
	})
	if err != nil {
		t.Fatalf("CreateTopic returned error: %v", err)
	}
	envelope, ok := publisher.envelope(appevents.TopicCreated)
	if !ok {
		t.Fatalf("expected topic.created event, got %#v", publisher.names)
	}
	payload, ok := envelope.Payload["tagSlugs"].([]string)
	if !ok || !stringSlicesEqual(payload, []string{"go", "nuxt"}) {
		t.Fatalf("expected tag slugs in created event, got %#v", envelope.Payload["tagSlugs"])
	}
}

func TestServiceNormalizesCategoryIconColor(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionCategoryManage: true},
	}

	created, err := service.CreateCategory(context.Background(), actor, CreateCategoryInput{
		GroupID:     1,
		Slug:        " General ",
		Name:        " 综合讨论 ",
		Visibility:  "public",
		DefaultSort: "latest",
		Icon:        " I-Tabler-Message-Circle ",
		IconColor:   " #0F766E ",
	})
	if err != nil {
		t.Fatalf("CreateCategory returned error: %v", err)
	}
	if created.Icon != "i-tabler-message-circle" || created.IconColor != "#0f766e" {
		t.Fatalf("expected normalized category visual fields, got icon=%q color=%q", created.Icon, created.IconColor)
	}
	if store.createdCategory.Icon != "i-tabler-message-circle" || store.createdCategory.IconColor != "#0f766e" {
		t.Fatalf("expected normalized visual fields passed to store, got %#v", store.createdCategory)
	}

	icon := " i-lucide-folder-open "
	color := ""
	updated, err := service.UpdateCategory(context.Background(), actor, UpdateCategoryInput{
		ID:        1,
		Icon:      &icon,
		IconColor: &color,
	})
	if err != nil {
		t.Fatalf("UpdateCategory returned error: %v", err)
	}
	if updated.Icon != "i-lucide-folder-open" || updated.IconColor != "" {
		t.Fatalf("expected update to normalize and clear visual fields, got icon=%q color=%q", updated.Icon, updated.IconColor)
	}
}

func TestServiceRejectsInvalidCategoryIconColor(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionCategoryManage: true},
	}

	_, err := service.CreateCategory(context.Background(), actor, CreateCategoryInput{
		GroupID:     1,
		Slug:        "general",
		Name:        "综合讨论",
		Visibility:  "public",
		DefaultSort: "latest",
		Icon:        "javascript:alert",
	})
	if !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("expected ErrInvalidTopic for invalid category icon, got %v", err)
	}

	badColor := "teal"
	_, err = service.UpdateCategory(context.Background(), actor, UpdateCategoryInput{
		ID:        1,
		IconColor: &badColor,
	})
	if !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("expected ErrInvalidTopic for invalid category color, got %v", err)
	}
}

func TestServiceNormalizesTagIconColor(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTagManage: true},
	}

	created, err := service.CreateTag(context.Background(), actor, CreateTagInput{
		Slug:      " Go ",
		Name:      " Go ",
		Status:    TagStatusActive,
		Icon:      " I-Lucide-Tag ",
		IconColor: " #2563EB ",
	})
	if err != nil {
		t.Fatalf("CreateTag returned error: %v", err)
	}
	if created.Icon != "i-lucide-tag" || created.IconColor != "#2563eb" {
		t.Fatalf("expected normalized tag visual fields, got icon=%q color=%q", created.Icon, created.IconColor)
	}
	if store.createdTag.Icon != "i-lucide-tag" || store.createdTag.IconColor != "#2563eb" {
		t.Fatalf("expected normalized visual fields passed to store, got %#v", store.createdTag)
	}

	icon := ""
	color := "#0f766e"
	updated, err := service.UpdateTag(context.Background(), actor, UpdateTagInput{
		ID:        1,
		Icon:      &icon,
		IconColor: &color,
	})
	if err != nil {
		t.Fatalf("UpdateTag returned error: %v", err)
	}
	if updated.Icon != "" || updated.IconColor != "#0f766e" {
		t.Fatalf("expected update to clear icon and normalize color, got icon=%q color=%q", updated.Icon, updated.IconColor)
	}
}

func TestServiceCreateTagAllowsChineseSlug(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTagManage: true},
	}

	created, err := service.CreateTag(context.Background(), actor, CreateTagInput{
		Slug:   " 中文标签 ",
		Name:   "中文标签",
		Status: TagStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateTag returned error: %v", err)
	}
	if created.Slug != "中文标签" || store.createdTag.Slug != "中文标签" {
		t.Fatalf("expected Chinese tag slug to be accepted and trimmed, got created=%#v store=%#v", created, store.createdTag)
	}
}

func TestServiceRejectsInvalidTagIconColor(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTagManage: true},
	}

	_, err := service.CreateTag(context.Background(), actor, CreateTagInput{
		Slug:   "go",
		Name:   "Go",
		Status: TagStatusActive,
		Icon:   "lucide:tag",
	})
	if !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("expected ErrInvalidTag for invalid tag icon, got %v", err)
	}

	badColor := "#12345g"
	_, err = service.UpdateTag(context.Background(), actor, UpdateTagInput{
		ID:        1,
		IconColor: &badColor,
	})
	if !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("expected ErrInvalidTag for invalid tag color, got %v", err)
	}
}

func TestServiceGetTopicDecoratesExtensionActions(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store, Settings: staticSettingsResolver{}, Publisher: nil, Indexer: nil, TopicActions: fakeTopicActionProvider{
		actions: []TopicExtensionAction{{
			ExtensionID: "demo.plugin",
			ID:          "demo.bookmark",
			Label:       map[string]string{"zh-CN": "收藏", "en-US": "Bookmark"},
			Icon:        "i-lucide-bookmark",
			Method:      "POST",
			URL:         "/extensions/demo.plugin/topic-actions/bookmark",
			Confirm:     true,
		}},
	}})

	topic, err := service.GetTopic(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTopic returned error: %v", err)
	}
	if len(topic.ExtensionActions) != 1 || topic.ExtensionActions[0].ID != "demo.bookmark" {
		t.Fatalf("expected topic extension action, got %#v", topic.ExtensionActions)
	}
}

func TestServiceRecordTopicViewDedupAndNilSafe(t *testing.T) {
	// GetTopic 本身不计浏览；仅 RecordTopicView / controller 副作用计数。
	counter := NewMemoryTopicViewCounter()
	service := NewService(ServiceConfig{Store: newServiceFakeStore()}).WithViewRecorder(counter)
	ctx := context.Background()
	service.RecordTopicView(ctx, 42, "u:1")
	service.RecordTopicView(ctx, 42, "u:1")
	if counter.Delta(42) != 1 {
		t.Fatalf("want 1 unique view, got %d", counter.Delta(42))
	}
	// Redis 故障模拟：不计且不 panic。
	failing := NewMemoryTopicViewCounter()
	failing.FailRecord = true
	service.WithViewRecorder(failing)
	service.RecordTopicView(ctx, 7, "u:9")
	if failing.Delta(7) != 0 {
		t.Fatal("failed recorder must not count")
	}
	// nil recorder 安全。
	service.WithViewRecorder(nil)
	service.RecordTopicView(ctx, 1, "u:1")
}

func TestServiceGetTopicBySlugDecoratesExtensionActions(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store, Settings: staticSettingsResolver{}, Publisher: nil, Indexer: nil, TopicActions: fakeTopicActionProvider{
		actions: []TopicExtensionAction{{
			ExtensionID: "demo.plugin",
			ID:          "demo.share",
			Label:       map[string]string{"zh-CN": "分享"},
			Icon:        "i-lucide-share-2",
			Method:      "POST",
			URL:         "/extensions/demo.plugin/topic-actions/share",
		}},
	}})

	topic, err := service.GetTopicBySlug(context.Background(), "hello-world")
	if err != nil {
		t.Fatalf("GetTopicBySlug returned error: %v", err)
	}
	if len(topic.ExtensionActions) != 1 || topic.ExtensionActions[0].ID != "demo.share" {
		t.Fatalf("expected slug topic extension action, got %#v", topic.ExtensionActions)
	}
}

func TestServiceGetTopicDecoratesExtensionSurfaces(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store, Settings: staticSettingsResolver{}, Publisher: nil, Indexer: nil, TopicActions: nil})
	service.WithTopicExtensionSurfaces(fakeTopicSurfaceProvider{
		sidebar: []TopicExtensionSidebarItem{{
			ExtensionID: "demo.plugin",
			ID:          "policy",
			Order:       10,
			Label:       map[string]string{"zh-CN": "规范"},
			Icon:        "i-lucide-book-open",
			Kind:        "hostLink",
			URL:         "/help",
		}},
		badges: []TopicExtensionBadge{{
			ExtensionID: "demo.plugin",
			ID:          "reviewed",
			Order:       1,
			Label:       map[string]string{"en-US": "OK"},
			Tone:        "success",
		}},
	})

	topic, err := service.GetTopic(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTopic returned error: %v", err)
	}
	if len(topic.ExtensionSidebar) != 1 || topic.ExtensionSidebar[0].ID != "policy" {
		t.Fatalf("expected extension sidebar, got %#v", topic.ExtensionSidebar)
	}
	if len(topic.ExtensionBadges) != 1 || topic.ExtensionBadges[0].Tone != "success" {
		t.Fatalf("expected extension badges, got %#v", topic.ExtensionBadges)
	}
}

func TestServiceListCommentsDecoratesExtensionActions(t *testing.T) {
	store := newServiceFakeStore()
	store.listCommentsResult = CommentList{
		Items:   []Comment{{ID: 1, TopicID: 10, Status: "active"}},
		Total:   1,
		Page:    1,
		PerPage: 20,
		View:    "tree",
	}
	service := NewService(ServiceConfig{Store: store, Settings: staticSettingsResolver{}, Publisher: nil, Indexer: nil, TopicActions: nil})
	service.WithCommentExtensionActions(fakeCommentActionProvider{
		actions: []CommentExtensionAction{{
			ExtensionID:  "demo.plugin",
			ID:           "demo.flag",
			Label:        map[string]string{"zh-CN": "标记"},
			Icon:         "i-lucide-flag",
			Method:       "POST",
			URL:          "/extensions/demo.plugin/comment-actions/flag",
			RequiresAuth: true,
		}},
	})

	list, err := service.ListComments(context.Background(), CommentListInput{TopicID: 10})
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(list.ExtensionActions) != 1 || list.ExtensionActions[0].ID != "demo.flag" {
		t.Fatalf("expected comment extension actions, got %#v", list.ExtensionActions)
	}
	if !list.ExtensionActions[0].RequiresAuth {
		t.Fatalf("expected requiresAuth on comment action: %#v", list.ExtensionActions[0])
	}
}

func TestServiceListTopicsDecoratesExtensionListBadges(t *testing.T) {
	store := newServiceFakeStore()
	store.listTopicsResult = TopicList{
		Items:   []TopicSummary{{ID: 1, Title: "A", Slug: "a", Status: TopicStatusActive}},
		Total:   1,
		Page:    1,
		PerPage: 20,
	}
	service := NewService(ServiceConfig{Store: store, Settings: staticSettingsResolver{}, Publisher: nil, Indexer: nil, TopicActions: nil})
	service.WithTopicExtensionSurfaces(fakeTopicSurfaceProvider{
		listBadges: []TopicExtensionBadge{{
			ExtensionID: "demo.plugin",
			ID:          "hot",
			Order:       1,
			Label:       map[string]string{"zh-CN": "热"},
			Tone:        "warning",
		}},
	})

	list, err := service.ListTopics(context.Background(), TopicListInput{})
	if err != nil {
		t.Fatalf("ListTopics returned error: %v", err)
	}
	if len(list.ExtensionListBadges) != 1 || list.ExtensionListBadges[0].ID != "hot" {
		t.Fatalf("expected list extension badges, got %#v", list.ExtensionListBadges)
	}
}

func TestServiceListTopicsResolvesSortToSiteDefault(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		requested   string
		siteDefault string
		wantSort    string
	}{
		{name: "empty request uses active default", requested: "", siteDefault: "active", wantSort: "active"},
		{name: "empty request keeps stored latest", requested: "", siteDefault: "latest", wantSort: "latest"},
		{name: "explicit sort wins", requested: "hot", siteDefault: "latest", wantSort: "hot"},
		{name: "invalid request uses site default", requested: "replies", siteDefault: "active", wantSort: "active"},
		{name: "empty setting falls back to recommended", requested: "", siteDefault: "", wantSort: "active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newServiceFakeStore()
			settings := testForumSettings()
			settings.ListDefaultSort = tc.siteDefault
			service := NewService(ServiceConfig{
				Store:    store,
				Settings: fakeSettingsResolver{settings: settings},
			})

			if _, err := service.ListTopics(context.Background(), TopicListInput{Sort: tc.requested}); err != nil {
				t.Fatalf("ListTopics returned error: %v", err)
			}
			if store.listTopicsInput.Sort != tc.wantSort {
				t.Fatalf("resolved sort = %q, want %q", store.listTopicsInput.Sort, tc.wantSort)
			}
		})
	}
}

func TestCommentPositionForInsertSupportsArbitraryDepth(t *testing.T) {
	parent := CommentSummary{
		ID:            7,
		RootCommentID: 3,
		Depth:         2,
		PathKey:       "000000000003.000000000005.000000000007",
	}

	position := CommentPositionForInsert(42, &parent)

	if position.RootCommentID != 3 {
		t.Fatalf("expected root comment 3, got %d", position.RootCommentID)
	}
	if position.Depth != 3 {
		t.Fatalf("expected depth 3, got %d", position.Depth)
	}
	if position.PathKey != "000000000003.000000000005.000000000007.000000000042" {
		t.Fatalf("unexpected path key %q", position.PathKey)
	}
}

func TestCommentPositionForInsertCreatesRootPosition(t *testing.T) {
	position := CommentPositionForInsert(9, nil)

	if position.RootCommentID != 9 || position.Depth != 0 || position.PathKey != "000000000009" {
		t.Fatalf("unexpected root position %#v", position)
	}
}

func TestBuildCommentTreeKeepsNestedChildren(t *testing.T) {
	rootID := int64(1)
	childID := int64(2)
	items := []Comment{
		{ID: rootID, PathKey: "000000000001", Depth: 0},
		{ID: childID, ParentID: &rootID, PathKey: "000000000001.000000000002", Depth: 1},
		{ID: 3, ParentID: &childID, PathKey: "000000000001.000000000002.000000000003", Depth: 2},
	}

	tree := buildCommentTree(items)

	if len(tree) != 1 || len(tree[0].Children) != 1 || len(tree[0].Children[0].Children) != 1 {
		t.Fatalf("expected nested tree to keep children, got %#v", tree)
	}
}

func TestServiceCreateCommentRejectsLockedTopic(t *testing.T) {
	store := newServiceFakeStore()
	store.topicForComment = TopicSummary{ID: 99, Status: TopicStatusLocked}
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostCreate: true},
	}

	_, err := service.CreateComment(context.Background(), actor, CreateCommentInput{
		TopicID: 99,
		Content: ContentInput{
			RawContent:   "不能回复锁定帖子",
			SourceFormat: SourceFormatMarkdown,
			EditorType:   EditorTypeMarkdown,
		},
	})
	if !errors.Is(err, ErrTopicClosed) {
		t.Fatalf("expected ErrTopicClosed, got %v", err)
	}
}

func commentCreator() identity.Actor {
	return identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostCreate: true},
	}
}

// TestServiceCreateCommentAppliesBeforeCreateFilterAndEmitsCreatedEvent
// E1.1：filter 可补丁正文，且成功创建后仍发出 comment.created。
func TestServiceCreateCommentAppliesBeforeCreateFilterAndEmitsCreatedEvent(t *testing.T) {
	store := newServiceFakeStore()
	store.topicForComment = TopicSummary{ID: 42, Status: TopicStatusActive}
	publisher := &fakeEventPublisher{results: map[string]appevents.Result{
		appevents.CommentBeforeCreate: {
			OK: true,
			Patch: map[string]any{
				"content": ContentInput{
					RawContent:   "插件改写后的回复",
					SourceFormat: SourceFormatMarkdown,
					EditorType:   EditorTypeMarkdown,
				},
			},
		},
	}}
	service := NewService(ServiceConfig{Store: store, Publisher: publisher})

	comment, err := service.CreateComment(context.Background(), commentCreator(), CreateCommentInput{
		TopicID: 42,
		Content: validMarkdownContent("原始回复"),
	})
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}
	if comment.Content.PlainText != "插件改写后的回复" {
		t.Fatalf("expected patched content, got %#v", comment.Content)
	}
	if store.createdComment.Content.RawContent != "插件改写后的回复" {
		t.Fatalf("expected store to receive patched content, got %#v", store.createdComment)
	}
	if !publisher.seen(appevents.CommentBeforeCreate) || !publisher.seen(appevents.CommentCreated) {
		t.Fatalf("expected before/create events, got %#v", publisher.names)
	}
	beforeEnv, ok := publisher.envelope(appevents.CommentBeforeCreate)
	if !ok {
		t.Fatal("missing comment.before_create envelope")
	}
	if beforeEnv.Payload["topicId"] != int64(42) {
		t.Fatalf("expected topicId in filter payload, got %#v", beforeEnv.Payload)
	}
	if beforeEnv.Payload["actorUserId"] != int64(12) {
		t.Fatalf("expected actorUserId in filter payload, got %#v", beforeEnv.Payload)
	}
	if _, hasParent := beforeEnv.Payload["parentId"]; hasParent {
		t.Fatalf("root comment should omit parentId, got %#v", beforeEnv.Payload)
	}
}

// TestServiceCreateCommentBeforeCreateCanReject 插件拒绝时不得落库，且错误可映射为 RejectedError。
func TestServiceCreateCommentBeforeCreateCanReject(t *testing.T) {
	store := newServiceFakeStore()
	publisher := &fakeEventPublisher{results: map[string]appevents.Result{
		appevents.CommentBeforeCreate: {
			OK:      false,
			Reason:  "moderation.spam_blocked",
			Message: "疑似垃圾回复",
		},
	}}
	service := NewService(ServiceConfig{Store: store, Publisher: publisher})

	_, err := service.CreateComment(context.Background(), commentCreator(), CreateCommentInput{
		TopicID: 1,
		Content: validMarkdownContent("spam body"),
	})
	var rejected *appevents.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected RejectedError, got %v", err)
	}
	if rejected.Reason != "moderation.spam_blocked" {
		t.Fatalf("unexpected reason %q", rejected.Reason)
	}
	if store.createdComment.TopicID != 0 {
		t.Fatalf("rejected comment must not be stored, got %#v", store.createdComment)
	}
	if publisher.seen(appevents.CommentCreated) {
		t.Fatal("comment.created must not fire after reject")
	}
}

// TestServiceCreateCommentBeforeCreateIncludesParentID 楼中楼请求应把 parentId 放进 filter payload。
func TestServiceCreateCommentBeforeCreateIncludesParentID(t *testing.T) {
	store := newServiceFakeStore()
	parentID := int64(7)
	store.topicForComment = TopicSummary{ID: 3, Status: TopicStatusActive}
	store.commentSummary = CommentSummary{
		ID:            parentID,
		TopicID:       3,
		Status:        CommentStatusActive,
		Depth:         0,
		PathKey:       "000000000007",
		RootCommentID: parentID,
	}
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Publisher: publisher})

	_, err := service.CreateComment(context.Background(), commentCreator(), CreateCommentInput{
		TopicID:  3,
		ParentID: &parentID,
		Content:  validMarkdownContent("子回复"),
	})
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}
	beforeEnv, ok := publisher.envelope(appevents.CommentBeforeCreate)
	if !ok {
		t.Fatal("missing comment.before_create")
	}
	if beforeEnv.Payload["parentId"] != parentID {
		t.Fatalf("expected parentId=%d, got %#v", parentID, beforeEnv.Payload["parentId"])
	}
}

// TestServiceCreateCommentBeforeCreateNotInvokedWhenTopicLocked 主题不可回复时不应触发 filter。
func TestServiceCreateCommentBeforeCreateNotInvokedWhenTopicLocked(t *testing.T) {
	store := newServiceFakeStore()
	store.topicForComment = TopicSummary{ID: 99, Status: TopicStatusLocked}
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Publisher: publisher})

	_, err := service.CreateComment(context.Background(), commentCreator(), CreateCommentInput{
		TopicID: 99,
		Content: validMarkdownContent("不能回复"),
	})
	if !errors.Is(err, ErrTopicClosed) {
		t.Fatalf("expected ErrTopicClosed, got %v", err)
	}
	if publisher.seen(appevents.CommentBeforeCreate) {
		t.Fatal("filter must not run for locked topics")
	}
}

func TestServiceUpdateCommentAllowsOwnerAndAdmin(t *testing.T) {
	store := newServiceFakeStore()
	store.commentSummary = CommentSummary{ID: 5, AuthorUserID: 12, Status: CommentStatusActive, CurrentRevision: 1}
	service := NewService(ServiceConfig{Store: store})
	owner := identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostEditOwn: true},
	}
	admin := identity.Actor{
		ID:          20,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostEditAny: true},
	}

	if _, err := service.UpdateComment(context.Background(), owner, UpdateCommentInput{
		CommentID: 5, ExpectedRevision: 1,
		Content: ContentInput{RawContent: "作者编辑", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	}); err != nil {
		t.Fatalf("expected owner to edit comment, got %v", err)
	}
	if _, err := service.UpdateComment(context.Background(), admin, UpdateCommentInput{
		CommentID: 5, ExpectedRevision: 1, Reason: "moderation correction",
		Content: ContentInput{RawContent: "管理员编辑", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	}); err != nil {
		t.Fatalf("expected admin to edit comment, got %v", err)
	}
}

func TestServiceUpdateCommentRejectsUnauthorizedActor(t *testing.T) {
	store := newServiceFakeStore()
	store.commentSummary = CommentSummary{ID: 5, AuthorUserID: 12, Status: CommentStatusActive, CurrentRevision: 1}
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{ID: 13, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.UpdateComment(context.Background(), actor, UpdateCommentInput{
		CommentID: 5, ExpectedRevision: 1,
		Content: ContentInput{RawContent: "越权编辑", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

// --- Topic lifecycle tests ---

func TestServiceUpdateTopicAllowsOwnerAndEditor(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 1}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})
	owner := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}
	editor := identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditAny: true}}

	title := "新标题"
	content := ContentInput{RawContent: "新正文", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown}
	if _, err := service.UpdateTopic(context.Background(), owner, UpdateTopicInput{TopicID: 7, ExpectedRevision: 1, Title: &title, Content: &content}); err != nil {
		t.Fatalf("expected owner to update topic, got %v", err)
	}
	if store.updatedTopic.Title != "新标题" {
		t.Fatalf("expected updated title, got %#v", store.updatedTopic)
	}
	if _, err := service.UpdateTopic(context.Background(), editor, UpdateTopicInput{TopicID: 7, ExpectedRevision: 1, Reason: "moderation correction", Title: &title}); err != nil {
		t.Fatalf("expected editor to update topic, got %v", err)
	}
}

func TestServiceUpdateTopicRejectsUnauthorizedActor(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 1}
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{ID: 13, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.UpdateTopic(context.Background(), actor, UpdateTopicInput{TopicID: 7, ExpectedRevision: 1, Title: strPtr("x")})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestServiceTopicEditSourceAuthorizationAndState(t *testing.T) {
	now := time.Now().UTC()
	source := EditableContentSource{
		RawContent: "M1_TOPIC_SOURCE_SECRET", SourceFormat: SourceFormatEditorDocument,
		EditorType: EditorTypeTiptap, EditorVersion: "sf-editor-v1", ContentHash: "source-hash",
		AttachmentIDs: []int64{3, 9}, CurrentRevision: 4,
	}
	settings := testForumSettings()
	settings.TopicEditWindowMinutes = 30
	tests := []struct {
		name       string
		actor      identity.Actor
		status     string
		createdAt  time.Time
		wantErr    error
		wantLoaded bool
	}{
		{name: "author", actor: identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}, status: TopicStatusActive, createdAt: now, wantLoaded: true},
		{name: "edit any outside window", actor: identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditAny: true}}, status: TopicStatusLocked, createdAt: now.Add(-24 * time.Hour), wantLoaded: true},
		{name: "unrelated active", actor: identity.Actor{ID: 13, Status: identity.UserStatusActive}, status: TopicStatusActive, createdAt: now, wantErr: ErrTopicNotFound},
		{name: "inactive author", actor: identity.Actor{ID: 12, Status: identity.UserStatusDisabled, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}, status: TopicStatusActive, createdAt: now, wantErr: ErrTopicNotFound},
		{name: "banned author", actor: identity.Actor{ID: 12, Status: identity.UserStatusBanned, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}, status: TopicStatusActive, createdAt: now, wantErr: ErrTopicNotFound},
		{name: "author outside window", actor: identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}, status: TopicStatusActive, createdAt: now.Add(-31 * time.Minute), wantErr: ErrEditWindowExpired},
		{name: "hidden", actor: identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditAny: true}}, status: TopicStatusHidden, createdAt: now, wantErr: ErrTopicNotFound},
		{name: "deleted", actor: identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditAny: true}}, status: TopicStatusDeleted, createdAt: now, wantErr: ErrTopicNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newServiceFakeStore()
			store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: tc.status, CreatedAt: tc.createdAt, CurrentRevision: 4}
			store.topicEditSource = source
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}})
			got, err := NewEditableSourceService(service).GetTopicEditSource(context.Background(), tc.actor, 7)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantLoaded && (got.RawContent != source.RawContent || store.topicSourceCalls != 1) {
				t.Fatalf("source = %#v calls=%d", got, store.topicSourceCalls)
			}
			if !tc.wantLoaded && store.topicSourceCalls != 0 {
				t.Fatalf("denied source read reached store: %d", store.topicSourceCalls)
			}
		})
	}
}

func TestServiceCommentEditSourceAuthorizationAndOwningTopicState(t *testing.T) {
	now := time.Now().UTC()
	settings := testForumSettings()
	settings.CommentEditWindowMinutes = 30
	owner := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostEditOwn: true}}
	moderator := identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostEditAny: true}}
	tests := []struct {
		name        string
		actor       identity.Actor
		comment     CommentSummary
		topicStatus string
		wantErr     error
		wantLoaded  bool
	}{
		{name: "author", actor: owner, comment: CommentSummary{ID: 9, TopicID: 7, AuthorUserID: 12, Status: CommentStatusActive, CreatedAt: now}, topicStatus: TopicStatusActive, wantLoaded: true},
		{name: "edit any outside window", actor: moderator, comment: CommentSummary{ID: 9, TopicID: 7, AuthorUserID: 12, Status: CommentStatusPending, CreatedAt: now.Add(-24 * time.Hour)}, topicStatus: TopicStatusLocked, wantLoaded: true},
		{name: "unrelated", actor: identity.Actor{ID: 99, Status: identity.UserStatusActive}, comment: CommentSummary{ID: 9, TopicID: 7, AuthorUserID: 12, Status: CommentStatusActive, CreatedAt: now}, topicStatus: TopicStatusActive, wantErr: ErrCommentNotFound},
		{name: "outside window", actor: owner, comment: CommentSummary{ID: 9, TopicID: 7, AuthorUserID: 12, Status: CommentStatusActive, CreatedAt: now.Add(-31 * time.Minute)}, topicStatus: TopicStatusActive, wantErr: ErrEditWindowExpired},
		{name: "hidden comment", actor: moderator, comment: CommentSummary{ID: 9, TopicID: 7, AuthorUserID: 12, Status: CommentStatusHidden, CreatedAt: now}, topicStatus: TopicStatusActive, wantErr: ErrCommentNotFound},
		{name: "deleted comment", actor: moderator, comment: CommentSummary{ID: 9, TopicID: 7, AuthorUserID: 12, Status: CommentStatusDeleted, CreatedAt: now}, topicStatus: TopicStatusActive, wantErr: ErrCommentNotFound},
		{name: "hidden topic", actor: moderator, comment: CommentSummary{ID: 9, TopicID: 7, AuthorUserID: 12, Status: CommentStatusActive, CreatedAt: now}, topicStatus: TopicStatusHidden, wantErr: ErrCommentNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newServiceFakeStore()
			store.commentSummary = tc.comment
			store.actionTopic = TopicSummary{ID: 7, Status: tc.topicStatus}
			store.commentEditSource = EditableContentSource{RawContent: "M1_COMMENT_SOURCE_SECRET", CurrentRevision: 3}
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}})
			got, err := NewEditableSourceService(service).GetCommentEditSource(context.Background(), tc.actor, 9)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantLoaded && (got.RawContent == "" || store.commentSourceCalls != 1) {
				t.Fatalf("source = %#v calls=%d", got, store.commentSourceCalls)
			}
			if !tc.wantLoaded && store.commentSourceCalls != 0 {
				t.Fatalf("denied source read reached store: %d", store.commentSourceCalls)
			}
		})
	}
}

func TestServiceUpdateTopicRejectsEmptyTitle(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 1}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})
	owner := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}

	empty := "  "
	_, err := service.UpdateTopic(context.Background(), owner, UpdateTopicInput{TopicID: 7, ExpectedRevision: 1, Title: &empty})
	if !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("expected ErrInvalidTopic, got %v", err)
	}
}

func topicEditor() identity.Actor {
	return identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicEditOwn: true},
	}
}

// TestServiceUpdateTopicAppliesBeforeUpdateFilterAndEmitsUpdatedEvent
// E1.2：filter 可补丁标题/标签，成功后仍发出 topic.updated。
func TestServiceUpdateTopicAppliesBeforeUpdateFilterAndEmitsUpdatedEvent(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, Title: "原标题", CurrentRevision: 1}
	publisher := &fakeEventPublisher{results: map[string]appevents.Result{
		appevents.TopicBeforeUpdate: {
			OK: true,
			Patch: map[string]any{
				"title":    "插件改写标题",
				"tagSlugs": []string{"forced-tag"},
			},
		},
	}}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})

	title := "用户标题"
	_, err := service.UpdateTopic(context.Background(), topicEditor(), UpdateTopicInput{
		TopicID: 7, ExpectedRevision: 1,
		Title:    &title,
		TagSlugs: []string{"original"},
	})
	if err != nil {
		t.Fatalf("UpdateTopic returned error: %v", err)
	}
	if store.updatedTopic.Title != "插件改写标题" {
		t.Fatalf("expected patched title, got %#v", store.updatedTopic)
	}
	if !stringSlicesEqual(store.updatedTopic.TagSlugs, []string{"forced-tag"}) {
		t.Fatalf("expected patched tag slugs, got %#v", store.updatedTopic.TagSlugs)
	}
	if !publisher.seen(appevents.TopicBeforeUpdate) || !publisher.seen(appevents.TopicUpdated) {
		t.Fatalf("expected before_update/updated events, got %#v", publisher.names)
	}
	beforeEnv, ok := publisher.envelope(appevents.TopicBeforeUpdate)
	if !ok {
		t.Fatal("missing topic.before_update envelope")
	}
	if beforeEnv.Payload["topicId"] != int64(7) {
		t.Fatalf("expected topicId in filter payload, got %#v", beforeEnv.Payload)
	}
	if beforeEnv.Payload["title"] != "用户标题" {
		t.Fatalf("expected original title in payload before patch, got %#v", beforeEnv.Payload["title"])
	}
	if beforeEnv.ResourceID != "7" {
		t.Fatalf("expected ResourceID=7, got %q", beforeEnv.ResourceID)
	}
}

// TestServiceUpdateTopicBeforeUpdateCanReject 插件拒绝时不得落库。
func TestServiceUpdateTopicBeforeUpdateCanReject(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 1}
	publisher := &fakeEventPublisher{results: map[string]appevents.Result{
		appevents.TopicBeforeUpdate: {
			OK:      false,
			Reason:  "moderation.title_blocked",
			Message: "标题不允许修改",
		},
	}}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})

	title := "违禁标题"
	_, err := service.UpdateTopic(context.Background(), topicEditor(), UpdateTopicInput{
		TopicID: 7, ExpectedRevision: 1,
		Title: &title,
	})
	var rejected *appevents.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected RejectedError, got %v", err)
	}
	if rejected.Reason != "moderation.title_blocked" {
		t.Fatalf("unexpected reason %q", rejected.Reason)
	}
	if store.updatedTopic.TopicID != 0 {
		t.Fatalf("rejected update must not be stored, got %#v", store.updatedTopic)
	}
	if publisher.seen(appevents.TopicUpdated) {
		t.Fatal("topic.updated must not fire after reject")
	}
}

// TestServiceUpdateTopicBeforeUpdateCanForceTagsWithoutRequestTags
// 插件可在请求未带标签时强制写入 tagSlugs。
func TestServiceUpdateTopicBeforeUpdateCanForceTagsWithoutRequestTags(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 1}
	publisher := &fakeEventPublisher{results: map[string]appevents.Result{
		appevents.TopicBeforeUpdate: {
			OK:    true,
			Patch: map[string]any{"tagSlugs": []string{"ops-required"}},
		},
	}}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})

	title := "只改标题"
	_, err := service.UpdateTopic(context.Background(), topicEditor(), UpdateTopicInput{
		TopicID: 7, ExpectedRevision: 1,
		Title: &title,
	})
	if err != nil {
		t.Fatalf("UpdateTopic returned error: %v", err)
	}
	if !stringSlicesEqual(store.updatedTopic.TagSlugs, []string{"ops-required"}) {
		t.Fatalf("expected forced tags, got %#v", store.updatedTopic.TagSlugs)
	}
	beforeEnv, ok := publisher.envelope(appevents.TopicBeforeUpdate)
	if !ok {
		t.Fatal("missing topic.before_update")
	}
	if _, hasTags := beforeEnv.Payload["tagSlugs"]; hasTags {
		t.Fatalf("request without tags should omit tagSlugs in payload, got %#v", beforeEnv.Payload)
	}
}

// TestServiceUpdateTopicBeforeUpdateNotInvokedWhenUnauthorized 无权限时不触发 filter。
func TestServiceUpdateTopicBeforeUpdateNotInvokedWhenUnauthorized(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 1}
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Publisher: publisher})
	actor := identity.Actor{ID: 13, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.UpdateTopic(context.Background(), actor, UpdateTopicInput{
		TopicID: 7,
		Title:   strPtr("x"),
	})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
	if publisher.seen(appevents.TopicBeforeUpdate) {
		t.Fatal("filter must not run without edit permission")
	}
}

func TestServiceUpdateTopicStaleRevisionDoesNotInvokeFilter(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 2}
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Publisher: publisher})
	_, err := service.UpdateTopic(context.Background(), topicEditor(), UpdateTopicInput{TopicID: 7, ExpectedRevision: 1, Title: strPtr("stale")})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
	if publisher.seen(appevents.TopicBeforeUpdate) {
		t.Fatal("stale request must not invoke topic.before_update")
	}
}

func TestServiceCrossAuthorEditRequiresReason(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 1}
	actor := identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditAny: true}}
	_, err := NewService(ServiceConfig{Store: store}).UpdateTopic(context.Background(), actor, UpdateTopicInput{TopicID: 7, ExpectedRevision: 1, Title: strPtr("staff edit")})
	if !errors.Is(err, ErrRevisionReasonRequired) {
		t.Fatalf("expected required reason, got %v", err)
	}
}

func TestServiceUpdateCommentFilterPatchAndEvent(t *testing.T) {
	store := newServiceFakeStore()
	store.commentSummary = CommentSummary{ID: 9, TopicID: 7, AuthorUserID: 12, Status: CommentStatusActive, CurrentRevision: 1, CreatedAt: time.Now().UTC()}
	publisher := &fakeEventPublisher{results: map[string]appevents.Result{appevents.CommentBeforeUpdate: {OK: true, Patch: map[string]any{"content": ContentInput{RawContent: "patched", SourceFormat: SourceFormatMarkdown}}}}}
	updated, err := NewService(ServiceConfig{Store: store, Publisher: publisher}).UpdateComment(context.Background(), topicEditorWithCommentPermission(), UpdateCommentInput{CommentID: 9, ExpectedRevision: 1, Content: validMarkdownContent("original")})
	if err != nil {
		t.Fatalf("UpdateComment: %v", err)
	}
	if store.updatedComment.Content.RawContent != "patched" || !publisher.seen(appevents.CommentUpdated) || updated.CurrentRevision != 2 {
		t.Fatalf("comment update contract failed: record=%#v events=%#v updated=%#v", store.updatedComment, publisher.names, updated)
	}
}

func topicEditorWithCommentPermission() identity.Actor {
	return identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostEditOwn: true}}
}

// TestServiceUpdateTopicRequeuesPendingOnPublicationPolicy 编辑正文触发预审时应 pending。
func TestServiceUpdateTopicRequeuesPendingOnPublicationPolicy(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
	svc := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: appevents.NoopPublisher{}, Indexer: nil, PublicationPolicy: staticPublicationPolicy{decision: PublicationDecision{Pending: true, Triggers: []string{"external_link"}}}})

	owner := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}
	content := ContentInput{RawContent: "see https://evil.example", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown}

	updated, err := svc.UpdateTopic(context.Background(), owner, UpdateTopicInput{TopicID: 7, ExpectedRevision: 1, Content: &content})
	if err != nil {
		t.Fatalf("UpdateTopic: %v", err)
	}
	if !store.updatedTopic.RequeuePending {
		t.Fatal("expected RequeuePending on content edit")
	}
	if len(store.updatedTopic.ModerationTriggers) != 1 || store.updatedTopic.ModerationTriggers[0] != "external_link" {
		t.Fatalf("expected triggers, got %v", store.updatedTopic.ModerationTriggers)
	}
	if updated.Status != TopicStatusPending {
		t.Fatalf("expected pending topic, got %q", updated.Status)
	}
}

// TestServiceUpdateCommentRequeuesPendingOnPublicationPolicy 评论编辑同样受发布策略约束。
func TestServiceUpdateCommentRequeuesPendingOnPublicationPolicy(t *testing.T) {
	store := newServiceFakeStore()
	store.commentSummary = CommentSummary{ID: 9, TopicID: 3, AuthorUserID: 12, Status: CommentStatusActive, CreatedAt: time.Now().UTC(), CurrentRevision: 1}
	svc := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: appevents.NoopPublisher{}, Indexer: nil, PublicationPolicy: staticPublicationPolicy{decision: PublicationDecision{Pending: true, Triggers: []string{"external_link"}}}})

	owner := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostEditOwn: true}}

	updated, err := svc.UpdateComment(context.Background(), owner, UpdateCommentInput{
		CommentID: 9, ExpectedRevision: 1,
		Content: ContentInput{RawContent: "https://outside.test", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	})
	if err != nil {
		t.Fatalf("UpdateComment: %v", err)
	}
	if updated.Status != CommentStatusPending {
		t.Fatalf("expected pending comment, got %q", updated.Status)
	}
}

func TestServiceDeleteTopicAllowsOwnerAndModerator(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
	service := NewService(ServiceConfig{Store: store})
	owner := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicDeleteOwn: true}}
	moderator := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicDeleteAny: true}}

	if _, err := service.DeleteTopic(context.Background(), owner, 7); err != nil {
		t.Fatalf("expected owner to delete topic, got %v", err)
	}
	if store.deletedTopicID != 7 {
		t.Fatalf("expected deleted topic id 7, got %d", store.deletedTopicID)
	}
	if _, err := service.DeleteTopic(context.Background(), moderator, 7); err != nil {
		t.Fatalf("expected moderator to delete topic, got %v", err)
	}
}

func TestServiceDeleteTopicRejectsUnauthorizedActor(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{ID: 13, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.DeleteTopic(context.Background(), actor, 7)
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestServiceApplyTopicActionEnforcesPermissions(t *testing.T) {
	cases := []struct {
		name       string
		action     string
		permission string
	}{
		{name: "hide", action: TopicActionHide, permission: identity.PermissionTopicDeleteAny},
		{name: "restore", action: TopicActionRestore, permission: identity.PermissionTopicDeleteAny},
		{name: "lock", action: TopicActionLock, permission: identity.PermissionTopicLock},
		{name: "unlock", action: TopicActionUnlock, permission: identity.PermissionTopicLock},
		{name: "pin", action: TopicActionPin, permission: identity.PermissionTopicPin},
		{name: "unpin", action: TopicActionUnpin, permission: identity.PermissionTopicPin},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/allowed", func(t *testing.T) {
			store := newServiceFakeStore()
			store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
			service := NewService(ServiceConfig{Store: store})
			actor := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{tc.permission: true}}

			result, err := service.ApplyTopicAction(context.Background(), actor, TopicLifecycleInput{TopicID: 7, Action: tc.action})
			if err != nil {
				t.Fatalf("expected %s to succeed with permission, got %v", tc.name, err)
			}
			if result.TopicID != 7 {
				t.Fatalf("expected topic id 7, got %d", result.TopicID)
			}
			if store.appliedAction != tc.action {
				t.Fatalf("expected store action %s, got %s", tc.action, store.appliedAction)
			}
		})
		t.Run(tc.name+"/denied", func(t *testing.T) {
			store := newServiceFakeStore()
			store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
			service := NewService(ServiceConfig{Store: store})
			actor := identity.Actor{ID: 13, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

			_, err := service.ApplyTopicAction(context.Background(), actor, TopicLifecycleInput{TopicID: 7, Action: tc.action})
			if !errors.Is(err, identity.ErrPermissionDenied) {
				t.Fatalf("expected permission denied for %s, got %v", tc.name, err)
			}
			if store.appliedAction != "" {
				t.Fatalf("store should not apply action when denied, got %s", store.appliedAction)
			}
		})
	}
}

func TestServiceApplyTopicActionRejectsInvalidAction(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicLock: true}}

	_, err := service.ApplyTopicAction(context.Background(), actor, TopicLifecycleInput{TopicID: 7, Action: "bogus"})
	if !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("expected ErrInvalidAction, got %v", err)
	}
}

func TestServiceApplyTopicActionBlocksActionsOnHiddenTopic(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusHidden}
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicLock: true}}

	_, err := service.ApplyTopicAction(context.Background(), actor, TopicLifecycleInput{TopicID: 7, Action: TopicActionLock})
	if !errors.Is(err, ErrTopicNotFound) {
		t.Fatalf("expected ErrTopicNotFound on hidden topic, got %v", err)
	}
}

func TestServiceApplyTopicActionAllowsRestoreOnHiddenTopic(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusHidden}
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicDeleteAny: true}}

	result, err := service.ApplyTopicAction(context.Background(), actor, TopicLifecycleInput{TopicID: 7, Action: TopicActionRestore})
	if err != nil {
		t.Fatalf("expected restore on hidden topic, got %v", err)
	}
	if result.Status != TopicStatusActive {
		t.Fatalf("expected active status after restore, got %s", result.Status)
	}
}

func TestServiceTopicActionEmitsEvents(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{
		ID: 7, AuthorUserID: 12, Author: &UserSummary{ID: 12, Username: "alice", DisplayName: "Alice"},
		CategoryID: 3, CategorySlug: "general", CategoryName: "综合讨论",
		Title: "事件主题", Slug: "event-topic", Status: TopicStatusActive,
		Tags: []TopicTagSummary{{ID: 9, Slug: "code", Name: "代码", Status: TagStatusActive}},
	}
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Publisher: publisher})
	actor := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicLock: true}}

	if _, err := service.ApplyTopicAction(context.Background(), actor, TopicLifecycleInput{TopicID: 7, Action: TopicActionLock}); err != nil {
		t.Fatalf("lock failed: %v", err)
	}
	envelope, ok := publisher.envelope(appevents.TopicLocked)
	if !ok {
		t.Fatalf("expected topic.locked event, got %#v", publisher.names)
	}
	if envelope.Payload["url"] != "/t/7/event-topic" {
		t.Fatalf("unexpected topic.locked URL: %#v", envelope.Payload["url"])
	}
	author, ok := envelope.Payload["author"].(map[string]any)
	if !ok || author["username"] != "alice" {
		t.Fatalf("unexpected topic.locked author: %#v", envelope.Payload["author"])
	}
	category, ok := envelope.Payload["category"].(map[string]any)
	if !ok || category["name"] != "综合讨论" {
		t.Fatalf("unexpected topic.locked category: %#v", envelope.Payload["category"])
	}
}

func TestListAuthorReviewItemsScopesByActor(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})
	actor := identity.Actor{ID: 42, Status: identity.UserStatusActive}
	items, err := service.ListAuthorReviewItems(context.Background(), actor)
	if err != nil {
		t.Fatalf("list author review items: %v", err)
	}
	if store.authorReviewUserID != 42 {
		t.Fatalf("author review scope = %d, want 42", store.authorReviewUserID)
	}
	if len(items.Items) != 1 || items.Items[0].TargetID != 9 {
		t.Fatalf("unexpected author review items: %#v", items.Items)
	}
}

func strPtr(value string) *string { return &value }

func containsUnsafeHTML(value string) bool {
	return stringsContains(value, "<script") || stringsContains(value, "javascript:")
}

func stringsContains(value string, needle string) bool {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

type fakeEventPublisher struct {
	names     []string
	envelopes []appevents.Envelope
	results   map[string]appevents.Result
}

func (p *fakeEventPublisher) Emit(_ context.Context, envelope appevents.Envelope) appevents.Result {
	p.names = append(p.names, envelope.Name)
	p.envelopes = append(p.envelopes, envelope)
	if result, ok := p.results[envelope.Name]; ok {
		return result
	}
	return appevents.Result{OK: true}
}

func (p *fakeEventPublisher) seen(name string) bool {
	for _, item := range p.names {
		if item == name {
			return true
		}
	}
	return false
}

func (p *fakeEventPublisher) envelope(name string) (appevents.Envelope, bool) {
	for _, item := range p.envelopes {
		if item.Name == name {
			return item, true
		}
	}
	return appevents.Envelope{}, false
}

func topicCreator() identity.Actor {
	return identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}
}

func validMarkdownContent(value string) ContentInput {
	return ContentInput{RawContent: value, SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown}
}

func TestServicePropagatesContentAttachmentReferences(t *testing.T) {
	ctx := context.Background()
	ids := []int64{9, 3, 9}

	t.Run("create topic", func(t *testing.T) {
		store := &serviceFakeStore{nextID: 1}
		service := NewService(ServiceConfig{Store: store})
		actor := identity.Actor{ID: 10, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicCreate: true}}
		_, err := service.CreateTopic(ctx, actor, CreateTopicInput{
			CategorySlug: "general", Title: "带附件主题",
			Content: ContentInput{RawContent: "正文", SourceFormat: SourceFormatMarkdown, AttachmentIDs: &ids},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(store.createdTopic.AttachmentIDs, []int64{9, 3}) {
			t.Fatalf("attachment ids=%v", store.createdTopic.AttachmentIDs)
		}
	})

	t.Run("update topic explicit empty clears", func(t *testing.T) {
		store := &serviceFakeStore{actionTopic: TopicSummary{ID: 5, AuthorUserID: 10, Status: TopicStatusActive, CreatedAt: time.Now()}}
		service := NewService(ServiceConfig{Store: store})
		actor := identity.Actor{ID: 10, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}
		empty := []int64{}
		content := ContentInput{RawContent: "更新正文", SourceFormat: SourceFormatMarkdown, AttachmentIDs: &empty}
		if _, err := service.UpdateTopic(ctx, actor, UpdateTopicInput{TopicID: 5, ExpectedRevision: 1, Content: &content}); err != nil {
			t.Fatal(err)
		}
		if !store.updatedTopic.ReplaceAttachments || len(store.updatedTopic.AttachmentIDs) != 0 {
			t.Fatalf("update record=%#v", store.updatedTopic)
		}
	})

	t.Run("update topic omitted preserves", func(t *testing.T) {
		store := &serviceFakeStore{actionTopic: TopicSummary{ID: 5, AuthorUserID: 10, Status: TopicStatusActive, CreatedAt: time.Now()}}
		service := NewService(ServiceConfig{Store: store})
		actor := identity.Actor{ID: 10, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditOwn: true}}
		content := ContentInput{RawContent: "更新正文", SourceFormat: SourceFormatMarkdown}
		if _, err := service.UpdateTopic(ctx, actor, UpdateTopicInput{TopicID: 5, ExpectedRevision: 1, Content: &content}); err != nil {
			t.Fatal(err)
		}
		if store.updatedTopic.ReplaceAttachments {
			t.Fatalf("omitted attachmentIds must preserve references: %#v", store.updatedTopic)
		}
	})

	t.Run("create and update comment", func(t *testing.T) {
		store := &serviceFakeStore{
			nextID:          1,
			topicForComment: TopicSummary{ID: 7, Status: TopicStatusActive},
			commentSummary:  CommentSummary{ID: 8, TopicID: 7, AuthorUserID: 10, Status: CommentStatusActive, CreatedAt: time.Now()},
		}
		service := NewService(ServiceConfig{Store: store})
		actor := identity.Actor{ID: 10, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionPostCreate: true, identity.PermissionPostEditOwn: true,
		}}
		if _, err := service.CreateComment(ctx, actor, CreateCommentInput{
			TopicID: 7, Content: ContentInput{RawContent: "评论正文", SourceFormat: SourceFormatMarkdown, AttachmentIDs: &ids},
		}); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(store.createdComment.AttachmentIDs, []int64{9, 3}) {
			t.Fatalf("create comment ids=%v", store.createdComment.AttachmentIDs)
		}
		if _, err := service.UpdateComment(ctx, actor, UpdateCommentInput{
			CommentID: 8, ExpectedRevision: 1, Content: ContentInput{RawContent: "更新评论", SourceFormat: SourceFormatMarkdown, AttachmentIDs: &ids},
		}); err != nil {
			t.Fatal(err)
		}
		if !store.updatedComment.ReplaceAttachments || !slices.Equal(store.updatedComment.AttachmentIDs, []int64{9, 3}) {
			t.Fatalf("update comment record=%#v", store.updatedComment)
		}
	})
}

func TestServiceCreatesImageOnlyTopicAndComment(t *testing.T) {
	ctx := context.Background()
	attachmentIDs := []int64{42}
	content := ContentInput{
		RawContent:    `{"type":"doc","content":[{"type":"image","attrs":{"src":"/media/attachments/0123456789abcdef0123456789abcdef","alt":"sample.png","attachmentId":42,"attachmentPublicId":"0123456789abcdef0123456789abcdef"}}]}`,
		SourceFormat:  SourceFormatEditorDocument,
		EditorType:    EditorTypeTiptap,
		AttachmentIDs: &attachmentIDs,
	}

	t.Run("topic", func(t *testing.T) {
		store := &serviceFakeStore{nextID: 1}
		service := NewService(ServiceConfig{Store: store})
		created, err := service.CreateTopic(ctx, topicCreator(), CreateTopicInput{
			CategorySlug: "general",
			Title:        "纯图片主题",
			Content:      content,
		})
		if err != nil {
			t.Fatalf("CreateTopic: %v", err)
		}
		if created.Content.PlainText != "" || !strings.Contains(created.Content.HTMLContent, "<img") {
			t.Fatalf("content = %#v", created.Content)
		}
		if !slices.Equal(store.createdTopic.AttachmentIDs, attachmentIDs) {
			t.Fatalf("attachment ids = %v", store.createdTopic.AttachmentIDs)
		}
	})

	t.Run("comment", func(t *testing.T) {
		store := &serviceFakeStore{
			nextID:          1,
			topicForComment: TopicSummary{ID: 7, Status: TopicStatusActive},
		}
		service := NewService(ServiceConfig{Store: store})
		created, err := service.CreateComment(ctx, commentCreator(), CreateCommentInput{
			TopicID: 7,
			Content: content,
		})
		if err != nil {
			t.Fatalf("CreateComment: %v", err)
		}
		if created.Content.PlainText != "" || !strings.Contains(created.Content.HTMLContent, "<img") {
			t.Fatalf("content = %#v", created.Content)
		}
		if !slices.Equal(store.createdComment.AttachmentIDs, attachmentIDs) {
			t.Fatalf("attachment ids = %v", store.createdComment.AttachmentIDs)
		}
	})
}

func TestNormalizeContentAttachmentIDsRejectsInvalidValues(t *testing.T) {
	invalid := []int64{1, 0}
	if _, _, err := normalizeContentAttachmentIDs(&invalid); !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("expected invalid content, got %v", err)
	}
	tooMany := make([]int64, 101)
	for index := range tooMany {
		tooMany[index] = int64(index + 1)
	}
	if _, _, err := normalizeContentAttachmentIDs(&tooMany); !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("expected too many attachments to fail, got %v", err)
	}
}

func TestServiceRevisionHistoryPermissionAndPreview(t *testing.T) {
	store := newServiceFakeStore()
	store.revisionListResult = RevisionList{Items: []ForumRevisionSummary{{
		ID:               1,
		RevisionNo:       2,
		Current:          true,
		Operation:        "edit",
		Origin:           "staff",
		ChangedFields:    []string{"content"},
		CommittedAt:      time.Now(),
		SnapshotComplete: true,
		RestorableFields: []string{"attachments", "content"},
	}}, PerPage: 20}
	store.revisionDetailResult = ForumRevisionDetail{
		ForumRevisionSummary: store.revisionListResult.Items[0],
		RawContent:           "历史 **正文** <script>alert(1)</script>",
		SourceFormat:         SourceFormatMarkdown,
		EditorType:           EditorTypeMarkdown,
		EditorVersion:        "test",
		RenderVersion:        RenderVersion,
		ContentHash:          "hash",
	}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

	denied := identity.Actor{ID: 1, Status: identity.UserStatusActive}
	if _, err := service.ListTopicRevisions(context.Background(), denied, 10, RevisionListInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected topic history denial, got %v", err)
	}
	if store.topicRevisionCalls != 0 {
		t.Fatal("denied topic history request should not reach store")
	}

	topicViewer := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionTopicRevisionViewAny: true,
	}}
	list, err := service.ListTopicRevisions(context.Background(), topicViewer, 10, RevisionListInput{})
	if err != nil {
		t.Fatalf("ListTopicRevisions returned error: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].RevisionNo != 2 {
		t.Fatalf("unexpected topic revision list %#v", list)
	}
	detail, err := service.GetTopicRevision(context.Background(), topicViewer, 10, 2)
	if err != nil {
		t.Fatalf("GetTopicRevision returned error: %v", err)
	}
	if detail.RawContent == "" || detail.Preview == nil || containsUnsafeHTML(detail.Preview.HTMLContent) {
		t.Fatalf("expected source plus safe preview, got %#v", detail)
	}

	postViewer := identity.Actor{ID: 3, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionPostRevisionViewAny: true,
	}}
	if _, err := service.ListCommentRevisions(context.Background(), postViewer, 20, RevisionListInput{}); err != nil {
		t.Fatalf("ListCommentRevisions returned error: %v", err)
	}
	if store.commentRevisionCalls == 0 {
		t.Fatal("expected comment revision store call")
	}
}

func TestServiceAdminContentReadPermissionCombinations(t *testing.T) {
	store := newServiceFakeStore()
	store.adminContentListResult = AdminForumContentList{Items: []AdminForumContentRow{{
		TargetType:      "topic",
		ID:              10,
		CurrentRevision: 1,
	}}}
	store.adminTopicDetailResult = AdminForumTopicDetail{
		AdminForumContentRow: AdminForumContentRow{TargetType: "topic", ID: 10, CurrentRevision: 1},
		Content:              RenderedContent{RawContent: "body", SourceFormat: SourceFormatMarkdown},
	}
	store.adminCommentDetailResult = AdminForumCommentDetail{
		AdminForumContentRow: AdminForumContentRow{TargetType: "comment", ID: 20, TopicID: 10, CurrentRevision: 1},
		Content:              RenderedContent{RawContent: "comment", SourceFormat: SourceFormatMarkdown},
	}
	service := NewService(ServiceConfig{Store: store})
	ctx := context.Background()

	historyOnly := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionTopicRevisionViewAny: true,
		identity.PermissionPostRevisionViewAny:  true,
	}}
	if _, err := service.ListAdminForumTopics(ctx, historyOnly, AdminForumContentListInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected admin.access denial, got %v", err)
	}
	if store.adminTopicCalls != 0 {
		t.Fatal("admin.access denial should not reach store")
	}

	adminOnly := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAdminAccess: true,
	}}
	if _, err := service.ListAdminForumComments(ctx, adminOnly, AdminForumContentListInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected content permission denial, got %v", err)
	}

	topicAdmin := identity.Actor{ID: 3, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAdminAccess:          true,
		identity.PermissionTopicRevisionViewAny: true,
	}}
	if _, err := service.ListAdminForumTopics(ctx, topicAdmin, AdminForumContentListInput{}); err != nil {
		t.Fatalf("ListAdminForumTopics returned error: %v", err)
	}
	if _, err := service.GetAdminForumTopic(ctx, topicAdmin, 10); err != nil {
		t.Fatalf("GetAdminForumTopic returned error: %v", err)
	}

	commentAdmin := identity.Actor{ID: 4, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionAdminAccess:         true,
		identity.PermissionPostRevisionViewAny: true,
	}}
	if _, err := service.ListAdminForumComments(ctx, commentAdmin, AdminForumContentListInput{}); err != nil {
		t.Fatalf("ListAdminForumComments returned error: %v", err)
	}
	if _, err := service.GetAdminForumComment(ctx, commentAdmin, 20); err != nil {
		t.Fatalf("GetAdminForumComment returned error: %v", err)
	}
	if store.adminTopicCalls == 0 || store.adminCommentCalls == 0 {
		t.Fatalf("expected admin content store calls, topic=%d comment=%d", store.adminTopicCalls, store.adminCommentCalls)
	}
}

func TestServiceRestoreRequiresHistoryAndEditAnyAndReusesUpdatePipeline(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusHidden, CurrentRevision: 2, CreatedAt: time.Now().UTC()}
	store.revisionDetailResult = ForumRevisionDetail{ForumRevisionSummary: ForumRevisionSummary{ID: 91, RevisionNo: 1, SnapshotComplete: true}, RawContent: "historical body", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown, EditorVersion: "test", TopicMetadata: &TopicRevisionMetadata{Title: "historical title", CategorySlug: "general", TagSlugs: []string{}}}
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})
	historyOnly := identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicRevisionViewAny: true}}
	if _, err := service.RestoreTopic(context.Background(), historyOnly, 7, 1, RestoreRevisionInput{ExpectedRevision: 2, Reason: "restore"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("history-only restore error=%v", err)
	}
	allowed := identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicRevisionViewAny: true, identity.PermissionTopicEditAny: true}}
	updated, err := service.RestoreTopic(context.Background(), allowed, 7, 1, RestoreRevisionInput{ExpectedRevision: 2, Reason: "restore historical content"})
	if err != nil {
		t.Fatalf("RestoreTopic: %v", err)
	}
	if updated.CurrentRevision != 3 || store.updatedTopic.Operation != RevisionOperationRestore || store.updatedTopic.RestoredFromRevisionID != 91 || store.updatedTopic.Title != "historical title" || store.updatedTopic.Content.RawContent != "historical body" {
		t.Fatalf("restore did not use canonical update record: %#v", store.updatedTopic)
	}
	updatedEvent, ok := publisher.envelope(appevents.TopicUpdated)
	if !publisher.seen(appevents.TopicBeforeUpdate) || !ok || updatedEvent.Payload["operation"] != RevisionOperationRestore || updatedEvent.Payload["restoredFromRevisionNo"] != int64(1) {
		t.Fatalf("restore must traverse filters and emit canonical event: %#v", updatedEvent)
	}
	if _, err := service.RestoreTopic(context.Background(), allowed, 7, 1, RestoreRevisionInput{ExpectedRevision: 2}); !errors.Is(err, ErrRevisionReasonRequired) {
		t.Fatalf("restore without reason error=%v", err)
	}
	if _, err := service.RestoreTopic(context.Background(), allowed, 7, 1, RestoreRevisionInput{ExpectedRevision: 1, Reason: "stale"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale restore error=%v", err)
	}
}

func TestServiceRedactionIsSuperAdminOnly(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})
	member := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicRevisionViewAny: true}}
	if err := service.RedactTopicRevision(context.Background(), member, 7, 1, RedactRevisionInput{ExpectedRevision: 2, Reason: "privacy", Confirmation: "REDACT"}); !errors.Is(err, ErrRevisionRedactionForbidden) {
		t.Fatalf("member redaction error=%v", err)
	}
	admin := identity.Actor{ID: 3, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}}
	if err := service.RedactTopicRevision(context.Background(), admin, 7, 1, RedactRevisionInput{ExpectedRevision: 2, Reason: "privacy", Confirmation: "REDACT"}); err != nil {
		t.Fatalf("super admin redaction: %v", err)
	}
}

func testForumSettings() ForumSettings {
	return defaultForumSettings()
}

type fakeSettingsResolver struct {
	settings ForumSettings
	err      error
}

func (r fakeSettingsResolver) ForumSettings(context.Context) (ForumSettings, error) {
	if r.err != nil {
		return ForumSettings{}, r.err
	}
	return r.settings, nil
}

type fakeSettingsManager struct {
	settings ForumSettings
	updated  UpdateForumSettingsInput
}

func (m *fakeSettingsManager) ForumSettings(context.Context) (ForumSettings, error) {
	return m.settings, nil
}

func (m *fakeSettingsManager) UpdateForumSettings(_ context.Context, _ identity.Actor, input UpdateForumSettingsInput) (ForumSettings, error) {
	m.updated = input
	return m.settings, nil
}

func (m *fakeSettingsManager) ResetForumSettings(context.Context, identity.Actor) (ForumSettings, error) {
	return m.settings, nil
}

type fakeTopicActionProvider struct {
	actions []TopicExtensionAction
	err     error
}

func (p fakeTopicActionProvider) TopicExtensionActions(context.Context) ([]TopicExtensionAction, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.actions, nil
}

type fakeTopicSurfaceProvider struct {
	sidebar    []TopicExtensionSidebarItem
	badges     []TopicExtensionBadge
	listBadges []TopicExtensionBadge
	err        error
}

func (p fakeTopicSurfaceProvider) TopicExtensionSidebar(context.Context) ([]TopicExtensionSidebarItem, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.sidebar, nil
}

func (p fakeTopicSurfaceProvider) TopicExtensionBadges(context.Context) ([]TopicExtensionBadge, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.badges, nil
}

func (p fakeTopicSurfaceProvider) TopicExtensionListBadges(context.Context) ([]TopicExtensionBadge, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.listBadges, nil
}

type fakeCommentActionProvider struct {
	actions []CommentExtensionAction
	err     error
}

func (p fakeCommentActionProvider) CommentExtensionActions(context.Context) ([]CommentExtensionAction, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.actions, nil
}

type serviceFakeStore struct {
	nextID            int64
	createdCategory   CreateCategoryInput
	updatedCategory   UpdateCategoryInput
	deletedCategoryID int64
	deletedGroupID    int64
	deleteCategoryErr error
	deleteGroupErr    error
	createdTag        CreateTagInput
	updatedTag        UpdateTagInput
	// ListCategories 供删除前的默认分类保护判定读取。
	listCategoriesResult        []Category
	listCategoriesIncludeHidden bool
	createdTopic                CreateTopicRecord
	createdComment              CreateCommentRecord
	topicForComment             TopicSummary
	actionTopic                 TopicSummary
	updatedTopic                UpdateTopicRecord
	updatedComment              UpdateCommentRecord
	deletedTopicID              int64
	appliedAction               string
	commentSummary              CommentSummary
	commentSummaryErr           error
	topicEditSource             EditableContentSource
	commentEditSource           EditableContentSource
	topicSourceCalls            int
	commentSourceCalls          int
	resolvedTags                []TopicTagSummary
	resolveTagsErr              error
	resolveTagsCalled           bool
	resolvedTagsInput           ResolveTopicTagsInput
	// GetTopic 可配置返回错误，供评论可见性兜底测试模拟隐藏/不可见主题。
	getTopicErr error
	// ListComments 可配置返回值与调用记录，供分页/view 校验测试断言。
	listCommentsResult CommentList
	listCommentsInput  CommentListInput
	listCommentsCalled bool
	// CountCommentsBefore 可配置返回值，供 ResolveCommentPage 反查页码测试断言。
	countCommentsBefore       int64
	countCommentsBeforeErr    error
	countCommentsBeforeCalled bool
	// 记录最近一次调用的软删可见范围参数，供 viewer 口径断言。
	countCommentsBeforeIncludeDeleted bool
	countCommentsBeforeAuthorID       int64
	// 记录最近一次调用的位置键，供 flat 排序口径断言（必须是发表时间，不是 path_key）。
	countCommentsBeforeCreatedAt time.Time
	countCommentsBeforeID        int64
	listTopicsInput              TopicListInput
	listTopicsResult             TopicList
	// ListCommentReplies 可配置返回值与调用记录，供回复可见性兜底测试断言。
	listCommentRepliesResult []Comment
	listCommentRepliesCalled bool
	lastReplyListInput       CommentReplyListInput
	revisionListResult       RevisionList
	revisionDetailResult     ForumRevisionDetail
	revisionErr              error
	topicRevisionCalls       int
	commentRevisionCalls     int
	adminTopicCalls          int
	adminCommentCalls        int
	adminContentListResult   AdminForumContentList
	adminTopicDetailResult   AdminForumTopicDetail
	adminCommentDetailResult AdminForumCommentDetail
	// existingSlugs 模拟已占用的 slug 集合，供 TopicSlugExists 判重。
	existingSlugs map[string]bool
	// existingTitles 模拟重复标题（小写 key），供 ActiveTopicTitleExists。
	existingTitles     map[string]bool
	autoLockIdleDays   int
	autoLockLimit      int
	autoLockResult     int
	autoLockErr        error
	authorReviewUserID int64
}

func newServiceFakeStore() *serviceFakeStore {
	return &serviceFakeStore{
		nextID:          1,
		topicForComment: TopicSummary{ID: 1, Status: TopicStatusActive},
		actionTopic:     TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive, CurrentRevision: 1},
	}
}

func (s *serviceFakeStore) ListCategories(_ context.Context, includeHidden bool) ([]Category, error) {
	s.listCategoriesIncludeHidden = includeHidden
	return s.listCategoriesResult, nil
}

func (s *serviceFakeStore) ListCategoryGroups(context.Context, bool) ([]CategoryGroup, error) {
	return nil, nil
}

func (s *serviceFakeStore) ListTags(context.Context, bool) ([]Tag, error) {
	return nil, nil
}

func (s *serviceFakeStore) CreateCategoryGroup(_ context.Context, input CreateCategoryGroupInput) (CategoryGroup, error) {
	return CategoryGroup{ID: 1, Slug: input.Slug, Name: input.Name, Description: input.Description, Visibility: input.Visibility, Position: input.Position}, nil
}

func (s *serviceFakeStore) UpdateCategoryGroup(_ context.Context, input UpdateCategoryGroupInput) (CategoryGroup, error) {
	return CategoryGroup{ID: input.ID, Slug: "default", Name: "默认版块", Visibility: "public"}, nil
}

func (s *serviceFakeStore) DeleteCategoryGroup(_ context.Context, groupID int64) (CategoryGroup, error) {
	s.deletedGroupID = groupID
	if s.deleteGroupErr != nil {
		return CategoryGroup{}, s.deleteGroupErr
	}
	return CategoryGroup{ID: groupID, Slug: "default", Name: "默认版块", Visibility: "public"}, nil
}

func (s *serviceFakeStore) CreateCategory(_ context.Context, input CreateCategoryInput) (Category, error) {
	s.createdCategory = input
	return Category{ID: 1, GroupID: input.GroupID, Slug: input.Slug, Name: input.Name, Description: input.Description, Visibility: input.Visibility, Position: input.Position, DefaultSort: input.DefaultSort, Icon: input.Icon, IconColor: input.IconColor}, nil
}

func (s *serviceFakeStore) UpdateCategory(_ context.Context, input UpdateCategoryInput) (Category, error) {
	s.updatedCategory = input
	item := Category{ID: input.ID, GroupID: 1, Slug: "general", Name: "综合讨论", Visibility: "public", DefaultSort: "latest"}
	if input.Icon != nil {
		item.Icon = *input.Icon
	}
	if input.IconColor != nil {
		item.IconColor = *input.IconColor
	}
	return item, nil
}

func (s *serviceFakeStore) DeleteCategory(_ context.Context, categoryID int64) (Category, error) {
	s.deletedCategoryID = categoryID
	if s.deleteCategoryErr != nil {
		return Category{}, s.deleteCategoryErr
	}
	return Category{ID: categoryID, GroupID: 1, Slug: "support", Name: "Support", Visibility: "public"}, nil
}

func (s *serviceFakeStore) CreateTag(_ context.Context, input CreateTagInput) (Tag, error) {
	s.createdTag = input
	return Tag{ID: 1, Slug: input.Slug, Name: input.Name, Description: input.Description, Status: input.Status, Icon: input.Icon, IconColor: input.IconColor}, nil
}

func (s *serviceFakeStore) UpdateTag(_ context.Context, input UpdateTagInput) (Tag, error) {
	s.updatedTag = input
	item := Tag{ID: input.ID, Slug: "go", Name: "Go", Status: TagStatusActive}
	if input.Icon != nil {
		item.Icon = *input.Icon
	}
	if input.IconColor != nil {
		item.IconColor = *input.IconColor
	}
	return item, nil
}

func (s *serviceFakeStore) ListTopics(_ context.Context, input TopicListInput) (TopicList, error) {
	s.listTopicsInput = input
	if s.listTopicsResult.Items != nil || s.listTopicsResult.Total > 0 {
		return s.listTopicsResult, nil
	}
	return TopicList{}, nil
}

// ListAllTopicIDs 默认返回一个可预测的 ID 列表，供重建相关测试断言。
func (s *serviceFakeStore) ListAllTopicIDs(context.Context) ([]int64, error) {
	return []int64{1, 2, 3}, nil
}

func (s *serviceFakeStore) GetTopic(context.Context, int64) (TopicDetail, error) {
	if s.getTopicErr != nil {
		return TopicDetail{}, s.getTopicErr
	}
	return TopicDetail{}, nil
}

func (s *serviceFakeStore) GetTopicBySlug(context.Context, string) (TopicDetail, error) {
	return TopicDetail{}, nil
}

func (s *serviceFakeStore) ListAuthorReviewItems(_ context.Context, authorUserID int64) (AuthorReviewList, error) {
	s.authorReviewUserID = authorUserID
	return AuthorReviewList{Items: []AuthorReviewItem{{TargetType: "topic", TargetID: 9, Status: TopicStatusPending}}}, nil
}

func (s *serviceFakeStore) TopicSlugExists(_ context.Context, slug string, excludeTopicID int64) (bool, error) {
	if s.existingSlugs == nil {
		return false, nil
	}
	return s.existingSlugs[slug], nil
}

func (s *serviceFakeStore) ActiveTopicTitleExists(_ context.Context, title string, excludeTopicID int64) (bool, error) {
	if s.existingTitles == nil {
		return false, nil
	}
	return s.existingTitles[strings.ToLower(strings.TrimSpace(title))], nil
}

func (s *serviceFakeStore) AutoLockIdleTopics(_ context.Context, idleDays int, limit int) (int, error) {
	s.autoLockIdleDays = idleDays
	s.autoLockLimit = limit
	return s.autoLockResult, s.autoLockErr
}

func (s *serviceFakeStore) CreateTopic(_ context.Context, input CreateTopicRecord) (TopicDetail, error) {
	tags := input.Tags
	if len(tags) == 0 && len(input.TagSlugs) > 0 {
		var err error
		tags, err = s.topicTags(input.TagSlugs)
		if err != nil {
			return TopicDetail{}, err
		}
		input.Tags = tags
	}
	input.ID = s.nextID
	s.nextID++
	input.Content.ID = s.nextID
	s.nextID++
	s.createdTopic = input
	return TopicDetail{
		TopicSummary: TopicSummary{
			ID:           input.ID,
			CategoryID:   input.CategoryID,
			AuthorUserID: input.AuthorUserID,
			Title:        input.Title,
			Slug:         input.Slug,
			Status:       input.Status,
			Tags:         tags,
		},
		Content: ToPublicRenderedContent(input.Content),
	}, nil
}

func (s *serviceFakeStore) ResolveTopicTags(_ context.Context, input ResolveTopicTagsInput) ([]TopicTagSummary, error) {
	s.resolveTagsCalled = true
	s.resolvedTagsInput = input
	return s.topicTags(input.Slugs)
}

func (s *serviceFakeStore) topicTags(slugs []string) ([]TopicTagSummary, error) {
	if s.resolveTagsErr != nil {
		return nil, s.resolveTagsErr
	}
	if s.resolvedTags != nil {
		return s.resolvedTags, nil
	}
	items := make([]TopicTagSummary, 0, len(slugs))
	for index, slug := range slugs {
		items = append(items, TopicTagSummary{
			ID:     int64(index + 1),
			Slug:   slug,
			Name:   slug,
			Status: TagStatusActive,
		})
	}
	return items, nil
}

func (s *serviceFakeStore) GetTopicForComment(context.Context, int64) (TopicSummary, error) {
	return s.topicForComment, nil
}

func (s *serviceFakeStore) GetTopicForAction(context.Context, int64) (TopicSummary, error) {
	return s.actionTopic, nil
}

func (s *serviceFakeStore) GetTopicEditSource(context.Context, int64) (EditableContentSource, error) {
	s.topicSourceCalls++
	return s.topicEditSource, nil
}

func (s *serviceFakeStore) UpdateTopic(_ context.Context, input UpdateTopicRecord) (TopicDetail, error) {
	s.updatedTopic = input
	title := input.Title
	if title == "" {
		title = "原标题"
	}
	status := TopicStatusActive
	if input.RequeuePending {
		status = TopicStatusPending
	}
	return TopicDetail{
		TopicSummary:  TopicSummary{ID: input.TopicID, Title: title, Status: status, CurrentRevision: input.ExpectedRevision + 1},
		Content:       ToPublicRenderedContent(input.Content),
		UpdateApplied: true,
	}, nil
}

func (s *serviceFakeStore) DeleteTopic(_ context.Context, topicID int64) (TopicDetail, error) {
	s.deletedTopicID = topicID
	return TopicDetail{
		TopicSummary: TopicSummary{ID: topicID, Status: TopicStatusDeleted, Excerpt: "secret"},
		Content:      PublicRenderedContent{HTMLContent: "<p>secret</p>", PlainText: "secret"},
	}, nil
}

func (s *serviceFakeStore) ApplyTopicAction(_ context.Context, input TopicLifecycleInput) (TopicLifecycleRecord, error) {
	s.appliedAction = input.Action
	status := TopicStatusActive
	isPinned := false
	switch input.Action {
	case TopicActionHide:
		status = TopicStatusHidden
	case TopicActionLock:
		status = TopicStatusLocked
	case TopicActionPin, TopicActionUnpin:
		status = s.actionTopic.Status
		isPinned = input.Action == TopicActionPin
	}
	return TopicLifecycleRecord{TopicID: input.TopicID, Status: status, IsPinned: isPinned}, nil
}

func (s *serviceFakeStore) CreateComment(_ context.Context, input CreateCommentRecord) (Comment, error) {
	s.createdComment = input
	input.ID = s.nextID
	s.nextID++
	input.Content.ID = s.nextID
	s.nextID++
	position := CommentPositionForInsert(input.ID, input.Parent)
	return Comment{
		ID:            input.ID,
		TopicID:       input.TopicID,
		AuthorUserID:  input.AuthorUserID,
		ParentID:      input.ParentID,
		RootCommentID: position.RootCommentID,
		PathKey:       position.PathKey,
		Depth:         position.Depth,
		Status:        input.Status,
		Content:       ToPublicRenderedContent(input.Content),
	}, nil
}

func (s *serviceFakeStore) LatestAuthorTopicCreatedAt(context.Context, int64) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *serviceFakeStore) CountAuthorTopicsSince(context.Context, int64, time.Time) (int64, error) {
	return 0, nil
}
func (s *serviceFakeStore) LatestAuthorCommentCreatedAt(context.Context, int64) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *serviceFakeStore) CountAuthorCommentsSince(context.Context, int64, time.Time) (int64, error) {
	return 0, nil
}

func (s *serviceFakeStore) GetCommentSummary(context.Context, int64) (CommentSummary, error) {
	return s.commentSummary, s.commentSummaryErr
}

func (s *serviceFakeStore) GetCommentEditSource(context.Context, int64) (EditableContentSource, error) {
	s.commentSourceCalls++
	return s.commentEditSource, nil
}

func (s *serviceFakeStore) CountCommentsBefore(_ context.Context, _ int64, createdAt time.Time, id int64, includeDeleted bool, deletedAuthorUserID int64) (int64, error) {
	s.countCommentsBeforeCalled = true
	s.countCommentsBeforeIncludeDeleted = includeDeleted
	s.countCommentsBeforeAuthorID = deletedAuthorUserID
	s.countCommentsBeforeCreatedAt = createdAt
	s.countCommentsBeforeID = id
	return s.countCommentsBefore, s.countCommentsBeforeErr
}

func (s *serviceFakeStore) UpdateComment(_ context.Context, input UpdateCommentRecord) (Comment, error) {
	s.updatedComment = input
	status := CommentStatusActive
	if input.RequeuePending {
		status = CommentStatusPending
	}
	return Comment{
		ID:              input.CommentID,
		AuthorUserID:    s.commentSummary.AuthorUserID,
		Status:          status,
		CurrentRevision: input.ExpectedRevision + 1,
		Content:         ToPublicRenderedContent(input.Content),
		UpdateApplied:   true,
	}, nil
}

func (s *serviceFakeStore) DeleteComment(context.Context, int64) (Comment, error) {
	return Comment{Status: CommentStatusDeleted, Content: PublicRenderedContent{
		HTMLContent: "<p>secret</p>", PlainText: "secret",
	}}, nil
}

func (s *serviceFakeStore) ListComments(_ context.Context, input CommentListInput) (CommentList, error) {
	s.listCommentsCalled = true
	s.listCommentsInput = input
	return s.listCommentsResult, nil
}

func (s *serviceFakeStore) ListCommentReplies(_ context.Context, input CommentReplyListInput) ([]Comment, error) {
	s.listCommentRepliesCalled = true
	s.lastReplyListInput = input
	return s.listCommentRepliesResult, nil
}

func (s *serviceFakeStore) ListTopicRevisions(context.Context, int64, RevisionListInput) (RevisionList, error) {
	s.topicRevisionCalls++
	if s.revisionErr != nil {
		return RevisionList{}, s.revisionErr
	}
	return s.revisionListResult, nil
}

func (s *serviceFakeStore) ListTopicContributionTimeline(_ context.Context, topicID int64, input RevisionListInput) (TopicContributionTimeline, error) {
	if topicID <= 0 {
		return TopicContributionTimeline{}, ErrTopicNotFound
	}
	if s.revisionErr != nil {
		return TopicContributionTimeline{}, s.revisionErr
	}
	items := make([]TopicContributionEvent, 0, len(s.revisionListResult.Items))
	for _, item := range s.revisionListResult.Items {
		items = append(items, publicContributionEvent(item))
	}
	perPage := input.PerPage
	if perPage <= 0 {
		perPage = 20
	}
	return TopicContributionTimeline{
		Items:      items,
		PerPage:    perPage,
		HasMore:    s.revisionListResult.HasMore,
		NextCursor: s.revisionListResult.NextCursor,
	}, nil
}

func (s *serviceFakeStore) GetTopicRevision(context.Context, int64, int64) (ForumRevisionDetail, error) {
	s.topicRevisionCalls++
	if s.revisionErr != nil {
		return ForumRevisionDetail{}, s.revisionErr
	}
	return s.revisionDetailResult, nil
}

func (s *serviceFakeStore) ListCommentRevisions(context.Context, int64, RevisionListInput) (RevisionList, error) {
	s.commentRevisionCalls++
	if s.revisionErr != nil {
		return RevisionList{}, s.revisionErr
	}
	return s.revisionListResult, nil
}

func (s *serviceFakeStore) GetCommentRevision(context.Context, int64, int64) (ForumRevisionDetail, error) {
	s.commentRevisionCalls++
	if s.revisionErr != nil {
		return ForumRevisionDetail{}, s.revisionErr
	}
	return s.revisionDetailResult, nil
}

func (s *serviceFakeStore) RedactTopicRevision(context.Context, RevisionRedactionRecord) error {
	return nil
}

func (s *serviceFakeStore) RedactCommentRevision(context.Context, RevisionRedactionRecord) error {
	return nil
}

func (s *serviceFakeStore) ListAdminForumTopics(context.Context, AdminForumContentListInput) (AdminForumContentList, error) {
	s.adminTopicCalls++
	return s.adminContentListResult, nil
}

func (s *serviceFakeStore) GetAdminForumTopic(context.Context, int64) (AdminForumTopicDetail, error) {
	s.adminTopicCalls++
	return s.adminTopicDetailResult, nil
}

func (s *serviceFakeStore) ListAdminForumComments(context.Context, AdminForumContentListInput) (AdminForumContentList, error) {
	s.adminCommentCalls++
	return s.adminContentListResult, nil
}

func (s *serviceFakeStore) GetAdminForumComment(context.Context, int64) (AdminForumCommentDetail, error) {
	s.adminCommentCalls++
	return s.adminCommentDetailResult, nil
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// TestServiceListCommentsDefaultsToTreeView 验证空 view 默认为 tree 并透传给 Store。
func TestServiceListCommentsDefaultsToTreeView(t *testing.T) {
	store := newServiceFakeStore()
	store.listCommentsResult = CommentList{Items: []Comment{{ID: 1}}, Total: 1, View: "tree"}
	service := NewService(ServiceConfig{Store: store})

	result, err := service.ListComments(context.Background(), CommentListInput{TopicID: 10})
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if !store.listCommentsCalled {
		t.Fatal("expected ListComments to be called on store")
	}
	if store.listCommentsInput.View != "tree" {
		t.Fatalf("expected default view 'tree', got %q", store.listCommentsInput.View)
	}
	if result.View != "tree" {
		t.Fatalf("expected result view 'tree', got %q", result.View)
	}
}

// TestServiceListCommentsRejectsInvalidView 验证非法 view 值被拒绝。
func TestServiceListCommentsRejectsInvalidView(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store})

	if _, err := service.ListComments(context.Background(), CommentListInput{TopicID: 1, View: "nested"}); err == nil {
		t.Fatal("expected error for invalid view, got nil")
	}
	if store.listCommentsCalled {
		t.Fatal("expected store.ListComments NOT to be called for invalid view")
	}
}

// TestServiceListCommentsPassesFlatView 验证 flat view 透传。
func TestServiceListCommentsPassesFlatView(t *testing.T) {
	store := newServiceFakeStore()
	store.listCommentsResult = CommentList{Items: []Comment{{ID: 1}, {ID: 2}}, Total: 2, View: "flat"}
	service := NewService(ServiceConfig{Store: store})

	result, err := service.ListComments(context.Background(), CommentListInput{TopicID: 5, View: "flat"})
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if store.listCommentsInput.View != "flat" {
		t.Fatalf("expected view 'flat', got %q", store.listCommentsInput.View)
	}
	if result.View != "flat" || result.Total != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// TestServiceListCommentsBlocksHiddenTopic 验证可见性兜底：
// 当主题不可见（隐藏/删除/非公开分类）时，GetTopic 返回 ErrTopicNotFound，
// 评论列表应直接返回该错误，且不调用 store.ListComments，避免泄漏隐藏主题内容。
func TestServiceListCommentsBlocksHiddenTopic(t *testing.T) {
	store := newServiceFakeStore()
	store.getTopicErr = ErrTopicNotFound
	service := NewService(ServiceConfig{Store: store})

	_, err := service.ListComments(context.Background(), CommentListInput{TopicID: 10, View: "tree"})
	if !errors.Is(err, ErrTopicNotFound) {
		t.Fatalf("expected ErrTopicNotFound, got %v", err)
	}
	if store.listCommentsCalled {
		t.Fatal("expected store.ListComments NOT to be called for hidden topic")
	}
}

// TestServiceListCommentsAllowsVisibleTopic 验证可见主题正常透传到评论查询。
func TestServiceListCommentsAllowsVisibleTopic(t *testing.T) {
	store := newServiceFakeStore()
	store.listCommentsResult = CommentList{Items: []Comment{{ID: 1}}, Total: 1, View: "tree"}
	service := NewService(ServiceConfig{Store: store})

	result, err := service.ListComments(context.Background(), CommentListInput{TopicID: 10, View: "tree"})
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if !store.listCommentsCalled {
		t.Fatal("expected store.ListComments to be called for visible topic")
	}
	if result.Total != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestServiceCreateTopicBlocksDuplicateTitleWhenPolicyBlock(t *testing.T) {
	store := newServiceFakeStore()
	store.existingTitles = map[string]bool{"hello": true}
	settings := testForumSettings()
	settings.DuplicateTitlePolicy = "block"
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}
	_, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		Title: "Hello", Content: ContentInput{SourceFormat: SourceFormatMarkdown, RawContent: "body"},
	})
	if !errors.Is(err, ErrDuplicateTitle) {
		t.Fatalf("expected ErrDuplicateTitle, got %v", err)
	}
}

func TestServiceCreateTopicAllowsDuplicateTitleWhenPolicyOff(t *testing.T) {
	store := newServiceFakeStore()
	store.existingTitles = map[string]bool{"hello": true}
	settings := testForumSettings()
	settings.DuplicateTitlePolicy = "off"
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}
	if _, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		Title: "Hello", Content: ContentInput{SourceFormat: SourceFormatMarkdown, RawContent: "body"},
	}); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
}

func TestServiceAuthorCanLockWhenAllowAuthorCloseReplies(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
	settings := testForumSettings()
	settings.AllowAuthorCloseReplies = true
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	author := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionTopicEditOwn: true,
	}}
	for _, action := range []string{TopicActionLock, TopicActionUnlock} {
		if _, err := service.ApplyTopicAction(context.Background(), author, TopicLifecycleInput{
			TopicID: 7, Action: action,
		}); err != nil {
			t.Fatalf("author %s should succeed: %v", action, err)
		}
	}
}

func TestServiceAuthorCannotLockWithoutTopicEditOwn(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
	settings := testForumSettings()
	settings.AllowAuthorCloseReplies = true
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	author := identity.Actor{ID: 12, Status: identity.UserStatusActive}
	if _, err := service.ApplyTopicAction(context.Background(), author, TopicLifecycleInput{
		TopicID: 7, Action: TopicActionLock,
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("author without topic.edit_own must be denied, got %v", err)
	}
}

func TestServiceAuthorCannotLockWhenAllowAuthorCloseRepliesDisabled(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
	settings := testForumSettings()
	settings.AllowAuthorCloseReplies = false
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	author := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionTopicEditOwn: true,
	}}
	if _, err := service.ApplyTopicAction(context.Background(), author, TopicLifecycleInput{
		TopicID: 7, Action: TopicActionLock,
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestServiceModeratorCanLockRegardlessOfAuthorSetting(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: TopicStatusActive}
	settings := testForumSettings()
	settings.AllowAuthorCloseReplies = false
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	mod := identity.Actor{
		ID: 99, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicLock: true},
	}
	if _, err := service.ApplyTopicAction(context.Background(), mod, TopicLifecycleInput{
		TopicID: 7, Action: TopicActionLock,
	}); err != nil {
		t.Fatalf("moderator lock should succeed: %v", err)
	}
}

func TestServiceCreateCommentSkipsMentionsWhenDisabled(t *testing.T) {
	store := newServiceFakeStore()
	store.topicForComment = TopicSummary{ID: 1, Status: TopicStatusActive}
	settings := testForumSettings()
	settings.MentionsEnabled = false
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	actor := identity.Actor{
		ID: 3, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostCreate: true},
	}
	if _, err := service.CreateComment(context.Background(), actor, CreateCommentInput{
		TopicID: 1,
		Content: ContentInput{SourceFormat: SourceFormatMarkdown, RawContent: "hi @alice"},
	}); err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if len(store.createdComment.MentionedUsernames) != 0 {
		t.Fatalf("expected no mentions when disabled, got %#v", store.createdComment.MentionedUsernames)
	}
}

func TestServiceCreateTopicSkipsMentionsWhenDisabled(t *testing.T) {
	store := newServiceFakeStore()
	settings := testForumSettings()
	settings.MentionsEnabled = false
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	actor := identity.Actor{
		ID: 3, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}
	if _, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		CategorySlug: "general",
		Title:        "Topic mention disabled",
		Content:      ContentInput{SourceFormat: SourceFormatMarkdown, RawContent: "hi @alice"},
	}); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	if len(store.createdTopic.MentionedUsernames) != 0 {
		t.Fatalf("expected no mentions when disabled, got %#v", store.createdTopic.MentionedUsernames)
	}
}

func TestServiceCreateTopicParsesMentionsWhenEnabled(t *testing.T) {
	store := newServiceFakeStore()
	settings := testForumSettings()
	settings.MentionsEnabled = true
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	actor := identity.Actor{
		ID: 3, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}
	if _, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		CategorySlug: "general",
		Title:        "Topic mention enabled",
		Content:      ContentInput{SourceFormat: SourceFormatMarkdown, RawContent: "hi @alice and `@ignored`"},
	}); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	if got := store.createdTopic.MentionedUsernames; len(got) != 1 || got[0] != "alice" {
		t.Fatalf("unexpected topic mentions: %#v", got)
	}
}

func TestServiceCreateCommentParsesMentionsWhenEnabled(t *testing.T) {
	store := newServiceFakeStore()
	store.topicForComment = TopicSummary{ID: 1, Status: TopicStatusActive}
	settings := testForumSettings()
	settings.MentionsEnabled = true
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	actor := identity.Actor{
		ID: 3, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostCreate: true},
	}
	if _, err := service.CreateComment(context.Background(), actor, CreateCommentInput{
		TopicID: 1,
		Content: ContentInput{SourceFormat: SourceFormatMarkdown, RawContent: "hi @alice"},
	}); err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if len(store.createdComment.MentionedUsernames) == 0 {
		t.Fatal("expected mentions when enabled")
	}
}

func TestFilterSoftDeletedCommentsAuthorAndStaff(t *testing.T) {
	deleted := Comment{
		ID: 2, AuthorUserID: 5, Status: CommentStatusDeleted,
		Content: PublicRenderedContent{PlainText: "secret", HTMLContent: "<p>secret</p>"},
	}
	items := []Comment{{ID: 1, Status: CommentStatusActive}, deleted}
	author := identity.Actor{ID: 5, Status: identity.UserStatusActive}
	got := filterSoftDeletedComments(items, "author_and_staff", author)
	if len(got) != 2 {
		t.Fatalf("author should see tombstone, got %d", len(got))
	}
	if got[1].Content.PlainText != "" || got[1].Content.HTMLContent != "" {
		t.Fatalf("tombstone must not leak content: %+v", got[1].Content)
	}
	stranger := identity.Actor{ID: 9, Status: identity.UserStatusActive}
	got = filterSoftDeletedComments(items, "author_and_staff", stranger)
	if len(got) != 1 {
		t.Fatalf("stranger should not see deleted, got %d", len(got))
	}
	staff := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostDeleteAny: true},
	}
	got = filterSoftDeletedComments(items, "staff_only", staff)
	if len(got) != 2 {
		t.Fatalf("staff should see under staff_only, got %d", len(got))
	}
	got = filterSoftDeletedComments(items, "hidden", staff)
	if len(got) != 1 {
		t.Fatalf("hidden hides from everyone including staff, got %d", len(got))
	}
}

func TestDeleteResponsesNeverReturnDeletedBody(t *testing.T) {
	store := newServiceFakeStore()
	store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 5, Status: TopicStatusActive}
	store.commentSummary = CommentSummary{ID: 8, TopicID: 7, AuthorUserID: 5, Status: CommentStatusActive}
	service := NewService(ServiceConfig{Store: store})
	moderator := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionTopicDeleteAny: true,
		identity.PermissionPostDeleteAny:  true,
	}}

	deletedTopic, err := service.DeleteTopic(context.Background(), moderator, 7)
	if err != nil {
		t.Fatal(err)
	}
	if deletedTopic.Excerpt != "" || deletedTopic.Content.HTMLContent != "" || deletedTopic.Content.PlainText != "" {
		t.Fatalf("topic delete response leaked body: %#v", deletedTopic)
	}
	deletedComment, err := service.DeleteComment(context.Background(), moderator, 8)
	if err != nil {
		t.Fatal(err)
	}
	if deletedComment.Content.HTMLContent != "" || deletedComment.Content.PlainText != "" {
		t.Fatalf("comment delete response leaked body: %#v", deletedComment)
	}
}

func TestListCommentsScopesDeletedRowsByViewer(t *testing.T) {
	deleted := Comment{
		ID: 2, AuthorUserID: 5, Status: CommentStatusDeleted,
		Content: PublicRenderedContent{HTMLContent: "<p>secret</p>", PlainText: "secret"},
	}
	tests := []struct {
		name             string
		visibility       string
		viewer           identity.Actor
		wantInclude      bool
		wantAuthorUserID int64
		wantItems        int
	}{
		{name: "anonymous", visibility: "author_and_staff", wantItems: 0},
		{name: "ordinary member", visibility: "author_and_staff", viewer: identity.Actor{ID: 9, Status: identity.UserStatusActive}, wantInclude: true, wantAuthorUserID: 9, wantItems: 0},
		{name: "author", visibility: "author_and_staff", viewer: identity.Actor{ID: 5, Status: identity.UserStatusActive}, wantInclude: true, wantAuthorUserID: 5, wantItems: 1},
		{name: "moderator", visibility: "staff_only", viewer: identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostDeleteAny: true}}, wantInclude: true, wantItems: 1},
		{name: "hidden from moderator", visibility: "hidden", viewer: identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostDeleteAny: true}}, wantItems: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newServiceFakeStore()
			store.listCommentsResult = CommentList{Items: []Comment{deleted}, Total: 1, View: "flat"}
			settings := testForumSettings()
			settings.SoftDeleteVisibility = tc.visibility
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})

			list, err := service.ListComments(context.Background(), CommentListInput{TopicID: 1, View: "flat", Viewer: tc.viewer})
			if err != nil {
				t.Fatal(err)
			}
			if store.listCommentsInput.IncludeDeleted != tc.wantInclude || store.listCommentsInput.DeletedAuthorUserID != tc.wantAuthorUserID {
				t.Fatalf("query scope = (%v,%d), want (%v,%d)", store.listCommentsInput.IncludeDeleted, store.listCommentsInput.DeletedAuthorUserID, tc.wantInclude, tc.wantAuthorUserID)
			}
			if len(list.Items) != tc.wantItems {
				t.Fatalf("items=%d want=%d", len(list.Items), tc.wantItems)
			}
			if len(list.Items) > 0 && (list.Items[0].Content.HTMLContent != "" || list.Items[0].Content.PlainText != "") {
				t.Fatalf("deleted body leaked: %#v", list.Items[0].Content)
			}
		})
	}
}

func TestListCommentRepliesUsesAuthorDeletedScope(t *testing.T) {
	store := newServiceFakeStore()
	store.commentSummary = CommentSummary{ID: 42, TopicID: 1, Status: CommentStatusActive}
	settings := testForumSettings()
	settings.SoftDeleteVisibility = "author_and_staff"
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
	viewer := identity.Actor{ID: 7, Status: identity.UserStatusActive}

	if _, err := service.ListCommentRepliesForViewer(context.Background(), 42, viewer); err != nil {
		t.Fatal(err)
	}
	if !store.lastReplyListInput.IncludeDeleted || store.lastReplyListInput.DeletedAuthorUserID != 7 {
		t.Fatalf("unexpected reply query scope: %#v", store.lastReplyListInput)
	}
}

func TestApplyCommentEditMarks(t *testing.T) {
	editedAt := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	items := []Comment{{
		ID: 1, ContentEdited: true, EditedAt: &editedAt,
	}}
	marked := applyCommentEditMarks(items, true)
	if !marked[0].Edited || marked[0].EditedAt == nil {
		t.Fatal("expected edited mark and accepted revision time")
	}
	unmarked := applyCommentEditMarks(items, false)
	if unmarked[0].Edited || unmarked[0].EditedAt != nil {
		t.Fatal("expected edit metadata to be hidden when disabled")
	}
}

func TestApplyTopicEditMarkMasksAcceptedRevisionTime(t *testing.T) {
	editedAt := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	topic := TopicDetail{EditedAt: &editedAt}
	marked := applyTopicEditMark(topic, true)
	if !marked.Edited || marked.EditedAt == nil {
		t.Fatal("expected edited mark and accepted revision time")
	}
	unmarked := applyTopicEditMark(topic, false)
	if unmarked.Edited || unmarked.EditedAt != nil {
		t.Fatal("expected edit metadata to be hidden when disabled")
	}
}

func TestServiceTopicEditMarkUsesStoredContentFactAndSetting(t *testing.T) {
	for _, show := range []bool{false, true} {
		store := newServiceFakeStore()
		store.listTopicsResult = TopicList{Items: []TopicSummary{{ID: 1, ContentEdited: true}}}
		settings := testForumSettings()
		settings.ShowTopicEditMark = show
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}, Publisher: nil})
		list, err := service.ListTopics(context.Background(), TopicListInput{})
		if err != nil {
			t.Fatal(err)
		}
		if list.Items[0].Edited != show {
			t.Fatalf("show=%v edited=%v", show, list.Items[0].Edited)
		}
	}
}

func TestServiceAutoLockIdleTopicsDelegates(t *testing.T) {
	store := newServiceFakeStore()
	store.autoLockResult = 4
	service := NewService(ServiceConfig{Store: store})
	n, err := service.AutoLockIdleTopics(context.Background(), 14, 50)
	if err != nil || n != 4 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if store.autoLockIdleDays != 14 || store.autoLockLimit != 50 {
		t.Fatalf("unexpected store call: idle=%d limit=%d", store.autoLockIdleDays, store.autoLockLimit)
	}
	n, err = service.AutoLockIdleTopics(context.Background(), 0, 50)
	if err != nil || n != 0 {
		t.Fatalf("disabled should no-op: n=%d err=%v", n, err)
	}
}

// TestServiceListCommentRepliesBlocksMissingComment 验证回复路径兜底：
// 父评论不存在时返回 ErrCommentNotFound，且不调用 store.ListCommentReplies。
func TestServiceListCommentRepliesBlocksMissingComment(t *testing.T) {
	store := newServiceFakeStore()
	store.commentSummaryErr = ErrCommentNotFound
	service := NewService(ServiceConfig{Store: store})

	_, err := service.ListCommentReplies(context.Background(), 42)
	if !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("expected ErrCommentNotFound, got %v", err)
	}
	if store.listCommentRepliesCalled {
		t.Fatal("expected store.ListCommentReplies NOT to be called for missing comment")
	}
}

// TestServiceListCommentRepliesBlocksHiddenTopic 验证回复路径兜底：
// 父评论存在但其所属主题不可见时返回 ErrTopicNotFound，
// 避免通过枚举 commentID 读取隐藏主题的回复。
func TestServiceListCommentRepliesBlocksHiddenTopic(t *testing.T) {
	store := newServiceFakeStore()
	store.commentSummary = CommentSummary{ID: 42, TopicID: 10, Status: CommentStatusActive}
	store.getTopicErr = ErrTopicNotFound
	service := NewService(ServiceConfig{Store: store})

	_, err := service.ListCommentReplies(context.Background(), 42)
	if !errors.Is(err, ErrTopicNotFound) {
		t.Fatalf("expected ErrTopicNotFound, got %v", err)
	}
	if store.listCommentRepliesCalled {
		t.Fatal("expected store.ListCommentReplies NOT to be called for hidden topic")
	}
}

// TestServiceListCommentRepliesAllowsVisibleTopic 验证可见主题的回复正常返回。
func TestServiceListCommentRepliesAllowsVisibleTopic(t *testing.T) {
	store := newServiceFakeStore()
	store.commentSummary = CommentSummary{ID: 42, TopicID: 10, Status: CommentStatusActive}
	store.listCommentRepliesResult = []Comment{{ID: 43}, {ID: 44}}
	service := NewService(ServiceConfig{Store: store})

	items, err := service.ListCommentReplies(context.Background(), 42)
	if err != nil {
		t.Fatalf("ListCommentReplies returned error: %v", err)
	}
	if !store.listCommentRepliesCalled {
		t.Fatal("expected store.ListCommentReplies to be called for visible topic")
	}
	if len(items) != 2 {
		t.Fatalf("unexpected items: %+v", items)
	}
}

// TestServiceResolveCommentPage 按 flat 排序下排在前面的可见评论数计算页码。
// perPage 默认 20；边界：0 条在前 → page 1，刚好 20 条 → page 2（第 21 条评论在第二页）；
// 超过 MaxTopicPage 的结果钳制到 MaxTopicPage（与 ListComments 的深翻页 clamp 同口径）。
func TestServiceResolveCommentPage(t *testing.T) {
	cases := []struct {
		name        string
		before      int64
		wantPage    int
		wantPerPage int
	}{
		{"第一条评论在第一页", 0, 1, 20},
		{"第20条评论仍在第一页", 19, 1, 20},
		{"第21条评论在第二页", 20, 2, 20},
		{"第40条评论仍在第二页", 39, 2, 20},
		{"第41条评论在第三页", 40, 3, 20},
		{"超过MaxTopicPage钳制到上限", int64(MaxTopicPage) * 20, MaxTopicPage, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newServiceFakeStore()
			publishedAt := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
			store.commentSummary = CommentSummary{
				ID: 99, TopicID: 10, Status: CommentStatusActive,
				PathKey: "000000000099", CreatedAt: publishedAt,
			}
			store.countCommentsBefore = tc.before
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

			page, perPage, err := service.ResolveCommentPage(context.Background(), 10, 99, identity.Actor{})
			if err != nil {
				t.Fatalf("ResolveCommentPage returned error: %v", err)
			}
			if page != tc.wantPage {
				t.Fatalf("page = %d, want %d (before=%d)", page, tc.wantPage, tc.before)
			}
			if perPage != tc.wantPerPage {
				t.Fatalf("perPage = %d, want %d", perPage, tc.wantPerPage)
			}
			if !store.countCommentsBeforeCalled {
				t.Fatal("expected store.CountCommentsBefore to be called")
			}
			// flat 位置键必须是发表时间：用 path_key 计数会让楼层随回复漂移。
			if !store.countCommentsBeforeCreatedAt.Equal(publishedAt) || store.countCommentsBeforeID != 99 {
				t.Fatalf("position key = (%s, %d), want (%s, 99)",
					store.countCommentsBeforeCreatedAt, store.countCommentsBeforeID, publishedAt)
			}
			// 匿名 viewer：软删墓碑不占位，计数范围应为 active-only。
			if store.countCommentsBeforeIncludeDeleted {
				t.Fatal("anonymous viewer must count active-only")
			}
		})
	}
}

// TestServiceResolveCommentPageViewerScope 位置计数必须与 viewer 实际看到的列表同口径：
// author_and_staff 策略下，staff 计入全部软删墓碑，作者只计入自己的墓碑。
func TestServiceResolveCommentPageViewerScope(t *testing.T) {
	staff := identity.Actor{
		ID:          7,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostDeleteAny: true},
	}
	author := identity.Actor{ID: 8, Status: identity.UserStatusActive}

	t.Run("staff计入全部墓碑", func(t *testing.T) {
		store := newServiceFakeStore()
		store.commentSummary = CommentSummary{ID: 99, TopicID: 10, Status: CommentStatusActive, PathKey: "000000000099"}
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

		if _, _, err := service.ResolveCommentPage(context.Background(), 10, 99, staff); err != nil {
			t.Fatalf("ResolveCommentPage returned error: %v", err)
		}
		if !store.countCommentsBeforeIncludeDeleted || store.countCommentsBeforeAuthorID != 0 {
			t.Fatalf("staff scope = (%v, %d), want (true, 0)",
				store.countCommentsBeforeIncludeDeleted, store.countCommentsBeforeAuthorID)
		}
	})
	t.Run("普通用户只计入自己的墓碑", func(t *testing.T) {
		store := newServiceFakeStore()
		store.commentSummary = CommentSummary{ID: 99, TopicID: 10, Status: CommentStatusActive, PathKey: "000000000099"}
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

		if _, _, err := service.ResolveCommentPage(context.Background(), 10, 99, author); err != nil {
			t.Fatalf("ResolveCommentPage returned error: %v", err)
		}
		if !store.countCommentsBeforeIncludeDeleted || store.countCommentsBeforeAuthorID != author.ID {
			t.Fatalf("author scope = (%v, %d), want (true, %d)",
				store.countCommentsBeforeIncludeDeleted, store.countCommentsBeforeAuthorID, author.ID)
		}
	})
	t.Run("staff可定位软删墓碑本身", func(t *testing.T) {
		store := newServiceFakeStore()
		store.commentSummary = CommentSummary{ID: 99, TopicID: 10, Status: CommentStatusDeleted, PathKey: "000000000099", AuthorUserID: 8}
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

		if _, _, err := service.ResolveCommentPage(context.Background(), 10, 99, staff); err != nil {
			t.Fatalf("staff should resolve visible tombstone, got %v", err)
		}
	})
	t.Run("他人软删墓碑对普通用户NotFound", func(t *testing.T) {
		store := newServiceFakeStore()
		store.commentSummary = CommentSummary{ID: 99, TopicID: 10, Status: CommentStatusDeleted, PathKey: "000000000099", AuthorUserID: 999}
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

		if _, _, err := service.ResolveCommentPage(context.Background(), 10, 99, author); !errors.Is(err, ErrCommentNotFound) {
			t.Fatalf("expected ErrCommentNotFound for other user's tombstone, got %v", err)
		}
	})
}

// TestServiceResolveCommentPageRejectsInvalidTarget 目标评论跨主题/非 active/主题隐藏均返回 ErrCommentNotFound，
// 且不泄漏软删状态。
func TestServiceResolveCommentPageRejectsInvalidTarget(t *testing.T) {
	t.Run("跨主题评论返回NotFound", func(t *testing.T) {
		store := newServiceFakeStore()
		store.commentSummary = CommentSummary{ID: 99, TopicID: 77, Status: CommentStatusActive}
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

		_, _, err := service.ResolveCommentPage(context.Background(), 10, 99, identity.Actor{})
		if !errors.Is(err, ErrCommentNotFound) {
			t.Fatalf("expected ErrCommentNotFound for cross-topic comment, got %v", err)
		}
		if store.countCommentsBeforeCalled {
			t.Fatal("must not count before rejecting cross-topic comment")
		}
	})
	t.Run("软删评论返回NotFound不泄漏状态", func(t *testing.T) {
		store := newServiceFakeStore()
		store.commentSummary = CommentSummary{ID: 99, TopicID: 10, Status: CommentStatusDeleted}
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

		_, _, err := service.ResolveCommentPage(context.Background(), 10, 99, identity.Actor{})
		if !errors.Is(err, ErrCommentNotFound) {
			t.Fatalf("expected ErrCommentNotFound for soft-deleted comment, got %v", err)
		}
		if store.countCommentsBeforeCalled {
			t.Fatal("must not count before rejecting non-active comment")
		}
	})
	t.Run("隐藏主题透传NotFound", func(t *testing.T) {
		store := newServiceFakeStore()
		store.getTopicErr = ErrTopicNotFound
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

		_, _, err := service.ResolveCommentPage(context.Background(), 10, 99, identity.Actor{})
		if !errors.Is(err, ErrTopicNotFound) {
			t.Fatalf("expected ErrTopicNotFound for hidden topic, got %v", err)
		}
	})
	t.Run("评论不存在透传NotFound", func(t *testing.T) {
		store := newServiceFakeStore()
		store.commentSummaryErr = ErrCommentNotFound
		service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: nil})

		_, _, err := service.ResolveCommentPage(context.Background(), 10, 999, identity.Actor{})
		if !errors.Is(err, ErrCommentNotFound) {
			t.Fatalf("expected ErrCommentNotFound for missing comment, got %v", err)
		}
	})
}
