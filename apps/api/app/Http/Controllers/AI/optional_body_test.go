package aicontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	modelsai "github.com/keeptrue/sforum/apps/api/app/Models/AI"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

const aiProbeUserID int64 = 41

// diagnose 的 body 是可选的（空 body 用默认探测消息），但「可选」不等于「随便」：
// 一段拼错的 JSON 若被当成空 body 继续往下走，一次坏请求就变成一次真实的付费
// 调用，而且运营者看到的是「探测通过」。所以非法 JSON 必须 422，且必须在调用
// service 之前就返回。
func TestDiagnoseRejectsMalformedBodyBeforeBilling(t *testing.T) {
	probe := newProbeHarness(t)
	status, body := probe.post(t, "/api/v1/admin/ai/diagnose", `{"message": "半截 JSON"`)

	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a malformed body, got %d after %d outbound provider calls",
			status, probe.invoker.callCount())
	}
	if reason := body.Data.Reason; reason != "validation.invalid" {
		t.Fatalf("expected validation.invalid, got %q", reason)
	}
	if calls := probe.settings.getCalls(); calls != 0 {
		t.Fatalf("a malformed body must not reach the AI service, got %d settings reads", calls)
	}
	if calls := probe.invoker.callCount(); calls != 0 {
		t.Fatalf("a malformed body must not reach the provider, got %d outbound calls", calls)
	}
	if calls := probe.credentials.configuredCalls(); calls != 0 {
		t.Fatalf("a malformed body must not probe credentials, got %d reads", calls)
	}
}

// 空 body 仍然合法：service 会用自己的默认探测消息。
func TestDiagnoseEmptyBodyKeepsDefaultProbeMessage(t *testing.T) {
	probe := newProbeHarness(t)
	status, body := probe.post(t, "/api/v1/admin/ai/diagnose", "")

	if status != fiber.StatusOK {
		t.Fatalf("expected 200 for an empty body, got %d (%s)", status, body.Data.Reason)
	}
	if calls := probe.invoker.callCount(); calls != 1 {
		t.Fatalf("expected exactly one probe call, got %d", calls)
	}
	if wire := probe.invoker.lastBody(); !strings.Contains(wire, supportai.DefaultDiagnoseMessage) {
		t.Fatalf("expected the default probe message in the outbound body, got %q", wire)
	}
}

// 合法 body 必须原样透传：运营者写了自己要看的内容，就不能被默认消息顶掉。
func TestDiagnoseForwardsExplicitMessage(t *testing.T) {
	probe := newProbeHarness(t)
	status, body := probe.post(t, "/api/v1/admin/ai/diagnose", `{"message":"自定义探测内容"}`)

	if status != fiber.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", status, body.Data.Reason)
	}
	if calls := probe.invoker.callCount(); calls != 1 {
		t.Fatalf("expected exactly one probe call, got %d", calls)
	}
	wire := probe.invoker.lastBody()
	if !strings.Contains(wire, "自定义探测内容") {
		t.Fatalf("expected the explicit message in the outbound body, got %q", wire)
	}
	if strings.Contains(wire, supportai.DefaultDiagnoseMessage) {
		t.Fatalf("the default message must not replace an explicit one: %q", wire)
	}
}

// 「获取模型列表」是同一个吞错误的形状：非法 JSON 不能变成一次对供应商的探测。
func TestListProfileModelsRejectsMalformedBodyBeforeProbe(t *testing.T) {
	probe := newProbeHarness(t)
	status, body := probe.post(t, "/api/v1/admin/ai/profiles/deepseek/models", `{"baseUrl": `)

	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a malformed body, got %d after %d provider probes",
			status, probe.models.callCount())
	}
	if reason := body.Data.Reason; reason != "validation.invalid" {
		t.Fatalf("expected validation.invalid, got %q", reason)
	}
	if calls := probe.models.callCount(); calls != 0 {
		t.Fatalf("a malformed body must not reach the provider, got %d probes", calls)
	}
}

func TestListProfileModelsKeepsEmptyBodyUsable(t *testing.T) {
	probe := newProbeHarness(t)
	status, body := probe.post(t, "/api/v1/admin/ai/profiles/deepseek/models", "")

	if status != fiber.StatusOK {
		t.Fatalf("expected 200 for an empty body, got %d (%s)", status, body.Data.Reason)
	}
	if calls := probe.models.callCount(); calls != 1 {
		t.Fatalf("expected exactly one model-list probe, got %d", calls)
	}
}

type probeHarness struct {
	app         *fiber.App
	cookie      string
	settings    *countingSettingsStore
	credentials *countingCredentials
	invoker     *recordingInvoker
	models      *countingModelLister
}

type probeEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Reason string `json:"reason"`
	} `json:"data"`
}

// post 发出一次探针请求并返回状态码与解析后的 envelope。
func (h *probeHarness) post(t *testing.T, path, rawBody string) (int, *probeEnvelope) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(rawBody)))
	request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	request.Header.Set(fiber.HeaderCookie, h.cookie)
	response, err := h.app.Test(request)
	if err != nil {
		t.Fatalf("request %s failed: %v", path, err)
	}
	defer response.Body.Close()

	raw, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}
	envelope := &probeEnvelope{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, envelope); err != nil {
			t.Fatalf("decode envelope %q: %v", string(raw), err)
		}
	}
	return response.StatusCode, envelope
}

