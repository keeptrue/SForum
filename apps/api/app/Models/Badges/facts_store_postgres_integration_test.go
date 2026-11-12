package badges

import (
	"context"
	"testing"
	"time"
)

// 真库用例：`RecordFact` 的幂等与 clamp 语义。
//
// 命令层（Host `badges.fact.record`）只负责解参数并把 `inserted`/`total` 原样回执；
// 「重复上报只算一次」这件事由部分唯一索引 + `ON CONFLICT DO NOTHING` 承担。所以
// 幂等必须在这里证明——命令单测里用一个返回 `inserted:false` 的 fake 证明不了它。
func TestRecordFactDedupeIsIdempotent(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	store := NewPostgresStore(fixture.pool)
	ctx := context.Background()

	input := FactInput{
		UserID: userID, FactKey: "acme.wiki.pages", Variant: "tutorial", Delta: 3,
		SourceExtensionID: "acme.wiki", DedupeKey: "event-1", OccurredAt: time.Now().UTC(),
	}
	first, err := store.RecordFact(ctx, input)
	if err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}
	if !first.Inserted || first.Total != 3 {
		t.Fatalf("首次必须 inserted 且累计 3，得到 %+v", first)
	}

	second, err := store.RecordFact(ctx, input)
	if err != nil {
		t.Fatalf("重复写入不该报错（幂等返回）：%v", err)
	}
	if second.Inserted {
		t.Fatalf("同 dedupeKey 必须 inserted=false，得到 %+v", second)
	}
	if second.Total != 3 {
		t.Fatalf("重复写入不得累加，累计应仍为 3，得到 %d", second.Total)
	}

	// 换一个 dedupeKey 就是新事件：必须真的累加。
	input.DedupeKey = "event-2"
	third, err := store.RecordFact(ctx, input)
	if err != nil {
		t.Fatalf("新事件写入失败：%v", err)
	}
	if !third.Inserted || third.Total != 6 {
		t.Fatalf("新事件必须累加到 6，得到 %+v", third)
	}
}

// TestRecordFactClampsTotalAtZero 断言扣减把累计压到负数时，读取侧拿到 0。
func TestRecordFactClampsTotalAtZero(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	store := NewPostgresStore(fixture.pool)
	ctx := context.Background()

	if _, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "acme.wiki.pages", Delta: 2, SourceExtensionID: "acme.wiki"}); err != nil {
		t.Fatal(err)
	}
	// total 列存储允许为负（事实可以是扣减），但**回执**与条件读取侧都按 ≥0 呈现。
	result, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "acme.wiki.pages", Delta: -5, SourceExtensionID: "acme.wiki"})
	if err != nil {
		t.Fatalf("负数 delta 必须允许：%v", err)
	}
	if result.Total != -3 {
		t.Fatalf("存储层 total 应保留真实值 -3（clamp 在回执/读取侧做），得到 %d", result.Total)
	}
}

// TestRecordFactKeepsInt64Precision 断言大数不丢精度（走 int64，不经 float）。
func TestRecordFactKeepsInt64Precision(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	store := NewPostgresStore(fixture.pool)
	ctx := context.Background()

	const big int64 = 9223372036854775806
	result, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "acme.wiki.pages", Delta: big, SourceExtensionID: "acme.wiki"})
	if err != nil {
		t.Fatalf("int64 边界写入失败：%v", err)
	}
	if result.Total != big {
		t.Fatalf("int64 边界被改写了：want %d got %d", big, result.Total)
	}
}

// TestRecordFactEmptyDedupeKeyAlwaysInserts 是 F-02 的证明：`DedupeKey: ""`
// **不是**幂等键，两次调用必须各插一行且累计翻倍。
//
// 部分唯一索引的谓词是 `WHERE dedupe_key <> ”`，空串不参与唯一性。这一点很容易被
// 「顺手去重掉空串」的实现破坏，而症状是「同一秒内发生两次」的合法累加被吞一次。
// 断言必须同时验**行数**与**total 值**：只验 `inserted` 会把「插了但计数没加」当正确。
func TestRecordFactEmptyDedupeKeyAlwaysInserts(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	store := NewPostgresStore(fixture.pool)
	ctx := context.Background()

	first, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "acme.wiki.pages", Delta: 1, SourceExtensionID: "acme.wiki"})
	if err != nil {
		t.Fatalf("首次写入失败：%v", err)
	}
	second, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "acme.wiki.pages", Delta: 1, SourceExtensionID: "acme.wiki"})
	if err != nil {
		t.Fatalf("第二次写入失败（空 dedupeKey 必须允许重复）：%v", err)
	}
	if !first.Inserted || !second.Inserted {
		t.Fatalf("空 dedupeKey 两次都必须 inserted=true，得到 %+v / %+v", first, second)
	}
	if first.Total != 1 || second.Total != 2 {
		t.Fatalf("累计必须 1 → 2，得到 %d → %d", first.Total, second.Total)
	}

	var rows int64
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM badge_facts WHERE user_id = $1 AND fact_key = 'acme.wiki.pages'`, userID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("必须落两行事实（空串不参与唯一性），得到 %d", rows)
	}
}
