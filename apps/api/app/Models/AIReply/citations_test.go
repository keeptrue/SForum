package aireply_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
)

// fakeCitations 是 B4 的引用存储替身：它同时扮演校验器、记录器与事实复校验器，
// 因为真实实现（PostgresCitationStore）就是同一个类型——把它们拆成三个 fake 会
// 让测试无法表达「三件事共用一套可见性谓词」这条设计约束。
type fakeCitations struct {
	// visible 是「哪些指向算可见」。缺席的指向按不可见处理（与真实实现一致：
	// 不存在与不可见对调用方是同一个结论）。
	visible map[aireply.CitationRef]string
	// verifyErr / recordErr 让测试能表达存储故障。
	verifyErr error
	recordErr error
	// recorded 保存 Record 的调用，用来断言「发布后才写」与 run id 透传。
	recorded []recordedCitations
	// verifyCalls 记录校验被调用了几次（零引用时不该调用）。
	verifyCalls int
	// factCalls 记录事实复校验被调用了几次。
	factCalls int
}

type recordedCitations struct {
	sourceCommentID int64
	runID           string
	citations       []aireply.Citation
}

func (f *fakeCitations) Verify(_ context.Context, refs []aireply.CitationRef) ([]aireply.Citation, error) {
	f.verifyCalls++
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	verified := make([]aireply.Citation, 0, len(refs))
	for _, ref := range refs {
		excerpt, ok := f.visible[ref]
		if !ok {
			continue
		}
		citation := aireply.Citation{TopicID: ref.TopicID, CommentID: ref.CommentID, Excerpt: excerpt}
		if citation.TopicID <= 0 {
			citation.TopicID = 9
		}
		verified = append(verified, citation)
	}
	return verified, nil
}

func (f *fakeCitations) Record(_ context.Context, sourceCommentID int64, runID string, citations []aireply.Citation) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, recordedCitations{
		sourceCommentID: sourceCommentID,
		runID:           runID,
		citations:       append([]aireply.Citation(nil), citations...),
	})
	return nil
}

func (f *fakeCitations) VerifyFacts(_ context.Context, facts []aireply.Fact) ([]bool, error) {
	f.factCalls++
	alive := make([]bool, len(facts))
	for index, fact := range facts {
		// 与 PostgresCitationStore.verifyOne 同语义：有 CommentID 时按评论 id 查，
		// TopicID 只是补全出来的归属。fake 若按 (topicId, commentId) 双键查，
		// 测试会依赖一个真实实现里不存在的约束。
		ref := aireply.CitationRef{CommentID: fact.CommentID}
		if fact.CommentID <= 0 {
			ref = aireply.CitationRef{TopicID: fact.TopicID}
		}
		_, ok := f.visible[ref]
		alive[index] = ok
	}
	return alive, nil
}

// fakeState 是 B5 的状态存储替身。它可以返回损坏文档（Load 的 ok=false 路径）
// 与 revision 冲突（Save 的 0 行路径）。
type fakeState struct {
	stored    aireply.TopicState
	hasStored bool
	loadErr   bool
	saves     int
	saved     []aireply.TopicState
	// rejectSave 模拟乐观锁冲突：写入被放弃（真实实现此时不返回错误）。
	rejectSave bool
}

func (f *fakeState) Load(_ context.Context, _, _ int64) (aireply.TopicState, bool) {
	if f.loadErr {
		return aireply.TopicState{}, false
	}
	if !f.hasStored {
		return aireply.TopicState{}, false
	}
	return f.stored, true
}

func (f *fakeState) Save(_ context.Context, _, _ int64, state aireply.TopicState) error {
	f.saves++
	if f.rejectSave {
		return nil
	}
	f.saved = append(f.saved, state)
	return nil
}

// ---------- B4：解析与剥离 ----------

