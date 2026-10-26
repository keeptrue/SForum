package identity

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// recordingPluginSessionInvalidator 记录每一次 InvalidateSessionBySID 的 sid，
// 可被设置为返回错误或 panic，用来证明擦除是 best-effort。
type recordingPluginSessionInvalidator struct {
	mu       sync.Mutex
	sids     []string
	failWith error
	panicNow bool
}

func (r *recordingPluginSessionInvalidator) InvalidateSessionBySID(_ context.Context, sid string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.panicNow {
		panic("invalidator exploded")
	}
	r.sids = append(r.sids, sid)
	if r.failWith != nil {
		return 0, r.failWith
	}
	return 1, nil
}

func (r *recordingPluginSessionInvalidator) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sids...)
}

func (r *recordingPluginSessionInvalidator) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sids = nil
}

type pluginSessionStatePGFixture struct {
	fixture *identityPersistencePGFixture
	store   *PostgresStore
	pool    *pgxpool.Pool
}

func (p *pluginSessionStatePGFixture) createSession(t *testing.T, userID int64, sid string) {
	t.Helper()
	if err := p.store.CreateSession(context.Background(), authsession.SessionRecordInput{
		UserID: userID, SID: sid, SessionHash: "hash-" + sid,
	}); err != nil {
		t.Fatalf("create session %s: %v", sid, err)
	}
}

func (p *pluginSessionStatePGFixture) setLastSeen(t *testing.T, sid string, offsetMinutes int) {
	t.Helper()
	if _, err := p.pool.Exec(context.Background(),
		`UPDATE user_sessions SET last_seen_at = transaction_timestamp() - make_interval(mins => $1) WHERE sid = $2`,
		offsetMinutes, sid); err != nil {
		t.Fatalf("set last_seen_at for %s: %v", sid, err)
	}
}

// newPluginSessionStatePGStore 建立带真实 PostgreSQL 会话目录的 PostgresStore。
// 复用既有 identityPersistencePGFixture（独立 schema + goose 迁移）与
// p7 joined 测试安装的 user_sessions/user_credentials/password_reset_tokens 表。
func newPluginSessionStatePGStore(t *testing.T, fake *recordingPluginSessionInvalidator) *pluginSessionStatePGFixture {
	t.Helper()
	fixture := newIdentityPersistencePGFixture(t)
	installP7JoinedIdentitySchema(t, fixture)
	pool := fixture.newPool("plugin-session-state-erasure")
	store := NewPostgresStore(pool)
	if fake != nil {
		store = store.WithPluginSessionStateInvalidator(fake)
	}
	return &pluginSessionStatePGFixture{fixture: fixture, store: store, pool: pool}
}

func sortedSIDs(sids []string) []string {
	out := append([]string(nil), sids...)
	sort.Strings(out)
	return out
}

