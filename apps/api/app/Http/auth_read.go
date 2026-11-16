package http

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// 公开读路径的主体解析：依赖故障不是身份问题。
//
// 同一套 `OptionalActor` 在改造前有五种答案（401 / 吞错匿名 / 500 / 刻意匿名 /
// 跳过 viewer），根因是「拿不到主体」这一件事被压成了一个 error，而 error 里混着
// 两类完全不同的原因：凭据指向的主体没了（重试无用，答 401），和 Redis/PG 现在
// 答不上来（重试有效，答 503）。压在一起时，运维看到 401 会去查权限，看到 500
// 会去翻代码，而真正的答案往往是「Redis 抖了一下」。
//
// 分类（A–E）与判定依据：
//
//	A 无凭据            (0,false,nil)                              → 零值 Actor + nil
//	B 凭据存在但不可解析  会话层折成 (0,false,nil) 后**不可观测**      → 等同 A（见下）
//	C 主体不可用         identity.ErrUserNotFound 或 disabled/banned → 401 auth.required
//	D 依赖故障           authsession/identity.ErrStoreUnavailable    → 503 service.not_ready
//	E 未识别             其它                                       → 503（fail-closed）
//
// 为什么 B 只能等同 A：authsession.Manager 把「会话令牌非法 / 已撤销 / 版本失配 /
// 续期被拒」全部折成 (0,false,nil)（manager.go:317、:330、:341、:364），这是既有的
// 身份语义，不是本轮要改的东西。要真正区分 B 需要给 CurrentUserID 换成三态返回，
// 属于独立改动（见 knowledge/decisions/2026-11-16-read-path-actor-semantics.md）。
//
// 为什么 E 也答 503：不确定时唯一安全的方向是「维持现状不回答」，而不是「按匿名
// 读」——匿名可见集合在若干端点上比登录集合更宽（EntityMeta 的受限字段就是例子）。

// actorFailure 是「可选主体解析失败」的类别。
type actorFailure int

const (
	// actorFailureDependency 与 actorFailureUnknown 都答 503：前者可判别，后者
	// fail-closed。
	actorFailureDependency actorFailure = iota
	actorFailureSubjectUnavailable
	actorFailureUnknown
)

// readPathDependencyFailure 标记「读路径主体解析遇到依赖故障」。
//
// 由 errors.go 的全局 handler 统一渲染成 503 + Retry-After: 1 + service.not_ready，
// 与 OPS2-01 的中间件层依赖故障保持同一份响应形状（错误码复用，不新增 locale 键）。
type readPathDependencyFailure struct{ cause error }

func (e *readPathDependencyFailure) Error() string {
	return fmt.Sprintf("read path dependency unavailable: %v", e.cause)
}

func (e *readPathDependencyFailure) Unwrap() error { return e.cause }

// readPathDependencyCause 取出被标记的原始错误（保留原文供日志）。
func readPathDependencyCause(err error) (error, bool) {
	var failure *readPathDependencyFailure
	if errors.As(err, &failure) {
		return failure.cause, true
	}
	return nil, false
}