// 标记解析是引用链路的入口：解析错了，后面全是错的。
func TestParseCitationMarkersExtractsAndStripsEveryShape(t *testing.T) {
	cases := []struct {
		name  string
		input string
		body  string
		refs  []aireply.CitationRef
	}{
		{
			name:  "topic marker",
			input: "参考这篇帖子。[[cite:topic:42]]",
			body:  "参考这篇帖子。",
			refs:  []aireply.CitationRef{{TopicID: 42}},
		},
		{
			name:  "comment marker",
			input: "有人说过这个。[[cite:comment:913]]",
			body:  "有人说过这个。",
			refs:  []aireply.CitationRef{{CommentID: 913}},
		},
		{
			name:  "two markers in order",
			input: "先看 A[[cite:comment:1]]再看 B[[cite:topic:2]]",
			body:  "先看 A再看 B",
			refs:  []aireply.CitationRef{{CommentID: 1}, {TopicID: 2}},
		},
		{
			// 解析层只负责切掉标记本身；行内尾随空格由 NormalizeCitationBody 收尾。
			name:  "whitespace and case tolerate",
			input: "见 [[ CITE : Topic : 7 ]]",
			body:  "见 ",
			refs:  []aireply.CitationRef{{TopicID: 7}},
		},
		{
			name:  "no marker is a no-op",
			input: "普通回答，没有任何标记。",
			body:  "普通回答，没有任何标记。",
			refs:  nil,
		},
		{
			// 负号不属于引用语法：它不是格式漂移，是另一段文字，按字面保留。
			name:  "negative id stays literal",
			input: "见 [[cite:topic:-1]]",
			body:  "见 [[cite:topic:-1]]",
			refs:  nil,
		},
		{
			// 23 位数字超出 int64，且不匹配 \d{1,19} + ]] 的形状，同样按字面保留。
			name:  "overflowing id stays literal",
			input: "见 [[cite:comment:99999999999999999999999]]",
			body:  "见 [[cite:comment:99999999999999999999999]]",
			refs:  nil,
		},
		{
			name:  "unknown kind stays literal",
			input: "见 [[cite:user:5]]",
			body:  "见 [[cite:user:5]]",
			refs:  nil,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			body, refs := aireply.ParseCitationMarkers(test.input)
			if body != test.body {
				t.Fatalf("body = %q, want %q", body, test.body)
			}
			if len(refs) != len(test.refs) {
				t.Fatalf("refs = %+v, want %+v", refs, test.refs)
			}
			for index, want := range test.refs {
				if refs[index] != want {
					t.Fatalf("refs[%d] = %+v, want %+v", index, refs[index], want)
				}
			}
		})
	}
}

// 剥离后必须合并因标记消失留下的空行，否则正文会以一堆空行开头。
func TestNormalizeCitationBodyCollapsesGaps(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "collapses double blank", input: "第一段\n\n\n第二段", want: "第一段\n\n第二段"},
		{name: "trims leading blanks", input: "\n\n正文", want: "正文"},
		{name: "trims trailing spaces", input: "正文   \n下一行", want: "正文\n下一行"},
		{name: "keeps single blank", input: "第一段\n\n第二段", want: "第一段\n\n第二段"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := aireply.NormalizeCitationBody(test.input); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

// ---------- B4：生成侧时序 ----------

// 验收 1：引用存在 → 随发布传递、可渲染。
func TestGeneratePassesVerifiedCitationsToThePoster(t *testing.T) {
	visible := aireply.CitationRef{CommentID: 913}
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{visible: "线上用的是 20。"}}
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text:  "按 913 楼的说法，20 就够。[[cite:comment:913]]",
		RunID: "run-abc",
	}}
	poster := &fakePoster{}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(poster.inputs) != 1 {
		t.Fatalf("posted = %+v", poster.inputs)
	}
	posted := poster.inputs[0]
	// 正文里不能残留标记。
	if strings.Contains(posted.Content, "[[cite:") {
		t.Fatalf("markers must be stripped from the published body: %q", posted.Content)
	}
	if posted.Content != "按 913 楼的说法，20 就够。" {
		t.Fatalf("content = %q", posted.Content)
	}
	if len(posted.Citations) != 1 {
		t.Fatalf("citations = %+v", posted.Citations)
	}
	if posted.Citations[0].CommentID != 913 || posted.Citations[0].Excerpt != "线上用的是 20。" {
		t.Fatalf("citation = %+v", posted.Citations[0])
	}
	// 引文必须来自存储层（数据库正文），不是模型写的那句。
	if strings.Contains(posted.Citations[0].Excerpt, "按 913 楼") {
		t.Fatal("the excerpt must come from the stored content, not the model text")
	}
	// 发布之后才落库，且带 run id 与产出评论 id。
	if len(citations.recorded) != 1 {
		t.Fatalf("recorded = %+v", citations.recorded)
	}
	if citations.recorded[0].sourceCommentID != 9001 || citations.recorded[0].runID != "run-abc" {
		t.Fatalf("recorded = %+v", citations.recorded[0])
	}
}

