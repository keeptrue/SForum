package apitokens

import (
	"testing"
	"time"
)

// P3-1 的 Postgres 侧证据：api_tokens.last_used_at 的写入必须被时间条件节流。
//
// 为什么断言在 Postgres 上而不是 fake store：节流是 TouchLastUsed 那条 UPDATE 的
// WHERE 条件，fake 只能把条件再复述一遍（测到的是 fake 自己）。这里跑真实的
// PostgresStore，鉴权链路其余部分（GetByPublicID → TokenVersion → 时间/哈希校验）
// 也是真实代码；只有 Actor 装载用 fake（staticActorLoader），与本包既有的
// token_version 集成测试口径一致。
//
// 变异要求：删掉 TouchLastUsed 里的
// `AND (last_used_at IS NULL OR last_used_at < now() - $2 * interval '1 second')`
// 之后，「second auth within window does not rewrite」必须红；边界用例里的
// 「阈值 +1s 必不写」也一并红（它同样是节流条件在守的那条线）。
func TestTouchLastUsedThrottlePostgres(t *testing.T) {
	// 阈值是对外可见的运维语义（「这条令牌最近一分钟内用过吗」），不是实现细节：
	// 改动它必须让这条断言跟着红，逼调用方重新评估写放大与运维可读性。
	if lastUsedThrottle != time.Minute {
		t.Fatalf("lastUsedThrottle=%s，期望 1m；改阈值需同步评估写放大与运维可见性", lastUsedThrottle)
	}

	fixture := newTokenVersionPGFixture(t)
	ctx := fixture.ctx

	// ① 同一令牌在窗口内连续两次鉴权，只写一次。
	//
	// 预置成「10 秒前刚写过」，而不是从 NULL 起步：这样「被改写」与「没被改写」
	// 在值上必然相差约 10 秒，不依赖两次 now() 是否恰好落在同一微秒。
	t.Run("second auth within window does not rewrite", func(t *testing.T) {
		userID := fixture.insertUser(t, "pat_throttle_within")
		token := fixture.createToken(t, userID, "ci-throttle-within")
		seeded := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Microsecond)
		seedLastUsedAt(t, fixture, token.Token.ID, seeded)

		for attempt := 1; attempt <= 2; attempt++ {
			if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext); err != nil {
				t.Fatalf("第 %d 次鉴权: %v", attempt, err)
			}
			got := loadLastUsedAt(t, fixture, token.Token.ID)
			if got == nil || !got.Equal(seeded) {
				t.Fatalf("第 %d 次鉴权改写了窗口内的 last_used_at：got=%s want=%s",
					attempt, formatTimestamp(got), formatTimestamp(&seeded))
			}
		}
	})

	// ② 超过阈值后再次鉴权会刷新。
	t.Run("auth after window refreshes last_used_at", func(t *testing.T) {
		userID := fixture.insertUser(t, "pat_throttle_after")
		token := fixture.createToken(t, userID, "ci-throttle-after")
		stale := time.Now().UTC().Add(-lastUsedThrottle - 30*time.Second).Truncate(time.Microsecond)
		seedLastUsedAt(t, fixture, token.Token.ID, stale)

		if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext); err != nil {
			t.Fatalf("鉴权: %v", err)
		}
		got := loadLastUsedAt(t, fixture, token.Token.ID)
		if got == nil {
			t.Fatal("超阈值后 last_used_at 仍为 NULL")
		}
		if !got.After(stale) {
			t.Fatalf("超阈值后没有刷新：got=%s stale=%s", formatTimestamp(got), formatTimestamp(&stale))
		}
		// 刷新值必须接近「现在」：防止实现把旧值原样写回也骗过 After 判定。
		if delta := time.Since(*got); delta < 0 || delta > time.Minute {
			t.Fatalf("刷新后的 last_used_at 不接近当前时间：got=%s delta=%s", formatTimestamp(got), delta)
		}
	})

	// ③ 迁移前签发的旧令牌（last_used_at 为 NULL）首次使用会写入，
	// 且写入之后的第二次鉴权立刻进入节流窗口。
	t.Run("legacy null last_used_at is written on first use", func(t *testing.T) {
		userID := fixture.insertUser(t, "pat_throttle_null")
		token := fixture.createToken(t, userID, "ci-throttle-null")
		if got := loadLastUsedAt(t, fixture, token.Token.ID); got != nil {
			t.Fatalf("前置条件不成立：新建令牌的 last_used_at 应为 NULL，实际 %s", formatTimestamp(got))
		}

		if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext); err != nil {
			t.Fatalf("首次鉴权: %v", err)
		}
		first := loadLastUsedAt(t, fixture, token.Token.ID)
		if first == nil {
			t.Fatal("NULL 的 last_used_at 首次使用后没有被写入（节流条件漏掉了 IS NULL 分支）")
		}

		if _, err := fixture.service(userID).AuthenticatePlaintext(ctx, token.Plaintext); err != nil {
			t.Fatalf("第二次鉴权: %v", err)
		}
		second := loadLastUsedAt(t, fixture, token.Token.ID)
		if second == nil || !second.Equal(*first) {
			t.Fatalf("首次写入后的第二次鉴权又写了一次：first=%s second=%s",
				formatTimestamp(first), formatTimestamp(second))
		}
	})

	// ④ 边界：阈值是严格下界（`<`，不是 `<=`）。
	//
	// 为什么不直接断言「恰好等于阈值时不写」：种子语句和鉴权语句各自是一个隐式
	// 事务，now() 是各自的事务时间戳，两条语句之间必然前进（一次网络往返 ≫ 1µs）。
	// 所以按 now() - 阈值 预置的值，到鉴权语句执行时已经严格小于阈值了 —— 它落在
	// 「写」的一侧，跨语句根本构造不出等值。等值只在同一事务/同一语句内成立，而
	// PostgresStore 绑定的是连接池、无法把种子与鉴权塞进同一事务，因此这里退一步
	// 断言两侧：阈值 -1s 必写、阈值 +1s 必不写；第三条断言把「跨语句的等值预置必然
	// 过期」这个事实固定下来，避免后来者误以为等值行为没有覆盖。
	t.Run("threshold is a strict lower bound", func(t *testing.T) {
		// 阈值 -1s（61 秒前）：严格超过阈值 → 必写。
		pastUser := fixture.insertUser(t, "pat_throttle_boundary_past")
		pastToken := fixture.createToken(t, pastUser, "ci-throttle-boundary-past")
		pastSeeded := time.Now().UTC().Add(-lastUsedThrottle - time.Second).Truncate(time.Microsecond)
		seedLastUsedAt(t, fixture, pastToken.Token.ID, pastSeeded)
		if _, err := fixture.service(pastUser).AuthenticatePlaintext(ctx, pastToken.Plaintext); err != nil {
			t.Fatalf("鉴权（阈值 -1s）: %v", err)
		}
		if got := loadLastUsedAt(t, fixture, pastToken.Token.ID); got == nil || !got.After(pastSeeded) {
			t.Fatalf("阈值 -1s 严格超过阈值，必须写：got=%s seeded=%s",
				formatTimestamp(got), formatTimestamp(&pastSeeded))
		}

		// 阈值 +1s（59 秒前）：仍在窗口内 → 必不写。
		insideUser := fixture.insertUser(t, "pat_throttle_boundary_inside")
		insideToken := fixture.createToken(t, insideUser, "ci-throttle-boundary-inside")
		insideSeeded := time.Now().UTC().Add(-lastUsedThrottle + time.Second).Truncate(time.Microsecond)
		seedLastUsedAt(t, fixture, insideToken.Token.ID, insideSeeded)
		if _, err := fixture.service(insideUser).AuthenticatePlaintext(ctx, insideToken.Plaintext); err != nil {
			t.Fatalf("鉴权（阈值 +1s）: %v", err)
		}
		if got := loadLastUsedAt(t, fixture, insideToken.Token.ID); got == nil || !got.Equal(insideSeeded) {
			t.Fatalf("阈值 +1s 仍在窗口内，不得写：got=%s seeded=%s",
				formatTimestamp(got), formatTimestamp(&insideSeeded))
		}

		// 按 now() - 阈值 跨语句预置：鉴权时它已严格小于阈值，因此会写。
		// 这条断言固定的是「严格小于」的方向，不是等值不写。
		equalUser := fixture.insertUser(t, "pat_throttle_boundary_equal")
		equalToken := fixture.createToken(t, equalUser, "ci-throttle-boundary-equal")
		if _, err := fixture.pool.Exec(ctx,
			`UPDATE api_tokens SET last_used_at = now() - $2 * interval '1 second' WHERE id = $1`,
			equalToken.Token.ID, int64(lastUsedThrottle.Seconds())); err != nil {
			t.Fatalf("预置等值边界: %v", err)
		}
		equalSeeded := loadLastUsedAt(t, fixture, equalToken.Token.ID)
		// 显式等一小会儿：让鉴权语句的 now() 确定地晚于种子语句，排除同微秒偶合。
		time.Sleep(5 * time.Millisecond)
		if _, err := fixture.service(equalUser).AuthenticatePlaintext(ctx, equalToken.Plaintext); err != nil {
			t.Fatalf("鉴权（等值预置）: %v", err)
		}
		if got := loadLastUsedAt(t, fixture, equalToken.Token.ID); got == nil || !got.After(*equalSeeded) {
			t.Fatalf("按 now()-阈值 跨语句预置后应已严格小于阈值并写入：got=%s seeded=%s",
				formatTimestamp(got), formatTimestamp(equalSeeded))
		}
	})
}

// formatTimestamp 统一按 UTC 打印，避免断言消息里同一时刻出现两种时区写法
// （pgx 读回的 timestamptz 带会话时区，Go 侧构造的是 UTC）。
func formatTimestamp(value *time.Time) string {
	if value == nil {
		return "<NULL>"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func seedLastUsedAt(t *testing.T, fixture *tokenVersionPGFixture, tokenID int64, at time.Time) {
	t.Helper()
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE api_tokens SET last_used_at = $2 WHERE id = $1`, tokenID, at); err != nil {
		t.Fatalf("预置 last_used_at: %v", err)
	}
}

func loadLastUsedAt(t *testing.T, fixture *tokenVersionPGFixture, tokenID int64) *time.Time {
	t.Helper()
	var value *time.Time
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT last_used_at FROM api_tokens WHERE id = $1`, tokenID).Scan(&value); err != nil {
		t.Fatalf("读取 last_used_at: %v", err)
	}
	return value
}