func newProbeHarness(t *testing.T) *probeHarness {
	t.Helper()
	settings := supportai.RecommendedSettings()
	settings.Enabled = true
	// 只启用 economy 档并把它绑到 deepseek：诊断按 economy → standard → premium
	// 的顺序选 profile，这里要让第一步就命中。
	for index := range settings.Profiles {
		settings.Profiles[index].Enabled = settings.Profiles[index].ID == "deepseek"
	}
	settings.CostClassProfiles = map[string]string{
		supportai.CostClassEconomy:  "deepseek",
		supportai.CostClassStandard: "deepseek",
		supportai.CostClassPremium:  "deepseek",
	}

	settingsStore := &countingSettingsStore{settings: settings}
	credentials := &countingCredentials{key: "sk-test-key"}
	invoker := &recordingInvoker{}
	models := &countingModelLister{}

	gateway := supportai.NewGateway(supportai.GatewayConfig{
		Settings: settingsStore,
		Creds:    credentials,
		Invoker:  invoker,
	})
	service := modelsai.NewService(modelsai.Config{
		Settings:         settingsStore,
		Gateway:          gateway,
		Credentials:      credentials,
		CredentialReader: credentials,
		Models:           models,
	})

	sessions := authsession.NewManager(
		session.NewStore(session.Config{Extractor: extractors.FromCookie("sforum_session")}),
		authsession.Config{HashSecret: "ai-probe-test"},
	)
	controller := NewController(service, staticActorStore{actor: aiManagerActor()}, sessions, nil)
	app := apphttp.NewApp(probeConfig(), nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{
			probeRouteProvider(func(api fiber.Router) {
				api.Post("/test-login", func(c fiber.Ctx) error {
					if _, err := sessions.Start(c, aiProbeUserID); err != nil {
						return err
					}
					return c.SendStatus(fiber.StatusNoContent)
				})
			}),
			controller,
		},
	})

	login, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/test-login", nil))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	login.Body.Close()
	cookies := login.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected a session cookie from the probe login")
	}

	return &probeHarness{
		app:         app,
		cookie:      cookies[0].Name + "=" + cookies[0].Value,
		settings:    settingsStore,
		credentials: credentials,
		invoker:     invoker,
		models:      models,
	}
}

func probeConfig() config.Config {
	return config.Config{
		AppName:          "SForum",
		AppEnv:           "test",
		AppLocale:        "zh-CN",
		SupportedLocales: []string{"zh-CN", "en-US"},
		CSRFEnabled:      false,
	}
}

func aiManagerActor() identity.Actor {
	return identity.Actor{
		ID:          aiProbeUserID,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAIManage: true},
	}
}

type probeRouteProvider func(fiber.Router)

func (f probeRouteProvider) RegisterRoutes(api fiber.Router) { f(api) }

type staticActorStore struct{ actor identity.Actor }

func (s staticActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

// countingSettingsStore 是「service.Diagnose/ListModels 是否被进入」的探针：
// 两条路径的第一件事都是读设置，因此它的调用次数就是 service 入口计数。
type countingSettingsStore struct {
	mu       sync.Mutex
	settings supportai.Settings
	calls    int
}

func (s *countingSettingsStore) GetSettings(context.Context) (supportai.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.settings, nil
}

func (s *countingSettingsStore) SaveSettings(_ context.Context, settings supportai.Settings, _ int64) (supportai.Settings, error) {
	return settings, nil
}

func (s *countingSettingsStore) ResetSettings(_ context.Context, settings supportai.Settings, _ int64) (supportai.Settings, error) {
	return settings, nil
}

func (s *countingSettingsStore) getCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// countingCredentials 同时是 CredentialStore（诊断查「密钥已配置」）与
// CredentialReader（模型列表读已保存密钥）。
type countingCredentials struct {
	mu         sync.Mutex
	key        string
	configured int
	resolved   int
}

func (c *countingCredentials) Put(context.Context, string, string, string) (int64, error) {
	return 1, nil
}

func (c *countingCredentials) Configured(context.Context, string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configured++
	return true, nil
}

func (c *countingCredentials) ResolveAIKey(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolved++
	return c.key, nil
}

func (c *countingCredentials) configuredCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.configured
}

// recordingInvoker 是一次付费出站的真实位置：它被调用一次，就是一次真实调用。
type recordingInvoker struct {
	mu    sync.Mutex
	calls int
	body  string
}

func (i *recordingInvoker) Invoke(_ context.Context, _ supportai.Profile, request supportai.WireRequest) (supportai.WireResponse, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls++
	i.body = string(request.Body)
	return supportai.WireResponse{
		Text:       "收到",
		StopReason: supportai.StopReasonEnd,
		Usage:      supportai.Usage{InputTokens: 12, OutputTokens: 2},
	}, nil
}

func (i *recordingInvoker) callCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
}

func (i *recordingInvoker) lastBody() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.body
}

type countingModelLister struct {
	mu    sync.Mutex
	calls int
}

func (l *countingModelLister) ListModels(context.Context, supportai.Profile, string) (supportai.ModelList, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return supportai.ModelList{Models: []string{"deepseek-chat"}, Count: 1}, nil
}

func (l *countingModelLister) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}