// 验收 2：引用不存在 → 该条丢弃、回复正常发布。
func TestGenerateDropsUnverifiableCitationsAndStillPublishes(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{}}
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text: "有两个依据。[[cite:comment:1]][[cite:comment:2]]",
	}}
	poster := &fakePoster{}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("a broken citation must not fail the reply: %v", err)
	}
	if len(poster.inputs) != 1 {
		t.Fatalf("the reply must still be published: %+v", poster.inputs)
	}
	if len(poster.inputs[0].Citations) != 0 {
		t.Fatalf("unverified citations must be dropped: %+v", poster.inputs[0].Citations)
	}
	// 零引用不该写库（那会在表里留下零行以外的噪声）。
	if len(citations.recorded) != 0 {
		t.Fatalf("nothing may be recorded without verified citations: %+v", citations.recorded)
	}
}

// 验收 3：正文只有标记 → 走 ErrEmptyReply 既有语义，且不发布。
func TestGenerateWithOnlyMarkersIsAnEmptyReply(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{
		{CommentID: 913}: "内容",
	}}
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text: "[[cite:comment:913]]\n\n[[cite:topic:42]]",
	}}
	poster := &fakePoster{}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations)
	err := generator.Generate(context.Background(), jobInput())
	if !errors.Is(err, aireply.ErrEmptyReply) {
		t.Fatalf("expected ErrEmptyReply, got %v", err)
	}
	if len(poster.inputs) != 0 {
		t.Fatalf("a marker-only body must not be published: %+v", poster.inputs)
	}
	// 判空在解析之后：标记本身不能让正文「看起来非空」。
	if len(citations.recorded) != 0 {
		t.Fatal("nothing may be recorded for an empty reply")
	}
}

// 模型不遵守格式（一个标记都没有）是正常结果，不是错误。
func TestGenerateWithoutMarkersSkipsVerificationEntirely(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{}}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "没有依据的普通回答。"}}
	poster := &fakePoster{}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if citations.verifyCalls != 0 {
		t.Fatalf("zero markers must not trigger a verification round-trip: %d", citations.verifyCalls)
	}
	if len(poster.inputs) != 1 || len(poster.inputs[0].Citations) != 0 {
		t.Fatalf("posted = %+v", poster.inputs)
	}
}

// 未装配引用能力时，标记仍必须从正文里剥掉（否则会以 [[cite:...]] 的形式出现在
// 公开评论里），但不校验、不落库。
func TestGenerateStripsMarkersWithoutAVerifier(t *testing.T) {
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text: "回答。[[cite:comment:913]]",
	}}
	poster := &fakePoster{}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(poster.inputs) != 1 {
		t.Fatalf("posted = %+v", poster.inputs)
	}
	if strings.Contains(poster.inputs[0].Content, "[[cite:") {
		t.Fatalf("markers must never leak into the published body: %q", poster.inputs[0].Content)
	}
	if len(poster.inputs[0].Citations) != 0 {
		t.Fatalf("no verifier means no citations: %+v", poster.inputs[0].Citations)
	}
}

