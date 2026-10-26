package ai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// requestRecorder 捕获最近一次请求。用结构体而不是裸指针：闭包里的赋值目标是
// 变量，只有通过结构体字段才能在测试里读到那一次请求。
type requestRecorder struct {
	request *http.Request
}

func (r *requestRecorder) seen() *http.Request { return r.request }

func modelListServer(t *testing.T, body string) (*httptest.Server, *requestRecorder) {
	t.Helper()
	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.request = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

func TestBuildModelListRequestUsesProtocolAuth(t *testing.T) {
	openai := deepseekProfile()
	openai.BaseURL = "https://llm.example.test/v1"
	request, err := ai.BuildModelListRequest(openai, "sk-live")
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if request.Method != http.MethodGet || request.URL != "https://llm.example.test/v1/models" {
		t.Fatalf("request = %+v", request)
	}
	if request.Headers["Authorization"] != "Bearer sk-live" {
		t.Fatalf("authorization = %q", request.Headers["Authorization"])
	}
	if len(request.Body) != 0 {
		t.Fatalf("model listing must not carry a body: %q", request.Body)
	}

	claude := claudeProfile()
	claude.BaseURL = "https://claude.example.test/v1"
	request, err = ai.BuildModelListRequest(claude, "sk-ant")
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if request.Headers["x-api-key"] != "sk-ant" || request.Headers["anthropic-version"] == "" {
		t.Fatalf("anthropic headers = %+v", request.Headers)
	}
	// Claude 的默认页只有 20 条：探测必须显式分页，否则只能看到最新的一小撮。
	if !strings.Contains(request.URL, "limit="+strconv.Itoa(ai.MaxModelListEntries)) {
		t.Fatalf("anthropic url must page: %q", request.URL)
	}
}

func TestBuildModelListRequestRejectsMissingCredentialAndBadEndpoint(t *testing.T) {
	profile := deepseekProfile()
	if _, err := ai.BuildModelListRequest(profile, "  "); !errors.Is(err, ai.ErrCredentialMissing) {
		t.Fatalf("err = %v", err)
	}
	broken := deepseekProfile()
	broken.BaseURL = "ftp://llm.example.test"
	if _, err := ai.BuildModelListRequest(broken, "sk-live"); !errors.Is(err, ai.ErrProfileInvalid) {
		t.Fatalf("err = %v", err)
	}
	withCreds := deepseekProfile()
	withCreds.BaseURL = "https://user:pass@llm.example.test"
	if _, err := ai.BuildModelListRequest(withCreds, "sk-live"); !errors.Is(err, ai.ErrProfileInvalid) {
		t.Fatalf("credentials in the URL must be rejected: %v", err)
	}
}

func TestWithProbeMergesOverridesAndAllowsEmptyModel(t *testing.T) {
	// 获取列表恰恰是为了挑模型：探测路径不得要求 model 已经填好。
	profile := ai.Profile{ID: "custom", Protocol: ai.ProtocolOpenAIChat, BaseURL: "https://old.example.test"}
	merged, err := profile.WithProbe(ai.ModelListProbe{BaseURL: "https://new.example.test/v1/", Protocol: ai.ProtocolAnthropicMessages})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if merged.BaseURL != "https://new.example.test/v1" || merged.Protocol != ai.ProtocolAnthropicMessages {
		t.Fatalf("merged = %+v", merged)
	}
	// 未设置超时时回落到默认：探测必须有上限。
	if merged.Defaults.TimeoutMS != ai.DefaultTimeoutMS {
		t.Fatalf("timeout = %d", merged.Defaults.TimeoutMS)
	}

	kept, err := profile.WithProbe(ai.ModelListProbe{})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if kept.BaseURL != "https://old.example.test" || kept.Protocol != ai.ProtocolOpenAIChat {
		t.Fatalf("empty probe must keep the stored endpoint: %+v", kept)
	}

	if _, err := profile.WithProbe(ai.ModelListProbe{Protocol: "grpc"}); !errors.Is(err, ai.ErrProfileInvalid) {
		t.Fatalf("err = %v", err)
	}
	if _, err := profile.WithProbe(ai.ModelListProbe{BaseURL: "not-a-url"}); !errors.Is(err, ai.ErrProfileInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseModelListResponseKeepsOrderAndDeduplicates(t *testing.T) {
	profile := deepseekProfile()
	body := []byte(`{"object":"list","data":[{"id":"deepseek-reasoner"},{"id":" deepseek-chat "},{"id":""},{"id":"deepseek-reasoner"}]}`)
	list, err := ai.ParseModelListResponse(profile, http.StatusOK, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	want := []string{"deepseek-reasoner", "deepseek-chat"}
	if len(list.Models) != len(want) {
		t.Fatalf("models = %+v", list.Models)
	}
	for index, id := range want {
		if list.Models[index] != id {
			t.Fatalf("模型必须保持供应商给出的顺序: %+v", list.Models)
		}
	}
	if list.Count != len(want) || list.Truncated {
		t.Fatalf("list = %+v", list)
	}
}

func TestParseModelListResponseReportsProviderFailure(t *testing.T) {
	profile := deepseekProfile()
	_, err := ai.ParseModelListResponse(profile, http.StatusUnauthorized, []byte(`{"error":{"message":"invalid api key"}}`))
	if !errors.Is(err, ai.ErrWire) {
		t.Fatalf("expected ErrWire, got %v", err)
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("error must carry status and provider detail: %v", err)
	}
}

func TestParseModelListResponseTruncatesBeyondCap(t *testing.T) {
	profile := deepseekProfile()
	entries := make([]string, 0, ai.MaxModelListEntries+10)
	for index := 0; index < ai.MaxModelListEntries+10; index++ {
		entries = append(entries, `{"id":"model-`+strconv.Itoa(index)+`"}`)
	}
	body := []byte(`{"data":[` + strings.Join(entries, ",") + `]}`)
	list, err := ai.ParseModelListResponse(profile, http.StatusOK, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if list.Count != ai.MaxModelListEntries || !list.Truncated {
		t.Fatalf("list = count %d truncated %v", list.Count, list.Truncated)
	}
}

func TestParseModelListResponseFlagsProviderPagination(t *testing.T) {
	claude := claudeProfile()
	list, err := ai.ParseModelListResponse(claude, http.StatusOK, []byte(`{"data":[{"id":"claude-x","display_name":"Claude X"}],"has_more":true}`))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	// 供应商明说还有下一页：界面必须能说明「这不是全部」，而不是假装列全了。
	if len(list.Models) != 1 || !list.Truncated {
		t.Fatalf("list = %+v", list)
	}
}

func TestHTTPModelListerCallsModelsEndpoint(t *testing.T) {
	server, recorder := modelListServer(t, `{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`)
	profile := localProfile(server)

	list, err := ai.NewHTTPModelLister(testInvoker(server)).ListModels(context.Background(), profile, "sk-live")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if list.Count != 2 || list.Models[1] != "deepseek-reasoner" {
		t.Fatalf("list = %+v", list)
	}
	seen := recorder.seen()
	if seen == nil {
		t.Fatal("provider was never called")
	}
	if seen.URL.Path != "/models" {
		t.Fatalf("path = %q", seen.URL.Path)
	}
	if seen.Header.Get("Authorization") != "Bearer sk-live" {
		t.Fatalf("authorization = %q", seen.Header.Get("Authorization"))
	}
}

func TestHTTPModelListerRejectsUnsafeTarget(t *testing.T) {
	server, _ := modelListServer(t, `{"data":[]}`)
	profile := localProfile(server)
	// 回环地址必须被出站守卫拒绝：探测不能绕过补全调用的同一套校验。
	invoker := ai.NewHTTPInvoker(ai.HTTPInvokerOptions{Client: server.Client()})
	_, err := ai.NewHTTPModelLister(invoker).ListModels(context.Background(), profile, "sk-live")
	if err == nil {
		t.Fatal("loopback target must be rejected by the SSRF guard")
	}
	// 分类必须穿透到调用方：界面要能说「改地址」，而不是笼统的「请求失败」。
	if got := ai.ReasonForProviderFailure(err); got != ai.ReasonUnsafeTarget {
		t.Fatalf("reason = %q", got)
	}
	// 允许 http 也不该让回环地址变得可达。
	allowed := ai.NewHTTPInvoker(ai.HTTPInvokerOptions{Client: server.Client(), AllowHTTP: true})
	_, err = ai.NewHTTPModelLister(allowed).ListModels(context.Background(), profile, "sk-live")
	if err == nil || ai.ReasonForProviderFailure(err) != ai.ReasonUnsafeTarget {
		t.Fatalf("http must not open loopback targets: %v", err)
	}
}

func TestHTTPModelListerWithoutInvokerIsUnavailable(t *testing.T) {
	lister := ai.NewHTTPModelLister(nil)
	if _, err := lister.ListModels(context.Background(), deepseekProfile(), "sk-live"); !errors.Is(err, ai.ErrModelListUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// 本机/内网模型服务（Ollama、vLLM 等）：运营者显式放行后必须真的能连通。
//
// 这里刻意不注入 server.Client，而是让 invoker 自己构造受控客户端，因此这条测试
// 覆盖 scheme → 地址 → 拨号三段校验，证明放行开关确实一路生效到底。
func TestHTTPModelListerReachesLoopbackWhenOperatorOptsIn(t *testing.T) {
	server, recorder := modelListServer(t, `{"object":"list","data":[{"id":"llama3.2"},{"id":"qwen2.5"}]}`)
	profile := localProfile(server)

	invoker := ai.NewHTTPInvoker(ai.HTTPInvokerOptions{AllowHTTP: true, AllowPrivate: true})
	list, err := ai.NewHTTPModelLister(invoker).ListModels(context.Background(), profile, "local-key")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if list.Count != 2 || list.Models[0] != "llama3.2" {
		t.Fatalf("list = %+v", list)
	}
	seen := recorder.seen()
	if seen == nil {
		t.Fatal("the local model server was never called")
	}
	if seen.URL.Path != "/models" || seen.Header.Get("Authorization") != "Bearer local-key" {
		t.Fatalf("request = %s %s auth %q", seen.Method, seen.URL.Path, seen.Header.Get("Authorization"))
	}
}
