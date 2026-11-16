package http

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

// storageFailureLogInterval 限制同一进程内 storage 故障告警的频率。
//
// 一次 Redis 抖动会让之后每个请求都失败；逐条打日志既会把第一条证据埋掉，也会
// 把日志配额烧光。运维需要的是「什么时候开始坏的、坏在哪」，不是几万行同样的记录。
const storageFailureLogInterval = 30 * time.Second

// storageFailure 标记一次由 fiber.Storage 后端故障导致的错误。
//
// limiter 与 csrf 会把 storage 错误原样（或以 %w 包装）向上抛：前者的默认归宿是
// 全局 errorHandler 的 500，后者的默认归宿是 csrfErrorHandler 的 403。两种答案都在
// 撒谎——请求本身没有问题，是依赖不可用。这个类型把「依赖不可用」从「请求不合法」
// 里分出来，宿主据此回答 503 + Retry-After，而不是把整个 /api/v1 报成权限问题。
type storageFailure struct{ err error }

func (e *storageFailure) Error() string { return e.err.Error() }

func (e *storageFailure) Unwrap() error { return e.err }

// storageFailureCause 取出被包装的原始 storage 错误（保留原文，供日志与诊断）。
func storageFailureCause(err error) (error, bool) {
	var failure *storageFailure
	if errors.As(err, &failure) {
		return failure.err, true
	}
	return nil, false
}

func markStorageFailure(err error) error {
	if err == nil {
		return nil
	}
	return &storageFailure{err: err}
}

// storageFailureStorage 把底层 fiber.Storage 的故障标记成 storageFailure。
//
// 只包装中间件用的这一份实例：同一份 Redis 还服务于会话目录，而会话层对自己那份
// 故障有自己的可判别哨兵（authsession.ErrStoreUnavailable，见 request_state.go 与
// manager.go 的令牌版本/撤销目录两处），由读路径在 auth_read.go 里分类成 503。
// 两条通道各认各的标记，互不改写对方的语义——这条注释此前写的是「会话层会答
// 503 + session_unavailable」，那是我方实现不了的承诺（该错误码只出现在注册路径
// Models/Identity/types.go:21），本轮已按实际行为改正。
type storageFailureStorage struct{ inner fiber.Storage }

func withStorageFailureMarking(inner fiber.Storage) fiber.Storage {
	if inner == nil {
		return nil
	}
	return storageFailureStorage{inner: inner}
}

func (s storageFailureStorage) GetWithContext(ctx context.Context, key string) ([]byte, error) {
	value, err := s.inner.GetWithContext(ctx, key)
	return value, markStorageFailure(err)
}

func (s storageFailureStorage) Get(key string) ([]byte, error) {
	value, err := s.inner.Get(key)
	return value, markStorageFailure(err)
}

func (s storageFailureStorage) SetWithContext(ctx context.Context, key string, value []byte, exp time.Duration) error {
	return markStorageFailure(s.inner.SetWithContext(ctx, key, value, exp))
}

func (s storageFailureStorage) Set(key string, value []byte, exp time.Duration) error {
	return markStorageFailure(s.inner.Set(key, value, exp))
}

func (s storageFailureStorage) DeleteWithContext(ctx context.Context, key string) error {
	return markStorageFailure(s.inner.DeleteWithContext(ctx, key))
}

func (s storageFailureStorage) Delete(key string) error {
	return markStorageFailure(s.inner.Delete(key))
}

func (s storageFailureStorage) ResetWithContext(ctx context.Context) error {
	return markStorageFailure(s.inner.ResetWithContext(ctx))
}

func (s storageFailureStorage) Reset() error {
	return markStorageFailure(s.inner.Reset())
}

// Close 不参与请求路径，故障照原样上抛：关闭失败应由持有者记录，不该被请求语义
// 包装成一次 503。
func (s storageFailureStorage) Close() error { return s.inner.Close() }

// safeMethodWithoutStorage 判定可以放行的读方法。
//
// 它们没有可被伪造的副作用，而就绪/健康探针恰恰都是 GET：把「Redis 抖了一下」
// 变成「整站 403」，最先坏掉的就是「这个站到底还活着吗」这个答案。
// TRACE/QUERY 不在 API 的路由表里，继续按失败处理即可（路由会给 404）。
func safeMethodWithoutStorage(method string) bool {
	switch method {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return true
	default:
		return false
	}
}

// storageUnavailable 渲染一次依赖故障：503 + Retry-After: 1。
//
// reason 复用既有的 service.not_ready：新增错误码要同步补齐所有 locale，而
// 「服务尚未就绪」正是这里要表达的意思——不是权限问题，也不是请求格式问题。
func storageUnavailable(c fiber.Ctx) error {
	c.Set(fiber.HeaderRetryAfter, "1")
	return ErrorResponse(c, fiber.StatusServiceUnavailable, "service.not_ready")
}

// storageFailureReporter 是依赖故障的限频日志出口：文案由调用方给（中间件层与
// csrf 层说法不同），但总是带上原始错误文本——否则运维只能看到一条 503，无法判断
// 是 Redis 掉线还是地址配错。
type storageFailureReporter struct {
	logger *slog.Logger
	mu     sync.Mutex
	last   time.Time
}

func newStorageFailureReporter(logger *slog.Logger) *storageFailureReporter {
	return &storageFailureReporter{logger: logger}
}

func (r *storageFailureReporter) warn(c fiber.Ctx, message string, cause error) {
	if r == nil || r.logger == nil {
		return
	}
	now := time.Now()
	r.mu.Lock()
	if !r.last.IsZero() && now.Sub(r.last) < storageFailureLogInterval {
		r.mu.Unlock()
		return
	}
	r.last = now
	r.mu.Unlock()
	r.logger.Warn(
		message,
		"method", c.Method(),
		"path", c.Path(),
		"error", cause,
	)
}