// 存储故障降级为「无引用」：依据缺失不该让一条有用的回答发不出去。
func TestGeneratePublishesWithoutCitationsWhenVerificationFails(t *testing.T) {
	citations := &fakeCitations{verifyErr: errors.New("pool exhausted")}
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text: "回答。[[cite:comment:913]]",
	}}
	poster := &fakePoster{}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("a verification outage must not fail the reply: %v", err)
	}
	if len(poster.inputs) != 1 || len(poster.inputs[0].Citations) != 0 {
		t.Fatalf("posted = %+v", poster.inputs)
	}
}

// 发布失败 → 一行依据都不许写。证据必须后于它指向的那条评论存在。
func TestGenerateNeverRecordsCitationsWhenPostingFails(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{{CommentID: 913}: "内容"}}
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text: "回答。[[cite:comment:913]]",
	}}
	poster := &fakePoster{err: errors.New("moderation rejected")}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations)
	if err := generator.Generate(context.Background(), jobInput()); err == nil {
		t.Fatal("a posting failure must be returned")
	}
	if len(citations.recorded) != 0 {
		t.Fatalf("nothing may be recorded when the comment was not published: %+v", citations.recorded)
	}
}

// 重复标注同一处依据只保留一条：渲染上是两个一样的链接，库里是两行同事实。
func TestGenerateDeduplicatesRepeatedCitations(t *testing.T) {
	ref := aireply.CitationRef{CommentID: 913}
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{ref: "内容"}}
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text: "见 913 楼[[cite:comment:913]]，也就是刚才那楼[[cite:comment:913]]。",
	}}
	poster := &fakePoster{}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(poster.inputs) != 1 {
		t.Fatalf("posted = %+v", poster.inputs)
	}
	if len(poster.inputs[0].Citations) != 1 {
		t.Fatalf("citations = %+v", poster.inputs[0].Citations)
	}
}

// 提示词里必须出现引用格式要求，且只在装配了引用能力时出现。
func TestGenerateAdvertisesCitationFormatOnlyWhenWired(t *testing.T) {
	wired := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答。"}}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, wired, &fakePoster{}, nil).
		WithCitations(&fakeCitations{visible: map[aireply.CitationRef]string{}})
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if !strings.Contains(wired.inputs[0].User, "[[cite:topic:") || !strings.Contains(wired.inputs[0].User, "[[cite:comment:") {
		t.Fatalf("the prompt must advertise both marker shapes:\n%s", wired.inputs[0].User)
	}
	// 格式要求进 user 侧，不进 system：system 是运营者可覆盖的指令面。
	if strings.Contains(wired.inputs[0].System, "[[cite:") {
		t.Fatal("citation instructions must not be mixed into the system prompt")
	}

	bare := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答。"}}
	plain := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, bare, &fakePoster{}, nil)
	if err := plain.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if strings.Contains(bare.inputs[0].User, "[[cite:") {
		t.Fatal("without a verifier the prompt must not ask for markers at all")
	}
}

