package bootstrap

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// trustPolicyWriteHook 是 options.Store 的装饰器：信任策略写成功后，批量重算一次。
//
// 为什么挂在 store 而不是 HTTP 控制器：`trust.levels` 的写入口不止一处
// （后台设置、恢复默认、将来的 CLI/插件命令），挂在唯一写面才不会漏。也因此
// **不允许**在这里改 Options.Service 的语义——它只观察「写成功了」。
type trustPolicyWriteHook struct {
	store  options.Store
	logger *slog.Logger

	mu      sync.RWMutex
	enqueue func(ctx context.Context) error
}

func newTrustPolicyWriteHook(store options.Store, logger *slog.Logger) *trustPolicyWriteHook {
	return &trustPolicyWriteHook{store: store, logger: logger}
}

// Bind 注入「批量重算」入队器（domains 阶段 dispatcher 就绪后调用）。
//
// 未绑定时只写不排队：infra 阶段造 service 时 dispatcher 还不存在，这里先空着。
func (h *trustPolicyWriteHook) Bind(enqueue func(ctx context.Context) error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.enqueue = enqueue
	h.mu.Unlock()
}

func (h *trustPolicyWriteHook) List(ctx context.Context) ([]options.Option, error) {
	return h.store.List(ctx)
}

func (h *trustPolicyWriteHook) InsertMissing(ctx context.Context, input options.UpdateInput) error {
	return h.store.InsertMissing(ctx, input)
}

func (h *trustPolicyWriteHook) Upsert(ctx context.Context, input options.UpdateInput) (options.Option, error) {
	option, err := h.store.Upsert(ctx, input)
	if err != nil {
		return option, err
	}
	h.observe(ctx, []string{input.Name})
	return option, nil
}

// UpsertMany 必须实现：Options.Service 见到 BatchUpsertStore 才走批量路径。
// 少这一个方法，注册策略的「enabled+mode 原子可见」会静默退化成逐条写。
func (h *trustPolicyWriteHook) UpsertMany(ctx context.Context, inputs []options.UpdateInput) ([]options.Option, error) {
	var (
		updated []options.Option
		err     error
	)
	if batch, ok := h.store.(options.BatchUpsertStore); ok {
		updated, err = batch.UpsertMany(ctx, inputs)
	} else {
		updated = make([]options.Option, 0, len(inputs))
		for _, input := range inputs {
			option, upsertErr := h.store.Upsert(ctx, input)
			if upsertErr != nil {
				return nil, upsertErr
			}
			updated = append(updated, option)
		}
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(inputs))
	for _, input := range inputs {
		names = append(names, input.Name)
	}
	h.observe(ctx, names)
	return updated, nil
}

// observe 只在**真的**写了信任策略键时入队一次。
//
// 其它键（site.name、mail.* 等）一律不触发：全员重算是昂贵动作，不能被设置页
// 上任意一次保存带出来。
func (h *trustPolicyWriteHook) observe(ctx context.Context, names []string) {
	if h == nil || !trustPolicyChanged(names) {
		return
	}
	h.mu.RLock()
	enqueue := h.enqueue
	h.mu.RUnlock()
	if enqueue == nil {
		return
	}
	// 入队失败不回传：策略已经落库了，让一次队列抖动把设置保存变成失败是错的。
	// 每日 sweep（mode=stale）会兜底补上这一次遗漏。
	if err := enqueue(ctx); err != nil && h.logger != nil {
		h.logger.Warn("enqueue trust level recompute after policy change failed", "error", err)
	}
}

// trustPolicyChanged 报告这批写入是否改了等级判定输入。
//
// 只认两个键：
//   - trust.levels（档位门槛/限额）；
//   - trust.new_user_days（新人窗口，判定里决定账龄闸）。
//
// **刻意不含 features.trust_levels**：开关从关到开会把全站用户一次性变成「可判定」，
// 此刻批量发事件就是一次自造的事件洪水；真变了等级的人由活动触发点与每日 sweep
// 分头收敛。
func trustPolicyChanged(names []string) bool {
	for _, name := range names {
		switch strings.TrimSpace(name) {
		case options.NameTrustLevels, options.NameTrustNewUserDays:
			return true
		}
	}
	return false
}
