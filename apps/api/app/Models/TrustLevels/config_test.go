package trustlevels_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// tierJSON 造一条合法的 v2 档位；id 传空串时表示「新档，序号由服务端派生」。
func tierJSON(id string) string {
	return `{"id":"` + id + `","name":{"zh":"自定义档","en":"Custom"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]}`
}

// tiersJSON 把若干档位拼成 tiers 数组字面量。
func tiersJSON(tiers ...string) string {
	return `{"schemaVersion":2,"tiers":[` + strings.Join(tiers, ",") + `]}`
}

// builtinTiersJSON 是「四档内置 id」的最小合法配置（TL1..TL4）。
func builtinTiersJSON() string {
	return tiersJSON(
		tierJSON("member"),
		tierJSON("resident"),
		tierJSON("veteran"),
		tierJSON("leader"),
	)
}

// newTiersJSON 生成 n 档全是新档（id 省略）的合法配置，用于 1..8 边界。
func newTiersJSON(n int) string {
	tiers := make([]string, 0, n)
	for index := 0; index < n; index++ {
		tiers = append(tiers, tierJSON(""))
	}
	return tiersJSON(tiers...)
}

// TestDecodeConfigRejectsUnknownField 锁定严格解码：未知字段必须报错。
//
// 静默丢弃会让运营以为自己保存成功（T16 的坑），所以这里拒绝而不是忽略。
// 严格性必须递归到嵌套对象：档位里的幻影字段同样致命。
func TestDecodeConfigRejectsUnknownField(t *testing.T) {
	cases := map[string]string{
		"顶层":     `{"schemaVersion":2,"tiers":[` + tierJSON("member") + `],"oops":1}`,
		"档位内":    `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[],"oops":1}]}`,
		"要求内嵌字段": `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{"account_age_days":3},"limits":{},"earnable":[]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := trustlevels.DecodeConfig(raw); err == nil {
				t.Fatal("未知字段被接受了")
			} else if !errors.Is(err, trustlevels.ErrInvalidConfig) {
				t.Fatalf("期望 ErrInvalidConfig，得到 %v", err)
			}
		})
	}
}

// TestDecodeConfigValidation 是配置面的表驱动拒绝测试（v2 + 版本混用）。
func TestDecodeConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"schema 版本不符", `{"schemaVersion":9,"tiers":[` + tierJSON("member") + `]}`},
		{"缺少 schemaVersion", `{"tiers":[` + tierJSON("member") + `]}`},
		{"v2 里混入 levels", `{"schemaVersion":2,"tiers":[` + tierJSON("member") + `],"levels":[]}`},
		{"v1 里混入 tiers", `{"schemaVersion":1,"tiers":[` + tierJSON("member") + `]}`},
		{"tiers 为空", tiersJSON()},
		{"id 重复", tiersJSON(tierJSON("member"), tierJSON("member"))},
		{"id 不合法（大小写）", tiersJSON(tierJSON("Member"))},
		{"id 不合法（其它前缀）", tiersJSON(tierJSON("vip_1"))},
		{"id 占用 TL0 保留字", tiersJSON(tierJSON("newcomer"))},
		{"name 缺 en", `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]}]}`},
		{"name 全部为空", `{"schemaVersion":2,"tiers":[{"id":"member","name":{},"icon":"leaf","requirements":{},"limits":{},"earnable":[]}]}`},
		{"name.zh 超长", `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"` + strings.Repeat("汉", 33) + `","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]}]}`},
		{"icon 不在白名单", `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"rocket","requirements":{},"limits":{},"earnable":[]}]}`},
		{"负阈值", `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{"topic_count":-1},"limits":{},"earnable":[]}]}`},
		{"负限额", `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{"dailyTopicLimit":-1},"earnable":[]}]}`},
		// earnable 含管理键**不在这里**（M4a/D24 修订）：解码期只判结构，危险键
		// 由写期的 `ValidateEarnableDeclared` 与运行期的 `EarnableRegistry` 拦住。
		// 见 TestDecodeConfigKeepsUndeclaredEarnableKeys。
		{"active 超过上限", newTiersJSON(trustlevels.MaxTiers + 1)},
		{"全部停用（0 active）", `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[],"retired":true}]}`},
		{"尾随内容", builtinTiersJSON() + ` {}`},
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
	raw := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"正式用户","en":"Member"},"icon":"leaf","requirements":{"days_visited":1,"comment_count":2},"limits":{"topicCooldownSeconds":0},"earnable":[],"retired":false},
		{"id":"resident","name":{"zh":"常驻用户","en":"Resident"},"icon":"home","requirements":{"topic_count":4},"limits":{},"earnable":["topic.create","topic.create"]},
		{"id":"","name":{"zh":"自定义","en":"Custom"},"icon":"","requirements":{"like_received_count":9},"limits":null,"earnable":null},
		{"id":"leader","name":{"zh":"社区领袖","en":"Leader"},"icon":"crown","requirements":{"bookmark_count":11},"limits":{},"earnable":["attachment.upload"]}]}`
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("合法配置被拒绝：%v", err)
	}
	// R6：newUserDays 不来自本 JSON，装配层用 trust.new_user_days 覆盖；
	// 这里必须保持推荐默认，绝不能变成 0（那等于把新人闸关掉）。
	if policy.NewUserDays != trustlevels.DefaultPolicy().NewUserDays {
		t.Fatalf("NewUserDays = %d，期望保持推荐默认 %d", policy.NewUserDays, trustlevels.DefaultPolicy().NewUserDays)
	}
	if got := policy.LastRank(); got != 4 {
		t.Fatalf("LastRank = %d，期望 4", got)
	}
	if got := policy.RequirementsOf(1).CommentCount; got != 2 {
		t.Fatalf("TL1 comment_count = %d", got)
	}
	if limits := policy.LimitsFor(1); limits.TopicCooldownSeconds == nil || *limits.TopicCooldownSeconds != 0 {
		t.Fatal("TL1 的 0 秒冷却没有被保留（0 必须与 nil 区分）")
	}
	if limits := policy.LimitsFor(2); limits.TopicCooldownSeconds != nil {
		t.Fatal("未配置的限额必须是 nil（沿用站点默认）")
	}
	// 新档 id 由解码期派生：第 3 档为空 id → tier_6（内置占掉 1..5）。
	if got := policy.TierID(3); got != "tier_6" {
		t.Fatalf("新档 id = %q，期望 tier_6", got)
	}
	// 空 icon 按 rank 取默认序列。
	if got := policy.IconOf(3); got != "mountain" {
		t.Fatalf("TL3 默认图标 = %q，期望默认序列第 3 项 mountain", got)
	}
	// 重复键去重
	if got := policy.Earnable.Grants(2); len(got) != 1 || got[0] != "topic.create" {
		t.Fatalf("TL2 已挣得 = %v", got)
	}
	// 高等级累计
	if got := policy.Earnable.Grants(4); len(got) != 2 {
		t.Fatalf("TL4 已挣得 = %v", got)
	}
}

// TestTierCountBounds 锁定 D-N7：active 档数 1..8，边界两侧都要真跑一遍。
func TestTierCountBounds(t *testing.T) {
	if _, err := trustlevels.DecodeConfig(newTiersJSON(trustlevels.MinTiers)); err != nil {
		t.Fatalf("%d 档被拒绝：%v", trustlevels.MinTiers, err)
	}
	if _, err := trustlevels.DecodeConfig(newTiersJSON(trustlevels.MaxTiers)); err != nil {
		t.Fatalf("%d 档被拒绝：%v", trustlevels.MaxTiers, err)
	}
	if _, err := trustlevels.DecodeConfig(newTiersJSON(trustlevels.MaxTiers + 1)); err == nil {
		t.Fatalf("%d 档被接受了", trustlevels.MaxTiers+1)
	}
	// 停用档位不占 active 名额，但仍保留在数组里。
	manyRetired := tiersJSON(tierJSON("member"), tierJSON(""), tierJSON(""), tierJSON(""),
		`{"id":"","name":{"zh":"停用","en":"Retired"},"icon":"leaf","requirements":{},"limits":{},"earnable":[],"retired":true}`)
	policy, err := trustlevels.DecodeConfig(manyRetired)
	if err != nil {
		t.Fatalf("含停用档的配置被拒绝：%v", err)
	}
	if policy.LastRank() != 4 {
		t.Fatalf("LastRank = %d，期望停用档不占 rank（4）", policy.LastRank())
	}
}

// TestConfigRoundTrip 锁定后台读写与恢复默认共用同一份形状，且编码只写 v2。
func TestConfigRoundTrip(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	policy.Earnable = trustlevels.Earnable{
		trustlevels.Level(2): {"topic.create"},
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if !strings.Contains(encoded, `"schemaVersion":2`) {
		t.Fatalf("编码结果不是 v2：%s", encoded)
	}
	if !strings.Contains(encoded, `"id":"member"`) {
		t.Fatalf("编码结果缺少 id：%s", encoded)
	}
	// v1 的字段名一个都不许再出现：storage 叫 tiers、身份叫 id。
	for _, forbidden := range []string{`"levelSlug"`, `"levels"`, `"slug"`} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("编码结果出现了旧字段 %s：%s", forbidden, encoded)
		}
	}
	if strings.Contains(encoded, "newUserDays") {
		t.Fatalf("编码结果不得包含 newUserDays（R6 单一来源）：%s", encoded)
	}
	var decodedConfig map[string]any
	if err := json.Unmarshal([]byte(encoded), &decodedConfig); err != nil {
		t.Fatalf("编码结果不是合法 JSON：%v", err)
	}
	roundTripped, err := trustlevels.DecodeConfig(encoded)
	if err != nil {
		t.Fatalf("DecodeConfig(EncodeConfig(p)) 失败：%v", err)
	}
	if roundTripped.LastRank() != policy.LastRank() {
		t.Fatalf("往返后档数变了：%d → %d", policy.LastRank(), roundTripped.LastRank())
	}
	for rank := trustlevels.LevelMember; rank <= policy.LastRank(); rank++ {
		if roundTripped.RequirementsOf(rank) != policy.RequirementsOf(rank) {
			t.Fatalf("往返后 TL%d 阈值变了", rank)
		}
		if roundTripped.TierID(rank) != policy.TierID(rank) {
			t.Fatalf("往返后 TL%d 身份变了：%s → %s", rank, policy.TierID(rank), roundTripped.TierID(rank))
		}
		if roundTripped.DisplayNameOf(rank) != policy.DisplayNameOf(rank) {
			t.Fatalf("往返后 TL%d 名称变了", rank)
		}
	}
	if got := roundTripped.Earnable.Grants(2); len(got) != 1 {
		t.Fatalf("往返后已挣得 = %v", got)
	}
}

// TestDecodeConfigEmptyFallsBackToDefault 锁定「空值 = 推荐默认」。
func TestDecodeConfigEmptyFallsBackToDefault(t *testing.T) {
	policy, err := trustlevels.DecodeConfig("   ")
	if err != nil {
		t.Fatalf("空值应回落到推荐默认：%v", err)
	}
	if policy.RequirementsOf(trustlevels.LevelMember) != trustlevels.DefaultPolicy().RequirementsOf(trustlevels.LevelMember) {
		t.Fatal("空值没有回落到推荐默认")
	}
	if policy.LastRank() != trustlevels.DefaultPolicy().LastRank() {
		t.Fatal("空值的档数与推荐默认不一致")
	}
}

// TestLimitsNullRoundTrip 锁定 D27 的哨兵语义：限额的「继承站点默认」是 JSON
// null，**不是 0**。0 在限额里有语义（免冷却），把 null 当缺省丢掉再写回 0
// 就是一次静默策略变更。
func TestLimitsNullRoundTrip(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
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
	for rank := trustlevels.LevelMember; rank <= decoded.LastRank(); rank++ {
		limits := decoded.LimitsFor(rank)
		if limits.TopicCooldownSeconds != nil || limits.CommentCooldownSeconds != nil ||
			limits.DailyTopicLimit != nil || limits.DailyCommentLimit != nil {
			t.Fatalf("TL%d 的 null 限额被还原成了非 nil（可能是 0）：%+v", rank, limits)
		}
	}

	// 反向：显式 0 必须原样保留为「免冷却」，不能被当成「继承」。
	explicit := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{"topicCooldownSeconds":0},"earnable":[]},
		{"id":"resident","name":{"zh":"乙","en":"B"},"icon":"home","requirements":{},"limits":{},"earnable":[]}]}`
	decodedExplicit, err := trustlevels.DecodeConfig(explicit)
	if err != nil {
		t.Fatalf("DecodeConfig(explicit): %v", err)
	}
	value := decodedExplicit.LimitsFor(1).TopicCooldownSeconds
	if value == nil || *value != 0 {
		t.Fatalf("显式 0 被丢失或改写：%v", value)
	}
	if decodedExplicit.LimitsFor(1).DailyTopicLimit != nil {
		t.Fatal("未出现的限额字段必须保持 nil（继承）")
	}
}

