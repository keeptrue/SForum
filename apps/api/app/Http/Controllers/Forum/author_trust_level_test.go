package forumcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

// staticTrustLevelSource 是 HTTP 层的固定来源（失败时返回 err）。
type staticTrustLevelSource struct {
	levels map[int64]forum.AuthorTrustLevel
	err    error
}

func (s staticTrustLevelSource) AuthorTrustLevels(context.Context, []int64) (map[int64]forum.AuthorTrustLevel, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.levels, nil
}

// trustLevelListStore 只覆盖列表读：其余 Store 方法沿用 controllerForumStore。
type trustLevelListStore struct {
	*controllerForumStore
	items []forum.TopicSummary
}

func (s trustLevelListStore) ListTopics(_ context.Context, input forum.TopicListInput) (forum.TopicList, error) {
	return forum.TopicList{Items: s.items, Total: int64(len(s.items)), Page: 1, PerPage: 20}, nil
}

// newTrustLevelTestApp 装配一个只有列表读的最小论坛 app，作者固定为 id=7。
func newTrustLevelTestApp(source forum.AuthorTrustLevelSource) *fiber.App {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerForumActors{actors: map[int64]identity.Actor{}}
	store := trustLevelListStore{
		controllerForumStore: &controllerForumStore{},
		items: []forum.TopicSummary{{
			ID: 1, Title: "作者等级", Slug: "author-trust-level", Status: forum.TopicStatusActive,
			Author: &forum.UserSummary{ID: 7, Username: "alice", DisplayName: "Alice"},
		}},
	}
	service := forum.NewService(forum.ServiceConfig{Store: store, Settings: store})
	controller := NewController(service, users, manager).WithAuthorTrustLevels(source)
	return apphttp.NewApp(
		config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}},
		nil,
		apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller}},
	)
}

func readForumBody(t *testing.T, app *fiber.App) (int, string) {
	t.Helper()
	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics", nil, nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw)
}

// TestControllerSendsAuthorTrustLevel 锁定话题列表的 HTTP 载荷：作者等级字段随行下发。
func TestControllerSendsAuthorTrustLevel(t *testing.T) {
	app := newTrustLevelTestApp(staticTrustLevelSource{levels: map[int64]forum.AuthorTrustLevel{
		7: {Level: 4, LevelSlug: "leader", Color: "indigo"},
	}})
	status, body := readForumBody(t, app)
	if status != nethttp.StatusOK {
		t.Fatalf("status = %d，body=%s", status, body)
	}
	var payload struct {
		Data struct {
			Items []struct {
				AuthorTrustLevel *struct {
					Level     int    `json:"level"`
					LevelSlug string `json:"levelSlug"`
					Color     string `json:"color"`
				} `json:"authorTrustLevel"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v，body=%s", err, body)
	}
	if len(payload.Data.Items) != 1 || payload.Data.Items[0].AuthorTrustLevel == nil {
		t.Fatalf("列表项没有 authorTrustLevel：%s", body)
	}
	got := payload.Data.Items[0].AuthorTrustLevel
	if got.Level != 4 || got.LevelSlug != "leader" || got.Color != "indigo" {
		t.Fatalf("authorTrustLevel = %+v", got)
	}
	// 负断言：等级是**只读摘要**，任何进度/瓶颈/行为计数都不该出现在公开载荷里
	// （like_given_count / bookmark_count 是刻意不公开的窥探面）。结构体层面安全，
	// 但这条能挡住将来顺手往 AuthorTrustLevel 里加字段。
	for _, forbidden := range []string{"progress", "bottleneck", "like_given_count", "bookmark_count"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("公开载荷不应出现 %q：%s", forbidden, body)
		}
	}
}

// TestControllerFailOpenWhenTrustLevelsFail 锁定降级：来源失败时 HTTP 仍 200、
// 载荷不带该字段（不是 500，也不是 null 占位）。
func TestControllerFailOpenWhenTrustLevelsFail(t *testing.T) {
	app := newTrustLevelTestApp(staticTrustLevelSource{err: errors.New("boom")})
	status, body := readForumBody(t, app)
	if status != nethttp.StatusOK {
		t.Fatalf("status = %d（期望 200），body=%s", status, body)
	}
	if strings.Contains(body, "authorTrustLevel") {
		t.Fatalf("失败时不应下发 authorTrustLevel：%s", body)
	}
}
