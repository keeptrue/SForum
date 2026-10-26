package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

// evalFixture 是一条 eval 用例：一段固定的社群上下文 + 谁在什么时候叫了机器人。
//
// 它替代真实数据库读路径，因此 eval 的结果不依赖开发库当前的内容——基线要能被
// 提交进仓库并跨机器比较，靠「本机恰好有这些帖子」是不成立的。
type evalFixture struct {
	ID string `json:"id"`
	// Note 是这条 fixture 覆盖什么的说明（可检索答案 / 该拒答 / 长楼 / 带图 / 跨语言）。
	// 它只给人看，不进提示词：eval 要测的是生成行为，不是覆盖说明的可读性。
	Note        string        `json:"note,omitempty"`
	TopicTitle  string        `json:"topicTitle"`
	TopicBody   string        `json:"topicBody"`
	TopicAuthor evalAuthor    `json:"topicAuthor,omitempty"`
	Comments    []evalComment `json:"comments"`
	// TriggerCommentIndex 是唤起本次回复的评论下标（-1 表示由主楼唤起）。
	TriggerCommentIndex int `json:"triggerCommentIndex"`
	// ParentCommentIndex 是回复要挂靠的评论下标；省略时等于 TriggerCommentIndex，
	// 与生产里「回复机器人」那条路径一致。
	ParentCommentIndex *int  `json:"parentCommentIndex,omitempty"`
	TriggerUserID      int64 `json:"triggerUserID"`
	BotUserID          int64 `json:"botUserID"`
	// Corpus 是工具可以看到的站内内容。它与下面的讨论上下文分开：上下文是「这栋楼」，
	// 语料是「站内其它地方」。工具读不到语料之外的东西，因此 eval 不会因为本机开发库
	// 恰好有一些帖子而给出另一份答案。
	Corpus evalCorpus `json:"corpus,omitempty"`
}

type evalAuthor struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

type evalComment struct {
	Author  evalAuthor `json:"author"`
	Content string     `json:"content"`
	IsBot   bool       `json:"isBot"`
	// Images 声明这条发言带的图片附件。图片字节由 eval 自己合成（见 evalImageReader）：
	// 基线要可复现，不能依赖本机附件存储里恰好有一张图。
	// 真正的图片发送仍受站点多模态开关控制，与生产同一条路径。
	Images []evalImage `json:"images,omitempty"`
}

// evalImage 是一条图片附件的声明。Description 只给人看（说明这张图在表达什么），
// 不进提示词——模型看到的是字节，不是这段说明。
type evalImage struct {
	PublicID    string `json:"publicId"`
	ContentType string `json:"contentType"`
	Description string `json:"description,omitempty"`
}

// evalCorpus 是 fixture 自带的站内语料：工具检索、读帖、列榜、查人都在这里发生。
type evalCorpus struct {
	Topics []evalCorpusTopic `json:"topics,omitempty"`
	Users  []evalCorpusUser  `json:"users,omitempty"`
}