// TestDecodeConfigRequiresLimitsAndEarnable 是 I-12 的回归靶子：
// 省略 limits / earnable 必须 422，不能静默重置。
func TestDecodeConfigRequiresLimitsAndEarnable(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "省略 limits",
			raw: `{"schemaVersion":2,"tiers":[
				{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"earnable":[]}]}`,
		},
		{
			name: "省略 earnable",
			raw: `{"schemaVersion":2,"tiers":[
				{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{}}]}`,
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
	explicit := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":null,"earnable":null}]}`
	policy, err := trustlevels.DecodeConfig(explicit)
	if err != nil {
		t.Fatalf("显式 null/{} / [] 被拒绝：%v", err)
	}
	if policy.LimitsFor(1).TopicCooldownSeconds != nil {
		t.Fatal("显式 null 应还原成「全继承」")
	}
	if got := policy.Earnable.Grants(1); len(got) != 0 {
		t.Fatalf("显式 null 的 earnable 应为空集，得到 %v", got)
	}
}

// TestDecodeConfigReturnsFieldDetails 锁定 D-N14 的领域侧契约：错误必须是**结构化可提取**的。
//
// 控制器只做投影，不许解析错误字符串；这里断言 rank/tierId/field 三个定位维度，
// 以及明细上界（一份全坏的配置不得刷出几百条）。
func TestDecodeConfigReturnsFieldDetails(t *testing.T) {
	raw := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]},
		{"id":"resident","name":{"zh":"乙","en":"B"},"icon":"home","requirements":{},"earnable":[]},
		{"id":"veteran","name":{"zh":"丙","en":"C"},"icon":"rocket","requirements":{"topic_count":-1},"limits":{},"earnable":[]}]}`
	_, err := trustlevels.DecodeConfig(raw)
	if err == nil {
		t.Fatal("非法配置被接受了")
	}
	if !errors.Is(err, trustlevels.ErrInvalidConfig) {
		t.Fatalf("整体拒绝的判据变了：%v", err)
	}
	details := trustlevels.ConfigErrorDetails(err)
	if len(details) == 0 {
		t.Fatal("没有字段级明细：控制器只能给一句「配置无效」")
	}
	want := map[string]struct{ field, code string }{
		"resident": {field: "limits", code: trustlevels.CodeLimitsMissing},
		"veteran":  {field: "icon", code: trustlevels.CodeIconNotWhitelisted},
	}
	for tierID, expected := range want {
		found := false
		for _, detail := range details {
			if detail.TierID != tierID || detail.Field != expected.field {
				continue
			}
			if detail.Code != expected.code {
				t.Fatalf("%s 的 %s 代码 = %q，期望 %q", tierID, expected.field, detail.Code, expected.code)
			}
			if detail.Reason == "" || detail.Rank <= 0 {
				t.Fatalf("%s 的 %s 缺少 rank/reason：%+v", tierID, expected.field, detail)
			}
			found = true
		}
		if !found {
			t.Fatalf("明细里缺少 %s 的 %s：%+v", tierID, expected.field, details)
		}
	}
	// 每个代码都必须在稳定枚举里：契约与前端 i18n 都以该枚举为准。
	known := make(map[string]bool, len(trustlevels.ConfigErrorReasonCodes()))
	for _, code := range trustlevels.ConfigErrorReasonCodes() {
		known[code] = true
	}
	for _, detail := range details {
		if !known[detail.Code] {
			t.Fatalf("明细里出现未登记的 reasonCode %q", detail.Code)
		}
	}
	// 插值参数：图标名与长度上限这类动态值必须能被前端拿到。
	for _, detail := range details {
		if detail.Code == trustlevels.CodeIconNotWhitelisted && detail.Params["icon"] != "rocket" {
			t.Fatalf("icon 明细缺少插值参数 icon=rocket：%+v", detail)
		}
	}
	// 同一档的多个问题都要报（veteran 的 icon + 阈值负数 + resident 缺 limits），
	// 不要短路到第一条。M4a 后这份夹具不再含「earnable 白名单外」（解码期不判白名单），
	// 所以期望条数是 3。
	if len(details) < 3 {
		t.Fatalf("明细只报了 %d 条，同一档的多个字段应各自成条：%+v", len(details), details)
	}
	if got := trustlevels.ConfigErrorDetails(err); !reflect.DeepEqual(got, details) {
		t.Fatal("ConfigErrorDetails 的两次返回不一致（应给副本）")
	}

	// 上界：逐条构造超过上界的错误，明细不得无限增长。
	many := make([]string, 0, trustlevels.MaxConfigErrorDetails+4)
	for index := 0; index < trustlevels.MaxConfigErrorDetails+4; index++ {
		many = append(many, `{"id":"","name":{"zh":"坏","en":"Bad"},"icon":"rocket","requirements":{},"limits":{},"earnable":[]}`)
	}
	_, capped := trustlevels.DecodeConfig(`{"schemaVersion":2,"tiers":[` + strings.Join(many, ",") + `]}`)
	if got := len(trustlevels.ConfigErrorDetails(capped)); got != trustlevels.MaxConfigErrorDetails {
		t.Fatalf("明细条数 = %d，期望上界 %d", got, trustlevels.MaxConfigErrorDetails)
	}
	// 非结构化错误返回 nil，调用方回落到统一码。
	if trustlevels.ConfigErrorDetails(errors.New("boom")) != nil {
		t.Fatal("非结构化错误不该有明细")
	}
}

// TestConfigErrorReasonCodesAreStable 钉住明细代码表（D-N14 的契约）。
//
// 这份字面量与 OpenAPI 的 TrustLevelConfigErrorDetail.reasonCode enum、以及前端
// i18n 键一一对应。增删必须同批改三处；改名等于破坏兼容，所以这里写成字面量
// 而不是从另一个常量表反推（反推等于没断言）。
func TestConfigErrorReasonCodesAreStable(t *testing.T) {
	want := []string{
		"schema_version_unsupported",
		"schema_shape_invalid",
		"json_syntax_invalid",
		"json_type_mismatch",
		"unknown_field",
		"config_unparseable",
		"tail_content",
		"tier_count_out_of_range",
		"id_invalid",
		"id_reserved_for_tl0",
		"id_duplicate",
		"id_derivation_conflict",
		"icon_not_whitelisted",
		"color_not_whitelisted",
		"name_missing",
		"name_too_long",
		"requirement_negative",
		"requirement_unknown_metric",
		"limits_missing",
		"limits_unparseable",
		"limit_negative",
		"earnable_missing",
		"earnable_unparseable",
		"earnable_key_empty",
		"earnable_not_whitelisted",
		"earnable_invalid",
		"earnable_not_declared",
		"rule_too_complex",
		"rule_unknown_condition",
		"rule_shape_invalid",
		"rule_reference_invalid",
		"rule_condition_not_available",
		"level_unknown",
		"level_reserved_for_tl0",
		"level_duplicate",
		"level_legacy_missing",
		"deleted_tier_id_invalid",
		"deleted_tier_id_duplicate",
		"deleted_tier_id_present",
	}
	if got := trustlevels.ConfigErrorReasonCodes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("明细代码表变了（改契约请同批改 OpenAPI enum 与前端 i18n）：\n got=%v\nwant=%v", got, want)
	}
}

// TestDecodeConfigClassifiesStdlibErrors 钉住 classifyDecodeError 的三条分支。
//
// 它守的不是覆盖率，是一个**行为承诺**：stdlib 措辞变化时，错误只会降级到
// config_unparseable（更笼统），不会误标成某条更具体的码——误标比退化更糟，
// 前端会据此高亮错字段、给错修正建议。
func TestDecodeConfigClassifiesStdlibErrors(t *testing.T) {
	assertDetail := func(t *testing.T, raw, wantCode, wantField string) {
		t.Helper()
		_, err := trustlevels.DecodeConfig(raw)
		if err == nil {
			t.Fatal("非法配置被接受了")
		}
		details := trustlevels.ConfigErrorDetails(err)
		if len(details) != 1 {
			t.Fatalf("明细条数 = %d，期望 1：%+v", len(details), details)
		}
		if details[0].Code != wantCode {
			t.Fatalf("reasonCode = %q，期望 %q", details[0].Code, wantCode)
		}
		if details[0].Field != wantField {
			t.Fatalf("field = %q，期望 %q", details[0].Field, wantField)
		}
	}

	t.Run("未知字段", func(t *testing.T) {
		// 这条守的是「stdlib 措辞变化时只会降级到 config_unparseable，不会误标」：
		// unknown field 在 stdlib 是无类型 errorString，只能靠消息前缀识别；Go 换措辞
		// 后本断言会先红（落到 config_unparseable），而不是被误标成语法错/类型错。
		assertDetail(t, `{"schemaVersion":2,"tiers":[`+tierJSON("member")+`],"oops":1}`,
			trustlevels.CodeUnknownField, "")
	})

	t.Run("坏 JSON", func(t *testing.T) {
		// 这条守的是「stdlib 措辞变化时只会降级到 config_unparseable，不会误标」：
		// *json.SyntaxError 走结构化匹配、完全不依赖措辞；谁要是把它改成字符串匹配，
		// 这条会立刻红——那正是「从一个具体码悄悄换成另一个具体码」的开始。
		assertDetail(t, "not-json", trustlevels.CodeJSONSyntaxInvalid, "")
	})

	t.Run("字段类型不符", func(t *testing.T) {
		// 这条守的是「stdlib 措辞变化时只会降级到 config_unparseable，不会误标」，
		// 并且额外证明 UnmarshalTypeError.Field 真的被用上：field 必须指向出错字段，
		// 否则前端只能高亮整份配置（永远空串就等于结构化信息被丢了）。
		assertDetail(t, `{"schemaVersion":"two","tiers":[]}`, trustlevels.CodeJSONTypeMismatch, "schemaVersion")
	})
}

// coloredTierJSON 造一条带颜色的 v2 档位；color 传空串时省略该字段（= 跟随位次）。
func coloredTierJSON(id, color string) string {
	if color == "" {
		return tierJSON(id)
	}
	return `{"id":"` + id + `","name":{"zh":"自定义档","en":"Custom"},"icon":"leaf","color":"` + color +
		`","requirements":{},"limits":{},"earnable":[]}`
}

