package aireply_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func TestGeneratePassesToolAllowlistAndToolContext(t *testing.T) {
	threads := &fakeThreads{thread: sampleThread()}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	poster := &fakePoster{}

	if err := aireply.NewGenerator(threads, completer, poster, nil).Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	input := completer.inputs[0]
	if len(input.Tools) != len(aireply.ReplyToolAllowlist) {
		t.Fatalf("tools = %+v", input.Tools)
	}
	for index, name := range aireply.ReplyToolAllowlist {
		if input.Tools[index] != name {
			t.Fatalf("tool order must follow the allowlist: %+v", input.Tools)
		}
	}
	// 工具以提问者视角、当前楼为单位取数。
	if input.ToolContext.ViewerUserID != 11 || input.ToolContext.TopicID != 9 || input.ToolContext.CommentID != 501 {
		t.Fatalf("tool context = %+v", input.ToolContext)
	}
	if input.PromptVersion != aireply.ReplyPromptVersion {
		t.Fatalf("prompt version = %q", input.PromptVersion)
	}
}

// TestReplyPromptVersionTracksPromptChanges 是一条提醒式断言：提示词语义变化时
// 必须递增版本号，否则历史执行记录无法对应到产出它的那版提示词。
func TestReplyPromptVersionTracksPromptChanges(t *testing.T) {
	if aireply.ReplyPromptVersion != "forum-reply@10" {
		t.Fatalf("prompt version = %q；改动提示词请递增并同步本断言", aireply.ReplyPromptVersion)
	}
}

func TestGenerateIncludesTopicBodyInPrompt(t *testing.T) {
	thread := sampleThread()
	thread.TopicBody = "报错信息是 too many clients already，服务是 Go 写的。"
	threads := &fakeThreads{thread: thread}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}

	if err := aireply.NewGenerator(threads, completer, &fakePoster{}, nil).Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	user := completer.inputs[0].User
	if !strings.Contains(user, "主楼正文") || !strings.Contains(user, "too many clients already") {
		t.Fatalf("topic body missing from the prompt:\n%s", user)
	}
}

func TestGenerateTruncatesTopicBody(t *testing.T) {
	thread := sampleThread()
	thread.TopicBody = strings.Repeat("长", 5_000)
	threads := &fakeThreads{thread: thread}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}

	if err := aireply.NewGenerator(threads, completer, &fakePoster{}, nil).Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	user := completer.inputs[0].User
	if !strings.Contains(user, "…") {
		t.Fatal("oversized topic body must be truncated")
	}
	if len([]rune(user)) > aireply.TopicBodyRuneLimit+1_000 {
		t.Fatalf("prompt too long: %d runes", len([]rune(user)))
	}
}

func TestGenerateOmitsEmptyTopicBody(t *testing.T) {
	threads := &fakeThreads{thread: sampleThread()}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}

	if err := aireply.NewGenerator(threads, completer, &fakePoster{}, nil).Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if strings.Contains(completer.inputs[0].User, "主楼正文") {
		t.Fatalf("empty body must not produce a heading:\n%s", completer.inputs[0].User)
	}
}

// fakePurposeParameters 模拟「用途声明 + 站点覆盖」解析后的结果。
type fakePurposeParameters struct {
	parameters aireply.ReplyPurposeParameters
	weighted   string
	err        error
}

func (f fakePurposeParameters) ReplySystemPrompt(context.Context) (string, error) {
	return f.weighted, nil
}

func (f fakePurposeParameters) ReplyPurposeParameters(context.Context) (aireply.ReplyPurposeParameters, error) {
	return f.parameters, f.err
}

// 运营者显式列名之后，插件的贡献工具才会出现在请求里；Core 工具被移出配置也同样
// 不可见——白名单是「这次对话能用哪些」，不是「站内存在哪些」。
func TestGenerateUsesConfiguredToolAllowlist(t *testing.T) {
	threads := &fakeThreads{thread: sampleThread()}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	generator := aireply.NewGenerator(threads, completer, &fakePoster{}, fakePurposeParameters{
		parameters: aireply.ReplyPurposeParameters{
			CostClass: supportai.CostClassStandard, ToolsAllowed: []string{"forum-search", "vendor-plugin-tool"},
			ToolsOverride: true, PromptVersion: "forum-reply@test",
		},
	})
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	input := completer.inputs[0]
	if len(input.Tools) != 2 || input.Tools[0] != "forum-search" || input.Tools[1] != "vendor-plugin-tool" {
		t.Fatalf("tools = %+v", input.Tools)
	}
}

