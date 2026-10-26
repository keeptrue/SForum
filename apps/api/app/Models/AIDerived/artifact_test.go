package aiderived_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	aiderived "github.com/keeptrue/sforum/apps/api/app/Models/AIDerived"
	aidomain "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 这一组是 B3 的单元证据：产物模型、提示词组装、生成时序与失败姿态。它们不需要
// 数据库，因此「没有环境也必须全绿」——落库形态由 postgres_test.go 在真库上验证。

func sampleTopic() aidomain.TopicDetail {
	return aidomain.TopicDetail{
		TopicSummary: aidomain.TopicSummary{
			ID:        42,
			Title:     "连接池怎么配才对",
			Status:    "active",
			Author:    &aidomain.UserSummary{ID: 1, Username: "alice", DisplayName: "Alice"},
			ViewCount: 1832,
		},
		Content: aidomain.PublicRenderedContent{PlainText: "线上 QPS 很高，连接池该设多少？"},
	}
}

func sampleComments() []aidomain.Comment {
	return []aidomain.Comment{
		{ID: 500, TopicID: 42, Author: &aidomain.UserSummary{Username: "bob", DisplayName: "Bob"}, Content: aidomain.PublicRenderedContent{PlainText: "池子设 20 就行"}},
		{ID: 501, TopicID: 42, Content: aidomain.PublicRenderedContent{PlainText: "匿名发言"}},
	}
}

// --- 产物模型 ---

