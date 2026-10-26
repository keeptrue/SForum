package search

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	searchjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Search"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// EngineGate 可选：在入队前用轻量检查（如 DB 选择）判断是否应调度索引 job。
// 未实现时回退到类型断言 UnavailableEngine。
type EngineGate interface {
	Enabled(ctx context.Context) bool
}

// Indexer 协调 forum 写流程、River worker 与搜索引擎三者：
//   - EnqueueIndex/EnqueueDelete：forum.Service 在主题写流程调用，经 dispatcher
//     异步入队 IndexTopicArgs/DeleteTopicArgs（事务外，失败只记日志）。
//   - IndexTopic/DeleteTopic：River worker 实际执行，读写 Engine。
//
// 无可用引擎时入队 no-op，避免堆积无用 job；已经入队的 worker 任务必须返回引擎错误，
// 交给 River 重试，不能把一次临时不可用误记为 completed。
type Indexer struct {
	engine     Engine
	reader     TopicReader
	dispatcher *supportjobs.Dispatcher
	state      IndexStateStore
}

func NewIndexer(engine Engine, reader TopicReader, dispatcher *supportjobs.Dispatcher) *Indexer {
	if engine == nil {
		engine = UnavailableEngine{}
	}
	return &Indexer{engine: engine, reader: reader, dispatcher: dispatcher}
}

// WithIndexStateStore 在引擎操作成功后提交 Host 同步账本。
// 账本失败会让 River 重试整个幂等操作，不能把未确认状态误记为完成。
func (i *Indexer) WithIndexStateStore(state IndexStateStore) *Indexer {
	if i != nil {
		i.state = state
	}
	return i
}

// EnsureIndex 委托引擎幂等建索引。
func (i *Indexer) EnsureIndex(ctx context.Context) error {
	if i == nil || i.engine == nil {
		return ErrEngineUnavailable
	}
	return i.engine.EnsureIndex(ctx)
}

// --- forum.TopicSearchIndexer 实现 ---

// EnqueueIndex 调度重新索引指定主题（异步）。无引擎时直接返回 nil。
func (i *Indexer) EnqueueIndex(ctx context.Context, topicID int64) error {
	if i == nil || i.dispatcher == nil {
		return nil
	}
	if !i.engineEnabled(ctx) {
		return nil
	}
	args := searchjobs.IndexTopicArgs{TopicID: topicID}
	_, err := i.dispatcher.Enqueue(ctx, args, args.QueueOpts())
	return err
}

// EnqueueDelete 调度从索引移除指定主题（异步）。无引擎时直接返回 nil。
func (i *Indexer) EnqueueDelete(ctx context.Context, topicID int64) error {
	if i == nil || i.dispatcher == nil {
		return nil
	}
	if !i.engineEnabled(ctx) {
		return nil
	}
	args := searchjobs.DeleteTopicArgs{TopicID: topicID}
	_, err := i.dispatcher.Enqueue(ctx, args, args.QueueOpts())
	return err
}

func (i *Indexer) engineEnabled(ctx context.Context) bool {
	if i == nil || i.engine == nil {
		return false
	}
	if gate, ok := i.engine.(EngineGate); ok {
		return gate.Enabled(ctx)
	}
	if _, unavailable := i.engine.(UnavailableEngine); unavailable {
		return false
	}
	return true
}

// --- searchjobs.TopicIndexer 实现（worker 执行） ---

// IndexTopic 从 forum 读取主题详情并 upsert 到引擎。
func (i *Indexer) IndexTopic(ctx context.Context, topicID int64) error {
	if i == nil || i.engine == nil {
		return ErrEngineUnavailable
	}
	if i.reader == nil {
		return fmt.Errorf("search indexer not fully configured")
	}
	doc, err := i.reader.GetTopicForSearch(ctx, topicID)
	if err != nil {
		// 主题不存在（已物理删除等）：视为删除索引项，避免残留。
		if strings.Contains(err.Error(), "not found") {
			return i.DeleteTopic(ctx, topicID)
		}
		return err
	}
	providerID, err := i.selectedProviderID(ctx)
	if err != nil {
		return err
	}
	if err := i.engine.Index(ctx, doc); err != nil {
		return fmt.Errorf("upsert topic %d to search engine: %w", topicID, err)
	}
	if err := i.confirmProviderAndMarkIndexed(ctx, providerID, doc); err != nil {
		return err
	}
	slog.InfoContext(ctx, "search: indexed topic", "topicId", topicID)
	return nil
}

// DeleteTopic 从引擎删除主题文档。
func (i *Indexer) DeleteTopic(ctx context.Context, topicID int64) error {
	if i == nil || i.engine == nil {
		return ErrEngineUnavailable
	}
	providerID, err := i.selectedProviderID(ctx)
	if err != nil {
		return err
	}
	if err := i.engine.Delete(ctx, topicID); err != nil {
		return fmt.Errorf("delete topic %d from search engine: %w", topicID, err)
	}
	if err := i.confirmProviderAndMarkDeleted(ctx, providerID, topicID); err != nil {
		return err
	}
	slog.InfoContext(ctx, "search: deleted topic index", "topicId", topicID)
	return nil
}

func (i *Indexer) selectedProviderID(ctx context.Context) (string, error) {
	if resolver, ok := i.engine.(ProviderIDResolver); ok {
		id, selected, err := resolver.SelectedID(ctx)
		if err != nil {
			return "", fmt.Errorf("resolve selected search provider: %w", err)
		}
		if !selected || strings.TrimSpace(id) == "" {
			return "", ErrEngineUnavailable
		}
		return strings.TrimSpace(id), nil
	}
	return DefaultSiteSearchExtensionID, nil
}

func (i *Indexer) confirmProviderAndMarkIndexed(ctx context.Context, providerID string, doc TopicSearchDoc) error {
	if i.state == nil {
		return nil
	}
	current, err := i.selectedProviderID(ctx)
	if err != nil {
		return err
	}
	if current != providerID {
		// provider 在传输过程中切换，重试后会把权威快照写到新的当前 provider。
		return fmt.Errorf("search provider changed during topic %d indexing: %s -> %s", doc.ID, providerID, current)
	}
	if err := i.state.MarkIndexed(ctx, providerID, doc.ID, doc.UpdatedAt); err != nil {
		return err
	}
	return nil
}

func (i *Indexer) confirmProviderAndMarkDeleted(ctx context.Context, providerID string, topicID int64) error {
	if i.state == nil {
		return nil
	}
	current, err := i.selectedProviderID(ctx)
	if err != nil {
		return err
	}
	if current != providerID {
		return fmt.Errorf("search provider changed during topic %d deletion: %s -> %s", topicID, providerID, current)
	}
	if err := i.state.MarkDeleted(ctx, providerID, topicID); err != nil {
		return err
	}
	return nil
}
