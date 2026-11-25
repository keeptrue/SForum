package identity

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestReplaceRolePermissionsWaitsForRoleRowLock 钉住替换与插件权限审批共享的串行化根。
// 只要角色行被占着，全量替换就必须排队；否则两条写路径会撞 role_permissions 主键，
// 管理端在并发审批期间改权限会拿到 23505。
func TestReplaceRolePermissionsWaitsForRoleRowLock(t *testing.T) {
	t.Parallel()
	fixture := newProductionIdentityApprovalFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 15*time.Second)
	defer cancel()

	holder, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(ctx, `
		SELECT id FROM roles WHERE key = $1 FOR NO KEY UPDATE
	`, RoleMember); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- fixture.roles.ReplaceRolePermissions(ctx, fixture.actorUserID, RoleMember, nil)
	}()

	<-started
	select {
	case err := <-done:
		t.Fatalf("replacement finished while the role row lock was held: %v", err)
	case <-time.After(500 * time.Millisecond):
	}

	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release role row lock: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("replacement after the lock was released: %v", err)
	}
	if keys := memberPermissionKeys(t, ctx, fixture.pool); len(keys) != 0 {
		t.Fatalf("member permission set=%v want empty after a full-set replacement", keys)
	}
}

func memberPermissionKeys(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT role_permissions.permission_key
		FROM role_permissions
		JOIN roles ON roles.id = role_permissions.role_id
		WHERE roles.key = $1
		ORDER BY role_permissions.permission_key
	`, RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keys := make([]string, 0, 4)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}