// ReadActor 解析公开读路径的可选登录主体。
//
// 无凭据请求的行为与改造前逐字一致：零值 Actor + nil，调用方继续匿名读。
// 只有「带了凭据却解析不出主体」才进入 401/503 分支。
func ReadActor(c fiber.Ctx, sessions *authsession.Manager, users identity.ActorStore) (identity.Actor, error) {
	actor, err := optionalActor(c, sessions, users, false)
	if err != nil {
		return identity.Actor{}, classifyActorError(err)
	}
	// C：主体解析成功但账号不可用。这里不存在「再查一次库」的需要——LoadActor 返回的
	// Status 就是权威值（postgres_store.go:394-415 原样透传 CurrentUser.Status）。
	if unusableSubjectStatus(actor.Status) {
		return identity.Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	return actor, nil
}

// RequireActor 解析必须登录的主体（写路径与私密读路径）。
//
// 与 ReadActor 只差一处：无凭据不是「匿名视图」，而是一次拒绝。分类完全一致
// （C→401、D/E→503），所以写路径也不会再把 Redis 抖动说成身份问题——必登端点
// 若继续直连 OptionalActor/LoadActor，依赖故障会原样抛成 500。
//
// 它**不**在门上判账号状态：写路径的「失效主体」由域层答 403 permission.denied
// （既有契约，见 EntityMeta 的 permission 测试），在门上改答 401 等于替换一个已经
// 声明过的状态码。读路径则不同——那里没有域层兜底，降级就是放宽，所以在 ReadActor
// 里判。
func RequireActor(c fiber.Ctx, sessions *authsession.Manager, users identity.ActorStore) (identity.Actor, error) {
	actor, err := optionalActor(c, sessions, users, false)
	if err != nil {
		return identity.Actor{}, classifyActorError(err)
	}
	if actor.ID == 0 {
		return identity.Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	return actor, nil
}

// classifyActorError 把 optionalActor 的失败翻成对外答案：C→401，D/E→503。
func classifyActorError(err error) error {
	if classifyActorFailure(err) == actorFailureSubjectUnavailable {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	return &readPathDependencyFailure{cause: err}
}

// classifyActorFailure 按哨兵错误分类，不做字符串匹配。
//
// 依据：
//   - identity.ErrUserNotFound：postgres_store.go:117-140 把 pgx.ErrNoRows 显式翻成
//     ErrUserNotFound（此前直接透传驱动错误，上层无从判别）；
//   - authsession.ErrStoreUnavailable：request_state.go:132-146（会话存储 Get）、
//     manager.go:345（令牌版本来源）、manager.go:356（撤销目录查询）三处依赖读；
//   - identity.ErrStoreUnavailable：postgres_store.go:117-140 的查询/扫描与
//     loadCurrentUserAccess 失败。
func classifyActorFailure(err error) actorFailure {
	switch {
	case errors.Is(err, identity.ErrUserNotFound):
		return actorFailureSubjectUnavailable
	case errors.Is(err, identity.ErrStoreUnavailable), errors.Is(err, authsession.ErrStoreUnavailable):
		return actorFailureDependency
	default:
		return actorFailureUnknown
	}
}

// unusableSubjectStatus 判定「已解析但不可用」的账号状态。
//
// 只认显式声明为不可用的两个状态：未知/空状态照原样放行，权限集合本来就是空的，
// 不会因此多读到一个字段。
func unusableSubjectStatus(status identity.UserStatus) bool {
	return status == identity.UserStatusDisabled || status == identity.UserStatusBanned
}

const readDependencyLogInterval = 30 * time.Second

// readDependencyLogThrottle 是读路径依赖故障的进程级限频器。
//
// 为什么是进程级而不是每请求：一次 Redis 抖动会让之后每个公开读都命中同一分支，
// 逐条打日志会把第一条证据埋掉并烧光日志配额。为什么不做成装配期实例：吞咽点分布在
// 控制器包里（Page Registry 的 guest 回退没有 errorHandler 可挂），进程级是唯一
// 能让两个出口共用同一窗口的位置；除时间戳外不持有任何状态。
//
// 包级可变状态在此是有意的例外：它只保存「上次打印时刻」这一项（外加互斥锁），
// 不改写任何请求语义、不参与判定、也不跨测试泄漏——测试用例只在开始前把 last
// 清零来隔离窗口。任何需要更多状态的实现都应该改成装配期依赖，而不是继续往这里加。
type readDependencyLogThrottle struct {
	mu   sync.Mutex
	last time.Time
}

var readDependencyLog = &readDependencyLogThrottle{}

func (t *readDependencyLogThrottle) warn(c fiber.Ctx, cause error) {
	now := time.Now()
	t.mu.Lock()
	if !t.last.IsZero() && now.Sub(t.last) < readDependencyLogInterval {
		t.mu.Unlock()
		return
	}
	t.last = now
	t.mu.Unlock()
	slog.Default().Warn(
		"api read path dependency unavailable",
		"method", c.Method(),
		"path", c.Path(),
		"error", cause,
	)
}

// LogActorDependency 记录一次读路径依赖故障（限频 30s，带原始错误文本）。
//
// 两个出口共用它：全局 handler 在答 503 时记录；Page Registry 的 guest 回退在
// **不答 503** 时也记录——否则「渲染路径静默降级」就会成为新的观测盲区。
// 非依赖类错误（C：主体不存在/不可用）不打这条日志：那是确定结论，不是故障。
func LogActorDependency(c fiber.Ctx, cause error) {
	if cause == nil || classifyActorFailure(cause) == actorFailureSubjectUnavailable {
		return
	}
	readDependencyLog.warn(c, cause)
}