// 显式空列表是有效配置：回复不允许调用任何工具。
func TestGenerateWithEmptyConfiguredAllowlistPassesNoTools(t *testing.T) {
	threads := &fakeThreads{thread: sampleThread()}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	generator := aireply.NewGenerator(threads, completer, &fakePoster{}, fakePurposeParameters{
		parameters: aireply.ReplyPurposeParameters{ToolsAllowed: []string{}, ToolsOverride: true},
	})
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if tools := completer.inputs[0].Tools; len(tools) != 0 {
		t.Fatalf("tools = %+v, want none", tools)
	}
}

// 输出预算来自该用途成本等级绑定 profile 的 Defaults.MaxTokens：写死一个小值会让
// 推理类模型把预算全花在思考上，永远写不出正文。
func TestGenerateUsesProfileOutputBudget(t *testing.T) {
	threads := &fakeThreads{thread: sampleThread()}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	generator := aireply.NewGenerator(threads, completer, &fakePoster{}, fakePurposeParameters{
		parameters: aireply.ReplyPurposeParameters{MaxTokens: 4096},
	})
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got := completer.inputs[0].MaxTokens; got != 4096 {
		t.Fatalf("max tokens = %d", got)
	}
}

// 参数来源没解析出 profile（MaxTokens=0）时回落到内置下限，而不是把预算变成 0
// ——那会让请求在契约校验就被拒。
func TestGenerateFallsBackToBuiltInOutputBudget(t *testing.T) {
	threads := &fakeThreads{thread: sampleThread()}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	generator := aireply.NewGenerator(threads, completer, &fakePoster{}, fakePurposeParameters{
		parameters: aireply.ReplyPurposeParameters{},
	})
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got := completer.inputs[0].MaxTokens; got != aireply.ReplyMaxTokens {
		t.Fatalf("max tokens = %d, want %d", got, aireply.ReplyMaxTokens)
	}
}

// 读配置失败回退到用途声明的默认列表（只有 Core 内置工具），而不是放开白名单：
// 插件工具永远不能因为一次设置读取失败而意外进入模型可见面。
func TestGenerateFallsBackToDeclaredAllowlistWhenSourceFails(t *testing.T) {
	threads := &fakeThreads{thread: sampleThread()}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	generator := aireply.NewGenerator(threads, completer, &fakePoster{}, fakePurposeParameters{
		err: errors.New("settings unavailable"),
	})
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	input := completer.inputs[0]
	if len(input.Tools) != len(aireply.ReplyToolAllowlist) {
		t.Fatalf("tools = %+v, want the declared default", input.Tools)
	}
	for index, name := range aireply.ReplyToolAllowlist {
		if input.Tools[index] != name {
			t.Fatalf("fallback allowlist drifted: %+v", input.Tools)
		}
	}
}

// localeParameters 在用途参数之外额外提供站点语言，用来验证 LocaleSource 这条
// 可选能力。
type localeParameters struct {
	fakePurposeParameters
	locale string
}

func (l localeParameters) DefaultLocale(context.Context) (string, error) { return l.locale, nil }

// 工具拿到的语言来自站点默认值：Identity 没有 per-user locale 字段，因此这里
// 能给出的只有站点语言。没有 LocaleSource 时保持空值，不阻断回复。
func TestGeneratePassesSiteLocaleToToolContext(t *testing.T) {
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, &fakePoster{}, localeParameters{
		fakePurposeParameters: fakePurposeParameters{parameters: aireply.ReplyPurposeParameters{
			CostClass: supportai.CostClassStandard, ToolsAllowed: []string{"web-search"}, PromptVersion: "forum-reply@test",
		}},
		locale: "zh-CN",
	})
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got := completer.inputs[0].ToolContext.Locale; got != "zh-CN" {
		t.Fatalf("tool context locale = %q", got)
	}

	plain := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	if err := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, plain, &fakePoster{}, nil).Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got := plain.inputs[0].ToolContext.Locale; got != "" {
		t.Fatalf("locale without a source must stay empty, got %q", got)
	}
}

// fakeToolSectioner 记录调用参数并返回可控的工具段。
type fakeToolSectioner struct {
	section     string
	costClasses []string
	names       [][]string
}

func (f *fakeToolSectioner) ToolSection(_ context.Context, costClass string, names []string) string {
	f.costClasses = append(f.costClasses, costClass)
	f.names = append(f.names, append([]string(nil), names...))
	return f.section
}

// fakeToolCallRecorder 记录落库调用。
type fakeToolCallRecorder struct {
	calls      [][]aireply.ToolCall
	commentIDs []int64
	topicIDs   []int64
	runIDs     []string
	err        error
}

