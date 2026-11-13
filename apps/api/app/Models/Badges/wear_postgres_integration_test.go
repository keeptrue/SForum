package badges

import (
	"context"
	"errors"
	"testing"
)

// 自由佩戴（M3）的真库集成测试。
//
// 单测的 fakeStore 复刻不了这一刀真正的风险：CTE 的「校验 + 写入是同一个快照」、
// ROW_NUMBER 给出的名次、以及 RecomputeTopBadge / RecomputeAll 对 manual 的让路
// 与失效回落。这些全部只写在 SQL 里，只有真 Postgres 能回答。
//
// 另外三条「重算不许覆盖用户选择」的断言必须成对读：只看 top_badge_id 会漏掉
// manual 标记没清（下次又拦住重算），只看标记会漏掉展示位没回落（chip 消失）。

// newWearPGFixture = 等级 fixture（同一份真实 DDL + 已种档位）。
//
// 复用而不是复制 DDL：佩戴语义相对等级表的 schema 差异只有 users.top_badge_manual
// 这一列，而那一列已经在等级 fixture 的 DDL 里（202611130003 之后它就是 users 的
// 一部分）。复制一份 DDL 只会让「迁移改了、fixture 没改」多一个藏身处。
func newWearPGFixture(t *testing.T) *badgeTiersPGFixture {
	t.Helper()
	return newBadgeTiersPGFixture(t)
}

// grantWearBadge 走真实写入路径授予一枚徽章。
func grantWearBadge(t *testing.T, store *PostgresStore, userID int64, badgeID int64) {
	t.Helper()
	if _, err := store.GrantManual(context.Background(), GrantInput{
		UserID: userID, BadgeID: badgeID, GrantedBy: userID,
	}); err != nil {
		t.Fatalf("grant badge %d: %v", badgeID, err)
	}
}

// wornState 直接读展示位的原始两列。
//
// 刻意不用 DisplayBadge 断言：它是投影（会把「manual 指向无效徽章」按 auto 呈现），
// 而这里要钉的是库里的真实状态——标记没清就是没清。
func wornState(t *testing.T, store *PostgresStore, userID int64) (*int64, bool) {
	t.Helper()
	var top *int64
	var manual bool
	if err := store.pool.QueryRow(context.Background(),
		`SELECT top_badge_id, top_badge_manual FROM users WHERE id = $1`, userID).Scan(&top, &manual); err != nil {
		t.Fatalf("read worn state: %v", err)
	}
	return top, manual
}

// hiddenState 直接读 hidden 标记（与 wornState 同一理由：钉住库里的真实状态）。
func hiddenState(t *testing.T, store *PostgresStore, userID int64) bool {
	t.Helper()
	var hidden bool
	if err := store.pool.QueryRow(context.Background(),
		`SELECT top_badge_hidden FROM users WHERE id = $1`, userID).Scan(&hidden); err != nil {
		t.Fatalf("read hidden state: %v", err)
	}
	return hidden
}

func mustDisplay(t *testing.T, store *PostgresStore, userID int64) BadgeDisplay {
	t.Helper()
	display, err := store.DisplayBadge(context.Background(), userID)
	if err != nil {
		t.Fatalf("display badge: %v", err)
	}
	return display
}

// wearFixture 建一个「gold 比 silver 更好，两枚都已授予」的起点。
//
// 返回的 top 起点是自动推导的 gold：所有「佩戴没生效」的断言都能靠它对照，
// 而不是只看 manual 标记——标记写了但展示位没动正是这一刀最容易出的假绿。
func wearFixture(t *testing.T) (fixture *badgeTiersPGFixture, store *PostgresStore, userID, goldID, silverID int64) {
	t.Helper()
	fixture = newWearPGFixture(t)
	store = NewPostgresStore(fixture.pool)
	userID = fixture.user(t, "alice")
	goldID = fixture.manualBadge(t, "gold-badge", "gold", 0)
	silverID = fixture.manualBadge(t, "silver-badge", "silver", 0)
	grantWearBadge(t, store, userID, goldID)
	grantWearBadge(t, store, userID, silverID)
	if err := store.RecomputeTopBadge(fixture.ctx, userID); err != nil {
		t.Fatalf("seed recompute: %v", err)
	}
	top, manual := wornState(t, store, userID)
	if top == nil || *top != goldID || manual {
		t.Fatalf("seed state = (top=%v, manual=%v), want (gold, false)", top, manual)
	}
	return fixture, store, userID, goldID, silverID
}

