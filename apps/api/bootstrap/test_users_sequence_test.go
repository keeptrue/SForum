package bootstrap

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// repairUsersIDSequence 把共享测试库的 users_id_seq 对齐到「表的 max(id)」，
// 让下一次默认插入拿到 max(id)+1 的 id。
//
// 为什么需要：`users.id` 是 BIGSERIAL，序列只在「不给 id 的插入」时前进。
// 同一分片的多个包共用一个测试库，其中不少夹具用显式 id 插 users（id=1/2/3/7/42…），
// 显式插入既不推进序列也不清理行；于是一次依赖默认 id 的插入可能取到已被占用的
// 值，撞 users_pkey 23505 —— 2026-11-14 门禁基线里两次打红的就是这个
// （extension_lifecycle_cleanup_fk_order_test.go 的夹具建用户）。
//
// 实现委托给 testdb.AlignSerial，本包不再保留第二套对齐逻辑：
//   - 旧实现在这里手写 `setval(GREATEST(max(id), 1))`，**无条件写值**——序列已经
//     领先时会把号回拨，等于把已经发出去的 id 重新发一遍（跨包共享 public schema
//     时是真实的撞号风险）。AlignSerial 读 `pg_sequence_last_value` 后「已领先就
//     不动」，顺手修掉了这个隐患；
//   - 两套实现并存本身就是缺陷来源：改一处漏一处，谁都不知道哪个才是权威。
//
// 为什么这不算「掩盖真实冲突」：修的是编号分配簿记，不是冲突检测。调用方仍然用
// 普通 INSERT（没有 ON CONFLICT 兜底），主键、username_lower、email_lower 的唯一性
// 照旧生效；真正重复的自增 id 或业务唯一键仍然直接报错。
// TestLifecyclePurgeFixtureSurvivesExplicitIDUserLandmine 用一条对照断言钉住这点。
func repairUsersIDSequence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	testdb.AlignSerial(t, ctx, pool, "users")
}
