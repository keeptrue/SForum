package bootstrap

// 徽章授予通知的端到端装配测试（真实 PostgreSQL + 真实迁移链）。
//
// 为什么这条必须在本包：`badgesDomainGrantedNotifier` / `badgesJobGrantedNotifier`
// 与 `notificationTargetVisibilityAdapter` 都是 **bootstrap 私有的装配产物**。
// 在 badges 包里用 fake notifier 只能证明「有人被调用了」，证明不了
// 「通知行带着 badge 目标、且这个目标能被收件箱解析成自己的资料页」——
// 而那正是本轮缺陷（收件箱里徽章通知不可点）的真实位置。
//
// 覆盖三条路径中的两条装配边（手动授予 = 领域端口；自动评估 = job 端口），
// 批量/重算与它们共用同一个 notifier 实现与同一份领域逻辑（见
// app/Models/Badges/grant_notifications_test.go 的调用次数断言）。

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	badgesjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Badges"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// countingTxEnqueuer 记录邮件投递入队（这里只关心次数，不跑 worker）。
type countingTxEnqueuer struct{ calls int }

func (e *countingTxEnqueuer) EnqueueTx(context.Context, pgx.Tx, river.JobArgs, supportjobs.EnqueueOptions) (*rivertype.JobInsertResult, error) {
	e.calls++
	return nil, nil
}

type badgeGrantFixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *badges.PostgresStore
	admin  *badges.AdminService
	target notificationTargetVisibilityAdapter
	userID int64
	badge  int64
}