// TestSetWornBadgeIsKeptByBothRecomputePaths 是这一刀的核心契约：
// 佩戴写入 → chip 读到的就是它 → 两条重算都不许覆盖仍有效的用户选择。
func TestSetWornBadgeIsKeptByBothRecomputePaths(t *testing.T) {
	fixture, store, userID, _, silverID := wearFixture(t)
	ctx := fixture.ctx

	if err := store.SetWornBadge(ctx, userID, "silver-badge"); err != nil {
		t.Fatalf("set worn badge: %v", err)
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != silverID || !manual {
		t.Fatalf("after wear = (top=%v, manual=%v), want (silver, true)", top, manual)
	}

	// 要求 1：展示位写入后，同一列（users.top_badge_id）的批量读口必须看到佩戴结果。
	// chip 本身由 Forum 的读 SQL 直接投影这一列（TopBadgesFor 已无生产调用方），
	// 这里借它断言「批量读口与 chip 读到的是同一枚」，而不是「chip 走 TopBadgesFor」。
	refs, err := store.TopBadgesFor(ctx, []int64{userID})
	if err != nil {
		t.Fatalf("top badges: %v", err)
	}
	if refs[userID].Slug != "silver-badge" {
		t.Fatalf("TopBadgesFor = %q, want silver-badge（佩戴必须赢过排序）", refs[userID].Slug)
	}

	// 要求 2：单用户重算不许覆盖仍有效的佩戴。
	if err := store.RecomputeTopBadge(ctx, userID); err != nil {
		t.Fatalf("recompute one: %v", err)
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != silverID || !manual {
		t.Fatalf("RecomputeTopBadge 覆盖了佩戴：(top=%v, manual=%v)", top, manual)
	}

	// 要求 2（续）：全量重算走的是另一段 SQL（manual_valid 分支），必须单独钉。
	if _, err := store.RecomputeAll(ctx); err != nil {
		t.Fatalf("recompute all: %v", err)
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != silverID || !manual {
		t.Fatalf("RecomputeAll 覆盖了佩戴：(top=%v, manual=%v)", top, manual)
	}

	display := mustDisplay(t, store, userID)
	if display.Mode != BadgeDisplayModeManual {
		t.Fatalf("mode = %q, want manual", display.Mode)
	}
	if display.Worn == nil || display.Worn.Slug != "silver-badge" {
		t.Fatalf("worn = %+v, want silver-badge", display.Worn)
	}
	if display.Automatic == nil || display.Automatic.Slug != "gold-badge" {
		t.Fatalf("automatic = %+v, want gold-badge", display.Automatic)
	}
	// 要求 3：存在更优徽章时 upgrade 非空，且带 slug+name（前端直接渲染「换成 X」）。
	if display.Upgrade == nil || display.Upgrade.Slug != "gold-badge" || display.Upgrade.Name == "" {
		t.Fatalf("upgrade = %+v, want gold-badge with name", display.Upgrade)
	}
}

// TestClearWornBadgeFallsBackToAutomatic 断言取消佩戴后标记清掉、展示位立刻回落。
//
// 顺序敏感：先清标记再重算。反过来 RecomputeTopBadge 会看到 manual=TRUE 而拒绝
// 覆盖，症状是「点了取消佩戴，chip 还在」——标记改了、展示位没动。
func TestClearWornBadgeFallsBackToAutomatic(t *testing.T) {
	fixture, store, userID, goldID, _ := wearFixture(t)
	ctx := fixture.ctx

	if err := store.SetWornBadge(ctx, userID, "silver-badge"); err != nil {
		t.Fatalf("set worn badge: %v", err)
	}
	if err := store.ClearWornBadge(ctx, userID); err != nil {
		t.Fatalf("clear worn badge: %v", err)
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != goldID || manual {
		t.Fatalf("after clear = (top=%v, manual=%v), want (gold, false)", top, manual)
	}
	display := mustDisplay(t, store, userID)
	if display.Mode != BadgeDisplayModeAuto {
		t.Fatalf("mode = %q, want auto", display.Mode)
	}
	if display.Worn == nil || display.Worn.Slug != "gold-badge" {
		t.Fatalf("worn = %+v, want 自动推导的 gold-badge", display.Worn)
	}
	if display.Upgrade != nil {
		t.Fatalf("auto 模式下 upgrade 必须为空，got %+v", display.Upgrade)
	}
}

// TestRecomputeFallsBackWhenWornBadgeStopsBeingValid 覆盖四条失效路径。
//
// 每条都必须同时满足两件事：展示位回落到自动推导的 gold，**且** manual 标记变 FALSE。
// 只清一半就是这一刀最典型的半坏状态——标记留着会拦住下一次重算，
// 展示位留着会让 chip 显示一枚排序里根本不存在的徽章。
func TestRecomputeFallsBackWhenWornBadgeStopsBeingValid(t *testing.T) {
	cases := []struct {
		name       string
		invalidate func(t *testing.T, fixture *badgeTiersPGFixture, userID, silverID int64)
	}{
		{
			name: "badge disabled",
			invalidate: func(t *testing.T, fixture *badgeTiersPGFixture, _, silverID int64) {
				t.Helper()
				if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE badges SET enabled = FALSE WHERE id = $1`, silverID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "grant revoked",
			invalidate: func(t *testing.T, fixture *badgeTiersPGFixture, userID, silverID int64) {
				t.Helper()
				if _, err := fixture.pool.Exec(fixture.ctx,
					`DELETE FROM user_badges WHERE user_id = $1 AND badge_id = $2`, userID, silverID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "badge deleted",
			invalidate: func(t *testing.T, fixture *badgeTiersPGFixture, _, silverID int64) {
				t.Helper()
				if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM badges WHERE id = $1`, silverID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "tier disabled",
			invalidate: func(t *testing.T, fixture *badgeTiersPGFixture, _, _ int64) {
				t.Helper()
				if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE badge_tiers SET enabled = FALSE WHERE key = 'silver'`); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, store, userID, goldID, silverID := wearFixture(t)
			if err := store.SetWornBadge(fixture.ctx, userID, "silver-badge"); err != nil {
				t.Fatalf("set worn badge: %v", err)
			}
			tc.invalidate(t, fixture, userID, silverID)

			if err := store.RecomputeTopBadge(fixture.ctx, userID); err != nil {
				t.Fatalf("recompute: %v", err)
			}
			top, manual := wornState(t, store, userID)
			if manual {
				t.Fatal("manual 标记必须变 FALSE：否则下一次重算又会被它拦住")
			}
			if top == nil || *top != goldID {
				t.Fatalf("top_badge_id = %v, want 回落为 gold（%d）", top, goldID)
			}
			display := mustDisplay(t, store, userID)
			if display.Mode != BadgeDisplayModeAuto {
				t.Fatalf("mode = %q, want auto", display.Mode)
			}
			if display.Worn == nil || display.Worn.Slug != "gold-badge" {
				t.Fatalf("worn = %+v, want gold-badge", display.Worn)
			}
			if display.Upgrade != nil {
				t.Fatalf("回落后的 auto 模式不该再有 upgrade，got %+v", display.Upgrade)
			}
		})
	}
}

// TestRecomputeAllClearsInvalidWornBadge 单独钉全量重算的失效分支。
//
// RecomputeTopBadge 与 RecomputeAll 的 manual 判断各写了一遍（单用户 CASE 与
// 子查询的 manual_valid），单用户那条绿了推不出全量那条也绿。
func TestRecomputeAllClearsInvalidWornBadge(t *testing.T) {
	fixture, store, userID, goldID, silverID := wearFixture(t)
	ctx := fixture.ctx

	if err := store.SetWornBadge(ctx, userID, "silver-badge"); err != nil {
		t.Fatalf("set worn badge: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE badges SET enabled = FALSE WHERE id = $1`, silverID); err != nil {
		t.Fatal(err)
	}

	refreshed, err := store.RecomputeAll(ctx)
	if err != nil {
		t.Fatalf("recompute all: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("refreshed = %d, want 1（佩戴失效必须计入 top_badge_id 变化）", refreshed)
	}
	top, manual := wornState(t, store, userID)
	if manual {
		t.Fatal("RecomputeAll 必须把失效佩戴的 manual 标记清成 FALSE")
	}
	if top == nil || *top != goldID {
		t.Fatalf("top_badge_id = %v, want gold（%d）", top, goldID)
	}

	// 幂等：再跑一次没有任何行需要改。
	again, err := store.RecomputeAll(ctx)
	if err != nil {
		t.Fatalf("second recompute all: %v", err)
	}
	if again != 0 {
		t.Fatalf("second refreshed = %d, want 0", again)
	}
}

// TestSetWornBadgeRejectsWhatTheUserDoesNotOwn 断言拒绝路径不写库。
//
// 四条输入折成同一个 ErrBadgeNotOwned 是刻意的（不区分「没获得」与「不存在」，
// 否则接口变成一枚徽章存在性探针）；这里同时钉住「拒绝时展示位不动」——
// 失败却改了 top_badge_id 会让用户看到「没成功但 chip 变了」。
func TestSetWornBadgeRejectsWhatTheUserDoesNotOwn(t *testing.T) {
	fixture, store, userID, goldID, silverID := wearFixture(t)
	ctx := fixture.ctx

	// 存在但从未授予的徽章：与「不存在」必须走同一个码。
	fixture.manualBadge(t, "unowned-badge", "gold", 0)
	// 已授予但已停用的徽章：仍必须被拒绝（enabled 参与 CTE 的校验）。
	disabledID := fixture.manualBadge(t, "disabled-badge", "bronze", 0)
	grantWearBadge(t, store, userID, disabledID)
	if _, err := fixture.pool.Exec(ctx, `UPDATE badges SET enabled = FALSE WHERE id = $1`, disabledID); err != nil {
		t.Fatal(err)
	}
	// 已授予但等级停用的徽章：与排序的 JOIN 同口径，也必须被拒绝。
	orphanTierID := fixture.manualBadge(t, "orphan-tier-badge", "bronze", 0)
	grantWearBadge(t, store, userID, orphanTierID)
	if _, err := fixture.pool.Exec(ctx, `UPDATE badge_tiers SET enabled = FALSE WHERE key = 'bronze'`); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		slug string
	}{
		{"unknown slug", "no-such-badge"},
		{"not granted", "unowned-badge"}, // 站内存在，但没授予过
		{"badge disabled", "disabled-badge"},
		{"tier disabled", "orphan-tier-badge"},
		{"empty slug", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.SetWornBadge(ctx, userID, tc.slug)
			if !errors.Is(err, ErrBadgeNotOwned) {
				t.Fatalf("err = %v, want ErrBadgeNotOwned", err)
			}
			if top, manual := wornState(t, store, userID); top == nil || *top != goldID || manual {
				t.Fatalf("被拒绝的佩戴改了展示位：(top=%v, manual=%v)", top, manual)
			}
		})
	}

	// 反证：把 silver 授予该用户后，同一个 slug 必须成功——否则上面的拒绝可能
	// 只是「这条 SQL 永远不写」，而不是「校验正确」。
	if err := store.SetWornBadge(ctx, userID, "silver-badge"); err != nil {
		t.Fatalf("granted badge must be wearable: %v", err)
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != silverID || !manual {
		t.Fatalf("after wear = (top=%v, manual=%v), want (silver, true)", top, manual)
	}
}

// TestDisplayBadgeAutoModeHasNoUpgrade 断言自动模式的 upgrade 恒为空。
//
// 「auto 还给 upgrade」会让前端永远显示一个「升级」按钮，点下去佩戴的正是
// 当前这枚——用户看不出任何变化，只会以为按钮坏了。
func TestDisplayBadgeAutoModeHasNoUpgrade(t *testing.T) {
	fixture, store, userID, _, _ := wearFixture(t)

	display := mustDisplay(t, store, userID)
	if display.Mode != BadgeDisplayModeAuto {
		t.Fatalf("mode = %q, want auto", display.Mode)
	}
	if display.Worn == nil || display.Automatic == nil || display.Worn.Slug != display.Automatic.Slug {
		t.Fatalf("auto 模式下 worn 必须等于 automatic：worn=%+v automatic=%+v", display.Worn, display.Automatic)
	}
	if display.Upgrade != nil {
		t.Fatalf("upgrade = %+v, want nil", display.Upgrade)
	}

	// 没有任何可展示徽章：三个字段都是 nil，而不是零值 BadgeRef。
	emptyUser := fixture.user(t, "bob")
	empty := mustDisplay(t, store, emptyUser)
	if empty.Mode != BadgeDisplayModeAuto || empty.Worn != nil || empty.Automatic != nil || empty.Upgrade != nil {
		t.Fatalf("empty wall display = %+v, want auto with all nil", empty)
	}
}

// TestDisplayBadgeSkipsBadgesThatCannotBeShown 断言停用/删除的徽章不会变成空壳 BadgeRef。
//
// 授予行在徽章停用后仍然存在（停用不是收回），所以「这行有没有徽章」必须看 JOIN
// 后的 badges.id，而不是授予行的 badge_id。看错了的症状很隐蔽：automatic 变成一枚
// slug 为空串的 BadgeRef，chip 渲染出一个没有名字的空徽章，而接口是 200。
//
// 这里刻意在重算**之前**读：重算会把 manual 清掉，而读路径必须在那一刻之前也
// 给出正确形状（否则资料页会在重算跑完前闪一个空徽章）。
func TestDisplayBadgeSkipsBadgesThatCannotBeShown(t *testing.T) {
	fixture, store, userID, _, silverID := wearFixture(t)
	ctx := fixture.ctx

	// 收回 gold、停用 silver，并把展示位直接写成 silver + manual（模拟停用后、
	// 下一次重算之前的真实脏数据）。
	if _, err := fixture.pool.Exec(ctx,
		`DELETE FROM user_badges WHERE user_id = $1 AND badge_id <> $2`, userID, silverID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE badges SET enabled = FALSE WHERE id = $1`, silverID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE users SET top_badge_id = $2, top_badge_manual = TRUE WHERE id = $1`, userID, silverID); err != nil {
		t.Fatal(err)
	}

	display := mustDisplay(t, store, userID)
	if display.Mode != BadgeDisplayModeAuto {
		t.Fatalf("mode = %q, want auto（停用徽章不得报 manual）", display.Mode)
	}
	if display.Worn != nil || display.Automatic != nil || display.Upgrade != nil {
		t.Fatalf("停用徽章不得变成空壳 BadgeRef：worn=%+v automatic=%+v upgrade=%+v",
			display.Worn, display.Automatic, display.Upgrade)
	}
}

// TestDisplayBadgeIgnoresBadgesWhoseTierIsDisabled 钉住「候选集与 chip / 重算同口径」。
//
// 事实：`t.enabled` 写在 LEFT JOIN 的 ON 里只让 t.* 变 NULL，**不过滤行**；判别列
// 若只看 badges.id，等级停用的徽章仍然是候选。症状是面板指向一枚「chip 不显示
// （top_badge_id 已被重算清掉）、SetWornBadge 直接 422、通知目标判不可见」的徽章，
// 而接口是 200——用户看到一个自己戴不上、也看不到的徽章。
//
// 两个方向都要钉：还有别的有效徽章时 automatic 必须落到那一枚（不是「碰巧 nil」），
// 只剩这一枚时才是 nil。只看后者的测试会让「候选集为空」和「判别列写对」无法区分。
func TestDisplayBadgeIgnoresBadgesWhoseTierIsDisabled(t *testing.T) {
	t.Run("falls back to the best remaining badge", func(t *testing.T) {
		fixture, store, userID, _, _ := wearFixture(t)
		if _, err := fixture.pool.Exec(fixture.ctx,
			`UPDATE badge_tiers SET enabled = FALSE WHERE key = 'gold'`); err != nil {
			t.Fatal(err)
		}

		display := mustDisplay(t, store, userID)
		if display.Mode != BadgeDisplayModeAuto {
			t.Fatalf("mode = %q, want auto", display.Mode)
		}
		if display.Automatic == nil || display.Automatic.Slug != "silver-badge" {
			t.Fatalf("automatic = %+v, want silver-badge（gold 档已停用，不得成为候选）", display.Automatic)
		}
		// top_badge_id 此刻仍指向 gold（脏数据，重算还没跑）：读路径必须回落到
		// 自动推导结果，而不是把一枚不可展示的徽章报成「佩戴中」。
		if display.Worn == nil || display.Worn.Slug != "silver-badge" {
			t.Fatalf("worn = %+v, want silver-badge（停用档位的徽章不得留在展示位）", display.Worn)
		}
		if display.Upgrade != nil {
			t.Fatalf("auto 模式下 upgrade 必须为空，got %+v", display.Upgrade)
		}
	})

	t.Run("only a disabled-tier badge left", func(t *testing.T) {
		fixture, store, userID, goldID, _ := wearFixture(t)
		// 只留 gold 这一枚，再停用 gold 档：候选集必须为空。
		if _, err := fixture.pool.Exec(fixture.ctx,
			`DELETE FROM user_badges WHERE user_id = $1 AND badge_id <> $2`, userID, goldID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(fixture.ctx,
			`UPDATE badge_tiers SET enabled = FALSE WHERE key = 'gold'`); err != nil {
			t.Fatal(err)
		}

		display := mustDisplay(t, store, userID)
		if display.Mode != BadgeDisplayModeAuto {
			t.Fatalf("mode = %q, want auto", display.Mode)
		}
		if display.Automatic != nil || display.Worn != nil || display.Upgrade != nil {
			t.Fatalf("等级停用的徽章不得成为候选：automatic=%+v worn=%+v upgrade=%+v",
				display.Automatic, display.Worn, display.Upgrade)
		}
	})
}

// TestHideWornBadgeClearsSlotAndSurvivesRecompute 是 hidden 刀的核心契约：
// 隐藏 = 三列同源写齐 → chip 读到空 → 两条重算都不许回落写入。
//
// 只看 top_badge_id 会漏掉 hidden 标记被回落逻辑顺手清掉（下次又替用户戴上）；
// 只看 hidden 标记会漏掉 top_badge_id 没被清（chip 还在，「什么都不戴」没生效）。
func TestHideWornBadgeClearsSlotAndSurvivesRecompute(t *testing.T) {
	fixture, store, userID, _, _ := wearFixture(t)
	ctx := fixture.ctx

	if err := store.SetWornBadge(ctx, userID, "silver-badge"); err != nil {
		t.Fatalf("set worn badge: %v", err)
	}
	if err := store.HideWornBadge(ctx, userID); err != nil {
		t.Fatalf("hide worn badge: %v", err)
	}

	// 三列同源：top_badge_id 清空、manual 清掉、hidden 写上。少任何一列都是半坏状态。
	if top, manual := wornState(t, store, userID); top != nil || manual {
		t.Fatalf("after hide = (top=%v, manual=%v), want (NULL, false)", top, manual)
	}
	if !hiddenState(t, store, userID) {
		t.Fatal("hidden 标记必须为 TRUE")
	}

	// chip 批量读口必须看到空：TopBadgesFor 按 top_badge_id JOIN，hidden 行天然为空。
	refs, err := store.TopBadgesFor(ctx, []int64{userID})
	if err != nil {
		t.Fatalf("top badges: %v", err)
	}
	if _, ok := refs[userID]; ok {
		t.Fatalf("TopBadgesFor = %+v, want 空（隐藏用户的 chip 不许显示徽章）", refs[userID])
	}

	// 单用户重算不许回落写入。
	if err := store.RecomputeTopBadge(ctx, userID); err != nil {
		t.Fatalf("recompute one: %v", err)
	}
	if top, manual := wornState(t, store, userID); top != nil || manual || !hiddenState(t, store, userID) {
		t.Fatalf("RecomputeTopBadge 回落覆盖了 hidden：(top=%v, manual=%v, hidden=%v)", top, manual, hiddenState(t, store, userID))
	}

	// 全量重算走的是另一段 SQL（manual_valid 分支），必须单独钉。
	if _, err := store.RecomputeAll(ctx); err != nil {
		t.Fatalf("recompute all: %v", err)
	}
	if top, manual := wornState(t, store, userID); top != nil || manual || !hiddenState(t, store, userID) {
		t.Fatalf("RecomputeAll 回落覆盖了 hidden：(top=%v, manual=%v, hidden=%v)", top, manual, hiddenState(t, store, userID))
	}

	// 幂等：再跑一次全量重算，hidden 行不计入变更。
	again, err := store.RecomputeAll(ctx)
	if err != nil {
		t.Fatalf("second recompute all: %v", err)
	}
	if again != 0 {
		t.Fatalf("second refreshed = %d, want 0（hidden 行不该被改）", again)
	}

	display := mustDisplay(t, store, userID)
	if display.Mode != BadgeDisplayModeHidden {
		t.Fatalf("mode = %q, want hidden", display.Mode)
	}
	if display.Worn != nil {
		t.Fatalf("worn = %+v, want nil（展示位确实是空的）", display.Worn)
	}
	// automatic 照常推导：前端可以据此显示「你的最佳徽章是 X」。
	if display.Automatic == nil || display.Automatic.Slug != "gold-badge" {
		t.Fatalf("automatic = %+v, want gold-badge", display.Automatic)
	}
	if display.Upgrade != nil {
		t.Fatalf("hidden 模式下 upgrade 必须为空，got %+v", display.Upgrade)
	}

}

// TestWearingBadgeAfterHideUnhides 断言重新佩戴解除隐藏。
//
// PUT 的语义覆盖 hidden：用户从「什么都不戴」改成「戴 X」，hidden 标记必须清掉，
// 否则它会在下一次重算里拦住佩戴值的维护（失效回落永远走不到）。
//
// 三列必须由**同一条** SetWornBadge 写齐：top_badge_id=silver、manual=TRUE、
// hidden=FALSE。少清 hidden 就是 YG 报的那个缺陷——接口 200、mode 仍是 hidden。
func TestWearingBadgeAfterHideUnhides(t *testing.T) {
	fixture, store, userID, _, silverID := wearFixture(t)
	ctx := fixture.ctx

	if err := store.HideWornBadge(ctx, userID); err != nil {
		t.Fatalf("hide worn badge: %v", err)
	}
	if top, manual := wornState(t, store, userID); top != nil || manual || !hiddenState(t, store, userID) {
		t.Fatalf("hidden 起点不对：(top=%v, manual=%v, hidden=%v)", top, manual, hiddenState(t, store, userID))
	}
	if err := store.SetWornBadge(ctx, userID, "silver-badge"); err != nil {
		t.Fatalf("set worn badge: %v", err)
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != silverID || !manual {
		t.Fatalf("after re-wear = (top=%v, manual=%v), want (silver, true)", top, manual)
	}
	if hiddenState(t, store, userID) {
		t.Fatal("重新佩戴必须解除 hidden：否则重算永远不维护这个展示位")
	}

	display := mustDisplay(t, store, userID)
	if display.Mode != BadgeDisplayModeManual || display.Worn == nil || display.Worn.Slug != "silver-badge" {
		t.Fatalf("display = %+v, want manual + silver-badge", display)
	}
	// upgrade 语义在「隐藏过再佩戴」之后必须照常：gold 比 silver 好，佩戴 silver
	// 就该提示可换成 gold。hidden 曾经把这个分支整个短路掉。
	if display.Upgrade == nil || display.Upgrade.Slug != "gold-badge" {
		t.Fatalf("upgrade = %+v, want gold-badge", display.Upgrade)
	}

	// 解除隐藏后，两条重算都必须按「有效的手动佩戴」对待：不改值、不重数。
	if err := store.RecomputeTopBadge(ctx, userID); err != nil {
		t.Fatalf("recompute one: %v", err)
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != silverID || !manual || hiddenState(t, store, userID) {
		t.Fatalf("RecomputeTopBadge 覆盖了重新佩戴：(top=%v, manual=%v, hidden=%v)",
			top, manual, hiddenState(t, store, userID))
	}
	refreshed, err := store.RecomputeAll(ctx)
	if err != nil {
		t.Fatalf("recompute all: %v", err)
	}
	if refreshed != 0 {
		t.Fatalf("refreshed = %d, want 0（有效的佩戴值不该被重写）", refreshed)
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != silverID || !manual || hiddenState(t, store, userID) {
		t.Fatalf("RecomputeAll 覆盖了重新佩戴：(top=%v, manual=%v, hidden=%v)",
			top, manual, hiddenState(t, store, userID))
	}
}

// TestRecomputeTopBadgeRepairsHalfBrokenHiddenRow 钉住「hidden = 三列同源」的收敛方向。
//
// 半坏行（hidden=TRUE 但 top_badge_id 非空）只可能来自直接改库或修复前的旧代码；
// 单用户重算必须把它收敛成 (NULL, FALSE, TRUE)，而不是把非空展示位当成「用户选择」
// 保留下来——chip 会照 top_badge_id 显示一枚徽章，而佩戴面板说「未佩戴」。
func TestRecomputeTopBadgeRepairsHalfBrokenHiddenRow(t *testing.T) {
	fixture, store, userID, goldID, _ := wearFixture(t)
	ctx := fixture.ctx

	if _, err := fixture.pool.Exec(ctx,
		`UPDATE users SET top_badge_id = $2, top_badge_manual = TRUE, top_badge_hidden = TRUE WHERE id = $1`,
		userID, goldID); err != nil {
		t.Fatalf("seed half-broken hidden row: %v", err)
	}
	if err := store.RecomputeTopBadge(ctx, userID); err != nil {
		t.Fatalf("recompute one: %v", err)
	}
	if top, manual := wornState(t, store, userID); top != nil || manual || !hiddenState(t, store, userID) {
		t.Fatalf("半坏 hidden 行未被收敛：(top=%v, manual=%v, hidden=%v)",
			top, manual, hiddenState(t, store, userID))
	}
	// 全量重算不碰 hidden 行：既不许修成「替用户戴上」，也不许把它算进 refreshed。
	refreshed, err := store.RecomputeAll(ctx)
	if err != nil {
		t.Fatalf("recompute all: %v", err)
	}
	if refreshed != 0 {
		t.Fatalf("refreshed = %d, want 0（hidden 行不在重算作用域内）", refreshed)
	}
	if top, manual := wornState(t, store, userID); top != nil || manual || !hiddenState(t, store, userID) {
		t.Fatalf("RecomputeAll 碰了 hidden 行：(top=%v, manual=%v, hidden=%v)",
			top, manual, hiddenState(t, store, userID))
	}
}

// TestClearWornBadgeAfterHideReturnsToAutomatic 断言 DELETE 对隐藏用户回到 auto。
//
// DELETE 的承诺是「回到自动推导」，对「什么都不戴」的用户同样成立：hidden 不是
// 不可撤销的终态，否则用户点错一次就永远找不到回到默认的入口。
func TestClearWornBadgeAfterHideReturnsToAutomatic(t *testing.T) {
	fixture, store, userID, goldID, _ := wearFixture(t)
	ctx := fixture.ctx

	if err := store.HideWornBadge(ctx, userID); err != nil {
		t.Fatalf("hide worn badge: %v", err)
	}
	if err := store.ClearWornBadge(ctx, userID); err != nil {
		t.Fatalf("clear worn badge: %v", err)
	}
	if hiddenState(t, store, userID) {
		t.Fatal("DELETE 必须清掉 hidden 标记")
	}
	if top, manual := wornState(t, store, userID); top == nil || *top != goldID || manual {
		t.Fatalf("after clear = (top=%v, manual=%v), want (gold, false)", top, manual)
	}
	display := mustDisplay(t, store, userID)
	if display.Mode != BadgeDisplayModeAuto || display.Worn == nil || display.Worn.Slug != "gold-badge" {
		t.Fatalf("display = %+v, want auto + gold-badge", display)
	}
}