func TestNormalizeArtifactDefaultsToDraft(t *testing.T) {
	// 缺省即草稿：本包不存在「默认发布」。这条断言是 B3「默认不自动发布」的
	// 代码级证据，不是注释。
	artifact, err := aiderived.NormalizeArtifact(aiderived.Artifact{
		Purpose:   aiderived.PurposeDigest,
		TopicID:   42,
		PromptVer: supportai.CoreDigestPromptVersion,
		Content:   "  一句话结论  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != aiderived.StatusDraft {
		t.Fatalf("status = %q, want draft", artifact.Status)
	}
	if artifact.Content != "一句话结论" {
		t.Fatalf("content must be trimmed, got %q", artifact.Content)
	}
}

func TestNormalizeArtifactRefusesInvalidShapes(t *testing.T) {
	valid := aiderived.Artifact{
		Purpose:   aiderived.PurposeTour,
		TopicID:   42,
		PromptVer: supportai.CoreTourPromptVersion,
		Content:   "主线",
	}
	cases := map[string]func(aiderived.Artifact) aiderived.Artifact{
		"undeclared purpose": func(a aiderived.Artifact) aiderived.Artifact { a.Purpose = "forum.summarize"; return a },
		"empty purpose":      func(a aiderived.Artifact) aiderived.Artifact { a.Purpose = "  "; return a },
		"zero topic":         func(a aiderived.Artifact) aiderived.Artifact { a.TopicID = 0; return a },
		"missing version":    func(a aiderived.Artifact) aiderived.Artifact { a.PromptVer = " "; return a },
		"empty content":      func(a aiderived.Artifact) aiderived.Artifact { a.Content = "  "; return a },
		"unknown status":     func(a aiderived.Artifact) aiderived.Artifact { a.Status = "archived"; return a },
	}
	for name, mutate := range cases {
		if _, err := aiderived.NormalizeArtifact(mutate(valid)); !errors.Is(err, aiderived.ErrArtifactInvalid) {
			t.Fatalf("%s: must be refused, got %v", name, err)
		}
	}
}

func TestNormalizeArtifactClipsLongContentByRune(t *testing.T) {
	// 按 rune 截断：按字节切会把多字节字符切成非法 UTF-8，存进库后在预览面上
	// 变成乱码——而那正是运营者要审阅的正文。
	long := strings.Repeat("摘要", aiderived.MaxArtifactContentRunes)
	artifact, err := aiderived.NormalizeArtifact(aiderived.Artifact{
		Purpose:   aiderived.PurposeDigest,
		TopicID:   42,
		PromptVer: supportai.CoreDigestPromptVersion,
		Content:   long,
	})
	if err != nil {
		t.Fatal(err)
	}
	runes := []rune(artifact.Content)
	if len(runes) != aiderived.MaxArtifactContentRunes+1 { // +1 是省略号
		t.Fatalf("clipped runes = %d", len(runes))
	}
	if !strings.HasSuffix(artifact.Content, "…") {
		t.Fatal("truncation must be visible")
	}
	if !isValidUTF8(artifact.Content) {
		t.Fatal("clipped content must stay valid UTF-8")
	}
}

func isValidUTF8(value string) bool {
	for _, r := range value {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func TestClampListLimit(t *testing.T) {
	if got := aiderived.ClampListLimit(0); got != aiderived.DefaultArtifactListLimit {
		t.Fatalf("zero limit = %d", got)
	}
	if got := aiderived.ClampListLimit(aiderived.MaxArtifactListLimit + 50); got != aiderived.MaxArtifactListLimit {
		t.Fatalf("oversized limit = %d", got)
	}
	if got := aiderived.ClampListLimit(5); got != 5 {
		t.Fatalf("explicit limit = %d", got)
	}
}

// --- 提示词 ---

func TestBuildPromptCarriesFloorNumbersAndAuthorIdentity(t *testing.T) {
	snapshot := aiderived.TopicSnapshot{Topic: sampleTopic(), Comments: sampleComments()}
	digest := aiderived.BuildDigestPrompt(snapshot)
	for _, want := range []string{"话题 #42", "Alice（@alice）", "#500", "Bob（@bob）", "池子设 20 就行", "#501", "匿名"} {
		if !strings.Contains(digest, want) {
			t.Fatalf("digest prompt is missing %q:\n%s", want, digest)
		}
	}
	tour := aiderived.BuildTourPrompt(snapshot)
	for _, want := range []string{"话题 #42", "#500", "楼层清单"} {
		if !strings.Contains(tour, want) {
			t.Fatalf("tour prompt is missing %q:\n%s", want, tour)
		}
	}
}

func TestBuildPromptDistinguishesEmptyThread(t *testing.T) {
	// 没有评论时必须明说，而不是给出一份空清单让模型自己猜——否则模型会
	// 「总结」出一段并不存在的讨论。
	empty := aiderived.TopicSnapshot{Topic: sampleTopic()}
	if prompt := aiderived.BuildDigestPrompt(empty); !strings.Contains(prompt, "还没有评论") {
		t.Fatalf("empty digest prompt = %q", prompt)
	}
	if prompt := aiderived.BuildTourPrompt(empty); !strings.Contains(prompt, "还没有评论") {
		t.Fatalf("empty tour prompt = %q", prompt)
	}
}

func TestBuildPromptTruncatesOverlongThreads(t *testing.T) {
	topic := sampleTopic()
	var comments []aidomain.Comment
	for index := 0; index < 200; index++ {
		comments = append(comments, aidomain.Comment{
			ID:      int64(1000 + index),
			TopicID: topic.ID,
			Content: aidomain.PublicRenderedContent{PlainText: strings.Repeat("话", 600)},
		})
	}
	digest := aiderived.BuildDigestPrompt(aiderived.TopicSnapshot{Topic: topic, Comments: comments})
	if !strings.Contains(digest, "讨论更长") {
		t.Fatal("a truncated digest prompt must say so")
	}
	// 提示词体积必须可预期：截断若失效，一次蒸馏会把整栋楼塞进请求。
	if len([]rune(digest)) > 60_000 {
		t.Fatalf("digest prompt runes = %d", len([]rune(digest)))
	}
}

func TestSystemPromptForUnknownPurposeIsEmpty(t *testing.T) {
	// 未登记的用途没有系统指令：调用方据此失败关闭，而不是带着空指令去调模型。
	if got := aiderived.SystemPromptFor("forum.summarize"); got != "" {
		t.Fatalf("unknown purpose prompt = %q", got)
	}
	for _, purpose := range []string{aiderived.PurposeDigest, aiderived.PurposeTour} {
		prompt := aiderived.SystemPromptFor(purpose)
		if prompt == "" {
			t.Fatalf("%s must have a system prompt", purpose)
		}
		// 防注入尾注不可绕过：话题正文是社区成员写的不可信输入。
		if !strings.Contains(prompt, supportai.ReplyPromptSafetyAppendix) {
			t.Fatalf("%s is missing the safety appendix", purpose)
		}
	}
}

// --- 生成时序与失败姿态 ---

type fakeTopicReader struct {
	topic aidomain.TopicDetail
	err   error
	calls int
}

func (r *fakeTopicReader) GetTopic(context.Context, int64) (aidomain.TopicDetail, error) {
	r.calls++
	return r.topic, r.err
}

type fakeCommentReader struct {
	items aidomain.CommentList
	err   error
	calls int
}

func (r *fakeCommentReader) ListComments(context.Context, aidomain.CommentListInput) (aidomain.CommentList, error) {
	r.calls++
	return r.items, r.err
}

type fakeArtifacts struct {
	inserted []aiderived.Artifact
	err      error
}

func (s *fakeArtifacts) Insert(_ context.Context, artifact aiderived.Artifact) (aiderived.Artifact, error) {
	if s.err != nil {
		return aiderived.Artifact{}, s.err
	}
	artifact.ID = int64(len(s.inserted) + 1)
	artifact.CreatedAt = time.Now()
	s.inserted = append(s.inserted, artifact)
	return artifact, nil
}

func (s *fakeArtifacts) Get(context.Context, int64) (aiderived.Artifact, error) {
	return aiderived.Artifact{}, aiderived.ErrArtifactNotFound
}

func (s *fakeArtifacts) List(context.Context, aiderived.ArtifactFilter) ([]aiderived.Artifact, error) {
	return append([]aiderived.Artifact(nil), s.inserted...), nil
}

type fakeExecutor struct {
	requests []supportai.CompletionRequest
	result   supportai.CompletionResult
	err      error
}

func (e *fakeExecutor) Execute(_ context.Context, request supportai.CompletionRequest) (supportai.CompletionResult, error) {
	e.requests = append(e.requests, request)
	return e.result, e.err
}

func newGenerator(t *testing.T, executor *fakeExecutor, artifacts *fakeArtifacts) *aiderived.Generator {
	t.Helper()
	return aiderived.NewGenerator(
		&fakeTopicReader{topic: sampleTopic()},
		&fakeCommentReader{items: aidomain.CommentList{Items: sampleComments()}},
		executor,
		artifacts,
		nil,
	)
}

func TestGenerateWritesDraftAndPassesDeclaredParameters(t *testing.T) {
	executor := &fakeExecutor{result: supportai.CompletionResult{Text: "一句话结论：设 20。"}}
	artifacts := &fakeArtifacts{}
	generator := newGenerator(t, executor, artifacts)

	artifact, err := generator.Generate(context.Background(), aiderived.GenerateInput{
		Purpose:     aiderived.PurposeDigest,
		TopicID:     42,
		RequestedBy: 7,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 请求形状：用途 / 成本等级 / 提示词版本全部来自用途声明，调用方不能自选。
	if len(executor.requests) != 1 {
		t.Fatalf("requests = %d", len(executor.requests))
	}
	request := executor.requests[0]
	if request.Purpose != supportai.CorePurposeForumDigest {
		t.Fatalf("purpose = %q", request.Purpose)
	}
	if request.CostClass != supportai.CostClassEconomy {
		t.Fatalf("cost class = %q", request.CostClass)
	}
	if request.PromptVersion != supportai.CoreDigestPromptVersion {
		t.Fatalf("prompt version = %q", request.PromptVersion)
	}
	if request.SubjectUserID != 0 {
		t.Fatalf("derived work must bill the site, not a member: subject=%d", request.SubjectUserID)
	}
	// 归属：类型是派生的（与回复的 ai.run 分开），id 是本次派生的关联 id——
	// 它必须与产物的 run_id 逐字相同，否则「这份草稿花了多少」无法 join。
	if request.Metadata.ResourceType != "ai.derived" {
		t.Fatalf("metadata resource type = %q", request.Metadata.ResourceType)
	}
	if strings.TrimSpace(request.Metadata.ResourceID) == "" {
		t.Fatal("derived execution must carry a correlation id")
	}
	if artifact.RunID != request.Metadata.ResourceID {
		t.Fatalf("artifact run id %q != execution resource id %q", artifact.RunID, request.Metadata.ResourceID)
	}
	if strings.TrimSpace(request.System) == "" {
		t.Fatal("the system prompt must not be empty")
	}
	if len(request.Messages) != 1 || len(request.Messages[0].Parts) != 1 {
		t.Fatalf("messages = %+v", request.Messages)
	}

	// 产物形状：草稿、带版本、带发起者。
	if artifact.Status != aiderived.StatusDraft {
		t.Fatalf("status = %q", artifact.Status)
	}
	if artifact.TopicID != 42 || artifact.CreatedBy != 7 {
		t.Fatalf("artifact = %+v", artifact)
	}
	if artifact.PromptVer != supportai.CoreDigestPromptVersion {
		t.Fatalf("artifact prompt version = %q", artifact.PromptVer)
	}
	if len(artifacts.inserted) != 1 {
		t.Fatalf("inserts = %d", len(artifacts.inserted))
	}
}

func TestGenerateRefusesEmptyModelOutput(t *testing.T) {
	// 空产出是确定性失败：重试只会得到同样的空正文，却把账单乘以尝试次数。
	// 同时它必须**不写行**——一张空草稿在预览面上与「没跑过」无法区分。
	artifacts := &fakeArtifacts{}
	generator := newGenerator(t, &fakeExecutor{result: supportai.CompletionResult{Text: "   ", Reasoning: "想了很久"}}, artifacts)
	_, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeTour, TopicID: 42})
	if !errors.Is(err, aiderived.ErrEmptyArtifact) {
		t.Fatalf("err = %v", err)
	}
	// 可解释性：光说「空」运营者无从下手。推理模型吃掉预算是最常见的形态，
	// 错误文本必须指出来（与 diagnose.go 的判据一致）。
	if !strings.Contains(err.Error(), "思考") || !strings.Contains(err.Error(), "maxTokens") {
		t.Fatalf("an empty artifact must explain itself: %v", err)
	}
	if len(artifacts.inserted) != 0 {
		t.Fatal("an empty artifact must not be stored")
	}
}

// 三种空产出的成因必须能被区分开：它们的处置完全不同（抬预算 / 换非推理模型 /
// 查协议 / 查内容策略）。一句笼统的「空正文」会让运营者只能试。
func TestEmptyArtifactHintDistinguishesCauses(t *testing.T) {
	artifacts := &fakeArtifacts{}
	cases := []struct {
		name     string
		result   supportai.CompletionResult
		contains string
	}{
		{
			name:     "reasoning ate the budget",
			result:   supportai.CompletionResult{Reasoning: "先回忆一下默认值……", StopReason: supportai.StopReasonLength, Usage: supportai.Usage{OutputTokens: 2048}},
			contains: "思考",
		},
		{
			name:     "filtered",
			result:   supportai.CompletionResult{StopReason: supportai.StopReasonFiltered},
			contains: "过滤",
		},
		{
			name:     "unknown empty completion",
			result:   supportai.CompletionResult{StopReason: supportai.StopReasonEnd},
			contains: "不兼容",
		},
	}
	for _, testCase := range cases {
		generator := newGenerator(t, &fakeExecutor{result: testCase.result}, artifacts)
		_, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeDigest, TopicID: 42})
		if !errors.Is(err, aiderived.ErrEmptyArtifact) {
			t.Fatalf("%s: err = %v", testCase.name, err)
		}
		if !strings.Contains(err.Error(), testCase.contains) {
			t.Fatalf("%s: hint must mention %q, got %v", testCase.name, testCase.contains, err)
		}
	}

	// 推理正文绝不进产物，也绝不进错误里的「内容」位置：它是模型的自言自语。
	artifacts = &fakeArtifacts{}
	generator := newGenerator(t, &fakeExecutor{result: supportai.CompletionResult{Reasoning: "SECRET-THOUGHT"}}, artifacts)
	_, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeDigest, TopicID: 42})
	if err == nil || strings.Contains(err.Error(), "SECRET-THOUGHT") {
		t.Fatalf("reasoning text must never be surfaced as content: %v", err)
	}
}