func newBadgeGrantFixture(t *testing.T) badgeGrantFixture {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	repairUsersIDSequence(t, ctx, pool)
	stamp := time.Now().UnixNano()
	username := fmt.Sprintf("badge_notify_%d", stamp)
	var userID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1,$1,$2,$2,$1,'active') RETURNING id`,
		username, username+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var badgeID int64
	// 手动徽章（rule 必须为 NULL，见 badges_rule_check）；tier 取迁移种子的 bronze。
	if err := pool.QueryRow(ctx, `
		INSERT INTO badges (slug, name, tier, icon, grant_mode, enabled, position)
		VALUES ($1,'通知测试徽章','bronze','i-lucide-award','manual',TRUE,0) RETURNING id`,
		fmt.Sprintf("notify-badge-%d", stamp)).Scan(&badgeID); err != nil {
		t.Fatalf("insert badge: %v", err)
	}

	store := badges.NewPostgresStore(pool)
	outbox := notifications.NewOutbox(pool, notifications.NewPostgresStore(pool), &countingTxEnqueuer{})
	admin := badges.NewAdminService(store)
	admin.SetGrantNotifier(badgesDomainGrantedNotifier{badgeGrantedNotifier{outbox: outbox}})
	t.Cleanup(func() {
		// 迁移链建在共享测试库上：删掉本夹具的行，避免影响同一轮的其它断言。
		_, _ = pool.Exec(context.Background(), `DELETE FROM notifications WHERE recipient_user_id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_badges WHERE user_id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM badges WHERE id=$1`, badgeID)
	})
	return badgeGrantFixture{
		ctx: ctx, pool: pool, store: store, admin: admin, userID: userID, badge: badgeID,
		// 徽章目标分支不读 forum/identities（它在 switch 之前返回），所以这两个依赖留 nil。
		target: notificationTargetVisibilityAdapter{badges: store},
	}
}

// badgeNotificationRow 是断言需要的那几个通知列（其余列与本轮无关）。
type badgeNotificationRow struct {
	targetType string
	badgeIDKey string
	badgeName  string
	targetID   int64
	actorID    int64
}

func (f badgeGrantFixture) notificationRows(t *testing.T, dedupePrefix string) []badgeNotificationRow {
	t.Helper()
	rows, err := f.pool.Query(f.ctx, `
		SELECT target_type, target_id, coalesce(actor_user_id, 0), payload->>'badgeId', payload->>'badgeName'
		FROM notifications WHERE recipient_user_id=$1 AND dedupe_key LIKE $2 ORDER BY id`,
		f.userID, dedupePrefix+"%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var items []badgeNotificationRow
	for rows.Next() {
		var item badgeNotificationRow
		if err := rows.Scan(&item.targetType, &item.targetID, &item.actorID, &item.badgeIDKey, &item.badgeName); err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return items
}

// TestManualBadgeGrantWritesClickableBadgeNotification 断言手动授予 →
// 领域端口 → 通知行（target=badge/ID）→ 目标解析出自己资料页徽章 tab。
func TestManualBadgeGrantWritesClickableBadgeNotification(t *testing.T) {
	fixture := newBadgeGrantFixture(t)
	slug, err := fixture.badgeSlug()
	if err != nil {
		t.Fatal(err)
	}
	username, err := fixture.username()
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{ID: fixture.userID, Permissions: map[string]bool{identity.PermissionBadgeManage: true}}
	if err := fixture.admin.Grant(fixture.ctx, actor, badges.GrantInput{
		UserID: fixture.userID, BadgeID: fixture.badge, GrantedBy: fixture.userID, GrantReason: "端到端",
	}); err != nil {
		t.Fatal(err)
	}

	rows := fixture.notificationRows(t, fmt.Sprintf("badge:%d:%s", fixture.userID, slug))
	if len(rows) != 1 {
		t.Fatalf("notifications = %d, want 1 (手动授予恰好一条)", len(rows))
	}
	row := rows[0]
	if row.targetType != "badge" || row.targetID != fixture.badge {
		t.Fatalf("target = %q/%d, want badge/%d", row.targetType, row.targetID, fixture.badge)
	}
	if row.badgeIDKey != fmt.Sprint(fixture.badge) {
		t.Fatalf("payload.badgeId = %q, want %d", row.badgeIDKey, fixture.badge)
	}
	if row.badgeName != "通知测试徽章" {
		t.Fatalf("payload.badgeName = %q", row.badgeName)
	}

	available, path, err := fixture.target.ResolveNotificationTarget(fixture.ctx, fixture.userID, "badge", fixture.badge)
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("持有徽章的收件人必须能解析出目标")
	}
	// 目标必须同时带 tab 与这枚徽章的 slug：只给 tab 会把用户丢进徽章墙自己找，
	// 而通知的语义是「你刚拿到这一枚」。
	parsed, parseErr := url.Parse(path)
	if parseErr != nil {
		t.Fatalf("target path %q is not a URL: %v", path, parseErr)
	}
	if want := "/u/" + url.PathEscape(username); parsed.Path != want {
		t.Fatalf("target path = %q, want %q", parsed.Path, want)
	}
	if got := parsed.Query().Get("tab"); got != "badges" {
		t.Fatalf("target tab = %q, want badges", got)
	}
	if got := parsed.Query().Get("badge"); got != slug {
		t.Fatalf("target badge = %q, want %q（深链必须指向这一枚徽章）", got, slug)
	}
}

// TestBadgeNotificationTargetFailsClosedAfterRevoke 断言收回后目标不可见（不报错）。
//
// fail-closed 的判据是 (false,"",nil)：返 error 会让整条收件箱 500，
// 返可用会让用户点进「我获得的徽章」却什么都没有。
func TestBadgeNotificationTargetFailsClosedAfterRevoke(t *testing.T) {
	fixture := newBadgeGrantFixture(t)
	actor := identity.Actor{ID: fixture.userID, Permissions: map[string]bool{identity.PermissionBadgeManage: true}}
	if err := fixture.admin.Grant(fixture.ctx, actor, badges.GrantInput{
		UserID: fixture.userID, BadgeID: fixture.badge, GrantedBy: fixture.userID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.admin.Revoke(fixture.ctx, actor, fixture.userID, fixture.badge); err != nil {
		t.Fatal(err)
	}
	available, path, err := fixture.target.ResolveNotificationTarget(fixture.ctx, fixture.userID, "badge", fixture.badge)
	if err != nil {
		t.Fatalf("目标解析不得报错（fail-closed），得到 %v", err)
	}
	if available || path != "" {
		t.Fatalf("收回后 target = %v/%q, want false/\"\"", available, path)
	}
}

// TestBadgeNotificationTargetFailsClosedWhenBadgeDisabled 断言停用徽章同样不可见。
func TestBadgeNotificationTargetFailsClosedWhenBadgeDisabled(t *testing.T) {
	fixture := newBadgeGrantFixture(t)
	actor := identity.Actor{ID: fixture.userID, Permissions: map[string]bool{identity.PermissionBadgeManage: true}}
	if err := fixture.admin.Grant(fixture.ctx, actor, badges.GrantInput{
		UserID: fixture.userID, BadgeID: fixture.badge, GrantedBy: fixture.userID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE badges SET enabled=FALSE WHERE id=$1`, fixture.badge); err != nil {
		t.Fatal(err)
	}
	available, _, err := fixture.target.ResolveNotificationTarget(fixture.ctx, fixture.userID, "badge", fixture.badge)
	if err != nil {
		t.Fatalf("目标解析不得报错（fail-closed），得到 %v", err)
	}
	if available {
		t.Fatal("停用徽章的通知目标必须不可见")
	}
}

