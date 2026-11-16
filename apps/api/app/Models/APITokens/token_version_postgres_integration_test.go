package apitokens

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// P2-2 的 Postgres 侧证据：PAT 必须随 token_version 一起失效。
//
// 覆盖四条路径：
//   - 管理员强制下线（Identity.RevokeUserSessions）；
//   - 密码重置确认（Identity.ConfirmPasswordResetAtomic）；
//   - 版本漂移（手工推进 users.current_token_version）；
//   - 对照组：没被触碰的 PAT 仍然可用。
//
// actor 投影用 fake（staticActorLoader）：本测试要证的是「令牌记录与 users 版本
// 的关系」，不是 Actor 装载；而两条 Identity 撤销路径、令牌存储、版本读取全部是
// 真实的 PostgresStore 代码。
func TestPATInvalidatedByTokenVersionAndRevokesPostgres(t *testing.T) {
	fixture := newTokenVersionPGFixture(t)
	ctx := fixture.ctx

	t.Run("admin revoke all sessions", func(t *testing.T) {
		userID := fixture.insertUser(t, "pat_admin_revoke")
		token := fixture.createToken(t, userID, "ci-admin-revoke")
		if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext); err != nil {
			t.Fatalf("precondition: fresh token must authenticate, got %v", err)
		}

		if _, err := fixture.identity.RevokeUserSessions(ctx, userID, "admin_revoke"); err != nil {
			t.Fatalf("revoke user sessions: %v", err)
		}
		if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext); !errors.Is(err, ErrTokenRevoked) {
			t.Fatalf("强制下线后 PAT 仍可用：err=%v", err)
		}
		// 记录本身也要显式撤销并写明原因（审计要能回答「为什么没了」）。
		rec, err := fixture.tokens.GetByPublicID(ctx, token.Token.PublicID)
		if err != nil {
			t.Fatalf("load token record: %v", err)
		}
		if rec.RevokedAt == nil || rec.RevokeReason != "admin_revoke" {
			t.Fatalf("token record revoked=%v reason=%q", rec.RevokedAt, rec.RevokeReason)
		}
	})

	t.Run("password reset confirm", func(t *testing.T) {
		userID := fixture.insertUser(t, "pat_password_reset")
		token := fixture.createToken(t, userID, "ci-password-reset")
		resetHash := fmt.Sprintf("reset-hash-%d", userID)
		if _, err := fixture.pool.Exec(ctx, `
			INSERT INTO password_reset_tokens (user_id, token_hash, expires_at)
			VALUES ($1, $2, now() + interval '1 hour')
		`, userID, resetHash); err != nil {
			t.Fatalf("seed password reset token: %v", err)
		}

		if _, err := fixture.identity.ConfirmPasswordResetAtomic(ctx, resetHash, "new-password-hash", identity.RevokeReasonPasswordReset); err != nil {
			t.Fatalf("confirm password reset: %v", err)
		}
		if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext); !errors.Is(err, ErrTokenRevoked) {
			t.Fatalf("改密后 PAT 仍可用：err=%v", err)
		}
		rec, err := fixture.tokens.GetByPublicID(ctx, token.Token.PublicID)
		if err != nil {
			t.Fatalf("load token record: %v", err)
		}
		if rec.RevokedAt == nil || rec.RevokeReason != identity.RevokeReasonPasswordReset {
			t.Fatalf("token record revoked=%v reason=%q", rec.RevokedAt, rec.RevokeReason)
		}
	})

	t.Run("version drift", func(t *testing.T) {
		userID := fixture.insertUser(t, "pat_version_drift")
		token := fixture.createToken(t, userID, "ci-version-drift")
		// 只推进版本、不撤销记录：模拟「版本是唯一闸门」的场景（例如旧版本二进制
		// 只递增版本、还没来得及撤 PAT 的滚动窗口）。
		if _, err := fixture.pool.Exec(ctx, `
			UPDATE users SET current_token_version = current_token_version + 1 WHERE id = $1
		`, userID); err != nil {
			t.Fatalf("advance token version: %v", err)
		}
		if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext); !errors.Is(err, ErrTokenRevoked) {
			t.Fatalf("版本漂移后 PAT 仍可用：err=%v", err)
		}
	})

	t.Run("untouched token keeps working", func(t *testing.T) {
		userID := fixture.insertUser(t, "pat_control")
		token := fixture.createToken(t, userID, "ci-control")
		authenticated, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext)
		if err != nil {
			t.Fatalf("control token must authenticate: %v", err)
		}
		if authenticated.UserID != userID {
			t.Fatalf("authenticated user=%d want %d", authenticated.UserID, userID)
		}
	})
}