func TestGenerateNormalizesMissingAndInvisibleTopics(t *testing.T) {
	// 不存在与不可见必须给出同一个答案，否则这条路径就是一个话题存在性探测器。
	for name, readErr := range map[string]error{
		"missing":  aidomain.ErrTopicNotFound,
		"internal": errors.New("boom"),
	} {
		artifacts := &fakeArtifacts{}
		generator := aiderived.NewGenerator(
			&fakeTopicReader{err: readErr},
			&fakeCommentReader{items: aidomain.CommentList{Items: sampleComments()}},
			&fakeExecutor{result: supportai.CompletionResult{Text: "x"}},
			artifacts, nil,
		)
		_, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeDigest, TopicID: 42})
		if err == nil {
			t.Fatalf("%s: must fail", name)
		}
		if name == "missing" && !errors.Is(err, aiderived.ErrTopicUnavailable) {
			t.Fatalf("missing topic must normalize to ErrTopicUnavailable, got %v", err)
		}
		if name == "internal" && errors.Is(err, aiderived.ErrTopicUnavailable) {
			// 内部故障不该被伪装成「话题不存在」：那会让运维去查话题状态。
			t.Fatalf("an internal read failure must not look like a missing topic: %v", err)
		}
		if len(artifacts.inserted) != 0 {
			t.Fatalf("%s: nothing may be stored on failure", name)
		}
	}
}

