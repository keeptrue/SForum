package authsession

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
)

type requestSessionKey struct {
	manager *Manager
}

// ErrStoreUnavailable 标记「会话存储后端不可用」，把依赖故障与凭据问题分开。
//
// 它存在的唯一理由是可判别性：Fiber 的 session store 对「cookie 缺失 / 提取失败 /
// 会话不存在」一律续造一个全新匿名会话而不报错（fiber v3.4.0
// middleware/session/store.go:142-158，getSessionID 在 :223-226 把提取失败折成空 id），
// 所以能走到 requestSessionState 里的 error 只剩存储这一族。读路径据此答 503，
// 而不是把「Redis 抖了一下」翻译成「你是匿名」或「你没有权限」。
var ErrStoreUnavailable = errors.New("authsession: session store unavailable")

type requestSessionState struct {
	session        *session.Session
	storageContext *requestSessionStorageContext
	pending        atomic.Bool
	lifecycleMu    sync.Mutex
	generation     uint64
	pendingActive  bool
}

func (s *requestSessionState) invalidateLocked() {
	if s == nil {
		return
	}
	s.generation++
	if s.generation == 0 {
		s.generation++
	}
	s.pendingActive = false
	s.pending.Store(true)
}

func (s *requestSessionState) isStableGeneration(generation uint64) bool {
	if s == nil {
		return false
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return !s.pending.Load() && !s.pendingActive && s.generation == generation
}

func (s *requestSessionState) run(ctx context.Context, operation func() error) error {
	if s == nil || s.storageContext == nil || operation == nil {
		return ErrSessionAuthorityChanged
	}
	return s.storageContext.run(ctx, operation)
}

// requestSessionStorageContext keeps Fiber's request/cookie surface while
// supplying the accepted Host context to context-aware session storage calls.
type requestSessionStorageContext struct {
	fiber.Ctx
	operationMu sync.Mutex
	contextMu   sync.RWMutex
	operation   context.Context
}

func (c *requestSessionStorageContext) run(ctx context.Context, operation func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.contextMu.Lock()
	c.operation = ctx
	c.contextMu.Unlock()
	defer func() {
		c.contextMu.Lock()
		c.operation = nil
		c.contextMu.Unlock()
	}()
	return operation()
}

func (c *requestSessionStorageContext) currentContext() context.Context {
	c.contextMu.RLock()
	ctx := c.operation
	c.contextMu.RUnlock()
	if ctx != nil {
		return ctx
	}
	if c.Ctx != nil {
		return c.Ctx.Context()
	}
	return context.Background()
}

func (c *requestSessionStorageContext) Deadline() (time.Time, bool) {
	return c.currentContext().Deadline()
}

func (c *requestSessionStorageContext) Done() <-chan struct{} {
	return c.currentContext().Done()
}

func (c *requestSessionStorageContext) Err() error {
	return c.currentContext().Err()
}

func (c *requestSessionStorageContext) Value(key any) any {
	if value := c.currentContext().Value(key); value != nil {
		return value
	}
	if c.Ctx != nil {
		return c.Ctx.Value(key)
	}
	return nil
}

type requestRenewalKey struct {
	manager *Manager
}

type requestRenewalResult struct {
	attempted bool
	err       error
}

func (m *Manager) requestSession(c fiber.Ctx) (*session.Session, error) {
	state, err := m.requestSessionState(c)
	if err != nil {
		return nil, err
	}
	return state.session, nil
}

func (m *Manager) requestSessionState(c fiber.Ctx) (*requestSessionState, error) {
	key := requestSessionKey{manager: m}
	if cached, ok := c.Locals(key).(*requestSessionState); ok && cached != nil {
		return cached, nil
	}
	storageContext := &requestSessionStorageContext{Ctx: c}
	loaded, err := m.store.Get(storageContext)
	if err != nil {
		// 双层 %w：外层给调用方一个稳定哨兵，内层保留原始错误文本——运维要能看出
		// 是 Redis 掉线、超时还是地址配错。判定只走 errors.Is，不做字符串匹配。
		return nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	state := &requestSessionState{session: loaded, storageContext: storageContext}
	c.Locals(key, state)
	return state, nil
}

func (m *Manager) requestRenewal(c fiber.Ctx) *requestRenewalResult {
	key := requestRenewalKey{manager: m}
	if cached, ok := c.Locals(key).(*requestRenewalResult); ok && cached != nil {
		return cached
	}
	result := &requestRenewalResult{}
	c.Locals(key, result)
	return result
}

func (m *Manager) resetRequestRenewal(c fiber.Ctx) {
	c.Locals(requestRenewalKey{manager: m}, &requestRenewalResult{})
}
