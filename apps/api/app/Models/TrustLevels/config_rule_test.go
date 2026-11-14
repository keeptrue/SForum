package trustlevels_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 本文件钉住 M1 的配置面：可选规则树的解码/校验/回写，以及「无规则配置逐字节不变」
// 这条回滚口径。错误码的**领域行为**在这里测；字段级 422 的传输面在控制器包里测。

// canonicalV2Config 是一份带可选字段（color / 停用档 / 删除墓碑）的合法配置，
// 用来证明「没有 rule 的配置经解码再编码后与引入 rule 之前逐字节相同」。
const canonicalV2Config = `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"正式用户","en":"Member"},"icon":"leaf","color":"teal","requirements":{"days_visited":3,"topic_count":5,"comment_count":0,"like_received_count":0,"like_given_count":0,"bookmark_count":0},"limits":{"topicCooldownSeconds":60,"commentCooldownSeconds":null,"dailyTopicLimit":10,"dailyCommentLimit":null},"earnable":["topic.create","post.create"],"retired":false},{"id":"tier_7","name":{"zh":"第七档","en":"Seventh"},"icon":"crown","requirements":{},"limits":null,"earnable":[],"retired":true}],"deletedTierIds":["tier_6"]}`

// canonicalV2Encoding 是上面那份输入在引入 rule **之前**的编码结果（HEAD 捕获）。
const canonicalV2Encoding = `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"正式用户","en":"Member"},"icon":"leaf","color":"teal","requirements":{"days_visited":3,"topic_count":5,"comment_count":0,"like_received_count":0,"like_given_count":0,"bookmark_count":0},"limits":{"topicCooldownSeconds":60,"commentCooldownSeconds":null,"dailyTopicLimit":10,"dailyCommentLimit":null},"earnable":["topic.create","post.create"],"retired":false},{"id":"tier_7","name":{"zh":"第七档","en":"Seventh"},"icon":"crown","requirements":{"days_visited":0,"topic_count":0,"comment_count":0,"like_received_count":0,"like_given_count":0,"bookmark_count":0},"limits":{"topicCooldownSeconds":null,"commentCooldownSeconds":null,"dailyTopicLimit":null,"dailyCommentLimit":null},"earnable":[],"retired":true}],"deletedTierIds":["tier_6"]}`

// defaultPolicyEncoding 是 `EncodeConfig(DefaultPolicy())` 在引入 rule **之前**的
// 逐字节输出（由 HEAD 的同一份代码捕获，见回报里的 golden 对比命令）。
//
// 它不是快照美学，而是**回滚口径的唯一证据**：一旦 rule 字段漏了 omitempty，或有人
// 给 TierConfig 加了一个非 omitempty 字段，这里立刻红——那份字节的变化意味着旧二进制
// 会整份拒绝「没配过规则」的配置。
const defaultPolicyEncoding = `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"正式用户","en":"Member"},"icon":"leaf","requirements":{"days_visited":3,"topic_count":0,"comment_count":5,"like_received_count":0,"like_given_count":0,"bookmark_count":0},"limits":{"topicCooldownSeconds":null,"commentCooldownSeconds":null,"dailyTopicLimit":null,"dailyCommentLimit":null},"earnable":[],"retired":false},{"id":"resident","name":{"zh":"常驻用户","en":"Resident"},"icon":"home","requirements":{"days_visited":15,"topic_count":2,"comment_count":20,"like_received_count":5,"like_given_count":3,"bookmark_count":0},"limits":{"topicCooldownSeconds":null,"commentCooldownSeconds":null,"dailyTopicLimit":null,"dailyCommentLimit":null},"earnable":[],"retired":false},{"id":"veteran","name":{"zh":"资深用户","en":"Veteran"},"icon":"mountain","requirements":{"days_visited":45,"topic_count":8,"comment_count":80,"like_received_count":30,"like_given_count":20,"bookmark_count":5},"limits":{"topicCooldownSeconds":null,"commentCooldownSeconds":null,"dailyTopicLimit":null,"dailyCommentLimit":null},"earnable":[],"retired":false},{"id":"leader","name":{"zh":"社区领袖","en":"Leader"},"icon":"crown","requirements":{"days_visited":100,"topic_count":25,"comment_count":250,"like_received_count":100,"like_given_count":80,"bookmark_count":20},"limits":{"topicCooldownSeconds":null,"commentCooldownSeconds":null,"dailyTopicLimit":null,"dailyCommentLimit":null},"earnable":[],"retired":false}]}`

