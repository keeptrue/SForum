package ai_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	modelsai "github.com/keeptrue/sforum/apps/api/app/Models/AI"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	outboundhttp "github.com/keeptrue/sforum/apps/api/app/Support/OutboundHTTP"
)

type stubModelLister struct {
	calls   int
	profile supportai.Profile
	apiKey  string
	list    supportai.ModelList
	err     error
}

func (s *stubModelLister) ListModels(_ context.Context, profile supportai.Profile, apiKey string) (supportai.ModelList, error) {
	s.calls++
	s.profile = profile
	s.apiKey = apiKey
	return s.list, s.err
}

type stubReader struct {
	key string
	err error
	ref string
}

func (s *stubReader) ResolveAIKey(_ context.Context, reference string) (string, error) {
	s.ref = reference
	return s.key, s.err
}

func modelService(store supportai.SettingsStore, lister *stubModelLister, reader *stubReader) *modelsai.Service {
	return modelsai.NewService(modelsai.Config{Settings: store, Models: lister, CredentialReader: reader})
}

func TestListModelsRequiresManagePermission(t *testing.T) {
	lister := &stubModelLister{}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, &stubReader{key: "sk"})

	if _, err := service.ListModels(context.Background(), memberActor(), "deepseek", supportai.ModelListProbe{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("err = %v", err)
	}
	if lister.calls != 0 {
		t.Fatal("denied callers must not reach the provider")
	}
}

// 默认路径：用已保存的地址与 Secret Store 里的密钥探测，运营者不需要重新粘贴密钥。
func TestListModelsUsesStoredProfileAndCredential(t *testing.T) {
	lister := &stubModelLister{list: supportai.ModelList{Models: []string{"deepseek-chat"}, Count: 1}}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, &stubReader{key: "sk-stored"})

	result, err := service.ListModels(context.Background(), managerActor(), "deepseek", supportai.ModelListProbe{})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if lister.apiKey != "sk-stored" {
		t.Fatalf("apiKey = %q", lister.apiKey)
	}
	if lister.profile.BaseURL != "https://api.deepseek.com" || lister.profile.Protocol != supportai.ProtocolOpenAIChat {
		t.Fatalf("profile = %+v", lister.profile)
	}
	if result.ProfileID != "deepseek" || result.Count != 1 || result.Models[0] != "deepseek-chat" {
		t.Fatalf("result = %+v", result)
	}
}

// 「一键获取」的核心：请求里的值优先，因此刚填的地址与密钥不必先保存。
func TestListModelsPrefersProbeValuesOverStoredOnes(t *testing.T) {
	lister := &stubModelLister{list: supportai.ModelList{Count: 0}}
	reader := &stubReader{key: "sk-stored"}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, reader)

	_, err := service.ListModels(context.Background(), managerActor(), "deepseek", supportai.ModelListProbe{
		BaseURL:  "https://gateway.example.test/v1",
		Protocol: supportai.ProtocolAnthropicMessages,
		APIKey:   "sk-typed",
	})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if lister.profile.BaseURL != "https://gateway.example.test/v1" || lister.profile.Protocol != supportai.ProtocolAnthropicMessages {
		t.Fatalf("probe values must win: %+v", lister.profile)
	}
	if lister.apiKey != "sk-typed" {
		t.Fatalf("apiKey = %q", lister.apiKey)
	}
	// 请求里带了密钥就不该再去读已保存的密钥：一次探测只解析一次凭证。
	if reader.ref != "" {
		t.Fatalf("stored credential must not be resolved: %q", reader.ref)
	}
}

// 尚未保存的自定义供应商：带了地址就允许一次性探测，否则「先填地址和密钥再挑模型」
// 必须先存一份没有模型的配置。
func TestListModelsAllowsUnsavedProfileWithProbeEndpoint(t *testing.T) {
	lister := &stubModelLister{list: supportai.ModelList{Models: []string{"local-a"}, Count: 1}}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, &stubReader{key: "sk"})

	result, err := service.ListModels(context.Background(), managerActor(), "custom", supportai.ModelListProbe{
		BaseURL: "https://self.example.test",
		APIKey:  "sk-typed",
	})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if result.ProfileID != "custom" || lister.profile.ID != "custom" {
		t.Fatalf("result = %+v profile = %+v", result, lister.profile)
	}
	if lister.profile.Model != "" {
		t.Fatalf("探测不得要求先填模型: %q", lister.profile.Model)
	}
}

