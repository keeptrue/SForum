package authsession

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
)

var (
	// ErrRenewalRejected means a Host renewal gate denied the cookie rotation.
	// CurrentUserID maps it to an unauthenticated session rather than a 500.
	ErrRenewalRejected         = errors.New("authsession: renewal rejected by host gate")
	ErrPendingConsumed         = errors.New("authsession: pending session has already been consumed")
	ErrSessionAuthorityChanged = errors.New("authsession: session authority changed")
)

const (
	sessionUserIDKey          = "user_id"
	sessionCreatedAtKey       = "created_at"
	sessionRenewedAtKey       = "renewed_at"
	sessionLoginIPKey         = "login_ip"
	sessionLoginAgentKey      = "login_user_agent"
	sessionTokenVersionKey    = "token_version"
	sessionSIDKey             = "sid"
	sessionLastSeenTouchedKey = "last_seen_touched"
)

// lastSeenInterval 限制刷新 user_sessions.last_seen_at 的写频率，避免每个请求都写。
const lastSeenInterval = time.Hour

const (
	sessionIssueCleanupTimeout = 5 * time.Second
	sessionReplacedReason      = "session_replaced"
)

// TokenVersionSource 返回用户当前的令牌版本号。
// 用于会话失效校验（M8）：session 存储创建时的版本号，校验时与用户当前版本比对，
// 不一致（如密码重置后递增了版本号）则视为会话失效。
// 为 nil 时跳过版本校验（向后兼容测试场景）。
type TokenVersionSource func(ctx context.Context, userID int64) (int64, error)

// SessionStore 是会话目录的存储契约（user_sessions 表）。
// 由 identity.PostgresStore 实现，authsession 通过本接口读写目录，
// 避免直接依赖 identity 包（防循环依赖）。
// 为 nil 时跳过设备目录功能（向后兼容测试场景）。
type SessionStore interface {
	// CreateSession 登记一条新会话/设备。
	CreateSession(ctx context.Context, input SessionRecordInput) error
	// IsSessionRevoked 判断 sid 是否已被下线（实现「下次请求失效」）。
	IsSessionRevoked(ctx context.Context, userID int64, sid string) (bool, error)
	// TouchSessionLastSeen 刷新最后活跃时间（节流调用）。
	TouchSessionLastSeen(ctx context.Context, userID int64, sid string) error
	// RevokeSession 标记单条会话下线（logout 时调用）。
	RevokeSession(ctx context.Context, userID int64, sid string, reason string) error
}

// SessionRecordInput 是登记会话时所需的展示字段（已由调用方从 UA/IP 解析好）。
type SessionRecordInput struct {
	UserID       int64
	SID          string
	SessionHash  string
	DeviceName   string
	Browser      string
	OS           string
	UserAgentRaw string
	// IPAddress 是规范化后的真实客户端 IP（全文，管理/风控用）。
	IPAddress string
	// IPPrefix 是脱敏展示前缀（用户「我的设备」列表）。
	IPPrefix string
}

// RenewalEffect is the Host-owned cookie rotation plus persistence mutation.
type RenewalEffect func(context.Context) error

// RenewalGate is the compatibility policy check that runs before renewal.
// New exact-admission consumers use RenewalEffectGate so the mutation remains
// inside their admission lease.
type RenewalGate func(ctx context.Context, userID int64) error

// RenewalEffectGate is an optional Host policy boundary. An allow path must invoke
// effect exactly once while its exact admission remains held. A policy error
// treats the session as invalid; effect/storage errors retain their original
// transport behavior. Revocation uses CurrentUserIDWithoutRenewal instead.
type RenewalEffectGate func(ctx context.Context, userID int64, tokenVersion int64, effect RenewalEffect) error

type Config struct {
	RenewalInterval time.Duration
	HashSecret      string
	TokenVersion    TokenVersionSource
	SessionStore    SessionStore
	// RenewalGate is retained for source compatibility and runs before the effect.
	RenewalGate RenewalGate
	// RenewalEffectGate owns the effect when configured and takes precedence over
	// RenewalGate. It is consulted only when a renewal interval actually fires.
	RenewalEffectGate RenewalEffectGate
}

