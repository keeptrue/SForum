package conditions_test

import (
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 这组测试是 `identity.account_age` 日界来源的常驻回归。
//
// 背景（2026-11-13 实测）：旧实现是
//
//	SELECT GREATEST($2::date - users.created_at::date, 0) ...
//
// 左操作数是 Go 的进程本地日，右操作数是 created_at 在 **PG 会话时区**里的日历日。
// 两个日界不同源，于是会话时区 ≠ 进程时区时，凡是注册本地时间落在偏移窗口内的用户
// 会**永久**多算 1 天（不是「凌晨几小时才有偏差」——偏差固定在那个用户身上）。
//
// 固定场景：进程侧「今天」= Asia/Shanghai 2026-11-13 04:25，正好落在本机
// 00:00–08:00（UTC 仍是 11-12）的必红窗口；用户真实注册瞬间 = CST 2026-11-03 04:25，
// 按 CST 日历恰好 10 天前，但它的 UTC 日历日是 2026-11-02 —— 旧实现给出 11。
var (
	accountAgeProcessZone = time.FixedZone("Asia/Shanghai", 8*3600)
	accountAgeNow         = time.Date(2026, 11, 13, 4, 25, 0, 0, accountAgeProcessZone)
	accountAgeCreated     = time.Date(2026, 11, 3, 4, 25, 0, 0, accountAgeProcessZone)
)

func requireSessionTimeZone(t *testing.T, f *fixture, want string) {
	t.Helper()
	var got string
	if err := f.pool.QueryRow(f.ctx, `SHOW TimeZone`).Scan(&got); err != nil {
		t.Fatalf("读取会话时区失败：%v", err)
	}
	if got != want {
		// 测试集群另有时区配置时，这条用例就失去意义了——直接红，不静默放行。
		t.Fatalf("会话时区应为 %q，实际 %q（夹具未能钉死会话时区）", want, got)
	}
}

func insertUserCreatedAt(t *testing.T, f *fixture, username string, createdAt time.Time) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx,
		`INSERT INTO users (username, created_at) VALUES ($1, $2) RETURNING id`,
		username, createdAt).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func resolveAccountAge(t *testing.T, f *fixture, userID int64, now time.Time) int64 {
	t.Helper()
	resolver := f.resolver(t, userID).WithClock(func() time.Time { return now })
	value, err := resolver.Resolve(f.ctx, ruleengine.Leaf{Type: conditions.KeyIdentityAccountAge})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return value.Count
}

// TestAccountAgeCountsProcessLocalCalendarDays 断言账户年龄 = 进程本地日历日差。
//
// 三个子用例覆盖「会话时区在 UTC 两侧偏移 + 与进程一致」：
//   - UTC：旧实现 11 / 正确实现 10（本机必红配置）；
//   - America/New_York：偏移方向相反，旧实现同样 11；
//   - Asia/Shanghai：会话与进程同时区，旧实现也是 10 —— 这一条是**对照**，
//     证明修复不是「换个地方错」而是只在两个日界不同源时才生效。
func TestAccountAgeCountsProcessLocalCalendarDays(t *testing.T) {
	cases := []struct {
		name            string
		sessionTimeZone string
	}{
		{"会话 UTC（旧实现给 11）", "UTC"},
		{"会话 America/New_York（偏移方向相反，旧实现同样给 11）", "America/New_York"},
		{"会话与进程同为 Asia/Shanghai（对照：一致配置结果不变）", "Asia/Shanghai"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureWithSessionTimeZone(t, tc.sessionTimeZone)
			requireSessionTimeZone(t, f, tc.sessionTimeZone)
			userID := insertUserCreatedAt(t, f, "age-boundary", accountAgeCreated)
			// 按 CST 日历：2026-11-13 − 2026-11-03 = 10 天。
			if age := resolveAccountAge(t, f, userID, accountAgeNow); age != 10 {
				t.Fatalf("账户年龄应为 10 个进程本地日历日，得到 %d（旧实现把会话时区的日历日 %s 拿来相减，会给 11）",
					age, accountAgeCreated.UTC().Format(time.DateOnly))
			}
		})
	}
}

// TestAccountAgeNeverNegative 保留 `GREATEST(...,0)` 的语义：时钟回拨或 created_at
// 在「今天」之后时年龄必须夹到 0，否则 `lte` 条件会把负年龄判成满足。
func TestAccountAgeNeverNegative(t *testing.T) {
	f := newFixtureWithSessionTimeZone(t, "UTC")
	future := accountAgeNow.Add(48 * time.Hour)
	userID := insertUserCreatedAt(t, f, "age-future", future)
	if age := resolveAccountAge(t, f, userID, accountAgeNow); age != 0 {
		t.Fatalf("未来创建的账号年龄应夹到 0，得到 %d", age)
	}
}
