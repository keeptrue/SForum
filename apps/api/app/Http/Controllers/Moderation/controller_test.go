package moderationcontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

type moderationEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

func TestControllerCreateReportRequiresLogin(t *testing.T) {
	app, _, _ := newModerationTestApp()
	body := []byte(`{"targetType":"topic","targetId":10,"reasonCode":"spam"}`)
	resp := performModerationRequest(t, app, nethttp.MethodPost, "/api/v1/moderation/reports", body, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}
}

func TestControllerCreateReportAllowsActiveUser(t *testing.T) {
	app, _, store := newModerationTestApp()
	cookie := loginModerationUser(t, app, 1) // 普通活跃用户。
	body := []byte(`{"targetType":"topic","targetId":10,"reasonCode":"spam","body":"spam"}`)
	resp := performModerationRequest(t, app, nethttp.MethodPost, "/api/v1/moderation/reports", body, cookie)
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("expected 201 create report, got %d", resp.StatusCode)
	}
	if store.createdInput.TargetType != moderation.TargetTypeTopic {
		t.Fatalf("expected topic target, got %s", store.createdInput.TargetType)
	}
}

func TestControllerListReportsRequiresReviewPermission(t *testing.T) {
	app, _, _ := newModerationTestApp()
	// 普通用户无 review 权限 -> 403。
	cookie := loginModerationUser(t, app, 1)
	resp := performModerationRequest(t, app, nethttp.MethodGet, "/api/v1/admin/moderation/reports", nil, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without review permission, got %d", resp.StatusCode)
	}
	// 审核员 -> 200。
	cookie = loginModerationUser(t, app, 2)
	resp = performModerationRequest(t, app, nethttp.MethodGet, "/api/v1/admin/moderation/reports", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 list reports, got %d", resp.StatusCode)
	}
}

func TestControllerUpdateReportRequiresReviewPermission(t *testing.T) {
	app, _, _ := newModerationTestApp()
	body := []byte(`{"status":"resolved","reviewNote":"handled"}`)
	// 普通用户 -> 403。
	cookie := loginModerationUser(t, app, 1)
	resp := performModerationRequest(t, app, nethttp.MethodPatch, "/api/v1/admin/moderation/reports/1", body, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without review permission, got %d", resp.StatusCode)
	}
	// 审核员 -> 200。
	cookie = loginModerationUser(t, app, 2)
	resp = performModerationRequest(t, app, nethttp.MethodPatch, "/api/v1/admin/moderation/reports/1", body, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 update report, got %d", resp.StatusCode)
	}
}

func TestControllerCreateReportRejectsInvalidTargetType(t *testing.T) {
	app, _, _ := newModerationTestApp()
	cookie := loginModerationUser(t, app, 1)
	body := []byte(`{"targetType":"user","targetId":1,"reasonCode":"spam"}`)
	resp := performModerationRequest(t, app, nethttp.MethodPost, "/api/v1/moderation/reports", body, cookie)
	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422 invalid target type, got %d", resp.StatusCode)
	}
}

func TestControllerSeparatesManagementAndReviewPermissions(t *testing.T) {
	app, _, _ := newModerationTestApp()
	reviewer := loginModerationUser(t, app, 2)
	manager := loginModerationUser(t, app, 3)

	if resp := performModerationRequest(t, app, nethttp.MethodGet, "/api/v1/admin/moderation/settings", nil, reviewer); resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("reviewer settings status = %d, want 403", resp.StatusCode)
	}
	if resp := performModerationRequest(t, app, nethttp.MethodGet, "/api/v1/admin/moderation/settings", nil, manager); resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("manager settings status = %d, want 200", resp.StatusCode)
	}
	if resp := performModerationRequest(t, app, nethttp.MethodGet, "/api/v1/moderation/workbench/counts", nil, manager); resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("manager queue status = %d, want 403", resp.StatusCode)
	}
	if resp := performModerationRequest(t, app, nethttp.MethodGet, "/api/v1/moderation/workbench/counts", nil, reviewer); resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("reviewer queue status = %d, want 200", resp.StatusCode)
	}
}

func TestControllerRejectDecisionRequiresReviewNote(t *testing.T) {
	app, _, _ := newModerationTestApp()
	reviewer := loginModerationUser(t, app, 2)
	body := []byte(`{"source":"pre_publish","targetType":"topic","targetId":10,"action":"reject"}`)
	resp := performModerationRequest(t, app, nethttp.MethodPost, "/api/v1/moderation/workbench/decisions", body, reviewer)
	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("reject without note status = %d, want 422", resp.StatusCode)
	}
}