type Manager struct {
	store             *session.Store
	renewalInterval   time.Duration
	cleanupTimeout    time.Duration
	hashSecret        []byte
	tokenVersion      TokenVersionSource
	sessions          SessionStore
	renewalGate       RenewalGate
	renewalEffectGate RenewalEffectGate
	now               func() time.Time
	// pluginSessionState 是装配层注入的插件命名空间会话状态擦除钩子。
	// 未注入（nil）时登出/登录完全照常；见 plugin_session_state.go 的语义说明。
	pluginSessionState PluginSessionStateInvalidator
}

type Info struct {
	ID        string
	Hash      string
	SID       string
	CreatedAt time.Time
	RenewedAt time.Time
}

func NewManager(store *session.Store, cfg Config) *Manager {
	if store == nil {
		store = session.NewStore()
	}
	store.RegisterType(time.Time{})

	return &Manager{
		store:             store,
		renewalInterval:   cfg.RenewalInterval,
		cleanupTimeout:    sessionIssueCleanupTimeout,
		hashSecret:        []byte(cfg.HashSecret),
		tokenVersion:      cfg.TokenVersion,
		sessions:          cfg.SessionStore,
		renewalGate:       cfg.RenewalGate,
		renewalEffectGate: cfg.RenewalEffectGate,
		now:               func() time.Time { return time.Now().UTC() },
	}
}

func (m *Manager) Start(c fiber.Ctx, userID int64) (Info, error) {
	pending, err := m.Begin(c, userID)
	if err != nil {
		return Info{}, err
	}
	if err := pending.Save(); err != nil {
		return Info{}, err
	}
	return pending.Info(), nil
}

func (m *Manager) Begin(c fiber.Ctx, userID int64) (*Pending, error) {
	return m.BeginWithContext(c, c.Context(), userID)
}

// BeginWithContext prepares a new browser session while using the accepted
// Host effect context for authority reads. The Fiber session transport itself
// remains bound to the current request context.
func (m *Manager) BeginWithContext(c fiber.Ctx, ctx context.Context, userID int64) (*Pending, error) {
	return m.beginWithContext(c, ctx, userID, nil)
}

// BeginWithAuthorityVersion rejects an issue effect when the credential proof's
// user token revision is no longer current.
func (m *Manager) BeginWithAuthorityVersion(
	c fiber.Ctx,
	ctx context.Context,
	userID int64,
	expectedTokenVersion int64,
) (*Pending, error) {
	return m.beginWithContext(c, ctx, userID, &expectedTokenVersion)
}

func (m *Manager) beginWithContext(
	c fiber.Ctx,
	ctx context.Context,
	userID int64,
	expectedTokenVersion *int64,
) (*Pending, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := m.requestSessionState(c)
	if err != nil {
		return nil, err
	}
	now := m.currentTime()
	sid, err := generateSID()
	if err != nil {
		return nil, err
	}
	var tokenVersion *int64
	if expectedTokenVersion != nil && m.tokenVersion == nil {
		return nil, ErrSessionAuthorityChanged
	}
	if m.tokenVersion != nil {
		version, versionErr := m.tokenVersion(ctx, userID)
		if versionErr != nil {
			return nil, versionErr
		}
		if expectedTokenVersion != nil && version != *expectedTokenVersion {
			return nil, ErrSessionAuthorityChanged
		}
		tokenVersion = &version
	}

	state.lifecycleMu.Lock()
	defer state.lifecycleMu.Unlock()
	if state.pendingActive {
		return nil, ErrSessionAuthorityChanged
	}
	state.generation++
	if state.generation == 0 {
		state.generation++
	}
	generation := state.generation
	state.pendingActive = true
	state.pending.Store(true)
	sess := state.session
	previousUserID, _ := sessionUserID(sess.Get(sessionUserIDKey))
	previousSID, _ := sess.Get(sessionSIDKey).(string)
	claimComplete := false
	defer func() {
		if panicValue := recover(); panicValue != nil {
			if !claimComplete {
				state.invalidateLocked()
				func() {
					defer func() { _ = recover() }()
					m.abortSessionCredential(
						ctx, c, state, sess, sess.ID(), previousUserID, previousSID, "issue_failed",
					)
				}()
			}
			panic(panicValue)
		}
	}()
	if err := state.run(ctx, sess.Reset); err != nil {
		state.invalidateLocked()
		m.abortSessionCredential(
			ctx, c, state, sess, sess.ID(), previousUserID, previousSID, "issue_failed",
		)
		claimComplete = true
		return nil, err
	}
	m.revokeSessionDirectory(ctx, previousUserID, previousSID, sessionReplacedReason)
	// 登录替换掉旧 sid：旧会话的插件状态不得被新会话继承（S4/S5），
	// best-effort 擦除（失败不影响登录）。
	m.dropPluginSessionState(ctx, previousSID)
	m.resetRequestRenewal(c)
	sess.Set(sessionUserIDKey, userID)
	sess.Set(sessionCreatedAtKey, now)
	sess.Set(sessionRenewedAtKey, now)
	// 登录 IP 写入 session payload（全文），供审计/风控；解析走 clientip 防代理伪造。
	sess.Set(sessionLoginIPKey, truncate(clientip.FromCtx(c), 128))
	sess.Set(sessionLoginAgentKey, truncate(c.Get(fiber.HeaderUserAgent), 512))
	sess.Set(sessionSIDKey, sid)
	// 记录创建会话时的令牌版本号，供后续 CurrentUserID 比对以实现会话失效（M8）。
	if tokenVersion != nil {
		sess.Set(sessionTokenVersionKey, *tokenVersion)
	}
	claimComplete = true

	pending := &Pending{
		manager:    m,
		session:    sess,
		state:      state,
		request:    c,
		context:    ctx,
		info:       m.info(sess, now, now, sid),
		userID:     userID,
		generation: generation,
	}
	if m.sessions != nil {
		pending.SetDeviceInfo(SessionRecordInput{
			UserID:       userID,
			UserAgentRaw: truncate(c.Get(fiber.HeaderUserAgent), 512),
			IPAddress:    truncate(clientip.FromCtx(c), 128),
		})
	}
	return pending, nil
}