// TestDecodeConfigTierColorThreeStates 锁定颜色的三态：合法白名单值被原样保留、
// 非法值拒绝、省略保持空串（**不填默认**）。
//
// 第三态最容易写错：解码期一旦替运营填上默认色，之后插入档位时颜色就不再跟着位次
// 走，而是钉死在保存那一刻的 rank 上。
func TestDecodeConfigTierColorThreeStates(t *testing.T) {
	t.Run("合法值被保留", func(t *testing.T) {
		raw := tiersJSON(coloredTierJSON("member", "rose"))
		policy, err := trustlevels.DecodeConfig(raw)
		if err != nil {
			t.Fatalf("DecodeConfig: %v", err)
		}
		tier, ok := policy.TierAt(trustlevels.LevelMember)
		if !ok || tier.Color != "rose" {
			t.Fatalf("TL1 的 color = %q，期望 rose", tier.Color)
		}
		// 有效色取显式覆盖，而不是默认色序。
		if got := policy.ColorOf(trustlevels.LevelMember); got != "rose" {
			t.Fatalf("ColorOf(TL1) = %q，期望 rose", got)
		}
	})

	t.Run("省略保持空串", func(t *testing.T) {
		policy, err := trustlevels.DecodeConfig(builtinTiersJSON())
		if err != nil {
			t.Fatalf("DecodeConfig: %v", err)
		}
		for _, tier := range policy.Tiers {
			if tier.Color != "" {
				t.Fatalf("档位 %q 的 color 被填成了 %q（省略必须保持空串 = 跟随位次）", tier.ID, tier.Color)
			}
		}
	})

	t.Run("非法值带字段级明细", func(t *testing.T) {
		raw := tiersJSON(coloredTierJSON("member", "neon"))
		_, err := trustlevels.DecodeConfig(raw)
		if !errors.Is(err, trustlevels.ErrInvalidConfig) {
			t.Fatalf("非法颜色必须被拒绝，得到 %v", err)
		}
		details := trustlevels.ConfigErrorDetails(err)
		if len(details) == 0 {
			t.Fatal("没有字段级明细")
		}
		first := details[0]
		if first.Code != trustlevels.CodeColorNotWhitelisted || first.Field != "color" ||
			first.TierID != "member" || first.Rank != trustlevels.LevelMember {
			t.Fatalf("明细定位不对：%+v", first)
		}
		if color, _ := first.Params["color"].(string); color != "neon" {
			t.Fatalf("params.color = %+v，期望 neon", first.Params)
		}
	})
}

