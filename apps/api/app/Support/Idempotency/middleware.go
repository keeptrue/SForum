package idempotency

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
)

// releaseTimeout 是 panic/提前返回时兜底释放 pending 的预算。释放本身是一次
// Redis CAS，给足 3s 只为覆盖网络抖动；它发生在响应已经写好之后，不拖慢业务。
const releaseTimeout = 3 * time.Second

// ActorResolver 解析当前请求的幂等作用域主体（通常为登录用户 ID）。
// 返回 0 表示匿名；同一 key 在匿名与登录用户之间不共享。
type ActorResolver func(c fiber.Ctx) (int64, error)

// Middleware 仅在请求携带 Idempotency-Key 时生效；缺省不强制。
// 应挂在需要去重的写路由上（如 POST /topics、POST /comments）。
func Middleware(store *Store, resolveActor ActorResolver) fiber.Handler {
	return func(c fiber.Ctx) error {
		key := strings.TrimSpace(c.Get(HeaderName))
		if key == "" || store == nil {
			return c.Next()
		}
		if err := ValidateKey(key); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "idempotency.key_invalid")
		}

		var actorID int64
		if resolveActor != nil {
			id, err := resolveActor(c)
			if err != nil {
				return err
			}
			actorID = id
		}

		// Path() 不含 query，避免 ? 参数破坏作用域。
		storageKey := StorageKey(actorID, c.Method(), c.Path(), key)
		rec, started, conflict, err := store.Begin(c.Context(), storageKey)
		if err != nil {
			// 存储故障时 fail-open：不阻断业务写路径。
			return c.Next()
		}
		if conflict {
			return fiber.NewError(fiber.StatusConflict, "idempotency.in_progress")
		}
		if !started && rec.State == stateCompleted {
			c.Set(ReplayedHeader, "true")
			return c.Status(rec.Status).Send(rec.Body)
		}

		// 从这里开始本请求持有 lease。兜底释放：处理器 panic（fiber 的 recover 会
		// 接住）或中间提前返回时，正常路径的 Complete/Abort 都到不了，pending 会一直
		// 挂到 90s 租约到期，客户端在这段时间里重试只能拿到 409（P2-4）。
		released := false
		defer func() {
			if released {
				return
			}
			// panic 时 c.Context() 可能已经失效（连接关闭/上下文取消），释放不能
			// 跟着一起取消，所以脱钩父上下文再给一个短超时。
			ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), releaseTimeout)
			defer cancel()
			if err := store.Abort(ctx, storageKey, rec.Lease); err != nil {
				logSettleFailure("abort(panic)", err)
			}
		}()

		err = c.Next()
		status := c.Response().StatusCode()
		// 仅缓存 2xx 成功响应；客户端可用同一 key 重试 4xx/5xx。
		if err == nil && status >= 200 && status < 300 {
			body := append([]byte(nil), c.Response().Body()...)
			if err := store.Complete(c.Context(), storageKey, rec.Lease, status, body); err != nil {
				// 收敛失败不阻断业务响应：结果已经写回客户端了。
				logSettleFailure("complete", err)
			} else {
				released = true
			}
			return nil
		}
		if err := store.Abort(c.Context(), storageKey, rec.Lease); err != nil {
			logSettleFailure("abort", err)
		} else {
			released = true
		}
		return err
	}
}

// logSettleFailure 把租约收敛失败降级成 debug：ErrLeaseLost 意味着租约过期后
// 已有别的请求接管这个 key，当前请求本来就不该写缓存，不是故障。
func logSettleFailure(operation string, err error) {
	if errors.Is(err, ErrLeaseLost) {
		slog.Debug("idempotency: pending lease was lost", "operation", operation)
		return
	}
	slog.Debug("idempotency: settle pending record failed", "operation", operation, "error", err)
}

// ValidateKey 限制可见 ASCII 与长度，避免控制字符。
func ValidateKey(key string) error {
	if key == "" || utf8.RuneCountInString(key) > MaxKeyLength {
		return errInvalidKey
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return errInvalidKey
		}
	}
	return nil
}

type invalidKeyError struct{}

func (invalidKeyError) Error() string { return "idempotency key invalid" }

var errInvalidKey = invalidKeyError{}