// TestEncodeConfigWithoutRuleIsByteIdentical 是回滚口径的守门测试。
func TestEncodeConfigWithoutRuleIsByteIdentical(t *testing.T) {
	encoded, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		t.Fatalf("encode default: %v", err)
	}
	if encoded != defaultPolicyEncoding {
		t.Fatalf("默认策略的编码字节变了（旧二进制将无法解码未配规则的配置）：\n got=%s\nwant=%s", encoded, defaultPolicyEncoding)
	}

	policy, err := trustlevels.DecodeConfig(canonicalV2Config)
	if err != nil {
		t.Fatalf("decode canonical: %v", err)
	}
	roundTripped, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("encode canonical: %v", err)
	}
	if roundTripped != canonicalV2Encoding {
		// 输入本身不是规范化形状（requirements 省略字段、limits 用 null），编码会把它
		// 抹平——这份「抹平后的字节」必须与引入 rule 之前逐字节相同。
		t.Fatalf("规范化后的字节变了：\n got=%s\nwant=%s", roundTripped, canonicalV2Encoding)
	}
	if strings.Contains(roundTripped, `"rule"`) {
		t.Fatalf("无规则的配置写出了 rule 字段：\n%s", roundTripped)
	}
	// 规范化的固定点：再解一次再编一次必须与第一次相同（否则每次保存都在改库值）。
	again, err := trustlevels.DecodeConfig(roundTripped)
	if err != nil {
		t.Fatalf("decode round-tripped: %v", err)
	}
	againEncoded, err := trustlevels.EncodeConfig(again)
	if err != nil {
		t.Fatalf("encode again: %v", err)
	}
	if againEncoded != roundTripped {
		t.Fatalf("编码不是固定点：\n first=%s\nsecond=%s", roundTripped, againEncoded)
	}
}

// ---- 规则树的解码与校验 ----

// ruleDoc 把一棵树包成顶层文档（`{"v":1,"root":…}`）。
func ruleDoc(root string) string { return `{"v":1,"root":` + root + `}` }

// allOf 把若干子节点拼成 all 组。
func allOf(children ...string) string {
	return `{"op":"all","children":[` + strings.Join(children, ",") + `]}`
}

// verifiedLeaf 是零参数布尔条件：最小的合法叶子（布尔条件禁止带 cmp）。
const verifiedLeaf = `{"type":"identity.email_verified"}`

// topicCountLeaf 是带 window 的累计计数叶子（window 是该条件的必填参数）。
const topicCountLeaf = `{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":3}}`

// ruleTierJSON 造一条带规则（或空规则）的合法档位。
func ruleTierJSON(id, rule string) string {
	base := `{"id":"` + id + `","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":[]`
	if rule == "" {
		return base + `}`
	}
	return base + `,"rule":` + rule + `}`
}