// TestDecodeConfigRejectsInvalidDeletedTierIDs 覆盖墓碑的四种非法形态。
//
// 四种都必须**拒绝**而不是静默修正：墓碑会被写进历史，一条对不上的墓碑等于让
// 「id 永不复用」的基准失真，而运营看不到任何提示。
func TestDecodeConfigRejectsInvalidDeletedTierIDs(t *testing.T) {
	deleted := func(ids ...string) string {
		quoted := make([]string, 0, len(ids))
		for _, id := range ids {
			quoted = append(quoted, `"`+id+`"`)
		}
		return `{"schemaVersion":2,"tiers":[` + tierJSON("member") + `],"deletedTierIds":[` +
			strings.Join(quoted, ",") + `]}`
	}
	cases := map[string]struct {
		raw  string
		code string
		id   string
	}{
		"不合法 id":  {deleted("tier_x"), trustlevels.CodeDeletedTierIDInvalid, "tier_x"},
		"保留给 TL0": {deleted(trustlevels.NewcomerID), trustlevels.CodeDeletedTierIDInvalid, trustlevels.NewcomerID},
		"重复":      {deleted("tier_6", "tier_6"), trustlevels.CodeDeletedTierIDDuplicate, "tier_6"},
		"与档位冲突":   {deleted("member"), trustlevels.CodeDeletedTierIDPresent, "member"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := trustlevels.DecodeConfig(testCase.raw)
			if !errors.Is(err, trustlevels.ErrInvalidConfig) {
				t.Fatalf("墓碑 %q 必须被拒绝，得到 %v", testCase.id, err)
			}
			details := trustlevels.ConfigErrorDetails(err)
			found := false
			for _, detail := range details {
				if detail.Code != testCase.code {
					continue
				}
				if detail.Field != "deletedTierIds" || detail.Rank != trustlevels.LevelNewcomer ||
					detail.TierID != testCase.id {
					t.Fatalf("墓碑明细定位不对：%+v", detail)
				}
				if id, _ := detail.Params["id"].(string); id != testCase.id {
					t.Fatalf("params.id = %+v，期望 %q", detail.Params, testCase.id)
				}
				found = true
			}
			if !found {
				t.Fatalf("明细里没有 %q：%+v", testCase.code, details)
			}
		})
	}
}