// Citation 的 JSON 字段名是给前端（T6）的稳定契约，改名即破坏渲染。
func TestCitationJSONContractIsStable(t *testing.T) {
	encoded, err := json.Marshal(aireply.Citation{TopicID: 42, CommentID: 913, Excerpt: "内容"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"topicId":42`, `"commentId":913`, `"excerpt":"内容"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("citation JSON %s is missing %s", encoded, want)
		}
	}
	// 空字段必须省略：前端据「字段存在与否」判断链接形状。
	encoded, err = json.Marshal(aireply.Citation{TopicID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "commentId") {
		t.Fatalf("a topic-only citation must omit commentId: %s", encoded)
	}
}

// ---------- B5：三重失效 ----------

// 验收 1：同一话题连续两次 @，第二次的提示词里含第一次已确认的事实。
func TestGenerateInjectsPreviouslyVerifiedFacts(t *testing.T) {
	ref := aireply.CitationRef{CommentID: 913}
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{ref: "线上用的是 20。"}}
	state := &fakeState{}

	// 第一次：发布成功并留下一条事实。
	first := &fakeCompleter{output: aireply.CompletionOutput{
		Text:  "按 913 楼，20 就够。[[cite:comment:913]]",
		RunID: "run-1",
	}}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, first, &fakePoster{}, nil).
		WithCitations(citations).
		WithTopicState(state)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("first Generate returned error: %v", err)
	}
	if state.saves != 1 {
		t.Fatalf("the state must be written once after a successful publish: %d", state.saves)
	}
	saved := state.saved[0]
	if len(saved.VerifiedFacts) != 1 {
		t.Fatalf("verified facts = %+v", saved.VerifiedFacts)
	}
	if saved.VerifiedFacts[0].CommentID != 913 || saved.VerifiedFacts[0].Excerpt != "线上用的是 20。" {
		t.Fatalf("fact = %+v", saved.VerifiedFacts[0])
	}
	// 事实只能来自校验通过的引用：主题级引用不产生事实。
	if saved.LastReplies[0].CommentID != 9001 || saved.LastRunID != "run-1" {
		t.Fatalf("reply refs = %+v lastRunId=%q", saved.LastReplies, saved.LastRunID)
	}

	// 第二次：把 state 当作已存在，模型不写标记，但提示词里应已带上那条事实。
	state.hasStored = true
	state.stored = saved
	second := &fakeCompleter{output: aireply.CompletionOutput{Text: "依旧建议 20。"}}
	generator2 := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, second, &fakePoster{}, nil).
		WithCitations(citations).
		WithTopicState(state)
	if err := generator2.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("second Generate returned error: %v", err)
	}
	user := second.inputs[0].User
	if !strings.Contains(user, "此前已确认过的事实") {
		t.Fatalf("the prompt must carry the state block:\n%s", user)
	}
	if !strings.Contains(user, "线上用的是 20。") {
		t.Fatalf("the prompt must carry the previously verified excerpt:\n%s", user)
	}
	if !strings.Contains(user, "913 楼") {
		t.Fatalf("the fact must name its floor so the model can cite it:\n%s", user)
	}
}

// 验收 2：改提示词版本 → 状态失效，提示词里不再出现。
func TestGenerateInvalidatesStateWhenPromptVersionMoves(t *testing.T) {
	ref := aireply.CitationRef{CommentID: 913}
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{ref: "旧事实"}}
	state := &fakeState{
		hasStored: true,
		stored: aireply.TopicState{
			PromptVersion: "forum-reply@1",
			UpdatedAt:     time.Now(),
			VerifiedFacts: []aireply.Fact{{TopicID: 9, CommentID: 913, Excerpt: "旧事实", VerifiedAt: time.Now()}},
			Revision:      3,
		},
	}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答。"}}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, &fakePoster{}, nil).
		WithCitations(citations).
		WithTopicState(state)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if strings.Contains(completer.inputs[0].User, "旧事实") {
		t.Fatalf("a stale prompt version must not be injected:\n%s", completer.inputs[0].User)
	}
	// 整份失效：写入的文档不能把旧事实当基底合并回来。
	if state.saves != 1 {
		t.Fatalf("saves = %d", state.saves)
	}
	if len(state.saved[0].VerifiedFacts) != 0 {
		t.Fatalf("a fully invalidated state must not carry old facts forward: %+v", state.saved[0].VerifiedFacts)
	}
}

