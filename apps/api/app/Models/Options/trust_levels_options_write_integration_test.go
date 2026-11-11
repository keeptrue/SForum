package options

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 这是写入面唯一打穿到**真实归一化**的断言（D-8 的教训）。
//
// 控制器单测用注入 writer 验的是「控制器逻辑对」，永远碰不到
// `normalizeOptionValue` → `normalizeCommunityPolicyOption` 这条真实路径；
// 而 D-8 的特征正是「200/422 与内容无关」，所以只断状态码抓不到它。
// 这里必须：写 → 从库里读回 → 断言值与归一化后的期望逐字相等。

func newTrustLevelsWriteFixture(t *testing.T) (*Service, *pgxpool.Pool, identity.Actor) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testdb.IsolatedURL(t))
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(pool.Close)

	service := NewServiceWithDefaults(NewPostgresStore(pool), Defaults{})
	if err := service.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTrustManage: true},
	}
	return service, pool, actor
}

// readStoredTrustLevels 用**新的** service 实例读回，绕过内存缓存。
func readStoredTrustLevels(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	fresh := NewServiceWithDefaults(NewPostgresStore(pool), Defaults{})
	values, err := fresh.InternalValues(context.Background())
	if err != nil {
		t.Fatalf("InternalValues: %v", err)
	}
	return values[NameTrustLevels]
}

// TestUpdateTrustLevelsPersistsNormalizedConfig 覆盖「合法写入 → 读回等于归一化结果」。
func TestUpdateTrustLevelsPersistsNormalizedConfig(t *testing.T) {
	service, pool, actor := newTrustLevelsWriteFixture(t)
	ctx := context.Background()

	// 一份带非默认阈值与分档限额的配置：这样「写进去的是原文还是归一化结果」
	// 才区分得出来（全默认值会让两者相等，测试就抓不到归一化被跳过）。
	input := `{"schemaVersion":1,"levels":[
		{"levelSlug":"member","requirements":{"days_visited":3,"comment_count":5},"limits":null,"earnable":null},
		{"levelSlug":"resident","requirements":{"topic_count":2},"limits":{"topicCooldownSeconds":0},"earnable":["topic.create"]},
		{"levelSlug":"veteran","requirements":{"like_received_count":30},"limits":{},"earnable":[]},
		{"levelSlug":"leader","requirements":{"bookmark_count":20},"limits":{"dailyTopicLimit":50},"earnable":[]}]}`

	if _, err := service.Update(ctx, actor, UpdateInput{Name: NameTrustLevels, Value: input}); err != nil {
		t.Fatalf("Update(trust.levels) 失败：%v（D-8 的症状就是这里返回 ErrInvalidOption）", err)
	}

	policy, err := trustlevels.DecodeConfig(input)
	if err != nil {
		t.Fatalf("DecodeConfig(input): %v", err)
	}
	want, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if got := readStoredTrustLevels(t, pool); got != want {
		t.Fatalf("库里的值与归一化结果不一致：\n got=%s\nwant=%s", got, want)
	}
}

// TestRestoreDefaultsPersistsRecommendedConfig 覆盖「推荐默认值本身能存进去」。
//
// 这是 D-8 最刺眼的地方：失败发生在解码之后、写库之前，与内容无关，
// 所以连内置推荐值都写不进去。
func TestRestoreDefaultsPersistsRecommendedConfig(t *testing.T) {
	service, pool, actor := newTrustLevelsWriteFixture(t)
	ctx := context.Background()

	if _, err := service.Update(ctx, actor, UpdateInput{Name: NameTrustLevels, Value: trustLevelOptionDefault()}); err != nil {
		t.Fatalf("写回推荐默认值失败：%v", err)
	}
	if got := readStoredTrustLevels(t, pool); got != trustLevelOptionDefault() {
		t.Fatalf("库里的推荐默认值与内置值不一致：\n got=%s\nwant=%s", got, trustLevelOptionDefault())
	}
}

// TestInvalidTrustLevelsWriteIsAtomic 覆盖「非法 config 不改变库里的值」。
func TestInvalidTrustLevelsWriteIsAtomic(t *testing.T) {
	service, pool, actor := newTrustLevelsWriteFixture(t)
	ctx := context.Background()

	before := readStoredTrustLevels(t, pool)
	cases := []struct {
		name  string
		value string
	}{
		{"未知字段", `{"schemaVersion":1,"levels":[{"levelSlug":"member","requirements":{},"limits":{},"earnable":[],"oops":1}]}`},
		{"错 schemaVersion", `{"schemaVersion":9,"levels":[]}`},
		{"省略 limits", `{"schemaVersion":1,"levels":[
			{"levelSlug":"member","requirements":{},"earnable":[]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := service.Update(ctx, actor, UpdateInput{Name: NameTrustLevels, Value: testCase.value}); err == nil {
				t.Fatal("非法配置被接受了")
			}
			if got := readStoredTrustLevels(t, pool); got != before {
				t.Fatalf("非法写入改变了库里的值（非原子）：\n got=%s\nwant=%s", got, before)
			}
		})
	}
	// 对照：合法写入必须真的能改（否则「值不变」可能只是因为整个写路径坏了）。
	changed := strings.Replace(before, `"days_visited":3`, `"days_visited":4`, 1)
	if changed == before {
		t.Fatalf("对照构造失败：默认配置里没有 days_visited:3 → %s", before)
	}
	if _, err := service.Update(ctx, actor, UpdateInput{Name: NameTrustLevels, Value: changed}); err != nil {
		t.Fatalf("对照写入失败：%v", err)
	}
	if got := readStoredTrustLevels(t, pool); got == before {
		t.Fatal("合法写入没有改变库里的值：写路径本身坏了")
	}
}
