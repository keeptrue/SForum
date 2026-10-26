package ai_test

import (
	"context"
	"strings"
	"testing"
	"time"

	modelsai "github.com/keeptrue/sforum/apps/api/app/Models/AI"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// diagnoseSettingsStore 提供一份已启用 deepseek 的配置，让诊断走到真正的调用路径。
type diagnoseSettingsStore struct{ settings supportai.Settings }

func (d *diagnoseSettingsStore) GetSettings(context.Context) (supportai.Settings, error) {
	return d.settings, nil
}
func (d *diagnoseSettingsStore) SaveSettings(_ context.Context, s supportai.Settings, _ int64) (supportai.Settings, error) {
	return s, nil
}
func (d *diagnoseSettingsStore) ResetSettings(_ context.Context, s supportai.Settings, _ int64) (supportai.Settings, error) {
	return s, nil
}

// diagnoseCreds 解析出密钥并报告已配置。
type diagnoseCreds struct{}

func (diagnoseCreds) ResolveAIKey(context.Context, string) (string, error) { return "sk-test", nil }

// stubDiagnoseCreds 报告密钥已配置，让诊断走到真正的调用路径。
type stubDiagnoseCreds struct{}

func (stubDiagnoseCreds) Put(_ context.Context, _, _, _ string) (int64, error) { return 1, nil }
func (stubDiagnoseCreds) Configured(_ context.Context, _ string) (bool, error) { return true, nil }

// diagnoseInvoker 返回一次可编排的补全结果，用来制造「成功但空正文」。
type diagnoseInvoker struct {
	response supportai.WireResponse
	lastMax  int
}

func (d *diagnoseInvoker) Invoke(_ context.Context, _ supportai.Profile, request supportai.WireRequest) (supportai.WireResponse, error) {
	d.lastMax = maxTokensFromBody(request.Body)
	return d.response, nil
}

func maxTokensFromBody(body []byte) int {
	needle := "\"max_tokens\":"
	index := strings.Index(string(body), needle)
	if index < 0 {
		return 0
	}
	rest := string(body)[index+len(needle):]
	value := 0
	for _, char := range rest {
		if char < '0' || char > '9' {
			break
		}
		value = value*10 + int(char-'0')
	}
	return value
}

func diagnoseSettings() supportai.Settings {
	settings := supportai.RecommendedSettings()
	settings.Enabled = true
	settings.Revision = 3
	for i := range settings.Profiles {
		if settings.Profiles[i].ID == "deepseek" {
			settings.Profiles[i].Enabled = true
		}
	}
	return settings
}

func newDiagnoseService(response supportai.WireResponse) (*modelsai.Service, *diagnoseInvoker) {
	invoker := &diagnoseInvoker{response: response}
	store := &diagnoseSettingsStore{settings: diagnoseSettings()}
	gateway := supportai.NewGateway(supportai.GatewayConfig{
		Settings: store,
		Creds:    diagnoseCreds{},
		Invoker:  invoker,
		Clock:    func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) },
	})
	service := modelsai.NewService(modelsai.Config{
		Settings:    store,
		Gateway:     gateway,
		Credentials: stubDiagnoseCreds{},
	})
	return service, invoker
}

// 诊断的价值在于如实报告。一次「调用成功但没有正文」的结果若被当成连接正常，
// 界面上就会出现一条空白的回复条——运营者只会以为功能坏了。
func TestDiagnoseReportsEmptyReplyInsteadOfClaimingSuccess(t *testing.T) {
	service, _ := newDiagnoseService(supportai.WireResponse{
		Text:       "",
		StopReason: supportai.StopReasonLength,
		Usage:      supportai.Usage{InputTokens: 85, OutputTokens: 64},
	})

	result, err := service.Diagnose(context.Background(), managerActor(), "")
	if err != nil {
		t.Fatalf("diagnose failed: %v", err)
	}
	if !result.OK {
		t.Fatal("an empty reply is still a successful round trip, not a transport failure")
	}
	if result.Reason != supportai.DiagnoseReasonEmptyReply {
		t.Fatalf("reason = %q", result.Reason)
	}
	if result.StopReason != supportai.StopReasonLength {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	// 提示必须指向可执行动作：预算被推理耗尽时该调预算，而不是反复重试。
	if result.Error == "" || !strings.Contains(result.Error, "最大输出 token") {
		t.Fatalf("error = %q", result.Error)
	}
}

// 只产出思考、没有正文时，诊断必须说清是哪一种空回复：推理预算被吃光要调预算，
// 什么都产不出才要怀疑协议兼容性。两者合成一句话，运营者只能靠试。
func TestDiagnoseExplainsReasoningOnlyReply(t *testing.T) {
	service, _ := newDiagnoseService(supportai.WireResponse{
		Text:       "",
		Reasoning:  "用户在问连通性，我应该只回复两个字：收到",
		StopReason: supportai.StopReasonEnd,
	})

	result, err := service.Diagnose(context.Background(), managerActor(), "")
	if err != nil {
		t.Fatalf("diagnose failed: %v", err)
	}
	if !result.OK || result.Reason != supportai.DiagnoseReasonEmptyReply {
		t.Fatalf("ok = %v reason = %q", result.OK, result.Reason)
	}
	// 思考正文绝不作为回复回传：它是模型的话，不是给运营者的测试答案。
	if result.Reply != "" {
		t.Fatalf("reply = %q；思考正文不得成为诊断回复", result.Reply)
	}
	if !strings.Contains(result.Error, "思考") || !strings.Contains(result.Error, "最大输出 token") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestDiagnoseStaysSuccessfulWhenTextIsPresent(t *testing.T) {
	service, _ := newDiagnoseService(supportai.WireResponse{
		Text:       "收到",
		StopReason: supportai.StopReasonEnd,
		Usage:      supportai.Usage{InputTokens: 12, OutputTokens: 3},
	})

	result, err := service.Diagnose(context.Background(), managerActor(), "")
	if err != nil {
		t.Fatalf("diagnose failed: %v", err)
	}
	if !result.OK || result.Reason != "" {
		t.Fatalf("ok = %v reason = %q", result.OK, result.Reason)
	}
	if result.Reply != "收到" {
		t.Fatalf("reply = %q", result.Reply)
	}
}

// 64 个 token 的预算对推理模型必然不够：思考先耗尽预算，于是配置正确的站点
// 也会拿到空正文。这条测试把预算钉住，防止它悄悄退回去。
func TestDiagnoseUsesAnOutputBudgetThatSurvivesReasoning(t *testing.T) {
	if supportai.DiagnoseMaxTokens < 256 {
		t.Fatalf("diagnose max tokens = %d, too small for reasoning models", supportai.DiagnoseMaxTokens)
	}
	service, invoker := newDiagnoseService(supportai.WireResponse{
		Text:       "收到",
		StopReason: supportai.StopReasonEnd,
	})
	if _, err := service.Diagnose(context.Background(), managerActor(), ""); err != nil {
		t.Fatalf("diagnose failed: %v", err)
	}
	if invoker.lastMax != supportai.DiagnoseMaxTokens {
		t.Fatalf("requested max_tokens = %d, want %d", invoker.lastMax, supportai.DiagnoseMaxTokens)
	}
}