// TestBadgeNotificationTargetFailsClosedWhenTierDisabled 断言等级停用同样不可见。
//
// 这一条与 `topBadgeOrderSQL` 的「停用档等于不存在」同源：资料页不会渲染那枚
// 徽章，链接进去就是空页。
func TestBadgeNotificationTargetFailsClosedWhenTierDisabled(t *testing.T) {
	fixture := newBadgeGrantFixture(t)
	actor := identity.Actor{ID: fixture.userID, Permissions: map[string]bool{identity.PermissionBadgeManage: true}}
	if err := fixture.admin.Grant(fixture.ctx, actor, badges.GrantInput{
		UserID: fixture.userID, BadgeID: fixture.badge, GrantedBy: fixture.userID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE badge_tiers SET enabled=FALSE WHERE key='bronze'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `UPDATE badge_tiers SET enabled=TRUE WHERE key='bronze'`)
	})
	available, _, err := fixture.target.ResolveNotificationTarget(fixture.ctx, fixture.userID, "badge", fixture.badge)
	if err != nil {
		t.Fatalf("目标解析不得报错（fail-closed），得到 %v", err)
	}
	if available {
		t.Fatal("等级停用后通知目标必须不可见")
	}
}

// TestBadgesJobNotifierForwardsBadgeID 断言自动评估（job 端口）也带徽章 id。
//
// 这条适配器与领域侧共用 `badgeGrantedNotifier.notify`，但转换层（jobs.GrantedBadge
// → 领域 GrantedBadge）是独立的一份代码，漏传 ID 的表现正是「自动授予的通知不可点」。
func TestBadgesJobNotifierForwardsBadgeID(t *testing.T) {
	fixture := newBadgeGrantFixture(t)
	slug, err := fixture.badgeSlug()
	if err != nil {
		t.Fatal(err)
	}
	outbox := notifications.NewOutbox(fixture.pool, notifications.NewPostgresStore(fixture.pool), &countingTxEnqueuer{})
	notifier := badgesJobGrantedNotifier{badgeGrantedNotifier{outbox: outbox}}
	if err := notifier.NotifyBadgeGranted(fixture.ctx, fixture.userID, []badgesjobs.GrantedBadge{
		{ID: fixture.badge, Slug: slug, Name: "通知测试徽章"},
	}); err != nil {
		t.Fatal(err)
	}
	rows := fixture.notificationRows(t, fmt.Sprintf("badge:%d:%s", fixture.userID, slug))
	if len(rows) != 1 {
		t.Fatalf("notifications = %d, want 1", len(rows))
	}
	if rows[0].targetType != "badge" || rows[0].targetID != fixture.badge {
		t.Fatalf("target = %q/%d, want badge/%d（job 适配器必须带 id）", rows[0].targetType, rows[0].targetID, fixture.badge)
	}
}

// TestBadgeNotificationTargetIgnoresForeignUser 断言别人的授予行不可见。
func TestBadgeNotificationTargetIgnoresForeignUser(t *testing.T) {
	fixture := newBadgeGrantFixture(t)
	actor := identity.Actor{ID: fixture.userID, Permissions: map[string]bool{identity.PermissionBadgeManage: true}}
	if err := fixture.admin.Grant(fixture.ctx, actor, badges.GrantInput{
		UserID: fixture.userID, BadgeID: fixture.badge, GrantedBy: fixture.userID,
	}); err != nil {
		t.Fatal(err)
	}
	available, _, err := fixture.target.ResolveNotificationTarget(fixture.ctx, fixture.userID+10_000, "badge", fixture.badge)
	if err != nil {
		t.Fatalf("目标解析不得报错（fail-closed），得到 %v", err)
	}
	if available {
		t.Fatal("未持有该徽章的用户不得拿到目标")
	}
}

func (f badgeGrantFixture) badgeSlug() (string, error) {
	var slug string
	err := f.pool.QueryRow(f.ctx, `SELECT slug FROM badges WHERE id=$1`, f.badge).Scan(&slug)
	return slug, err
}

func (f badgeGrantFixture) username() (string, error) {
	var name string
	err := f.pool.QueryRow(f.ctx, `SELECT username FROM users WHERE id=$1`, f.userID).Scan(&name)
	return name, err
}
