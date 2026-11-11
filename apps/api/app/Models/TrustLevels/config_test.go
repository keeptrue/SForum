package trustlevels_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// TestDecodeConfigRejectsUnknownField 锁定严格解码：未知字段必须报错。
//
// 静默丢弃会让运营以为自己保存成功（T16 的坑），所以这里拒绝而不是忽略。
func TestDecodeConfigRejectsUnknownField(t *testing.T) {
	raw := `{"schemaVersion":1,"levels":[
		{"levelSlug":"member","requirements":{},"limits":{},"earnable":[],"oops":1},
		{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`
	if _, err := trustlevels.DecodeConfig(raw); err == nil {
		t.Fatal("未知字段被接受了")
	}
}

// TestDecodeConfigValidation 是配置面的表驱动拒绝测试。
func TestDecodeConfigValidation(t *testing.T) {
	validLevels := `{"levelSlug":"member","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}`

	cases := []struct {
		name string
		raw  string
	}{
		{"schema 版本不符", `{"schemaVersion":2,"levels":[` + validLevels + `]}`},
		{"缺少等级", `{"schemaVersion":1,"levels":[
			{"levelSlug":"member","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]}]}`},
		{"等级重复", `{"schemaVersion":1,"levels":[
			{"levelSlug":"member","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"member","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`},
		{"未知等级", `{"schemaVersion":1,"levels":[
			{"levelSlug":"newcomer","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`},
		{"负阈值", `{"schemaVersion":1,"levels":[
			{"levelSlug":"member","requirements":{"topic_count":-1},"limits":{},"earnable":[]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`},
		{"负限额", `{"schemaVersion":1,"levels":[
			{"levelSlug":"member","requirements":{},"limits":{"dailyTopicLimit":-1},"earnable":[]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`},
		{"遗留的 newUserDays 字段", `{"schemaVersion":1,"newUserDays":7,"levels":[` + validLevels + `]}`},
		{"earnable 含管理键", `{"schemaVersion":1,"levels":[
			{"levelSlug":"member","requirements":{},"limits":{},"earnable":["user.ban"]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`},
		{"尾随内容", `{"schemaVersion":1,"levels":[` + validLevels + `]} {}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := trustlevels.DecodeConfig(testCase.raw)
			if err == nil {
				t.Fatal("非法配置被接受了")
			}
			if !errors.Is(err, trustlevels.ErrInvalidConfig) {
				t.Fatalf("期望 ErrInvalidConfig，得到 %v", err)
			}
		})
	}
}

// TestDecodeConfigAcceptsValid 是上面对照组的反向证明（T10）：
// 一份合法配置必须被接受，并且阈值/限额/earnable 逐字段落到 Policy 上。
func TestDecodeConfigAcceptsValid(t *testing.T) {
	raw := `{"schemaVersion":1,"levels":[
		{"levelSlug":"member","requirements":{"days_visited":1,"comment_count":2},"limits":{"topicCooldownSeconds":0},"earnable":[]},
		{"levelSlug":"resident","requirements":{"topic_count":4},"limits":{},"earnable":["topic.create","topic.create"]},
		{"levelSlug":"veteran","requirements":{"like_received_count":9},"limits":{},"earnable":[]},
		{"levelSlug":"leader","requirements":{"bookmark_count":11},"limits":{},"earnable":["attachment.upload"]}]}`
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("合法配置被拒绝：%v", err)
	}
	// R6：newUserDays 不来自本 JSON，装配层用 trust.new_user_days 覆盖；
	// 这里必须保持推荐默认，绝不能变成 0（那等于把新人闸关掉）。
	if policy.NewUserDays != trustlevels.DefaultPolicy().NewUserDays {
		t.Fatalf("NewUserDays = %d，期望保持推荐默认 %d", policy.NewUserDays, trustlevels.DefaultPolicy().NewUserDays)
	}
	if policy.Levels[trustlevels.LevelMember].CommentCount != 2 {
		t.Fatalf("TL1 comment_count = %d", policy.Levels[trustlevels.LevelMember].CommentCount)
	}
	if policy.Limits[trustlevels.LevelMember].TopicCooldownSeconds == nil ||
		*policy.Limits[trustlevels.LevelMember].TopicCooldownSeconds != 0 {
		t.Fatal("TL1 的 0 秒冷却没有被保留（0 必须与 nil 区分）")
	}
	if policy.Limits[trustlevels.LevelRegular].TopicCooldownSeconds != nil {
		t.Fatal("未配置的限额必须是 nil（沿用站点默认）")
	}
	// 重复键去重
	if got := policy.Earnable.Grants(trustlevels.LevelRegular); len(got) != 1 || got[0] != "topic.create" {
		t.Fatalf("TL2 已挣得 = %v", got)
	}
	// 高等级累计
	if got := policy.Earnable.Grants(trustlevels.LevelLeader); len(got) != 2 {
		t.Fatalf("TL4 已挣得 = %v", got)
	}
}

// TestConfigRoundTrip 锁定后台读写与恢复默认共用同一份形状。
func TestConfigRoundTrip(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	policy.Earnable = trustlevels.Earnable{
		trustlevels.LevelRegular: {"topic.create"},
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if !strings.Contains(encoded, `"levelSlug":"member"`) {
		t.Fatalf("编码结果缺少 levelSlug：%s", encoded)
	}
	if strings.Contains(encoded, `"slug"`) {
		t.Fatalf("编码结果出现了裸 slug（D21 禁止）：%s", encoded)
	}
	var decodedConfig map[string]any
	if err := json.Unmarshal([]byte(encoded), &decodedConfig); err != nil {
		t.Fatalf("编码结果不是合法 JSON：%v", err)
	}
	roundTripped, err := trustlevels.DecodeConfig(encoded)
	if err != nil {
		t.Fatalf("DecodeConfig(EncodeConfig(p)) 失败：%v", err)
	}
	if strings.Contains(encoded, "newUserDays") {
		t.Fatalf("编码结果不得包含 newUserDays（R6 单一来源）：%s", encoded)
	}
	for level := trustlevels.LevelMember; level < trustlevels.LevelCount; level++ {
		if roundTripped.Levels[level] != policy.Levels[level] {
			t.Fatalf("往返后 TL%d 阈值变了：%v → %v", level, policy.Levels[level], roundTripped.Levels[level])
		}
	}
	if got := roundTripped.Earnable.Grants(trustlevels.LevelRegular); len(got) != 1 {
		t.Fatalf("往返后已挣得 = %v", got)
	}
}

// TestDecodeConfigEmptyFallsBackToDefault 锁定「空值 = 推荐默认」。
func TestDecodeConfigEmptyFallsBackToDefault(t *testing.T) {
	policy, err := trustlevels.DecodeConfig("   ")
	if err != nil {
		t.Fatalf("空值应回落到推荐默认：%v", err)
	}
	if policy.Levels[trustlevels.LevelMember] != trustlevels.DefaultPolicy().Levels[trustlevels.LevelMember] {
		t.Fatal("空值没有回落到推荐默认")
	}
}

// TestLimitsNullRoundTrip 锁定 D27 的哨兵语义：限额的「继承站点默认」是 JSON
// null，**不是 0**。0 在限额里有语义（免冷却），把 null 当缺省丢掉再写回 0
// 就是一次静默策略变更。
func TestLimitsNullRoundTrip(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	// 默认全部继承 → 编码后限额字段必须是 null。
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if !strings.Contains(encoded, `"topicCooldownSeconds":null`) {
		t.Fatalf("未配置的限额应编码为 null：%s", encoded)
	}
	decoded, err := trustlevels.DecodeConfig(encoded)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	for level := trustlevels.LevelMember; level < trustlevels.LevelCount; level++ {
		limits := decoded.Limits[level]
		if limits.TopicCooldownSeconds != nil || limits.CommentCooldownSeconds != nil ||
			limits.DailyTopicLimit != nil || limits.DailyCommentLimit != nil {
			t.Fatalf("TL%d 的 null 限额被还原成了非 nil（可能是 0）：%+v", level, limits)
		}
	}

	// 反向：显式 0 必须原样保留为「免冷却」，不能被当成「继承」。
	explicit := `{"schemaVersion":1,"levels":[
		{"levelSlug":"member","requirements":{},"limits":{"topicCooldownSeconds":0},"earnable":[]},
		{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`
	decodedExplicit, err := trustlevels.DecodeConfig(explicit)
	if err != nil {
		t.Fatalf("DecodeConfig(explicit): %v", err)
	}
	value := decodedExplicit.Limits[trustlevels.LevelMember].TopicCooldownSeconds
	if value == nil || *value != 0 {
		t.Fatalf("显式 0 被丢失或改写：%v", value)
	}
	if decodedExplicit.Limits[trustlevels.LevelMember].DailyTopicLimit != nil {
		t.Fatal("未出现的限额字段必须保持 nil（继承）")
	}
}

// TestDecodeConfigRequiresLimitsAndEarnable 是 I-12 的回归靶子：
// 省略 limits / earnable 必须 422，不能静默重置。
//
// 事故背景：`Limits LevelLimits` 是值类型，省略 → 零值 → validateLimits 通过；
// `Earnable []string` 省略 → nil → len>0 分支跳过 → 也通过。于是「前端漏写 →
// 保存成功但策略被清空」，且没有任何东西报错。
func TestDecodeConfigRequiresLimitsAndEarnable(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "省略 limits",
			raw: `{"schemaVersion":1,"levels":[
				{"levelSlug":"member","requirements":{},"earnable":[]},
				{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
				{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
				{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`,
		},
		{
			name: "省略 earnable",
			raw: `{"schemaVersion":1,"levels":[
				{"levelSlug":"member","requirements":{},"limits":{}},
				{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
				{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
				{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := trustlevels.DecodeConfig(testCase.raw)
			if err == nil {
				t.Fatal("省略字段被接受了（静默重置风险）")
			}
			if !strings.Contains(err.Error(), "缺少") {
				t.Fatalf("错误信息不可读：%v", err)
			}
		})
	}

	// 对照组：显式 null / {} / [] 都是合法输入，语义区分保留。
	explicit := `{"schemaVersion":1,"levels":[
		{"levelSlug":"member","requirements":{},"limits":null,"earnable":null},
		{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`
	policy, err := trustlevels.DecodeConfig(explicit)
	if err != nil {
		t.Fatalf("显式 null/{} / [] 被拒绝：%v", err)
	}
	if policy.Limits[trustlevels.LevelMember].TopicCooldownSeconds != nil {
		t.Fatal("显式 null 应还原成「全继承」")
	}
	if got := policy.Earnable.Grants(trustlevels.LevelMember); len(got) != 0 {
		t.Fatalf("显式 null 的 earnable 应为空集，得到 %v", got)
	}
}