// TestCreateWaitsForConcurrentTokenVersionAdvance 是 P2-2 补刀的核心断言：
// Create 的 `INSERT ... SELECT users.current_token_version` 必须带 FOR SHARE，
// 否则 READ COMMITTED 下它会读语句快照、不等并发「强制下线」的未提交递增，
// 把旧版本写进新 PAT —— 对方一提交，这条刚建好的令牌立刻失效。
//
// 变异要求：去掉 `FOR SHARE`，第一条断言（Create 必须被阻塞）必须红。
func TestCreateWaitsForConcurrentTokenVersionAdvance(t *testing.T) {
	fixture := newTokenVersionPGFixture(t)
	userID := fixture.insertUser(t, "pat_create_race")
	ctx := fixture.ctx
	actor := identity.Actor{
		ID: userID, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}

	// A：持有一个未提交的版本递增（等价于「强制下线」事务正在进行）。
	blocking, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocking tx: %v", err)
	}
	defer func() { _ = blocking.Rollback(context.Background()) }()
	if _, err := blocking.Exec(ctx, `
		UPDATE users SET current_token_version = current_token_version + 1 WHERE id = $1
	`, userID); err != nil {
		t.Fatalf("advance version in blocking tx: %v", err)
	}

	type createResult struct {
		created CreatedToken
		err     error
	}
	done := make(chan createResult, 1)
	go func() {
		created, err := fixture.service(userID).Create(context.Background(), actor, CreateInput{
			Name: "race-probe", Scopes: []string{identity.PermissionTopicCreate},
		})
		done <- createResult{created: created, err: err}
	}()

	select {
	case result := <-done:
		t.Fatalf("Create 没有等待行锁就返回了（读到旧版本）：err=%v publicID=%s", result.err, result.created.Token.PublicID)
	case <-time.After(500 * time.Millisecond):
		// 期望路径：被 FOR SHARE 挡住。
	}

	if err := blocking.Commit(ctx); err != nil {
		t.Fatalf("commit blocking tx: %v", err)
	}

	var result createResult
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("解锁后 Create 仍未返回")
	}
	if result.err != nil {
		t.Fatalf("create after unlock: %v", result.err)
	}
	// 写入的版本必须等于提交后的当前版本（=1），而不是旧快照的 0。
	rec, err := fixture.tokens.GetByPublicID(ctx, result.created.Token.PublicID)
	if err != nil {
		t.Fatalf("load created token: %v", err)
	}
	if rec.TokenVersion != 1 {
		t.Fatalf("created token_version=%d want 1（读到旧快照会让它立刻失效）", rec.TokenVersion)
	}
	// 立刻可用：版本与 users 一致。
	if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, result.created.Plaintext); err != nil {
		t.Fatalf("刚创建的 PAT 立刻不可用：%v", err)
	}
}