func TestGenerateRefusesCommentReadFailure(t *testing.T) {
	// 主题可见但评论读不出来时，「产出一份没有讨论的摘要」比直接失败更糟：
	// 运营者会以为楼里真的没人说话。
	artifacts := &fakeArtifacts{}
	generator := aiderived.NewGenerator(
		&fakeTopicReader{topic: sampleTopic()},
		&fakeCommentReader{err: errors.New("boom")},
		&fakeExecutor{result: supportai.CompletionResult{Text: "x"}},
		artifacts, nil,
	)
	if _, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeDigest, TopicID: 42}); err == nil {
		t.Fatal("a comment read failure must not produce an artifact")
	}
	if len(artifacts.inserted) != 0 {
		t.Fatal("nothing may be stored")
	}
}

func TestGenerateDoesNotStoreWhenGatewayFails(t *testing.T) {
	artifacts := &fakeArtifacts{}
	generator := newGenerator(t, &fakeExecutor{err: supportai.ErrGatewayDisabled}, artifacts)
	_, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeDigest, TopicID: 42})
	if !errors.Is(err, supportai.ErrGatewayDisabled) {
		t.Fatalf("err = %v", err)
	}
	if len(artifacts.inserted) != 0 {
		t.Fatal("a gateway failure must not leave a row")
	}
}