func (f *fakeToolCallRecorder) Record(_ context.Context, sourceCommentID, topicID int64, runID string, calls []aireply.ToolCall) error {
	f.commentIDs = append(f.commentIDs, sourceCommentID)
	f.topicIDs = append(f.topicIDs, topicID)
	f.runIDs = append(f.runIDs, runID)
	f.calls = append(f.calls, append([]aireply.ToolCall(nil), calls...))
	return f.err
}

// 工具段是「模型怎么知道自己有 web-search」的唯一途径：它必须出现在 system 里，
// 而不是只存在于请求的 tools 声明里（弱模型不会去读 schema 猜自己有什么能力）。
func TestGenerateInjectsToolSectionIntoSystemPrompt(t *testing.T) {
	threads := &fakeThreads{thread: sampleThread()}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	sectioner := &fakeToolSectioner{section: "本次可调用的工具：\n- web-search：检索站外互联网。"}

	generator := aireply.NewGenerator(threads, completer, &fakePoster{}, nil).
		WithToolSectioner(sectioner)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	system := completer.inputs[0].System
	if !strings.Contains(system, "web-search") {
		t.Fatalf("工具段必须出现在系统指令里：\n%s", system)
	}
	if !strings.HasSuffix(system, supportai.ReplyPromptSafetyAppendix) {
		t.Fatalf("安全尾注必须仍是最后一段：\n%s", system)
	}
	if len(sectioner.names) != 1 || len(sectioner.names[0]) != len(aireply.ReplyToolAllowlist) {
		t.Fatalf("工具段必须按用途白名单生成，got %+v", sectioner.names)
	}
	if sectioner.costClasses[0] != "" && sectioner.costClasses[0] != supportai.CostClassEconomy {
		t.Fatalf("成本等级必须透传，got %q", sectioner.costClasses[0])
	}
}

// 没有装配 tool sectioner 时 system 必须与引入前逐字相同：那是 eval 指纹与历史
// 执行记录可比的前提。
func TestGenerateWithoutToolSectionerKeepsTheOldSystemPrompt(t *testing.T) {
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答"}}
	if err := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, &fakePoster{}, nil).
		Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	want := supportai.ReplySettings{}.EffectiveSystemPrompt()
	if completer.inputs[0].System != want {
		t.Fatalf("system = %q\nwant = %q", completer.inputs[0].System, want)
	}
}

// 轨迹必须在发布成功之后落库，且 Seq 用切片下标（并行调用共享 Step）。
func TestGenerateRecordsToolCallsAfterPublishing(t *testing.T) {
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text:  "回答",
		RunID: "run-abc",
		ToolTrace: []supportai.ToolTraceEntry{
			{Step: 1, Tool: "web-search", Arguments: `{"query":"SForum"}`, LatencyMS: 120},
			{Step: 1, Tool: "forum-search", Arguments: `{"query":"SForum"}`, Failure: supportai.ToolFailureNotAllowed},
		},
	}}
	recorder := &fakeToolCallRecorder{}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, &fakePoster{}, nil).
		WithToolCalls(recorder)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(recorder.calls) != 1 || len(recorder.calls[0]) != 2 {
		t.Fatalf("calls = %+v", recorder.calls)
	}
	if recorder.commentIDs[0] != 9001 || recorder.runIDs[0] != "run-abc" {
		t.Fatalf("attribution = %+v / %+v", recorder.commentIDs, recorder.runIDs)
	}
	if recorder.calls[0][0].Seq != 0 || recorder.calls[0][1].Seq != 1 {
		t.Fatalf("Seq 必须是切片下标：%+v", recorder.calls[0])
	}
	if recorder.calls[0][1].Failure != supportai.ToolFailureNotAllowed {
		t.Fatalf("被拒绝的调用也必须落库（运营者靠它看见模型在撞白名单）：%+v", recorder.calls[0][1])
	}
}

// 发布失败时不得写轨迹：一条从未出现的评论不该有工具记录（外键也会拦）。
func TestGenerateSkipsToolCallsWhenPublishingFails(t *testing.T) {
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text:      "回答",
		ToolTrace: []supportai.ToolTraceEntry{{Step: 1, Tool: "web-search"}},
	}}
	recorder := &fakeToolCallRecorder{}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer,
		&fakePoster{err: errors.New("boom")}, nil).WithToolCalls(recorder)
	if err := generator.Generate(context.Background(), jobInput()); err == nil {
		t.Fatal("publishing failure must surface")
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("发布失败时不得写轨迹：%+v", recorder.calls)
	}
}