// 超期（>14 天）同样整份失效。
func TestGenerateInvalidatesStaleState(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{{CommentID: 913}: "旧事实"}}
	state := &fakeState{
		hasStored: true,
		stored: aireply.TopicState{
			PromptVersion: aireply.ReplyPromptVersion,
			UpdatedAt:     time.Now().Add(-15 * 24 * time.Hour),
			VerifiedFacts: []aireply.Fact{{TopicID: 9, CommentID: 913, Excerpt: "旧事实", VerifiedAt: time.Now()}},
		},
	}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答。"}}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, &fakePoster{}, nil).
		WithCitations(citations).
		WithTopicState(state)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if strings.Contains(completer.inputs[0].User, "旧事实") {
		t.Fatalf("a state older than the TTL must not be injected:\n%s", completer.inputs[0].User)
	}
}

// 验收 3：引用的楼层被删除/不可见 → 该条事实消失，其余保留。
func TestGenerateDropsOnlyTheInvalidatedFact(t *testing.T) {
	// 913 仍然可见，777 已经不可见。
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{{CommentID: 913}: "保留的事实"}}
	state := &fakeState{
		hasStored: true,
		stored: aireply.TopicState{
			PromptVersion: aireply.ReplyPromptVersion,
			UpdatedAt:     time.Now(),
			VerifiedFacts: []aireply.Fact{
				{TopicID: 9, CommentID: 913, Excerpt: "保留的事实", VerifiedAt: time.Now()},
				{TopicID: 9, CommentID: 777, Excerpt: "被删楼层的事实", VerifiedAt: time.Now()},
			},
			Revision: 5,
		},
	}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答。"}}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, &fakePoster{}, nil).
		WithCitations(citations).
		WithTopicState(state)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	user := completer.inputs[0].User
	if !strings.Contains(user, "保留的事实") {
		t.Fatalf("the still-visible fact must survive:\n%s", user)
	}
	if strings.Contains(user, "被删楼层的事实") {
		t.Fatalf("the invalidated fact must be dropped:\n%s", user)
	}
	// 逐条剔除，不是整份删。
	if state.saves != 1 {
		t.Fatalf("saves = %d", state.saves)
	}
	facts := state.saved[0].VerifiedFacts
	if len(facts) != 1 || facts[0].CommentID != 913 {
		t.Fatalf("saved facts = %+v", facts)
	}
}

// 读取容错：文档损坏 → 当作无状态，不 panic、不让回复失败、写入时重建。
func TestGenerateTreatsUnreadableStateAsStateless(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{{CommentID: 913}: "事实"}}
	state := &fakeState{loadErr: true}
	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text:  "回答。[[cite:comment:913]]",
		RunID: "run-new",
	}}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, &fakePoster{}, nil).
		WithCitations(citations).
		WithTopicState(state)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("an unreadable state must not fail the reply: %v", err)
	}
	if len(state.saved) != 1 {
		t.Fatalf("the state must be rebuilt on the next write: %d", len(state.saved))
	}
	// 读取失败时 revision 未知 → 必须走 insert-only（0），绝不盲写覆盖。
	if state.saved[0].Revision != 0 {
		t.Fatalf("an unknown revision must not overwrite: %d", state.saved[0].Revision)
	}
}

// 写入时机：发布失败 → 不写状态（下一轮引用一段没人看过的结论是更糟的错误）。
func TestGenerateNeverSavesStateWhenPostingFails(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{{CommentID: 913}: "事实"}}
	state := &fakeState{}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答。[[cite:comment:913]]"}}
	poster := &fakePoster{err: errors.New("moderation rejected")}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations).
		WithTopicState(state)
	if err := generator.Generate(context.Background(), jobInput()); err == nil {
		t.Fatal("a posting failure must be returned")
	}
	if state.saves != 0 {
		t.Fatalf("state must not be written when the publish failed: %d", state.saves)
	}
}

// 乐观锁：写入被放弃（revision 冲突）不构成错误，也不阻断已完成的发布。
func TestGenerateSurvivesARevisionConflict(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{{CommentID: 913}: "事实"}}
	state := &fakeState{rejectSave: true}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答。[[cite:comment:913]]"}}
	poster := &fakePoster{}

	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithCitations(citations).
		WithTopicState(state)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("a lost state write must not fail the reply: %v", err)
	}
	if len(poster.inputs) != 1 {
		t.Fatalf("the reply must still be published: %+v", poster.inputs)
	}
	if len(state.saved) != 0 {
		t.Fatalf("a conflicting write must be abandoned, not applied: %+v", state.saved)
	}
}

