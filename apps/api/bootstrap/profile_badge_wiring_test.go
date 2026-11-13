package bootstrap

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// TestProfileTopBadgesAdapterReadsWornBadgeAndSkipsDisabledTier 是公开资料页
// 展示徽章的端到端数据源断言：适配器必须读 users.top_badge_id（佩戴优先，
// 否则排序第一的那一枚），并与 chip 同口径——等级停用后什么都不返回。
//
// 为什么要真库：适配器本身只是字段翻译，但它的正确性完全依赖
// badges.PostgresStore.TopBadgesFor 的 SQL（top_badge_id + badges.enabled +
// 等级启用）。fake 证明不了「等级停用被 JOIN 过滤」这件事。
//
// 造数用唯一的等级 key 与徽章 slug：测试库的 public schema 在包内共享，
// 写死 key 会与迁移种子（gold/silver/bronze）或后续用例互相干扰。
func TestProfileTopBadgesAdapterReadsWornBadgeAndSkipsDisabledTier(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("连接测试库失败：%v", err)
	}
	defer pool.Close()

	// 与 profile_activity_wiring_test.go 同一手法：共享测试库里 users_id_seq
	// 可能落后于 max(id)（有夹具显式插入 id），先对齐再 nextval。
	if _, err := pool.Exec(ctx, `
		SELECT setval(pg_get_serial_sequence('users', 'id'),
		              GREATEST((SELECT COALESCE(max(id), 0) FROM users), 1))`); err != nil {
		t.Fatalf("对齐 users 序列失败：%v", err)
	}

	suffix := time.Now().UnixNano()
	tierKey := fmt.Sprintf("pt%d", suffix)
	badgeSlug := fmt.Sprintf("profile-top-badge-%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO badge_tiers (key, name_zh, name_en, icon, color, position, enabled)
		VALUES ($1, '资料页档', 'Profile Tier', 'i-lucide-medal', 'teal', 0, TRUE)
	`, tierKey); err != nil {
		t.Fatalf("造等级失败：%v", err)
	}
	var badgeID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, enabled, position)
		VALUES ($1, '资料页徽章', $2, 'manual', TRUE, 0)
		RETURNING id
	`, badgeSlug, tierKey).Scan(&badgeID); err != nil {
		t.Fatalf("造徽章失败：%v", err)
	}
	username := fmt.Sprintf("profile_badge_%d", suffix)
	var userID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, top_badge_id)
		VALUES ($1, $1, $2, $2, 'Profile Badge', $3)
		RETURNING id
	`, username, username+"@example.test", badgeID).Scan(&userID); err != nil {
		t.Fatalf("造用户失败：%v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_badges (user_id, badge_id, granted_by)
		VALUES ($1, $2, $1)
	`, userID, badgeID); err != nil {
		t.Fatalf("造授予记录失败：%v", err)
	}

	adapter := profileTopBadges{store: badges.NewPostgresStore(pool)}
	refs, err := adapter.TopBadgesFor(ctx, []int64{userID})
	if err != nil {
		t.Fatalf("适配器读失败：%v", err)
	}
	ref, ok := refs[userID]
	if !ok {
		t.Fatalf("展示位没读出来：%+v", refs)
	}
	if ref.Slug != badgeSlug || ref.Tier != tierKey {
		t.Fatalf("读到 %+v，期望 slug=%s tier=%s", ref, badgeSlug, tierKey)
	}
	// 双语名与识别色必须一起出来：前端 chip 直接用它们，缺失等于「无颜色/无名字」。
	if ref.TierName.Zh != "资料页档" || ref.TierName.En != "Profile Tier" || ref.TierColor != "teal" {
		t.Fatalf("等级外观没映射过去：%+v", ref)
	}

	// 停用等级、不跑重算：users.top_badge_id 仍指向这枚徽章，展示位必须自己判不可展示。
	if _, err := pool.Exec(ctx, `UPDATE badge_tiers SET enabled = FALSE WHERE key = $1`, tierKey); err != nil {
		t.Fatalf("停用等级失败：%v", err)
	}
	refs, err = adapter.TopBadgesFor(ctx, []int64{userID})
	if err != nil {
		t.Fatalf("停用等级后适配器读失败：%v", err)
	}
	if ref, ok := refs[userID]; ok {
		t.Fatalf("等级停用后不应返回徽章，got %+v", ref)
	}
}