// TestDecodeConfigAcceptsRuleTree 是正向对照组：合法规则通过，且**原样**回写。
func TestDecodeConfigAcceptsRuleTree(t *testing.T) {
	raw := ruleDoc(topicCountLeaf)
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", raw)))
	if err != nil {
		t.Fatalf("合法规则被拒：%v", err)
	}
	if got := string(policy.Tiers[0].Rule); got != raw {
		t.Fatalf("规则字节被改写：\n got=%s\nwant=%s", got, raw)
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(encoded, `"rule":`+raw) {
		t.Fatalf("编码丢了规则树或改了字节：\n%s", encoded)
	}
	// 固定点：再走一遍必须完全一致（否则每次保存都在改库值）。
	policy2, err := trustlevels.DecodeConfig(encoded)
	if err != nil {
		t.Fatalf("decode encoded: %v", err)
	}
	encoded2, err := trustlevels.EncodeConfig(policy2)
	if err != nil {
		t.Fatalf("encode2: %v", err)
	}
	if encoded2 != encoded {
		t.Fatalf("规则编码不是固定点：\n first=%s\nsecond=%s", encoded, encoded2)
	}
}

// TestDecodeConfigTreatsNullRuleAsAbsent 钉住三态：省略与显式 null 都是「无规则」。
//
// 显式 null 必须归一化成 nil，否则编码会写出 `"rule":null` —— 那与「没配规则」在
// 旧二进制眼里是两种东西（一个被拒、一个可读），回滚口径就被悄悄破坏了。
func TestDecodeConfigTreatsNullRuleAsAbsent(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", "null")))
	if err != nil {
		t.Fatalf("显式 null 规则被拒：%v", err)
	}
	if policy.Tiers[0].Rule != nil {
		t.Fatalf("显式 null 应归一化为 nil，得到 %q", string(policy.Tiers[0].Rule))
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(encoded, `"rule"`) {
		t.Fatalf("null 规则写出了 rule 键：\n%s", encoded)
	}
}

// TestDecodeConfigRuleRejections 逐条覆盖引擎侧的错误归类与两条域层自产码。
//
// 每行都断言三件事：422 载体（*ConfigErrors）、**精确** reasonCode、以及定位信息
// （`field == "rule"` + `params.path` 非空）。只断言「被拒」会退化成桶码验收：
// 把映射表整个删掉，那些用例照样绿。
func TestDecodeConfigRuleRejections(t *testing.T) {
	deepRoot := verifiedLeaf
	for i := 0; i < 5; i++ {
		deepRoot = allOf(deepRoot)
	}
	// 65 节点但只有 32 叶：31 组（每组 2 节点 + 1 叶）+ 1 组 = 65 节点 / 32 叶。
	nodesRoot := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		nodesRoot = append(nodesRoot, allOf(verifiedLeaf))
	}
	nodesTooMany := allOf(nodesRoot...)

	cases := []struct {
		name string
		rule string
		code string
	}{
		{"深度越界", ruleDoc(deepRoot), "rule_too_complex"},
		{"节点过多", ruleDoc(nodesTooMany), "rule_too_complex"},
		{"cmp 值越界", ruleDoc(`{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":0}}`), "rule_too_complex"},
		{"未知条件", ruleDoc(`{"type":"activity.nope"}`), "rule_unknown_condition"},
		{"子集外条件（content.hidden）", ruleDoc(`{"type":"content.hidden","cmp":{"op":"gte","value":1}}`), "rule_condition_not_available"},
		{"子集外条件（content.pinned）", ruleDoc(`{"type":"content.pinned","cmp":{"op":"gte","value":1}}`), "rule_condition_not_available"},
		{"空 all", ruleDoc(`{"op":"all","children":[]}`), "rule_shape_invalid"},
		{"计数条件缺 cmp", ruleDoc(`{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}}}`), "rule_shape_invalid"},
		{"布尔条件带 cmp", ruleDoc(`{"type":"identity.email_verified","cmp":{"op":"gte","value":1}}`), "rule_shape_invalid"},
		{"缺必填参数（window）", ruleDoc(`{"type":"activity.count","params":{"metric":"topic_count"},"cmp":{"op":"gte","value":3}}`), "rule_shape_invalid"},
		{"未知参数", ruleDoc(`{"type":"identity.email_verified","params":{"nope":1}}`), "rule_shape_invalid"},
		{"枚举取值非法", ruleDoc(`{"type":"activity.count","params":{"metric":"nope","window":{"kind":"all"}},"cmp":{"op":"gte","value":3}}`), "rule_shape_invalid"},
		{"比较符不允许（eq）", ruleDoc(`{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"eq","value":3}}`), "rule_shape_invalid"},
		{"版本不支持", `{"v":2,"root":` + verifiedLeaf + `}`, "rule_shape_invalid"},
		{"规则不是对象", `"a-string"`, "rule_shape_invalid"},
		{"root 类型错", `{"v":1,"root":123}`, "rule_shape_invalid"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", testCase.rule)))
			if err == nil {
				t.Fatalf("非法规则被接受：%s", testCase.rule)
			}
			details := trustlevels.ConfigErrorDetails(err)
			if len(details) == 0 {
				t.Fatalf("缺少字段级明细：%v", err)
			}
			detail := details[0]
			if detail.Code != testCase.code {
				t.Fatalf("reasonCode = %q（期望 %q）；reason=%s", detail.Code, testCase.code, detail.Reason)
			}
			if detail.Field != "rule" {
				t.Fatalf("field = %q（期望 rule）", detail.Field)
			}
			if path, _ := detail.Params["path"].(string); path == "" {
				t.Fatalf("params.path 缺失（前端无法定位到节点）：%v", detail.Params)
			}
			if detail.Rank != 1 || detail.TierID != "member" {
				t.Fatalf("定位丢失：rank=%d tierId=%q", detail.Rank, detail.TierID)
			}
		})
	}
}