func (m *Manager) CurrentUserID(c fiber.Ctx) (int64, bool, error) {
	return m.currentUserID(c, true)
}

// CurrentUserIDWithoutRenewal authenticates the existing Host session without
// rotating or refreshing it. Security revocation paths use this entry so a
// third-party renew policy can never delay or veto Host-local revocation.
func (m *Manager) CurrentUserIDWithoutRenewal(c fiber.Ctx) (int64, bool, error) {
	return m.currentUserID(c, false)
}

func (m *Manager) currentUserID(c fiber.Ctx, refresh bool) (int64, bool, error) {
	state, err := m.requestSessionState(c)
	if err != nil {
		return 0, false, err
	}
	sess := state.session
	var renewal *requestRenewalResult
	if refresh {
		renewal = m.requestRenewal(c)
		// A failed rotation clears the in-memory payload. Preserve its memoized
		// result before parsing that payload so repeated lookups cannot silently
		// turn a storage failure into an unauthenticated success.
		if renewal.attempted && renewal.err != nil {
			if errors.Is(renewal.err, ErrRenewalRejected) {
				return 0, false, nil
			}
			return 0, false, renewal.err
		}
	}
	state.lifecycleMu.Lock()
	if state.pending.Load() || state.pendingActive {
		state.lifecycleMu.Unlock()
		return 0, false, nil
	}
	generation := state.generation
	userID, ok := sessionUserID(sess.Get(sessionUserIDKey))
	if !ok {
		state.lifecycleMu.Unlock()
		return 0, false, nil
	}
	sessionVersion, _ := sess.Get(sessionTokenVersionKey).(int64)
	sid, _ := sess.Get(sessionSIDKey).(string)
	state.lifecycleMu.Unlock()

	// 令牌版本号校验（M8）：session 版本与用户当前版本不一致则视为会话失效，
	// 用于密码重置/封禁后使旧会话立即失效。版本号缺失（旧 session 或未配置 source）时跳过。
	if m.tokenVersion != nil {
		currentVersion, err := m.tokenVersion(c.Context(), userID)
		if err != nil {
			return 0, false, nil
		}
		if sessionVersion != currentVersion {
			return 0, false, nil
		}
	}

	// 设备目录撤销校验：若 sid 已被下线（revoke_device/revoke_others/max_exceeded），
	// 视为未登录，实现「被下线设备在下次请求即失效」。
	if m.sessions != nil && sid != "" {
		revoked, err := m.sessions.IsSessionRevoked(c.Context(), userID, sid)
		if err != nil || revoked {
			return 0, false, nil
		}
	}

	if !refresh {
		if !state.isStableGeneration(generation) {
			return 0, false, nil
		}
		return userID, true, nil
	}
	if !renewal.attempted {
		renewal.err = m.refresh(c, c.Context(), state, sess, generation, userID, sessionVersion)
		renewal.attempted = true
	}
	if err := renewal.err; err != nil {
		// Host session policy (or another renewal gate) failed closed: treat the
		// browser session as unauthenticated without elevating to a transport error.
		if errors.Is(err, ErrRenewalRejected) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return userID, true, nil
}

// CurrentSID 返回当前请求所在会话的 opaque sid。
// 用于设备列表标记 isCurrent 与「下线其他设备」时排除当前会话。
// 未配置会话目录或无有效会话时返回空串。
func (m *Manager) CurrentSID(c fiber.Ctx) (string, error) {
	state, err := m.requestSessionState(c)
	if err != nil {
		return "", err
	}
	if state.pending.Load() {
		return "", nil
	}
	sess := state.session
	sid, _ := sess.Get(sessionSIDKey).(string)
	return sid, nil
}

func (m *Manager) Destroy(c fiber.Ctx) error {
	state, err := m.requestSessionState(c)
	if err != nil {
		return err
	}
	var (
		userID     int64
		sid        string
		destroyErr error
	)
	func() {
		state.lifecycleMu.Lock()
		defer state.lifecycleMu.Unlock()
		state.invalidateLocked()
		sess := state.session
		userID, _ = sessionUserID(sess.Get(sessionUserIDKey))
		sid, _ = sess.Get(sessionSIDKey).(string)
		sessionID := sess.ID()
		defer func() {
			if panicValue := recover(); panicValue != nil {
				func() {
					defer func() { _ = recover() }()
					m.abortSessionCredential(
						c.Context(), c, state, sess, sessionID, userID, sid, "logout",
					)
				}()
				panic(panicValue)
			}
		}()
		destroyErr = state.run(c.Context(), sess.Destroy)
		if destroyErr != nil {
			m.scrubSessionCredential(c.Context(), c, state, sess, sessionID)
		}
	}()

	// 登出时在目录标记本会话已下线（保留为历史记录），best-effort，不阻塞登出。
	m.revokeSessionDirectory(c.Context(), userID, sid, "logout")
	// 登出擦除该会话的插件状态；同样是 best-effort，失败不阻塞登出。
	m.dropPluginSessionState(c.Context(), sid)
	return destroyErr
}

func (m *Manager) Hash(sessionID string) string {
	mac := hmac.New(sha256.New, m.hashSecret)
	_, _ = mac.Write([]byte(sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) info(sess *session.Session, createdAt time.Time, renewedAt time.Time, sid string) Info {
	id := sess.ID()
	return Info{
		ID:        id,
		Hash:      m.Hash(id),
		SID:       sid,
		CreatedAt: createdAt,
		RenewedAt: renewedAt,
	}
}

func (m *Manager) currentTime() time.Time {
	if m == nil || m.now == nil {
		return time.Now().UTC()
	}
	return m.now().UTC()
}

// generateSID 生成稳定的 opaque 会话标识（与 cookie session id 独立）。
// 32 字节 crypto/rand → hex，用作 user_sessions 唯一键与前端「下线哪一条」的句柄。
// 注意：它是非认证凭证，泄漏也无法登录。
//
// 失败时返回 error 而非回退到时间戳派生值：后者可预测且可能碰撞（同纳秒并发），
// 会破坏 user_sessions 唯一性约束。crypto/rand 失败属于极端系统异常，此时拒绝登录
// 是正确的安全行为——用可预测值静默继续比登录失败更危险。
func generateSID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func sessionUserID(value any) (int64, bool) {
	switch id := value.(type) {
	case int64:
		return id, id != 0
	case int:
		return int64(id), id != 0
	default:
		return 0, false
	}
}

func sessionTime(value any) time.Time {
	parsed, ok := value.(time.Time)
	if !ok {
		return time.Time{}
	}
	return parsed
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len([]rune(value)) <= max {
		return value
	}

	runes := []rune(value)
	return string(runes[:max])
}