// TestRevokeAllForUserCountsOnlyActiveTokens 覆盖管理端「撤销全部 PAT」的计数口径：
// 已撤销的行不重复计数，别的用户的令牌不受影响。
func TestRevokeAllForUserCountsOnlyActiveTokens(t *testing.T) {
	fixture := newTokenVersionPGFixture(t)
	ctx := fixture.ctx
	target := fixture.insertUser(t, "pat_revoke_all_target")
	other := fixture.insertUser(t, "pat_revoke_all_other")

	first := fixture.createToken(t, target, "ci-revoke-all-1")
	_ = first
	fixture.createToken(t, target, "ci-revoke-all-2")
	fixture.createToken(t, other, "ci-other")

	revoked, err := fixture.tokens.RevokeAllForUser(ctx, target, RevokeReasonAdmin)
	if err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if revoked != 2 {
		t.Fatalf("revoked=%d want 2", revoked)
	}
	again, err := fixture.tokens.RevokeAllForUser(ctx, target, RevokeReasonAdmin)
	if err != nil {
		t.Fatalf("revoke all again: %v", err)
	}
	if again != 0 {
		t.Fatalf("second revoke=%d want 0（已撤销的行不重复计数）", again)
	}
	// 别的用户的令牌不受影响。
	if _, err := fixture.service(other).AuthenticatePlaintext(ctx, mustLastPlaintext(t, fixture, other)); err != nil {
		t.Fatalf("other user's token must survive: %v", err)
	}
}

func mustLastPlaintext(t *testing.T, fixture *tokenVersionPGFixture, userID int64) string {
	t.Helper()
	plaintext, ok := fixture.plaintext[userID]
	if !ok {
		t.Fatalf("no token plaintext recorded for user %d", userID)
	}
	return plaintext
}

type tokenVersionPGFixture struct {
	t        *testing.T
	ctx      context.Context
	admin    *pgxpool.Pool
	pool     *pgxpool.Pool
	tokens   *PostgresStore
	identity *identity.PostgresStore
	// plaintext 记录每个用户最后签发的明文，供对照组使用。
	plaintext map[int64]string
}

func newTokenVersionPGFixture(t *testing.T) *tokenVersionPGFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("apitokens_version_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, tokenVersionFixtureSchemaSQL); err != nil {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatalf("create fixture schema: %v", err)
	}
	fixture := &tokenVersionPGFixture{
		t: t, ctx: ctx, admin: admin, pool: pool,
		tokens:    NewPostgresStore(pool),
		identity:  identity.NewPostgresStore(pool),
		plaintext: map[int64]string{},
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
	})
	return fixture
}

func (f *tokenVersionPGFixture) insertUser(t *testing.T, username string) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, status) VALUES ($1, lower($1), 'active')
		RETURNING id
	`, username).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// service 构造一个 PAT 服务，actor 投影用 fake：本测试关心的是令牌记录与 users
// 版本的关系，不是 Actor 装载。
func (f *tokenVersionPGFixture) service(userID int64) *Service {
	return NewService(f.tokens, staticActorLoader{actor: identity.Actor{
		ID: userID, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}})
}

func (f *tokenVersionPGFixture) createToken(t *testing.T, userID int64, name string) CreatedToken {
	t.Helper()
	created, err := f.service(userID).Create(f.ctx, identity.Actor{
		ID: userID, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}, CreateInput{Name: name, Scopes: []string{identity.PermissionTopicCreate}})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	f.plaintext[userID] = created.Plaintext
	return created
}

const tokenVersionFixtureSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  username_lower TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'active',
  current_token_version BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_credentials (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  password_hash TEXT NOT NULL,
  method TEXT NOT NULL DEFAULT 'password',
  password_changed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_sessions (
  sid TEXT PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  revoked_at TIMESTAMPTZ,
  revoke_reason TEXT NOT NULL DEFAULT ''
);

CREATE TABLE password_reset_tokens (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL,
  consumed_at TIMESTAMPTZ,
  expires_at TIMESTAMPTZ NOT NULL
);

-- 镜像 database/migrations/202607120007_api_tokens.sql 与
-- 202611160001_api_tokens_token_version.sql。
CREATE TABLE api_tokens (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  public_id TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL,
  name TEXT NOT NULL,
  scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
  last_used_at TIMESTAMPTZ,
  expires_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  token_version BIGINT NOT NULL DEFAULT 0,
  revoke_reason TEXT NOT NULL DEFAULT ''
);
`