func newModerationTestApp() (*fiber.App, *authsession.Manager, *moderationFakeStore) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := moderationFakeActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReportReview: true}},
		3: {ID: 3, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationManage: true}},
	}}
	store := &moderationFakeStore{}
	validator := &moderationFakeValidator{topic: true, comment: true}
	controller := NewController(moderation.NewServiceWithWorkbench(store, validator, store, store), users, manager)
	loginProvider := moderationRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			userID, err := strconv.ParseInt(c.Params("id"), 10, 64)
			if err != nil || userID == 0 {
				userID = 1
			}
			_, err = manager.Start(c, userID)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})
	return app, manager, store
}

func loginModerationUser(t *testing.T, app *fiber.App, userID int64) *nethttp.Cookie {
	t.Helper()
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/test-login/"+strconv.FormatInt(userID, 10), nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK || len(resp.Cookies()) == 0 {
		t.Fatalf("expected login cookie, got %d", resp.StatusCode)
	}
	return resp.Cookies()[0]
}

func performModerationRequest(t *testing.T, app *fiber.App, method, path string, body []byte, cookie *nethttp.Cookie) *nethttp.Response {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return resp
}

type moderationFakeActors struct {
	actors map[int64]identity.Actor
}

func (a moderationFakeActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	return a.actors[userID], nil
}

type moderationRouteProviderFunc func(api fiber.Router)

func (f moderationRouteProviderFunc) RegisterRoutes(api fiber.Router) { f(api) }

type moderationFakeStore struct {
	createdInput moderation.CreateReportInput
}

func (s *moderationFakeStore) CreateReport(_ context.Context, input moderation.CreateReportInput) (moderation.Report, error) {
	s.createdInput = input
	return moderation.Report{ID: 1, TargetType: input.TargetType, TargetID: input.TargetID, ReasonCode: input.ReasonCode, Status: moderation.StatusOpen}, nil
}

func (s *moderationFakeStore) ListReports(_ context.Context, input moderation.ReportListInput) (moderation.ReportList, error) {
	return moderation.ReportList{Items: []moderation.Report{}, Total: 0, Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *moderationFakeStore) GetReport(context.Context, int64) (moderation.Report, error) {
	return moderation.Report{}, nil
}

func (s *moderationFakeStore) UpdateReport(_ context.Context, input moderation.UpdateReportInput) (moderation.Report, error) {
	return moderation.Report{ID: input.ReportID, Status: input.Status, ReviewNote: input.ReviewNote}, nil
}

func (s *moderationFakeStore) GetSettings(context.Context) (moderation.Settings, error) {
	return moderation.RecommendedSettings(), nil
}

func (s *moderationFakeStore) SaveSettings(_ context.Context, settings moderation.Settings, _ int64) (moderation.Settings, error) {
	return settings, nil
}

func (s *moderationFakeStore) ResetSettings(_ context.Context, settings moderation.Settings, _ int64) (moderation.Settings, error) {
	return settings, nil
}

func (s *moderationFakeStore) QueueCounts(context.Context) (moderation.QueueCounts, error) {
	return moderation.QueueCounts{}, nil
}

func (s *moderationFakeStore) ListPending(_ context.Context, input moderation.WorkbenchListInput) (moderation.PendingList, error) {
	return moderation.PendingList{Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *moderationFakeStore) ListReportItems(_ context.Context, input moderation.WorkbenchListInput) (moderation.ReportItemList, error) {
	return moderation.ReportItemList{Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *moderationFakeStore) ListDecisions(_ context.Context, input moderation.DecisionListInput) (moderation.DecisionList, error) {
	return moderation.DecisionList{Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *moderationFakeStore) GetReviewContext(context.Context, moderation.ReviewContextInput) (moderation.ReviewContext, error) {
	return moderation.ReviewContext{}, nil
}

func (s *moderationFakeStore) SubmitDecision(_ context.Context, input moderation.DecisionInput) (moderation.Decision, error) {
	return moderation.Decision{Source: input.Source, TargetType: input.TargetType, TargetID: input.TargetID, Action: input.Action}, nil
}

type moderationFakeValidator struct {
	topic   bool
	comment bool
}

func (v *moderationFakeValidator) IsReportableTopic(context.Context, int64) (bool, error) {
	return v.topic, nil
}

func (v *moderationFakeValidator) IsReportableComment(context.Context, int64) (bool, error) {
	return v.comment, nil
}

var _ = json.Marshal