func TestGenerateRefusesUndelcaredPurposeAndBadTopic(t *testing.T) {
	executor := &fakeExecutor{result: supportai.CompletionResult{Text: "x"}}
	artifacts := &fakeArtifacts{}
	generator := newGenerator(t, executor, artifacts)

	if _, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: "forum.summarize", TopicID: 42}); !errors.Is(err, aiderived.ErrArtifactInvalid) {
		t.Fatalf("undeclared purpose: %v", err)
	}
	if _, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeDigest, TopicID: 0}); !errors.Is(err, aiderived.ErrArtifactInvalid) {
		t.Fatalf("zero topic: %v", err)
	}
	if len(executor.requests) != 0 {
		t.Fatal("an invalid request must not reach the gateway")
	}
}

func TestGenerateRefusesWhenNotWired(t *testing.T) {
	var generator *aiderived.Generator
	if _, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeDigest, TopicID: 42}); !errors.Is(err, aiderived.ErrGeneratorUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// parameterSource 让测试观察「站点覆盖是否真的生效」。
type parameterSource struct {
	parameters aiderived.PurposeParameters
	err        error
}

func (s parameterSource) DerivedPurposeParameters(context.Context, string) (aiderived.PurposeParameters, error) {
	return s.parameters, s.err
}

func TestGenerateUsesConfiguredParametersOverDeclaredDefaults(t *testing.T) {
	executor := &fakeExecutor{result: supportai.CompletionResult{Text: "一句话结论"}}
	generator := aiderived.NewGenerator(
		&fakeTopicReader{topic: sampleTopic()},
		&fakeCommentReader{items: aidomain.CommentList{Items: sampleComments()}},
		executor,
		&fakeArtifacts{},
		parameterSource{parameters: aiderived.PurposeParameters{
			CostClass: supportai.CostClassStandard, PromptVersion: "forum-digest@9", MaxTokens: 2_048,
		}},
	)
	if _, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeDigest, TopicID: 42}); err != nil {
		t.Fatal(err)
	}
	request := executor.requests[0]
	if request.CostClass != supportai.CostClassStandard || request.PromptVersion != "forum-digest@9" || request.MaxTokens != 2_048 {
		t.Fatalf("configured parameters were not applied: %+v", request)
	}
}