// TestDecodeConfigKeepsTombstonesInOrder 锁定墓碑的保序语义（重复是 422，不是去重）。
func TestDecodeConfigKeepsTombstonesInOrder(t *testing.T) {
	raw := `{"schemaVersion":2,"tiers":[` + tierJSON("member") + `],"deletedTierIds":["tier_9","tier_6"]}`
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	want := []string{"tier_9", "tier_6"}
	if !reflect.DeepEqual(policy.DeletedTierIDs, want) {
		t.Fatalf("DeletedTierIDs = %v，期望提交顺序 %v", policy.DeletedTierIDs, want)
	}
}

// TestDecodeConfigDerivedTierAvoidsTombstones 是「id 永不复用」的解码期证据：
// 删掉 tier_6 之后同一次保存里新建一档，派生出的 id 必须是 tier_7。
func TestDecodeConfigDerivedTierAvoidsTombstones(t *testing.T) {
	raw := `{"schemaVersion":2,"tiers":[` + tierJSON("member") + `,` + tierJSON("") +
		`],"deletedTierIds":["tier_6"]}`
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if len(policy.Tiers) != 2 || policy.Tiers[1].ID != "tier_7" {
		t.Fatalf("派生出的 id = %q，期望跳过墓碑 tier_6 用 tier_7", policy.Tiers[1].ID)
	}
}