// TestPerTierRuleLimitsAreNotAggregated 钉住 D19-b：上限**只按单档**判，不设合计上限。
//
// 8 档 × 32 叶 = 256 叶。若有人「顺手」按合计判 `MaxLeaves`，这份配置会被拒并且要
// 凭空多造一个错误码——而每一档其实都合法。这条断言的作用是把这个决定钉成行为，
// 不是注释里的君子协定。
func TestPerTierRuleLimitsAreNotAggregated(t *testing.T) {
	leaves := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		leaves = append(leaves, verifiedLeaf)
	}
	tree := ruleDoc(allOf(leaves...))
	tiers := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		tiers = append(tiers, ruleTierJSON("", tree))
	}
	policy, err := trustlevels.DecodeConfig(tiersJSON(tiers...))
	if err != nil {
		t.Fatalf("8 档各 32 叶应当逐档合法，被拒：%v（若报 rule_too_complex，说明有人加了跨档合计上限）", err)
	}
	if len(policy.Tiers) != 8 {
		t.Fatalf("档位数 = %d（期望 8）", len(policy.Tiers))
	}
	// 单档越界仍然要拒：按档校验这条不能因为「不再判合计」而松掉。
	over := make([]string, 0, 33)
	for i := 0; i < 33; i++ {
		over = append(over, verifiedLeaf)
	}
	if _, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", ruleDoc(allOf(over...))))); err == nil {
		t.Fatal("单档 33 叶必须被拒（MaxLeaves=32）")
	}
}

// TestRuleConditionSubset 钉住子集裁剪：共享目录有、等级用不上。
func TestRuleConditionSubset(t *testing.T) {
	subset := trustlevels.RuleConditions()
	shared := conditions.Core().Definitions()
	// 子集 = 共享目录 − 2。今天共享目录 14 条（含 `identity.profile`），所以子集是 **12**。
	// 第一条按「共享 − 2」断而不是写死数字：加条件时它自动跟着走，不需要再来改一次
	// （改数字的测试等于没测试）。第二条写死的数字是**今日快照**：共享目录变了而子集
	// 没跟着变（例如误把新条件塞进排除清单）时它才红。
	if len(subset) != len(shared)-2 {
		t.Fatalf("子集应有 %d 条（共享 %d − 2），得到 %d", len(shared)-2, len(shared), len(subset))
	}
	if len(subset) != 12 {
		t.Fatalf("今日子集应为 12 条（共享 14 − 内容质量 2），得到 %d", len(subset))
	}
	for _, excluded := range []string{"content.pinned", "content.hidden"} {
		if trustlevels.RuleConditionAllowed(excluded) {
			t.Fatalf("%q 不该在信任等级子集里", excluded)
		}
		if _, ok := trustlevels.SharedCondition(excluded); !ok {
			t.Fatalf("%q 必须仍在共享目录里（徽章继续用）", excluded)
		}
	}
	if _, ok := trustlevels.SharedCondition("activity.nope"); ok {
		t.Fatal("activity.nope 不该在共享目录里")
	}
	if !trustlevels.RuleConditionAllowed("activity.count") {
		t.Fatal("activity.count 必须在子集里")
	}
	if !trustlevels.RuleConditionAllowed("identity.profile") {
		t.Fatal("identity.profile 必须进子集：新条件默认可用，被排除的只有内容质量那两条")
	}
}