// TestPluginSessionStateErasureFollowsRevokeSession 证明单会话撤销擦除的正是刚结束的 sid。
func TestPluginSessionStateErasureFollowsRevokeSession(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{}
	p := newPluginSessionStatePGStore(t, fake)
	user := p.fixture.targetUserID
	p.createSession(t, user, "erasure-revoke-s1")
	p.createSession(t, user, "erasure-revoke-s2")

	if err := p.store.RevokeSession(context.Background(), user, "erasure-revoke-s1", "test"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got := fake.recorded(); len(got) != 1 || got[0] != "erasure-revoke-s1" {
		t.Fatalf("erased sids = %#v, want exactly [erasure-revoke-s1]", got)
	}
	revoked, err := p.store.IsSessionRevoked(context.Background(), user, "erasure-revoke-s1")
	if err != nil || !revoked {
		t.Fatalf("session should be revoked: revoked=%v err=%v", revoked, err)
	}
}

// TestPluginSessionStateErasureFollowsRevokeOtherSessions 证明「下线其他设备」
// 只擦除被下线的 sid，当前会话的插件状态保留。
func TestPluginSessionStateErasureFollowsRevokeOtherSessions(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{}
	p := newPluginSessionStatePGStore(t, fake)
	user := p.fixture.targetUserID
	p.createSession(t, user, "erasure-other-keep")
	p.createSession(t, user, "erasure-other-a")
	p.createSession(t, user, "erasure-other-b")

	count, err := p.store.RevokeOtherSessions(context.Background(), user, "erasure-other-keep", "test")
	if err != nil {
		t.Fatalf("revoke other: %v", err)
	}
	if count != 2 {
		t.Fatalf("revoked count = %d, want 2", count)
	}
	if got := sortedSIDs(fake.recorded()); strings.Join(got, ",") != "erasure-other-a,erasure-other-b" {
		t.Fatalf("erased sids = %#v, want exactly the two non-current sids", got)
	}
}

// TestPluginSessionStateErasureFollowsRevokeUserSessions 证明管理员清空全部会话时
// 每个被结束的 sid 都被擦除。
func TestPluginSessionStateErasureFollowsRevokeUserSessions(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{}
	p := newPluginSessionStatePGStore(t, fake)
	user := p.fixture.targetUserID
	p.createSession(t, user, "erasure-all-a")
	p.createSession(t, user, "erasure-all-b")
	p.createSession(t, user, "erasure-all-c")

	count, err := p.store.RevokeUserSessions(context.Background(), user, "admin_revoke")
	if err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if count != 3 {
		t.Fatalf("revoked count = %d, want 3", count)
	}
	want := strings.Join(sortedSIDs([]string{"erasure-all-a", "erasure-all-b", "erasure-all-c"}), ",")
	if got := strings.Join(sortedSIDs(fake.recorded()), ","); got != want {
		t.Fatalf("erased sids = %q, want %q", got, want)
	}
}

// TestPluginSessionStateErasureFollowsDeviceCapEviction 证明设备数驱逐只擦除被踢出的
// sid：当前会话与最新保留会话的插件状态不被动。
func TestPluginSessionStateErasureFollowsDeviceCapEviction(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{}
	p := newPluginSessionStatePGStore(t, fake)
	user := p.fixture.targetUserID
	// last_seen_at 显式错开，避免并列时间戳导致保留集合不确定。
	p.createSession(t, user, "erasure-cap-current")
	p.createSession(t, user, "erasure-cap-keep")
	p.createSession(t, user, "erasure-cap-evict-old")
	p.createSession(t, user, "erasure-cap-evict-older")
	p.setLastSeen(t, "erasure-cap-current", 0)
	p.setLastSeen(t, "erasure-cap-keep", 10)
	p.setLastSeen(t, "erasure-cap-evict-old", 20)
	p.setLastSeen(t, "erasure-cap-evict-older", 30)

	count, err := p.store.EnforceMaxSessions(context.Background(), user, "erasure-cap-current", 2)
	if err != nil {
		t.Fatalf("enforce max sessions: %v", err)
	}
	if count != 2 {
		t.Fatalf("evicted count = %d, want 2", count)
	}
	want := strings.Join(sortedSIDs([]string{"erasure-cap-evict-old", "erasure-cap-evict-older"}), ",")
	if got := strings.Join(sortedSIDs(fake.recorded()), ","); got != want {
		t.Fatalf("erased sids = %q, want exactly the evicted pair %q", got, want)
	}
}

// TestPluginSessionStateErasureFollowsPasswordReset 证明改密（确认重置）结束后
// 该用户全部活跃会话的插件状态被擦除。
func TestPluginSessionStateErasureFollowsPasswordReset(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{}
	p := newPluginSessionStatePGStore(t, fake)
	user := p.fixture.targetUserID
	p.createSession(t, user, "erasure-reset-a")
	p.createSession(t, user, "erasure-reset-b")

	if _, err := p.pool.Exec(context.Background(),
		`INSERT INTO user_credentials (user_id, password_hash) VALUES ($1, 'old-hash')`, user); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	tokenHash := "erasure-reset-token-" + p.fixture.schema
	if _, err := p.pool.Exec(context.Background(),
		`INSERT INTO password_reset_tokens (user_id, token_hash, expires_at)
		 VALUES ($1, $2, transaction_timestamp() + interval '1 hour')`, user, tokenHash); err != nil {
		t.Fatalf("seed reset token: %v", err)
	}

	userID, err := p.store.ConfirmPasswordResetAtomic(
		context.Background(), tokenHash, "new-hash", RevokeReasonPasswordReset)
	if err != nil {
		t.Fatalf("confirm password reset: %v", err)
	}
	if userID != user {
		t.Fatalf("user id = %d, want %d", userID, user)
	}
	if got := strings.Join(sortedSIDs(fake.recorded()), ","); got != "erasure-reset-a,erasure-reset-b" {
		t.Fatalf("erased sids = %q, want both reset sessions", got)
	}
}

// TestPluginSessionStateErasureIsIdempotentOnAlreadyRevokedSession 证明对「已下线但行存在」
// 的 sid 再撤销一次是幂等成功，且仍然补擦插件状态。
func TestPluginSessionStateErasureIsIdempotentOnAlreadyRevokedSession(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{}
	p := newPluginSessionStatePGStore(t, fake)
	user := p.fixture.targetUserID
	p.createSession(t, user, "erasure-idem-s1")

	if err := p.store.RevokeSession(context.Background(), user, "erasure-idem-s1", "test"); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	fake.reset()
	if err := p.store.RevokeSession(context.Background(), user, "erasure-idem-s1", "test"); err != nil {
		t.Fatalf("idempotent revoke should succeed, got %v", err)
	}
	if got := fake.recorded(); len(got) != 1 || got[0] != "erasure-idem-s1" {
		t.Fatalf("erased sids after re-revoke = %#v, want exactly [erasure-idem-s1]", got)
	}
}

// TestPluginSessionStateErasureSkipsMissingSession 证明撤销不存在的 sid 返回
// ErrSessionNotFound 且不触发擦除。
func TestPluginSessionStateErasureSkipsMissingSession(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{}
	p := newPluginSessionStatePGStore(t, fake)
	err := p.store.RevokeSession(context.Background(), p.fixture.targetUserID, "erasure-missing", "test")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
	if got := fake.recorded(); len(got) != 0 {
		t.Fatalf("invalidator should not be called, got %#v", got)
	}
}

// TestPluginSessionStateErasureFailureDoesNotFailRevoke 证明擦除失败（返回错误或 panic）
// 都不影响撤销本身——best-effort 语义。
func TestPluginSessionStateErasureFailureDoesNotFailRevoke(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{failWith: errors.New("redis down")}
	p := newPluginSessionStatePGStore(t, fake)
	user := p.fixture.targetUserID
	p.createSession(t, user, "erasure-fail-s1")

	if err := p.store.RevokeSession(context.Background(), user, "erasure-fail-s1", "test"); err != nil {
		t.Fatalf("revoke must not fail when erasure fails: %v", err)
	}
	revoked, err := p.store.IsSessionRevoked(context.Background(), user, "erasure-fail-s1")
	if err != nil || !revoked {
		t.Fatalf("session must still be revoked: revoked=%v err=%v", revoked, err)
	}

	// panic 路径同样被吞掉。
	panicker := &recordingPluginSessionInvalidator{panicNow: true}
	p2 := newPluginSessionStatePGStore(t, panicker)
	p2.createSession(t, p2.fixture.targetUserID, "erasure-panic-s1")
	if err := p2.store.RevokeSession(context.Background(), p2.fixture.targetUserID, "erasure-panic-s1", "test"); err != nil {
		t.Fatalf("revoke must not fail when erasure panics: %v", err)
	}
	revoked, err = p2.store.IsSessionRevoked(context.Background(), p2.fixture.targetUserID, "erasure-panic-s1")
	if err != nil || !revoked {
		t.Fatalf("session must still be revoked after panicking erasure: revoked=%v err=%v", revoked, err)
	}
}

// TestPluginSessionStateRevokesWorkWithoutInvalidator 证明未注入 invalidator 时
// 主操作照常（装配前/未接存储的部署不被拖挂）。
func TestPluginSessionStateRevokesWorkWithoutInvalidator(t *testing.T) {
	p := newPluginSessionStatePGStore(t, nil)
	user := p.fixture.targetUserID
	p.createSession(t, user, "erasure-plain-s1")

	if err := p.store.RevokeSession(context.Background(), user, "erasure-plain-s1", "test"); err != nil {
		t.Fatalf("revoke without invalidator: %v", err)
	}
	revoked, err := p.store.IsSessionRevoked(context.Background(), user, "erasure-plain-s1")
	if err != nil || !revoked {
		t.Fatalf("session must be revoked without invalidator: revoked=%v err=%v", revoked, err)
	}
}

// TestPluginSessionStateErasureDistinguishesUsers 证明擦除按 sid 精确到达：
// 其他用户的会话不受本用户撤销的影响（子会话隔离的目录侧表达）。
func TestPluginSessionStateErasureDistinguishesUsers(t *testing.T) {
	fake := &recordingPluginSessionInvalidator{}
	p := newPluginSessionStatePGStore(t, fake)
	p.createSession(t, p.fixture.targetUserID, "erasure-scope-target")
	p.createSession(t, p.fixture.otherUserID, "erasure-scope-other")

	if _, err := p.store.RevokeUserSessions(context.Background(), p.fixture.targetUserID, "test"); err != nil {
		t.Fatalf("revoke target user sessions: %v", err)
	}
	got := fake.recorded()
	if len(got) != 1 || got[0] != "erasure-scope-target" {
		t.Fatalf("erased sids = %#v, want only the target user's sid", got)
	}
}