// TestEncodeConfigOmitsNewFieldsWhenUnused 锁定回滚口径：没选颜色、没删过档时，
// 写出的 JSON 与引入这两个字段之前同形（旧二进制仍能解码）。
//
// 断言用「解析成 map 后看 key 是否存在」，而不是 strings.Contains：后者只要别处
// 出现同样字面量（例如墓碑 id、reason 文案）就会假阳性。
func TestEncodeConfigOmitsNewFieldsWhenUnused(t *testing.T) {
	encoded, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		t.Fatalf("编码结果不是合法 JSON：%v，%s", err, encoded)
	}
	if _, ok := config["deletedTierIds"]; ok {
		t.Fatalf("没删过档时不应出现 deletedTierIds 键：%s", encoded)
	}
	tiers, ok := config["tiers"].([]any)
	if !ok || len(tiers) == 0 {
		t.Fatalf("tiers 形状不对：%s", encoded)
	}
	for index, raw := range tiers {
		tier, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("第 %d 档不是对象：%s", index+1, encoded)
		}
		if _, ok := tier["color"]; ok {
			t.Fatalf("第 %d 档没选颜色，却写出了 color 键：%s", index+1, encoded)
		}
	}
	// 反向：真正用过时要写出来，否则「选了颜色但保存后消失」。
	policy := trustlevels.DefaultPolicy()
	policy.Tiers[0].Color = "rose"
	policy.DeletedTierIDs = []string{"tier_9"}
	encoded, err = trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		t.Fatalf("编码结果不是合法 JSON：%v，%s", err, encoded)
	}
	if _, ok := config["deletedTierIds"]; !ok {
		t.Fatalf("墓碑必须落字段：%s", encoded)
	}
	tiers = config["tiers"].([]any)
	if color, ok := tiers[0].(map[string]any)["color"]; !ok || color != "rose" {
		t.Fatalf("显式颜色必须落字段：%s", encoded)
	}
}