func TestListModelsRejectsUnknownProfileWithoutEndpoint(t *testing.T) {
	lister := &stubModelLister{}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, &stubReader{key: "sk"})

	if _, err := service.ListModels(context.Background(), managerActor(), "nope", supportai.ModelListProbe{}); !errors.Is(err, supportai.ErrProfileUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if lister.calls != 0 {
		t.Fatal("unknown profile must not reach the provider")
	}
}

func TestListModelsRequiresCredential(t *testing.T) {
	lister := &stubModelLister{}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, &stubReader{})
	if _, err := service.ListModels(context.Background(), managerActor(), "deepseek", supportai.ModelListProbe{}); !errors.Is(err, supportai.ErrCredentialMissing) {
		t.Fatalf("err = %v", err)
	}
	// 没有可解析密钥的部署：同样是「密钥缺失」，而不是一个内部错误。
	// 这里直接构造服务，避免把带类型的 nil 指针塞进接口（那会让 nil 判断失效）。
	noReader := modelsai.NewService(modelsai.Config{Settings: &stubSettings{stored: supportai.RecommendedSettings()}, Models: lister})
	if _, err := noReader.ListModels(context.Background(), managerActor(), "deepseek", supportai.ModelListProbe{}); !errors.Is(err, supportai.ErrCredentialMissing) {
		t.Fatalf("err = %v", err)
	}
	if lister.calls != 0 {
		t.Fatal("a probe without a credential must not reach the provider")
	}
}

func TestListModelsRejectsInvalidEndpointBeforeCallingProvider(t *testing.T) {
	lister := &stubModelLister{}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, &stubReader{key: "sk"})

	if _, err := service.ListModels(context.Background(), managerActor(), "deepseek", supportai.ModelListProbe{BaseURL: "::::"}); !errors.Is(err, supportai.ErrProfileInvalid) {
		t.Fatalf("err = %v", err)
	}
	if lister.calls != 0 {
		t.Fatal("an invalid endpoint must not reach the provider")
	}
}

// 供应商失败是结论而不是错误：界面要展示「地址能连上但密钥无效」，而不是一个 500。
func TestListModelsTreatsProviderFailureAsResult(t *testing.T) {
	lister := &stubModelLister{err: errors.New("ai: provider call failed: 401 invalid api key")}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, &stubReader{key: "sk"})

	result, err := service.ListModels(context.Background(), managerActor(), "deepseek", supportai.ModelListProbe{})
	if err != nil {
		t.Fatalf("provider failure must not be a transport error: %v", err)
	}
	if result.Reason != supportai.ReasonProviderFailed || result.Error == "" {
		t.Fatalf("result = %+v", result)
	}
	if result.Count != 0 || len(result.Models) != 0 {
		t.Fatalf("failed probe must not report models: %+v", result)
	}
}

// 出站守卫拒绝是另一种结论：运营者要改的是地址，不是重试或换密钥。
func TestListModelsReportsGuardRejectionAsItsOwnReason(t *testing.T) {
	guardErr := fmt.Errorf("%w: http not allowed", outboundhttp.ErrUnsafeURL)
	lister := &stubModelLister{err: guardErr}
	service := modelService(&stubSettings{stored: supportai.RecommendedSettings()}, lister, &stubReader{key: "sk"})

	result, err := service.ListModels(context.Background(), managerActor(), "deepseek", supportai.ModelListProbe{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Reason != supportai.ReasonUnsafeTarget {
		t.Fatalf("reason = %q, error = %q", result.Reason, result.Error)
	}
}

func TestListModelsWithoutListerIsUnavailable(t *testing.T) {
	service := modelsai.NewService(modelsai.Config{Settings: &stubSettings{stored: supportai.RecommendedSettings()}})
	if _, err := service.ListModels(context.Background(), managerActor(), "deepseek", supportai.ModelListProbe{}); !errors.Is(err, supportai.ErrModelListUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
