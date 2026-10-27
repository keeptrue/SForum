package attachmentscontroller

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func newCleanupDecodeApp() *fiber.App {
	app := fiber.New()
	app.Post("/cleanup", func(c fiber.Ctx) error {
		limit, err := decodeCleanupRequest(c)
		if err != nil {
			return err
		}
		return c.JSON(fiber.Map{"limit": limit})
	})
	return app
}

// 空 body 必须落到 service 的默认归一化（0 → 100），非法 JSON 必须 400；
// 修复前 `_ = c.Bind().Body(&req)` 会把坏请求吞成 limit=0 后继续删 100 条。
func TestDecodeCleanupRequest(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantLimit   int
	}{
		{name: "empty body without content type", body: "", contentType: "", wantStatus: 200, wantLimit: 0},
		{name: "empty json object", body: "{}", contentType: "application/json", wantStatus: 200, wantLimit: 0},
		{name: "explicit limit", body: `{"limit":50}`, contentType: "application/json", wantStatus: 200, wantLimit: 50},
		{name: "explicit zero keeps service default", body: `{"limit":0}`, contentType: "application/json", wantStatus: 200, wantLimit: 0},
		{name: "malformed json", body: `{"limit":`, contentType: "application/json", wantStatus: 400},
		{name: "body without content type", body: `{"limit":50}`, contentType: "", wantStatus: 400},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/cleanup", strings.NewReader(tc.body))
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			response, err := newCleanupDecodeApp().Test(request)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.wantStatus {
				payload, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d, want %d (body %q)", response.StatusCode, tc.wantStatus, payload)
			}
			if tc.wantStatus != 200 {
				return
			}
			var decoded struct {
				Limit int `json:"limit"`
			}
			if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if decoded.Limit != tc.wantLimit {
				t.Fatalf("limit = %d, want %d", decoded.Limit, tc.wantLimit)
			}
		})
	}
}

// countingCleanupStore 只关心「service 有没有被调用」：候选列表方法被调用就记数。
type countingCleanupStore struct {
	listCleanupCalls int
}

func (s *countingCleanupStore) Create(context.Context, attachments.CreateAttachmentInput) (attachments.Attachment, error) {
	return attachments.Attachment{}, nil
}

func (s *countingCleanupStore) GetByPublicID(context.Context, string) (attachments.Attachment, error) {
	return attachments.Attachment{}, nil
}

func (s *countingCleanupStore) GetByID(context.Context, int64) (attachments.Attachment, error) {
	return attachments.Attachment{}, nil
}

func (s *countingCleanupStore) List(context.Context, attachments.AttachmentListInput) (attachments.AttachmentList, error) {
	return attachments.AttachmentList{}, nil
}

func (s *countingCleanupStore) ListReferences(context.Context, int64) ([]attachments.AttachmentReference, error) {
	return nil, nil
}

func (s *countingCleanupStore) ListReferenceAccess(context.Context, int64) ([]attachments.ReferenceAccess, error) {
	return nil, nil
}

func (s *countingCleanupStore) UpdateStatus(context.Context, int64, string, bool) (attachments.Attachment, error) {
	return attachments.Attachment{}, nil
}

func (s *countingCleanupStore) ListCleanupCandidates(context.Context, time.Time, int) ([]attachments.Attachment, error) {
	s.listCleanupCalls++
	return nil, nil
}

func (s *countingCleanupStore) DeleteMetadata(context.Context, int64) error { return nil }

type fixedActorStore struct {
	actor identity.Actor
}

func (s fixedActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

// handler 级证明：非法 JSON 不会触达 service（候选列表计数保持 0）。
// service 刻意不注入 options：万一请求真的进到清理流程，会因 nil options panic，
// 让误调用在测试里立刻炸出来，而不是静默删数据。
func TestCleanupAdminRejectsMalformedBodyBeforeService(t *testing.T) {
	store := &countingCleanupStore{}
	service := attachments.NewService(store, nil)
	controller := NewController(service, fixedActorStore{actor: identity.Actor{
		ID:          7,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentManage: true},
	}}, nil)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		// 走 PAT 主体解析路径，避免为单测搭一套会话存储。
		c.SetContext(context.WithValue(c.Context(), apitokens.ContextUserID, int64(7)))
		return c.Next()
	})
	app.Post("/cleanup", controller.cleanupAdmin)

	request := httptest.NewRequest("POST", "/cleanup", strings.NewReader(`{"limit":`))
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusBadRequest {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 400 (body %q)", response.StatusCode, payload)
	}
	if store.listCleanupCalls != 0 {
		t.Fatalf("malformed body reached the service: %d cleanup list calls", store.listCleanupCalls)
	}
}