// TestTierColorPaletteCoversEveryValidRank 钉住「色板长度 = 合法 rank 数」。
//
// 这条防的是「改档数又撞色」：MaxTiers 一旦上调（或色板被删项），满配站点的顶档
// 就会回退到首项、与 TL0 同色。它比逐项断言更能防止将来只改一处。
func TestTierColorPaletteCoversEveryValidRank(t *testing.T) {
	if got, want := len(trustlevels.TierColors()), int(trustlevels.MaxLevel)+1; got != want {
		t.Fatalf("色板长度 = %d，期望 %d（rank 0..%d 一一对应；少一项顶档会与 TL0 撞色）",
			got, want, trustlevels.MaxLevel)
	}
	if got := trustlevels.DefaultTierColor(trustlevels.LevelNewcomer); got != "slate" {
		t.Fatalf("TL0 默认色 = %q，期望 slate", got)
	}
	// 满配 8 档时顶档必须拿到 amber（设计稿的「终点色」），而不是回退到 slate。
	if got := trustlevels.DefaultTierColor(trustlevels.MaxLevel); got != "amber" {
		t.Fatalf("TL%d 默认色 = %q，期望 amber（不能回退成 TL0 的 slate）", trustlevels.MaxLevel, got)
	}
}

// TestDecodeConfigAcceptsConfigWithoutNewFields 锁定存量 v2 值（无 color / 无墓碑）
// 仍能解码，且不会因此多出任何字段。
func TestDecodeConfigAcceptsConfigWithoutNewFields(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(builtinTiersJSON())
	if err != nil {
		t.Fatalf("存量 v2 值解码失败：%v", err)
	}
	if len(policy.Tiers) != 4 || len(policy.DeletedTierIDs) != 0 {
		t.Fatalf("形状不对：tiers=%d deleted=%v", len(policy.Tiers), policy.DeletedTierIDs)
	}
}

