package pageviewmodels

import (
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// B-17：/badges 页的 ViewModel 必须**真的带数据**。
//
// 「每个契约都 populate 不报错」那条测试只断言不报错（source_test.go 的
// TestCorePageViewModelSourcePopulatesEveryCatalogContract），case 返回空 struct
// 也会绿。所以这里直接钉住行数、earned 与分组，而不是「没 panic」。
func TestBadgeIndexPageViewModelCarriesCatalogRows(t *testing.T) {
	source := newTestSource(&sourceForum{}, defaultSourceOptions("public"))
	populated, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: pages.CorePageViewModelRequest{
			PageID: "forum.badge.index", Locale: "en-US", Path: "/badges",
			SEO: themecompiler.PageSEOView{Title: "forum.badge.index"},
		},
		// 登录查看者：earned 必须按人合并。
		Actor: identity.Actor{ID: 8, Status: identity.UserStatusActive},
	})
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	model := populated.Data.BadgeIndex
	if model == nil {
		t.Fatal("badge index view model is nil: the Populate case does not fill data")
	}
	if len(model.Groups) != 2 {
		t.Fatalf("want 2 catalog groups, got %d", len(model.Groups))
	}
	items := 0
	earned := 0
	for _, group := range model.Groups {
		items += len(group.Badges)
		for _, item := range group.Badges {
			if item.Earned {
				earned++
				if item.Slug != "first-post" {
					t.Fatalf("unexpected earned badge %q", item.Slug)
				}
			}
		}
	}
	if items != 3 {
		t.Fatalf("want 3 catalog badges, got %d", items)
	}
	if earned != 1 {
		t.Fatalf("want exactly 1 earned badge for the viewer, got %d", earned)
	}
	if model.Groups[0].Key != "starter" || model.Groups[0].Badges[0].Slug != "first-post" {
		t.Fatalf("catalog order drifted: %#v", model.Groups[0])
	}
	if populated.SEO.Title != "Badges" {
		t.Fatalf("SEO title = %q, want Badges", populated.SEO.Title)
	}
}

// B-09②：features.badges=false → 页面数据 404（不是空壳）。
//
// 语义分层：页面 404（RequiresFeatures → ErrCorePageDataNotFound → HTTP 404）
// 与徽章端点 403 forum.badges_disabled 是两套口径，刻意不统一——改 404 映射
// 会影响所有带 RequiresFeatures 的页（forum.search / forum.profile.show）。
func TestBadgeIndexPageIsNotFoundWhenBadgesFeatureDisabled(t *testing.T) {
	// 用完整装配（而不是只有 Options）：这样一旦 RequiresFeatures 被漏掉，
	// Populate 会**成功**返回，红在「err == nil」，而不是红在别的缺依赖上。
	source := newTestSource(&sourceForum{}, sourceOptions{
		guestRead: "public", ready: true,
		values: map[string]string{
			options.NameSiteName: "SForum", options.NameSiteURL: "https://forum.example",
		},
		features: map[string]bool{options.NameFeatureBadges: false},
	})
	_, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: pages.CorePageViewModelRequest{
			PageID: "forum.badge.index", Locale: "en-US", Path: "/badges",
			SEO: themecompiler.PageSEOView{Title: "forum.badge.index"},
		},
	})
	if !errors.Is(err, ErrCorePageDataNotFound) {
		t.Fatalf("disabled badges page error = %v, want ErrCorePageDataNotFound (404)", err)
	}
}

// 匿名查看者不得触发 earned 查询，也不得因缺 earned 而少条目。
func TestBadgeIndexPageKeepsAllRowsForAnonymousViewer(t *testing.T) {
	source := newTestSource(&sourceForum{}, defaultSourceOptions("public"))
	populated, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: pages.CorePageViewModelRequest{
			PageID: "forum.badge.index", Locale: "en-US", Path: "/badges",
			SEO: themecompiler.PageSEOView{Title: "forum.badge.index"},
		},
	})
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	model := populated.Data.BadgeIndex
	if model == nil || len(model.Groups) != 2 {
		t.Fatalf("anonymous catalog = %#v", model)
	}
	for _, group := range model.Groups {
		for _, item := range group.Badges {
			if item.Earned {
				t.Fatalf("anonymous viewer must not be marked earned: %#v", item)
			}
		}
	}
}