// TestRuleJSONIsOrdinaryObject 用 encoding/json 复核 wire 形状（不是字符串包含断言）。
func TestRuleJSONIsOrdinaryObject(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", ruleDoc(topicCountLeaf))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var document struct {
		Tiers []struct {
			Rule json.RawMessage `json:"rule"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatalf("编码结果不是合法 JSON：%v", err)
	}
	if len(document.Tiers) != 1 || len(document.Tiers[0].Rule) == 0 {
		t.Fatalf("rule 不是对象而是被字符串化：%s", encoded)
	}
	if !bytes.Contains(document.Tiers[0].Rule, []byte(`"root"`)) {
		t.Fatalf("rule 内容不是规则文档：%s", string(document.Tiers[0].Rule))
	}
}

// TestRetiredTierRuleRoundTrip 钉住「停用档的规则真的被保留」（N1）。
//
// 「不报错」不等于「保留」：`retired` 档位不参与判定，但配置必须原样留在 JSON 里——
// 运营恢复该档时不该发现自己配的规则没了（D-N2 的承诺）。所以判据是 encode → decode
// 之后 `Rule` 仍在、且**逐字节相同**。
func TestRetiredTierRuleRoundTrip(t *testing.T) {
	rule := ruleDoc(topicCountLeaf)
	retiredTier := `{"id":"tier_7","name":{"zh":"停用档","en":"Retired"},"icon":"crown","requirements":{},"limits":{},"earnable":[],"retired":true,"rule":` + rule + `}`
	policy, err := trustlevels.DecodeConfig(tiersJSON(ruleTierJSON("member", ""), retiredTier))
	if err != nil {
		t.Fatalf("停用档带规则被拒：%v", err)
	}
	retired := retiredTierOf(t, policy)
	if string(retired.Rule) != rule {
		t.Fatalf("解码后停用档规则被改写：\n got=%s\nwant=%s", string(retired.Rule), rule)
	}

	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(encoded, `"rule":`+rule) {
		t.Fatalf("编码丢了停用档的规则树：\n%s", encoded)
	}
	// round-trip：再解一次，规则仍逐字节相同（「读一次再存一次」不得改写它）。
	again, err := trustlevels.DecodeConfig(encoded)
	if err != nil {
		t.Fatalf("decode encoded: %v", err)
	}
	if string(retiredTierOf(t, again).Rule) != rule {
		t.Fatalf("round-trip 后停用档规则变了：\n got=%s\nwant=%s", string(retiredTierOf(t, again).Rule), rule)
	}
	// 对照组：新建/启用档位不受影响（停用档的规则不能串到别的档上）。
	for _, tier := range again.Tiers {
		if !tier.Retired && len(tier.Rule) != 0 {
			t.Fatalf("启用档 %q 被写入了规则：%s", tier.ID, string(tier.Rule))
		}
	}
}

// retiredTierOf 取出唯一那条停用档。
func retiredTierOf(t *testing.T, policy trustlevels.Policy) trustlevels.Tier {
	t.Helper()
	for _, tier := range policy.Tiers {
		if tier.Retired {
			return tier
		}
	}
	t.Fatal("策略里没有停用档")
	return trustlevels.Tier{}
}