// TestDecodeConfigKeepsUndeclaredEarnableKeys 钉住 D24 修订的核心行为：
// **解码期不再硬拒未声明键**。
//
// 为什么这条必须存在：若保留旧硬拒，一次插件停用 → 整份 `trust.levels` 解码失败 →
// 装载路径按未解析处理（权限面等同全员 TL0，不是换一份默认策略）。那是灾难性失败，
// 不是保守。安全边界改由两处兜住：写期（`ValidateEarnableDeclared` → 422）与
// 运行期（`EarnableRegistry` 不授予）。
func TestDecodeConfigKeepsUndeclaredEarnableKeys(t *testing.T) {
	raw := `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":["user.ban","acme.shop.orders"]}]}`
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("解码期不该因未声明键失败（插件停用会让整站等级未解析）：%v", err)
	}
	if got := policy.Earnable.Permissions(trustlevels.LevelMember); len(got) != 2 {
		t.Fatalf("配置必须原样保留两枚键（否则运营重开插件后发现勾选被清了），得到 %v", got)
	}
	// 编码回去也必须原样带着它们（读一次存一次不改写运营的选择）。
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, key := range []string{"user.ban", "acme.shop.orders"} {
		if !strings.Contains(encoded, `"`+key+`"`) {
			t.Fatalf("编码丢了 %q：%s", key, encoded)
		}
	}
	// 运行期：两者都不授予（一个从未声明、一个插件未启用）。
	registry := trustlevels.DefaultEarnableRegistry()
	if granted := registry.FilterGranted([]string{"user.ban", "acme.shop.orders"}); len(granted) != 0 {
		t.Fatalf("未声明的键不该被授予，得到 %v", granted)
	}
}