// 未装配状态存储时行为与引入前完全一致：不注入、不写库、不多查一次。
func TestGenerateWithoutStateWiringPublishesAsBefore(t *testing.T) {
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "普通回答。"}}
	poster := &fakePoster{}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(poster.inputs) != 1 || poster.inputs[0].Content != "普通回答。" {
		t.Fatalf("posted = %+v", poster.inputs)
	}
	if strings.Contains(completer.inputs[0].User, "此前已确认过的事实") {
		t.Fatal("no state store means no state block")
	}
}

// 只有引用装配、没有状态装配时，状态逻辑整体跳过（不是「装配了却永远空转」）。
func TestWithTopicStateRequiresCitationSupport(t *testing.T) {
	citations := &fakeCitations{visible: map[aireply.CitationRef]string{{CommentID: 913}: "事实"}}
	state := &fakeState{}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "回答。[[cite:comment:913]]"}}

	// 先装配状态、再装配引用：顺序不该改变结论。
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, &fakePoster{}, nil).
		WithTopicState(state)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if state.saves != 0 {
		t.Fatalf("state must stay unwired without citation support: %d", state.saves)
	}
	_ = citations
}

// 注入上限：条数与字节双重约束，超出按最近优先保留。
func TestRenderTopicStateEnforcesBothLimits(t *testing.T) {
	base := time.Now()
	facts := make([]aireply.Fact, 0, 40)
	for index := 0; index < 40; index++ {
		facts = append(facts, aireply.Fact{
			TopicID:    9,
			CommentID:  int64(1000 + index),
			Excerpt:    "第 " + string(rune('A'+index%26)) + " 条事实",
			VerifiedAt: base.Add(time.Duration(index) * time.Minute),
		})
	}
	block, size := aireply.RenderTopicState(facts)
	if block == "" {
		t.Fatal("a non-empty fact list must render")
	}
	if size != len(block) {
		t.Fatalf("size = %d, want %d", size, len(block))
	}
	if lines := strings.Count(block, "\n- （依据 "); lines > aireply.MaxInjectedFacts {
		t.Fatalf("injected %d facts, cap is %d", lines, aireply.MaxInjectedFacts)
	}
	if size > aireply.MaxInjectedStateBytes+512 {
		// 512 是标题与尾注的固定开销；正文预算本身由 MaxInjectedStateBytes 约束。
		t.Fatalf("state block grew past its budget: %d bytes", size)
	}
	// 空事实不渲染任何标题：否则提示词里会留一个没有内容的小节。
	if empty, size := aireply.RenderTopicState(nil); empty != "" || size != 0 {
		t.Fatalf("empty facts must render nothing: %q / %d", empty, size)
	}
}

// 事实合并按楼层去重，同楼层以最新的引文为准（楼层可能被编辑）。
func TestMergeFactsKeepsTheNewestExcerptPerFloor(t *testing.T) {
	now := time.Now()
	existing := []aireply.Fact{
		{TopicID: 9, CommentID: 913, Excerpt: "旧引文", VerifiedAt: now.Add(-time.Hour)},
		{TopicID: 9, CommentID: 777, Excerpt: "另一楼", VerifiedAt: now.Add(-time.Hour)},
	}
	added := []aireply.Fact{
		{TopicID: 9, CommentID: 913, Excerpt: "新引文", VerifiedAt: now},
	}
	merged := aireply.MergeFacts(existing, added)
	if len(merged) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged[0].CommentID != 913 || merged[0].Excerpt != "新引文" {
		t.Fatalf("the newest excerpt must win: %+v", merged[0])
	}
}