type evalCorpusTopic struct {
	ID           int64      `json:"id"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	CategorySlug string     `json:"categorySlug"`
	Status       string     `json:"status"`
	CommentCount int64      `json:"commentCount"`
	ViewCount    int64      `json:"viewCount"`
	Author       evalAuthor `json:"author"`
}

type evalCorpusUser struct {
	Username     string `json:"username"`
	DisplayName  string `json:"displayName"`
	Bio          string `json:"bio"`
	TopicCount   int64  `json:"topicCount"`
	CommentCount int64  `json:"commentCount"`
}

// loadEvalFixtures 读取目录下全部 *.json 并按 id 排序。
//
// 排序是基线可比性的一部分：目录遍历顺序不保证稳定，而 diff 的输出顺序必须稳定。
func loadEvalFixtures(dir string) ([]evalFixture, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return nil, fmt.Errorf("--fixture-dir is required")
	}
	entries, err := os.ReadDir(trimmed)
	if err != nil {
		return nil, fmt.Errorf("read fixture dir %s: %w", trimmed, err)
	}
	fixtures := make([]evalFixture, 0, len(entries))
	seen := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(trimmed, entry.Name())
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read fixture %s: %w", path, err)
		}
		var fixture evalFixture
		if err := json.Unmarshal(payload, &fixture); err != nil {
			return nil, fmt.Errorf("parse fixture %s: %w", path, err)
		}
		fixture.ID = strings.TrimSpace(fixture.ID)
		if fixture.ID == "" {
			return nil, fmt.Errorf("fixture %s has no id", path)
		}
		if previous, exists := seen[fixture.ID]; exists {
			return nil, fmt.Errorf("duplicate fixture id %q in %s and %s", fixture.ID, previous, path)
		}
		seen[fixture.ID] = path
		if err := fixture.validate(); err != nil {
			return nil, fmt.Errorf("fixture %s: %w", path, err)
		}
		fixtures = append(fixtures, fixture)
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("no *.json fixtures in %s", trimmed)
	}
	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].ID < fixtures[j].ID })
	return fixtures, nil
}

// validate 拒绝「跑得通但没有意义」的 fixture：没有讨论内容就没有提示词，
// 下标越界会让 fixture 静默退化成主楼唤起。
func (f evalFixture) validate() error {
	if strings.TrimSpace(f.TopicTitle) == "" {
		return fmt.Errorf("topicTitle is empty")
	}
	if f.TriggerCommentIndex < -1 || f.TriggerCommentIndex >= len(f.Comments) {
		return fmt.Errorf("triggerCommentIndex %d out of range (%d comments)", f.TriggerCommentIndex, len(f.Comments))
	}
	if f.TriggerCommentIndex == -1 && strings.TrimSpace(f.TopicBody) == "" {
		return fmt.Errorf("topic-level trigger needs a topicBody")
	}
	if f.ParentCommentIndex != nil && (*f.ParentCommentIndex < 0 || *f.ParentCommentIndex >= len(f.Comments)) {
		return fmt.Errorf("parentCommentIndex %d out of range (%d comments)", *f.ParentCommentIndex, len(f.Comments))
	}
	return nil
}

// replyContext 把 fixture 还原成生成器要的那份线程快照。
//
// 评论 id 用「下标 + 1」的 fixture 局部编号：eval 不需要真实 id，只需要能在同一条
// fixture 内稳定地把 parent / trigger 指回具体某条评论。
//
// 近期发言只带最后 aireply.RecentCommentWindow 条，与生产
// PostgresReader.recentComments 的 LIMIT 同口径。这条不能省：长楼的覆盖目标正是
// 「前面的发言已经掉出窗口，模型必须补读」，若 fixture 把所有评论都塞进提示词，
// 长楼用例就退化成一次普通问答，`forum-topic-read` 永远不会被逼出来。
// 被回复/触发的那条评论不受窗口限制（生产里它是单独查出来的，见 ParentContent）。
func (f evalFixture) replyContext() aireply.ReplyContext {
	thread := aireply.ReplyContext{
		TopicTitle:  f.TopicTitle,
		TopicBody:   f.TopicBody,
		TopicAuthor: authorRef(f.TopicAuthor),
	}
	recent := f.Comments
	if len(recent) > aireply.RecentCommentWindow {
		recent = recent[len(recent)-aireply.RecentCommentWindow:]
	}
	for _, comment := range recent {
		thread.RecentComments = append(thread.RecentComments, aireply.ThreadComment{
			Author:  authorRef(comment.Author),
			Content: comment.Content,
			IsBot:   comment.IsBot,
		})
	}
	parentIndex := f.TriggerCommentIndex
	if f.ParentCommentIndex != nil {
		parentIndex = *f.ParentCommentIndex
	}
	if parentIndex >= 0 && parentIndex < len(f.Comments) {
		parent := f.Comments[parentIndex]
		thread.ParentAuthor = authorRef(parent.Author)
		thread.ParentContent = parent.Content
		thread.ParentIsBot = parent.IsBot
	}
	if f.TriggerCommentIndex >= 0 && f.TriggerCommentIndex < len(f.Comments) && f.TriggerCommentIndex != parentIndex {
		trigger := f.Comments[f.TriggerCommentIndex]
		thread.TriggerAuthor = authorRef(trigger.Author)
		thread.TriggerContent = trigger.Content
	}
	return thread
}

func authorRef(author evalAuthor) aireply.AuthorRef {
	return aireply.AuthorRef{Username: author.Username, DisplayName: author.DisplayName}
}

// fixtureThreadReader 用 fixture 代替 PostgresReader。
//
// 它实现 aireply.ImageAttachmentReader 但不是为了读附件存储：候选图片来自 fixture
// 声明，字节由 evalImageReader 现场合成（一张确定性 PNG）。这样「带图」这条路径
// ——开关检查、Part 组装、协议翻译、图片计数尾注——全部是生产代码，而内容可复现。
type fixtureThreadReader struct {
	threads map[int64]aireply.ReplyContext
	images  map[string][]aireply.ImageAttachment
}

func newFixtureThreadReader(fixtures []evalFixture) *fixtureThreadReader {
	threads := make(map[int64]aireply.ReplyContext, len(fixtures))
	images := map[string][]aireply.ImageAttachment{}
	for index, fixture := range fixtures {
		topicID := int64(index + 1)
		threads[topicID] = fixture.replyContext()
		// 图片候选来自「主楼 + 被回复评论」：与生产 PostgresReader 的收集范围一致
		// （近期讨论窗口里的图不收集）。
		candidates := make([]aireply.ImageAttachment, 0, 2)
		parentIndex := fixture.TriggerCommentIndex
		if fixture.ParentCommentIndex != nil {
			parentIndex = *fixture.ParentCommentIndex
		}
		if parentIndex >= 0 && parentIndex < len(fixture.Comments) {
			for _, image := range fixture.Comments[parentIndex].Images {
				candidates = append(candidates, aireply.ImageAttachment{
					PublicID: image.PublicID, ContentType: image.ContentType,
				})
			}
		}
		images[fmt.Sprint(topicID)] = candidates
	}
	return &fixtureThreadReader{threads: threads, images: images}
}

func (r *fixtureThreadReader) ReplyContext(_ context.Context, topicID int64, _, _ int64) (aireply.ReplyContext, error) {
	thread, ok := r.threads[topicID]
	if !ok {
		return aireply.ReplyContext{}, fmt.Errorf("fixture thread %d not found", topicID)
	}
	return thread, nil
}

// ReplyImageAttachments 返回该主题下声明的图片候选（主楼与被回复评论）。
func (r *fixtureThreadReader) ReplyImageAttachments(_ context.Context, topicID, _ int64) ([]aireply.ImageAttachment, error) {
	return r.images[fmt.Sprint(topicID)], nil
}

// evalImageReader 用确定性字节回应图片读取请求。
//
// 它证明的是「图片会走到协议层」，不是「附件存储能读」——后者由附件的既有测试
// 覆盖。因此字节是一张 8×8 的合法 PNG，与 fixture 里声明的 publicId 无关。
type evalImageReader struct{}

func (evalImageReader) ReadImage(_ context.Context, _ int64, _ string, maxBytes int64) (io.ReadCloser, error) {
	if int64(len(evalPNG)) > maxBytes {
		return nil, fmt.Errorf("eval image exceeds %d bytes", maxBytes)
	}
	return io.NopCloser(bytes.NewReader(evalPNG)), nil
}

// evalPNG 是 8×8 纯灰 PNG。手写死字节而不是现场编码：现场编码依赖 image/png 的
// 压缩实现细节，不同 Go 版本可能产出不同字节，而这里需要的是「每次完全一样」。
var evalPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x08,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x4b, 0x6d, 0x29,
	0xdc, 0x00, 0x00, 0x00, 0x1c, 0x49, 0x44, 0x41,
	0x54, 0x28, 0x91, 0x63, 0x64, 0x60, 0x60, 0x60,
	0x60, 0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0x0f,
	0x00, 0x08, 0x08, 0x08, 0x08, 0x0c, 0x24, 0x2c,
	0x18, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

// capturePoster 是「只读的发布者」：生成器照常走完发布那一步，但评论只落到内存。
//
// eval 不发布（任务书易错点 1），而生成器唯一会写库的动作就是 PostBotComment。
// 注入这个实现比给 Generator 开一个「不发布」入口更干净：生产类型不需要知道
// eval 的存在，也就不存在一条只在 eval 里生效的分支。
type capturePoster struct {
	content string
	called  bool
}

func (p *capturePoster) PostBotComment(_ context.Context, _ int64, input aireply.BotCommentInput) (int64, error) {
	p.content = input.Content
	p.called = true
	return 0, nil
}

// recordingCompleter 记录生成器最后一次得到的补全结果。
//
// 用量与工具轨迹只在这里能拿到：Generate 把它们写进日志后就不再返回，而快照需要
// 结构化地保存它们。包一层比让 Generate 改签名代价小得多。
type recordingCompleter struct {
	inner  aireply.Completer
	output aireply.CompletionOutput
	err    error
	calls  int
	// request 是最后一次真正发给 completer 的输入。它让快照能带一个确定性的
	// prompt 指纹：文本会因采样而抖动，而「这次请求到底发了什么」不会。
	request aireply.CompletionInput
}

func (c *recordingCompleter) Complete(ctx context.Context, input aireply.CompletionInput) (aireply.CompletionOutput, error) {
	c.calls++
	c.request = input
	output, err := c.inner.Complete(ctx, input)
	c.output = output
	c.err = err
	return output, err
}

// promptDigest 对「本次请求的全部决定性输入」取摘要。
//
// 它是 eval 的判据核心：模型回答本质上是概率性的，同一份输入两次运行可能给出
// 不同文本（provider 采样，且网关不发送 temperature，见知识库）。因此「文本变了」
// 不能直接等于「我改的东西生效了」。把 system、user、工具白名单、提示词版本、
// 成本等级与输出预算都摘要起来，就有了一个不受采样影响的比较基准：
// 指纹相同而文本不同 = 噪声；指纹不同 = 真的改了东西。
func (c *recordingCompleter) promptDigest() string {
	hash := sha256.New()
	write := func(parts ...string) {
		for _, part := range parts {
			hash.Write([]byte(part))
			hash.Write([]byte{0})
		}
	}
	write("ai.eval@1", c.request.Purpose, c.request.CostClass, c.request.PromptVersion,
		strconv.Itoa(c.request.MaxTokens), c.request.System, c.request.User)
	for _, tool := range c.request.Tools {
		write("tool", tool)
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// evalUsage 是快照里的用量字段。它与 supportai.Usage 同形但在 JSON 里只保留
// 两个必需项：缓存 token 对「回答变没变」没有解释力，进基线只会增加噪声。
type evalUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// evalSnapshot 是一条 fixture 的观测结果。
//
// 前六个字段与任务书一致；error 是任务书未列的新增字段——没有它，一次网关拒绝
// （例如密钥失效）会产出与「模型什么都没说」完全相同的快照，diff 会把它报成
// unchanged，退化就被藏起来了。
type evalSnapshot struct {
	ID         string                     `json:"id"`
	Text       string                     `json:"text"`
	StopReason string                     `json:"stopReason"`
	ToolTrace  []supportai.ToolTraceEntry `json:"toolTrace"`
	ModelCalls int                        `json:"modelCalls"`
	Usage      evalUsage                  `json:"usage"`
	Error      string                     `json:"error,omitempty"`
	// PromptDigest 是本次请求全部决定性输入的摘要（system/user/工具/版本/预算）。
	//
	// 它是「改动是否真的生效」的判据：文本会因 provider 采样而抖动，digest 不会。
	// 只比较文本时，一次无关的噪声会被读成退化，而一次真正的提示词改动可能因为
	// 模型恰好给出相近长度的回答而被漏掉。
	PromptDigest string `json:"promptDigest"`
}

// newEvalSnapshot 把一次生成的结果收敛成快照。
//
// Text 取「本会发布出去的那段正文」：生成器会先 TrimSpace 再发布，空回复则根本不
// 发布。两处都以生成器为准，快照才和线上实际产出同一口径。
func newEvalSnapshot(id string, output aireply.CompletionOutput, published string, called bool, digest string, err error) evalSnapshot {
	text := strings.TrimSpace(published)
	if !called {
		text = strings.TrimSpace(output.Text)
	}
	trace := output.ToolTrace
	if trace == nil {
		// nil 与 [] 在 JSON 里是两种形状（null / []）。基线统一成 []，否则
		// 「本来没有工具调用」会被 diff 读成一次结构变化。
		trace = []supportai.ToolTraceEntry{}
	}
	snapshot := evalSnapshot{
		ID:         id,
		Text:       text,
		StopReason: output.StopReason,
		ToolTrace:  trace,
		ModelCalls: output.ModelCalls,
		Usage: evalUsage{
			InputTokens:  output.Usage.InputTokens,
			OutputTokens: output.Usage.OutputTokens,
		},
		PromptDigest: digest,
	}
	if err != nil {
		snapshot.Error = err.Error()
	}
	return snapshot
}

// evalCorpusReader 把 fixture 的语料适配成六个内置工具依赖的窄接口。
//
// 关键点：工具本身仍然是 aitools 里的真实实现（真实的参数解析、真实的截断、真实的
// 「不可见就说没有」纪律），只有「站内有什么内容」换成了常量。于是 eval 与开发库
// 内容解耦，而工具行为仍然是生产行为。
type evalCorpusReader struct {
	topics []evalCorpusTopic
	users  []evalCorpusUser
	// self 是 fixture 自己的讨论上下文所对应的那个主题：让它也能被 forum-topic-read
	// 读到，否则模型读「当前这栋楼」时会得到一个不存在的结果。
	self evalCorpusTopic
	// selfComments 是当前这栋楼的发言，供 forum-topic-read 复核上下文。
	selfComments []evalComment
}

func newEvalCorpusReader(fixture evalFixture) *evalCorpusReader {
	self := evalCorpusTopic{
		ID:           1,
		Title:        fixture.TopicTitle,
		Body:         fixture.TopicBody,
		CategorySlug: "general",
		Status:       "active",
		CommentCount: int64(len(fixture.Comments)),
		Author:       fixture.TopicAuthor,
	}
	topics := make([]evalCorpusTopic, 0, len(fixture.Corpus.Topics)+1)
	topics = append(topics, self)
	for _, topic := range fixture.Corpus.Topics {
		if topic.ID == self.ID {
			continue
		}
		if strings.TrimSpace(topic.CategorySlug) == "" {
			topic.CategorySlug = "general"
		}
		if strings.TrimSpace(topic.Status) == "" {
			topic.Status = "active"
		}
		topics = append(topics, topic)
	}
	return &evalCorpusReader{topics: topics, users: fixture.Corpus.Users, self: self, selfComments: fixture.Comments}
}

// Search 近似生产搜索引擎的召回语义。
//
// 生产走 PostgreSQL（PostgresSiteEngine）：websearch_to_tsquery 把查询拆成词项并
// **要求全部命中**，另有一条 CJK n-gram 路径与一条 ILIKE 包含路径。这里复刻的是
// 这个结构，而不是「整串子串匹配」：模型换一种说法（搜「Go channel mutex 选择
// 并发」而语料标题是「channel 还是 mutex」）时，整串匹配会返回「没有找到」，
// 而真实部署会命中——那样 eval 就测不到「可检索答案」这条覆盖。
func (r *evalCorpusReader) Search(_ context.Context, input search.SearchInput) (search.SearchResult, error) {
	terms := evalSearchTerms(input.Query)
	items := make([]search.TopicSearchDoc, 0, len(r.topics))
	for _, topic := range r.topics {
		haystack := strings.ToLower(topic.Title + " " + topic.Body + " " +
			topic.Author.Username + " " + topic.Author.DisplayName + " " + topic.CategorySlug)
		if !matchesAllTerms(haystack, terms) {
			continue
		}
		items = append(items, r.doc(topic))
	}
	return search.SearchResult{Items: items, Total: int64(len(items)), Page: 1, PerPage: len(items)}, nil
}

// matchesAllTerms 要求每个词项都出现在正文里（生产侧是 tsquery 的 AND 语义）。
func matchesAllTerms(haystack string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

// evalSearchTerms 把查询拆成词项，切法与站内引擎的 cjkNgrams 一致：
// 连续 Han 段生成相邻二元词（单字段落退回单字），非 Han 段按非字母数字切分并
// 小写。复刻切法而不是自创规则，是为了让「模型怎么写查询」这件事在 eval 里的
// 后果与真实部署一致。
func evalSearchTerms(raw string) []string {
	terms := make([]string, 0, 8)
	seen := map[string]struct{}{}
	add := func(term string) {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	hanRun := make([]rune, 0, 16)
	asciiRun := make([]rune, 0, 16)
	flushHan := func() {
		if len(hanRun) == 1 {
			add(string(hanRun))
		}
		for i := 0; i+1 < len(hanRun); i++ {
			add(string(hanRun[i : i+2]))
		}
		hanRun = hanRun[:0]
	}
	flushASCII := func() {
		if len(asciiRun) > 0 {
			add(string(asciiRun))
		}
		asciiRun = asciiRun[:0]
	}
	for _, char := range raw {
		switch {
		case unicode.In(char, unicode.Han):
			flushASCII()
			hanRun = append(hanRun, char)
		case unicode.IsLetter(char) || unicode.IsDigit(char):
			flushHan()
			asciiRun = append(asciiRun, char)
		default:
			flushHan()
			flushASCII()
		}
	}
	flushHan()
	flushASCII()
	return terms
}

func (r *evalCorpusReader) doc(topic evalCorpusTopic) search.TopicSearchDoc {
	return search.TopicSearchDoc{
		ID:             topic.ID,
		Title:          topic.Title,
		Excerpt:        firstRunes(topic.Body, 120),
		PlainText:      topic.Body,
		CategorySlug:   topic.CategorySlug,
		CategoryName:   topic.CategorySlug,
		AuthorUsername: topic.Author.Username,
		Slug:           topic.CategorySlug + "-" + fmt.Sprint(topic.ID),
		Status:         topic.Status,
		CommentCount:   topic.CommentCount,
		ViewCount:      topic.ViewCount,
	}
}

func (r *evalCorpusReader) GetTopic(_ context.Context, topicID int64) (forum.TopicDetail, error) {
	topic, ok := r.findTopic(topicID)
	if !ok {
		return forum.TopicDetail{}, forum.ErrTopicNotFound
	}
	return forum.TopicDetail{
		TopicSummary: forum.TopicSummary{
			ID:           topic.ID,
			CategorySlug: topic.CategorySlug,
			CategoryName: topic.CategorySlug,
			Title:        topic.Title,
			Status:       topic.Status,
			CommentCount: topic.CommentCount,
			ViewCount:    topic.ViewCount,
			Author:       r.summary(topic.Author),
		},
		Content: forum.PublicRenderedContent{PlainText: topic.Body, Excerpt: firstRunes(topic.Body, 120)},
	}, nil
}

func (r *evalCorpusReader) ListComments(_ context.Context, input forum.CommentListInput) (forum.CommentList, error) {
	// 只有「当前这栋楼」有评论：语料里的其它主题是背景知识，不带楼中楼。
	items := []forum.Comment{}
	if input.TopicID == r.self.ID {
		for index, comment := range r.selfComments {
			items = append(items, forum.Comment{
				ID:      int64(index + 1),
				TopicID: input.TopicID,
				Status:  "active",
				Author:  r.summary(comment.Author),
				Content: forum.PublicRenderedContent{PlainText: comment.Content},
			})
		}
	}
	return forum.CommentList{Items: items, Total: int64(len(items)), Page: 1, PerPage: len(items), View: "flat"}, nil
}

func (r *evalCorpusReader) ListTopics(_ context.Context, input forum.TopicListInput) (forum.TopicList, error) {
	items := make([]forum.TopicSummary, 0, len(r.topics))
	for _, topic := range r.topics {
		if input.CategorySlug != "" && input.CategorySlug != topic.CategorySlug {
			continue
		}
		items = append(items, forum.TopicSummary{
			ID:           topic.ID,
			CategorySlug: topic.CategorySlug,
			CategoryName: topic.CategorySlug,
			Title:        topic.Title,
			Status:       topic.Status,
			CommentCount: topic.CommentCount,
			ViewCount:    topic.ViewCount,
			Author:       r.summary(topic.Author),
			Excerpt:      firstRunes(topic.Body, 120),
		})
	}
	return forum.TopicList{Items: items, Total: int64(len(items)), Page: 1, PerPage: len(items)}, nil
}

func (r *evalCorpusReader) GetPublicProfile(_ context.Context, username string) (profile.PublicProfile, error) {
	name := strings.TrimPrefix(strings.TrimSpace(username), "@")
	for _, user := range r.users {
		if !strings.EqualFold(user.Username, name) {
			continue
		}
		return profile.PublicProfile{
			Username:     user.Username,
			DisplayName:  user.DisplayName,
			Profile:      profile.Profile{Bio: user.Bio},
			TopicCount:   user.TopicCount,
			CommentCount: user.CommentCount,
			JoinedAt:     evalEpoch(),
		}, nil
	}
	return profile.PublicProfile{}, profile.ErrProfileNotFound
}

func (r *evalCorpusReader) SearchPublicUsers(_ context.Context, input profile.SearchUsersInput) (profile.UserSearchResult, error) {
	query := strings.ToLower(strings.TrimSpace(input.Query))
	items := make([]profile.UserSearchHit, 0, len(r.users))
	for _, user := range r.users {
		if query != "" && !strings.Contains(strings.ToLower(user.Username+" "+user.DisplayName), query) {
			continue
		}
		items = append(items, profile.UserSearchHit{
			Username: user.Username, DisplayName: user.DisplayName,
		})
	}
	return profile.UserSearchResult{Items: items}, nil
}

func (r *evalCorpusReader) findTopic(topicID int64) (evalCorpusTopic, bool) {
	for _, topic := range r.topics {
		if topic.ID == topicID {
			return topic, true
		}
	}
	return evalCorpusTopic{}, false
}

func (r *evalCorpusReader) summary(author evalAuthor) *forum.UserSummary {
	if strings.TrimSpace(author.Username) == "" && strings.TrimSpace(author.DisplayName) == "" {
		return nil
	}
	return &forum.UserSummary{Username: author.Username, DisplayName: author.DisplayName}
}

// evalEpoch 是语料里「加入时间」的固定值。用 time.Now 会让每次运行的提示词不同，
// 基线就失去可比性。
func evalEpoch() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// evalFixedClock 是 time-now 工具的固定时钟。它的产出若随运行时间变化，
// 基线永远无法稳定——而这条工具的用途（相对时间问题）用固定值同样能测。
func evalFixedClock() time.Time {
	return time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
}

// firstRunes 按字符截断，避免在 UTF-8 中间切开。
func firstRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// selectEvalFixtures 按 --only 过滤 fixture，并保持目录原有的 id 顺序。
//
// 指定了不存在的 id 时直接报错而不是静默忽略：手打错一个 id 却「跑成功了 0 条」，
// 比报错更难发现——那会让人以为基线已经补齐。
func selectEvalFixtures(fixtures []evalFixture, only string) ([]evalFixture, error) {
	raw := strings.TrimSpace(only)
	if raw == "" {
		return fixtures, nil
	}
	wanted := make(map[string]struct{}, 4)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		wanted[id] = struct{}{}
	}
	if len(wanted) == 0 {
		return fixtures, nil
	}
	selected := make([]evalFixture, 0, len(wanted))
	for _, fixture := range fixtures {
		if _, ok := wanted[fixture.ID]; ok {
			selected = append(selected, fixture)
			delete(wanted, fixture.ID)
		}
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for id := range wanted {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("--only 指定了不存在的 fixture id：%s", strings.Join(missing, ", "))
	}
	return selected, nil
}