func TestGenerateFallsBackToDeclaredDefaultsOnParameterFailure(t *testing.T) {
	// 配置存储抖动能回退到声明（与 aireply.resolveParameters 同一纪律），
	// 但绝不能回退到「随便挑一档」：成本等级必须仍是声明里的 economy。
	executor := &fakeExecutor{result: supportai.CompletionResult{Text: "一句话结论"}}
	generator := aiderived.NewGenerator(
		&fakeTopicReader{topic: sampleTopic()},
		&fakeCommentReader{items: aidomain.CommentList{Items: sampleComments()}},
		executor,
		&fakeArtifacts{},
		parameterSource{err: errors.New("settings store down")},
	)
	if _, err := generator.Generate(context.Background(), aiderived.GenerateInput{Purpose: aiderived.PurposeTour, TopicID: 42}); err != nil {
		t.Fatal(err)
	}
	request := executor.requests[0]
	if request.CostClass != supportai.CostClassEconomy {
		t.Fatalf("cost class = %q", request.CostClass)
	}
	if request.PromptVersion != supportai.CoreTourPromptVersion {
		t.Fatalf("prompt version = %q", request.PromptVersion)
	}
	// 解析不出预算时用本包的构造上限，而不是回复用途那个 512（会把蒸馏截断
	// 在第一条要点）。
	if request.MaxTokens <= 512 {
		t.Fatalf("max tokens = %d, want the derived floor", request.MaxTokens)
	}
}

// --- 服务层权限 ---

type fakeEnqueuer struct {
	inputs []aiderived.EnqueueInput
	err    error
}

func (e *fakeEnqueuer) EnqueueDerive(_ context.Context, input aiderived.EnqueueInput) (string, error) {
	e.inputs = append(e.inputs, input)
	if e.err != nil {
		return "", e.err
	}
	return input.RequestID, nil
}

func managerActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAIManage: true},
	}
}

func memberActor() identity.Actor {
	return identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
}

func TestServiceRequiresAIManageOnEveryEntry(t *testing.T) {
	// 复用 ai.manage，不注册新权限键：手动触发是管理动作。
	enqueuer := &fakeEnqueuer{}
	service := aiderived.NewService(aiderived.ServiceConfig{Artifacts: &fakeArtifacts{}, Enqueuer: enqueuer})
	ctx := context.Background()

	if _, err := service.Enqueue(ctx, memberActor(), aiderived.EnqueueInput{Purpose: aiderived.PurposeDigest, TopicID: 42}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := service.List(ctx, memberActor(), aiderived.ArtifactFilter{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("list: %v", err)
	}
	if _, err := service.Get(ctx, memberActor(), 1); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("get: %v", err)
	}
	// 被拒绝的调用不得入队：否则一次无权限请求会在 worker 里真的花掉配额。
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("a denied enqueue must not reach the queue: %+v", enqueuer.inputs)
	}
}

func TestServiceEnqueueGeneratesRequestIDAndRecordsActor(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	service := aiderived.NewService(aiderived.ServiceConfig{Artifacts: &fakeArtifacts{}, Enqueuer: enqueuer})

	first, err := service.Enqueue(context.Background(), managerActor(), aiderived.EnqueueInput{Purpose: aiderived.PurposeTour, TopicID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(first) == "" {
		t.Fatal("the service must supply a request id")
	}
	second, err := service.Enqueue(context.Background(), managerActor(), aiderived.EnqueueInput{Purpose: aiderived.PurposeTour, TopicID: 42})
	if err != nil {
		t.Fatal(err)
	}
	// 两次独立触发的标识必须不同：相同就会被 River 的 Unique ByArgs 去重，
	// 运营者「重新生成一份」会永远失败。
	if first == second {
		t.Fatalf("request ids must differ per trigger: %q", first)
	}
	if enqueuer.inputs[0].RequestedBy != 1 {
		t.Fatalf("requestedBy = %d", enqueuer.inputs[0].RequestedBy)
	}
}

func TestServiceRefusesInvalidFilters(t *testing.T) {
	service := aiderived.NewService(aiderived.ServiceConfig{Artifacts: &fakeArtifacts{}, Enqueuer: &fakeEnqueuer{}})
	ctx := context.Background()
	if _, err := service.List(ctx, managerActor(), aiderived.ArtifactFilter{Purpose: "forum.summarize"}); !errors.Is(err, aiderived.ErrArtifactInvalid) {
		t.Fatalf("bad purpose filter: %v", err)
	}
	if _, err := service.List(ctx, managerActor(), aiderived.ArtifactFilter{Status: "archived"}); !errors.Is(err, aiderived.ErrArtifactInvalid) {
		t.Fatalf("bad status filter: %v", err)
	}
	if _, err := service.Enqueue(ctx, managerActor(), aiderived.EnqueueInput{Purpose: "forum.summarize", TopicID: 42}); !errors.Is(err, aiderived.ErrArtifactInvalid) {
		t.Fatalf("bad enqueue purpose: %v", err)
	}
}
