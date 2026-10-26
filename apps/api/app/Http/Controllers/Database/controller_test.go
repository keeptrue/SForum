package databasecontroller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	dbmodel "github.com/keeptrue/sforum/apps/api/app/Models/Database"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

type apiEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type apiErrorData struct {
	Reason string `json:"reason"`
}

func TestListTablesRequiresAuthentication(t *testing.T) {
	app, _ := newDatabaseTestApp(databaseHTTPActor(), &databaseHTTPStore{})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/database/tables", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	var body apiEnvelope[apiErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Reason != "auth.required" {
		t.Fatalf("expected auth.required, got %#v", body)
	}
}

func TestListTablesRequiresDatabaseManagePermission(t *testing.T) {
	app, cookie := newDatabaseTestApp(identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{}}, &databaseHTTPStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/database/tables", nil)
	req.AddCookie(cookie)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	var body apiEnvelope[apiErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Reason != "permission.denied" {
		t.Fatalf("expected permission.denied, got %#v", body)
	}
}

func TestRowsReturnEnvelopeAndMaskedSensitiveCells(t *testing.T) {
	app, cookie := newDatabaseTestApp(databaseHTTPActor(), &databaseHTTPStore{
		detail: databaseHTTPDetail(),
		rows: []map[string]any{{
			"id":        int64(1),
			"api_token": "secret-token",
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/database/tables/public/users/rows?perPage=20", nil)
	req.AddCookie(cookie)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body apiEnvelope[dbmodel.RowsResult]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	cell := body.Data.Rows[0].Values["api_token"]
	if !cell.Masked || cell.Value != dbmodel.SensitiveMask {
		t.Fatalf("expected masked api_token cell, got %#v", cell)
	}
}

func TestExportCSVReturnsCSVWithMaskedSensitiveCells(t *testing.T) {
	app, cookie := newDatabaseTestApp(databaseHTTPActor(), &databaseHTTPStore{
		detail: databaseHTTPDetail(),
		rows: []map[string]any{{
			"id":        int64(1),
			"api_token": "secret-token",
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/database/tables/public/users/export.csv", nil)
	req.AddCookie(cookie)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/csv") {
		t.Fatalf("expected csv content type, got %q", contentType)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "id,api_token\n1,••••••••\n" {
		t.Fatalf("unexpected csv:\n%s", string(body))
	}
}

func newDatabaseTestApp(actor identity.Actor, store *databaseHTTPStore) (*fiber.App, *http.Cookie) {
	sessions := authsession.NewManager(session.NewStore(), authsession.Config{})
	controller := NewController(dbmodel.NewService(store), databaseHTTPActorStore{actor: actor}, sessions)
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{
			routeProviderFunc(func(api fiber.Router) {
				api.Post("/test-login", func(c fiber.Ctx) error {
					_, err := sessions.Start(c, actor.ID)
					return err
				})
			}),
			controller,
		},
	})

	loginResp, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/test-login", nil))
	if err != nil {
		panic(err)
	}
	defer loginResp.Body.Close()
	cookies := loginResp.Cookies()
	if len(cookies) == 0 {
		panic("expected session cookie")
	}
	return app, cookies[0]
}

func databaseHTTPActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionDatabaseManage: true},
	}
}

func databaseHTTPDetail() dbmodel.TableDetail {
	return dbmodel.TableDetail{
		Schema:     "public",
		Name:       "users",
		Kind:       "table",
		PrimaryKey: []string{"id"},
		Columns: []dbmodel.Column{
			{Name: "id", DataType: "bigint", IsPrimaryKey: true},
			{Name: "api_token", DataType: "text"},
		},
	}
}

type routeProviderFunc func(api fiber.Router)

func (fn routeProviderFunc) RegisterRoutes(api fiber.Router) {
	fn(api)
}

type databaseHTTPActorStore struct {
	actor identity.Actor
}

func (s databaseHTTPActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

type databaseHTTPStore struct {
	detail dbmodel.TableDetail
	rows   []map[string]any
}

func (s *databaseHTTPStore) ListTables(context.Context) ([]dbmodel.TableSummary, error) {
	return []dbmodel.TableSummary{{Schema: "public", Name: "users", Kind: "table"}}, nil
}

func (s *databaseHTTPStore) TableDetail(context.Context, dbmodel.TableRef) (dbmodel.TableDetail, error) {
	if s.detail.Schema == "" {
		return databaseHTTPDetail(), nil
	}
	return s.detail, nil
}

func (s *databaseHTTPStore) TableRows(context.Context, dbmodel.TableRef, dbmodel.TableDetail, dbmodel.RowsInput) ([]map[string]any, bool, error) {
	return s.rows, false, nil
}

func (s *databaseHTTPStore) RevealCell(context.Context, dbmodel.TableRef, dbmodel.TableDetail, dbmodel.RevealInput) (any, error) {
	return "secret-token", nil
}
