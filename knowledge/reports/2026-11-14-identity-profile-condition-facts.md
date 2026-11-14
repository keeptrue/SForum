# `identity.profile` 条件落点核实（只读，无代码改动）

- 核实时间：2026-11-14
- 基线 HEAD：`5a1d4083f`（`git rev-parse --short HEAD` → `5a1d4083f`；工作树只有 `.dsh/`、`work-team/` 未跟踪）
- 任务：task-4（`R：identity.profile 条件落点核实`）
- 性质：**只读核实 + 方案**。本文件不产生任何 production 改动，行号全部以 `5a1d4083f` 为准。
- 证据规则：每条事实带「命令 + 输出片段」或「文件:行号 + 原文摘录」。凡我未实测的推断，一律标 `TODO(核实)` 并写明核实方式。

---

## 0. 结论速览（实施方照这个做）

1. **它确实从未实现**。`Models/Conditions/registry.go` 的全部 `Key:` 只有 13 处，无 `identity.profile`；全仓 `rg -n 'identity\.profile'` 去掉 `knowledge/`、`work-team/` 与 `fixture.identity.profile` 权限夹具之后，代码侧只剩 `Models/TrustLevels/config_rule_test.go:250-258` 的注释与 fatal 文案提到它，而那里自己写着「还没落地」。
2. **落点是 4 个生产文件 + 1 个新解析器 + 1 个分派 case**：`registry.go`（常量块 + 定义）、`phrases.go`（名词表 + 句子函数）、`resolve_identity.go`（新 loader + 一条 SQL）、`batch.go:160`（type 分派）。
3. **必改断言共 4 处硬编码**（不是任务描述里的 2 处）：
   - `requirement_text_test.go:168`（13 → 14）+ `:55` 的 `phraseSamples()` 补样本；
   - `conditions_postgres_integration_test.go:264`（13 → 14）；
   - **`rule_catalog_test.go:61`（6 → 7）**——这条最容易被漏，因为它数的是「不依赖候选的条件有几条」；
   - **`config_rule_test.go:257`（11 → 12）**——信任子集。
   其余条数断言都是**相对式**（`len(shared)-2` / `len(shared)-3` / `subset-1` / `len(definitions)-1`），加了条件自动跟随，**不要改**（改了等于把「自动跟随」退化成两处真值源）。
4. **四值枚举不需要裁决**：枚举四值 `bio` / `signature` / `website` / `location`，`website` 是**契约字面量**、列是 `user_profiles.website_url`。impl-facts 里「三值 / 三列」的措辞是 v1 残留，已被 `copy-spec §0.1 Q5`（含删除记录与能红断言）与 `plan §3.3:143` 覆盖。详见 §6。
5. **判据必须是非空串**，且**「资料行不存在」必须判 `false` 而不是 `incomplete`**——今天没有注册期插行的逻辑（资料行按需 upsert），所以「没有行」是常态而非异常。这条是本次核实里最容易写错的地方，见 §5.3。
6. **OpenAPI 零改动**，**前端 `badgeRule.ts` 零改动**（8 行纯 re-export）。但**下拉文案有缺口**：`field` 的四个取值在 locale 里没有文案，编辑器会显示裸 slug；补文案会撞 `adminBadgesI18n.test.ts` 的双向断言，必须同批登记枚举（见 §4 第 8 条）。

---

## 1. 事实与证据

### F1｜注册块确实只有 13 条，且无 `identity.profile`

```
$ rg -n 'Key:' apps/api/app/Models/Conditions/registry.go
125:		Key:            def.Key,
217:			Key:       KeyActivityCount,
230:			Key:       KeyActivityStreak,
242:			Key:       KeyObjectEngagement,
253:			Key:       KeyObjectAuthoring,
267:			Key:       KeyIdentityAccountAge,
276:			Key:       KeyIdentityEmailVerify,
283:			Key:       KeyIdentityOAuthLink,
293:			Key:       KeyIdentityRole,
303:			Key:       KeyIdentityAvatar,
310:			Key:       KeyContentPinned,
319:			Key:       KeyContentHidden,
328:			Key:       KeyBadgeOwned,
338:			Key:       KeyFactCount,
```

13 条注册（`:125` 是 `ConditionMeta` 构造，不是注册项）：`activity.count`、`activity.streak`、`object.engagement`、`object.authoring`、`identity.account_age`、`identity.email_verified`、`identity.oauth_link`、`identity.role`、`identity.avatar`、`content.pinned`、`content.hidden`、`badge.owned`、`fact.count`。

### F2｜key 常量块与注册块的确切插入位置

**命名撞车预警（会误导核对）**：`identity.profile` 这个字符串在代码里已经存在，但它**不是条件**，而是 Identity 的 provider handler id：

```
$ rg -n 'identity\.profile' apps/api --glob '*.go' | grep -v fixture
apps/api/app/Models/Identity/profile_provider_composer_test.go:104:  Handler: "identity.profile", Priority: priority, Operations: ops,
apps/api/app/Support/Extensions/manager_identity_transport_test.go:710:  Kind: identityregistry.ProviderKindProfile, Handler: "identity.profile",
apps/api/app/Models/TrustLevels/config_rule_test.go:250/251/258   ← 注释与 fatal 文案
```

→ 别用「`rg identity\.profile` 归零」当完成判据（永远归不了零）。正确的完成判据是 `rg -n 'KeyIdentityProfile' apps/api` 或 `conditions.Core().Definition("identity.profile")` 命中。两个命名空间互不冲突（条件 key vs provider handler），不需要改名。

`registry.go:22-38` 是常量块：

```go
22: const (
23: 	KeyActivityCount       = "activity.count"
...
30: 	KeyIdentityRole        = "identity.role"
31: 	KeyIdentityAvatar      = "identity.avatar"
32: 	KeyContentPinned       = "content.pinned"
...
37: 	KeyFactCount = "fact.count"
38: )
```

- **常量插在 `:31` 之后、`:32` 之前**（`KeyIdentityProfile = "identity.profile"`），保持 identity 族连续；插在 `:37`（`KeyFactCount`，带自己的文档注释块 `:35-36`）之后会把注释与常量拆开。
- 注册块 `defs := []Definition{` 在 `:215`；`identity.avatar` 的定义是 `:302-308`（`:308` 为 `},`），`content.pinned` 从 `:309` 开始。

```
$ sed -n '302,310p' apps/api/app/Models/Conditions/registry.go
		{
			Key:       KeyIdentityAvatar,
...
		},
		{
			Key:       KeyContentPinned,
```

**新定义插在 `:308` 与 `:309` 之间**（identity 族末尾、content 族之前），与常量块顺序一致。

### F3｜`phrases.go` 的插入位置与文件头计数

- 文件头 `:11` 原文：`// 本文件是 13 种条件的「运营口吻句子」。它是**唯一实现**：公开面与后台预览端点` → **13 改 14**。
- `phraseIdentityAvatar` 在 `:233-236`，`content.pinned` 的注释从 `:238` 开始。**新名词表 + 句子函数插在 `:236` 与 `:238` 之间**。
- 现有 `paramString`（`:115-118`）与 `fmt`/`strings` 已 import（`:3-9`），不需要新增 import。

### F4｜解析器真实文件名：`resolve_identity.go`（任务描述里的猜测正确），但它是「五个」

`resolve_identity.go:11-27`：

```go
11: // loadIdentity 处理五个身份类条件。它们各自是「一次查询」，按 type 分派。
12: func (r *Resolver) loadIdentity(ctx context.Context, typ string, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
13: 	switch typ {
14: 	case KeyIdentityAccountAge:
...
22: 	case KeyIdentityAvatar:
23: 		return r.loadAvatar(ctx, leaves)
24: 	default:
25: 		return nil, fmt.Errorf("conditions: 未知身份条件 %q", typ)
26: 	}
27: }
```

- `:11` 注释「五个」→ **六**。
- **新 case 插在 `:23` 之后、`default`(`:24`) 之前**。
- 同文件已有 `avatarSQL`（`:204-209`）作为「同表 `user_profiles` + `EXISTS`」的先例，新 SQL 紧邻其后（`loadAvatar` 结束于 `:225`，文件共 225 行）。

### F5｜type 分派在 `batch.go`，identity 是一个合并 case

```
$ rg -n 'KeyIdentityAccountAge' apps/api/app/Models/Conditions/batch.go
160:	case KeyIdentityAccountAge, KeyIdentityEmailVerify, KeyIdentityOAuthLink, KeyIdentityRole, KeyIdentityAvatar:
161:		return r.loadIdentity(ctx, typ, leaves)
```

**`:160` 的 case 列表要加 `KeyIdentityProfile`**。漏这一处的后果：`Prepare` 走 `:168 default` → `conditions: 未知条件 %q` → 所有含该条件的规则在求值期变成 unknown（fail-closed，但功能等于没做），而 `Validate` 却认为合法（`Catalog` 认它）——**「保存成功、永不成立、零报错」**，正是这个仓库反复踩的那类 bug。

### F6｜`user_profiles` 的列名与列定义原文

`apps/api/database/migrations/202607070004_user_profiles.sql:3-12`：

```sql
CREATE TABLE user_profiles (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  bio TEXT NOT NULL DEFAULT '',
  signature TEXT NOT NULL DEFAULT '',
  location TEXT NOT NULL DEFAULT '',
  website_url TEXT NOT NULL DEFAULT '',
  avatar_attachment_id BIGINT REFERENCES attachments(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- 列名是 **`website_url`**，不是 `website`。
- 四列全部 **`TEXT NOT NULL DEFAULT ''`**：因此 `IS NOT NULL` 恒真、`= ''` 是唯一有意义的判据。
- 全仓对 `user_profiles` 没有后续 `ALTER TABLE ... ADD COLUMN`：

```
$ rg -n 'user_profiles' apps/api/database/migrations/*.sql -A2 | rg 'ADD COLUMN|ALTER TABLE user_profiles'
（无输出）
```

其余命中只有 `202610180001_stable_core_views_e10.sql:76-78`（视图 LEFT JOIN）与 `202607070004` 本条（`:15` 索引、`:19-20` Down）。

### F7｜四列都是资料编辑器的真实写入口（`location` 也在）

```
$ rg -n 'Location|Website' apps/api/app/Models/Profile/postgres_store.go apps/api/app/Http/Controllers/Profile/*.go
apps/api/app/Models/Profile/postgres_store.go:55:	`, input.UserID, input.Bio, input.Signature, input.Location, input.WebsiteURL, nullableInt64(input.AvatarAttachmentID))
apps/api/app/Http/Controllers/Profile/controller.go:38:	Location           *string `json:"location"`
apps/api/app/Http/Controllers/Profile/controller.go:39:	WebsiteURL         *string `json:"websiteUrl"`
```

资料更新 SELECT/INSERT 的列表（`apps/api/app/Models/Profile/postgres_store.go:29` / `:45`）都是 `bio, signature, location, website_url` 四列齐出。→ 「`location` 与其他三列同表同机制」成立，`location` 不是新增负担。

### F8｜**没有注册期插行**：资料行可能不存在

```
$ rg -n 'INSERT INTO user_profiles' apps/api --glob '*.go'
apps/api/app/Models/Identity/postgres_store_admin_users.go:501
apps/api/app/Models/Conditions/conditions_object_identity_integration_test.go:117
apps/api/app/Models/Profile/postgres_store.go:45      ← Update 的 upsert
apps/api/app/Models/Profile/postgres_store.go:136     ← 只写 avatar 的分支
apps/api/app/Models/Forum/reference_selector_postgres_test.go:126
apps/api/app/Support/HostAPI/v2_query_postgres_integration_test.go:364
```

迁移自己的注释（`202607070004_user_profiles.sql:14`）：

```
-- 每个用户注册后应有一条资料行；先建表，资料行在首次读取/更新时按需 upsert。
```

实际实现是**按需 upsert**，没有注册钩子。`TestIdentityConditions` 就是活证据：`conditions_object_identity_integration_test.go:117` 才第一次插 `user_profiles` 行，`:67-116` 全部在该行不存在的前提下跑。→ **「无资料行」是常态分支，不是异常**。

### F9｜`rule_impact.go` 的估算是 `default → 估不了`，不是硬编码清单

```
$ sed -n '200,205p' apps/api/app/Models/Badges/rule_impact.go
	case conditions.KeyObjectAuthoring, conditions.KeyObjectEngagement, conditions.KeyIdentityOAuthLink, conditions.KeyActivityStreak, conditions.KeyFactCount:
		// 这些形状的人数需要「按窗口/按对象」的额外语义，M1 不给估算：
		// 返回「估不了」比给一个偏差很大的数字安全（运营会拿它对账）。
		return 0, false, nil
	default:
		return 0, false, nil
```

→ `identity.profile` 落进 `default`，语义是「估不了」（`(0, false, nil)`），**不会 panic、不会被误当成 0 人**。属于「不阻塞但要知道」的一条，见 §9-1。

### F10｜另外三处按 key 的 switch 都有 `default` 兜底，无需为 `identity.profile` 加 case

- `Models/Badges/rule_store.go:45-107`（保存期引用存在性）：`:107-108` 是 `default: continue`。`identity.profile` 的参数是枚举不是 `objectRef`，本来就没有「存在性」要查。
- `Models/Badges/rule_health.go:149-219`（失效引用体检）：switch 无 `default`，未命中即落到 `:220` 的 `return nil`，不产生 issue。
- `Models/Badges/ref_names.go:78-101`（真名预取）：switch 无 `default`，未命中不收集任何引用。

→ 这三处**不需要改**，但实施单应当把「我们看过、靠 default 兜住」写进去，否则下一个人会以为是漏了。

### F11｜OpenAPI 里没有任何条件 key 的 enum

```
$ rg -n '"(activity|identity|object|content|badge|fact)\.[a-z_]+"' contracts/
（无输出）
```

两个契约面把 `key` 都声明成自由字符串：

- `contracts/openapi/schemas/badges.yaml:1365-1366`（`BadgeCondition.key`，`required` 里也有 `key`，`:1358`）：`key: {type: string}`，无 enum；`params[].options: {type: array, items: {type: string}}`（`:1344-1350`）——枚举取值由**服务端下发**，契约不枚举。
- `contracts/openapi/schemas/trust_levels.yaml:573-575`（`TrustLevelCondition.key`）：`key: {type: string}`；`options` 的说明（`:556-563`）明确「空数组 = 关闭集，缺席 = 开放集」。

→ **OpenAPI 不需要加 enum，也不需要改任何 `$ref`**（`ruby scripts/validate-openapi-refs.rb` 自然绿；本次不改契约，无需跑）。

### F12｜前端 `badgeRule.ts` 是 8 行 re-export，枚举由服务端下发

```
$ wc -l apps/web/app/utils/admin/badgeRule.ts apps/web/app/utils/admin/badgeRuleParams.ts
   8 apps/web/app/utils/admin/badgeRule.ts
 196 apps/web/app/utils/admin/badgeRuleParams.ts
```

`badgeRuleParams.ts:44` 原文：`options?: string[]`，`:40-43` 注释「wire 上就是裸字符串数组…选项文案不随数据下发」。全前端 `rg -n "'(bio|signature|website|location)'"` → **零命中**（exit 1）。→ 参数候选无需前端改动，与计划 §3.3「前端 `badgeRule.ts` 的 `enum` 候选无需改」一致。

---

## 2. 落点清单（逐文件逐行，可直接照做）

| # | 文件 | 位置（HEAD `5a1d4083f`） | 动作 |
| --- | --- | --- | --- |
| 1 | `apps/api/app/Models/Conditions/registry.go` | 常量块 `:22-38`，插在 `:31`（`KeyIdentityAvatar`）与 `:32`（`KeyContentPinned`）之间 | `KeyIdentityProfile = "identity.profile"` |
| 2 | 同上 | `:215` 的 `defs` 里，插在 `:308`（avatar 定义闭合 `},`）与 `:309`（`{` of content.pinned）之间 | 新增 `Definition`（见下） |
| 3 | `apps/api/app/Models/Conditions/phrases.go` | `:11` 文件头注释 | `13 种` → `14 种` |
| 4 | 同上 | 插在 `:236`（`phraseIdentityAvatar` 闭合）与 `:238`（`phraseContentPinned` 注释）之间 | `profileFieldPhrases` + `phraseIdentityProfile` |
| 5 | `apps/api/app/Models/Conditions/resolve_identity.go` | `:11` 注释「五个」→「六个」；`:13-26` switch 里 `:23` 之后加 case | `case KeyIdentityProfile: return r.loadProfile(ctx, leaves)` |
| 6 | 同上 | `loadAvatar` 结束于 `:225`（文件末尾）之后追加；SQL 常量紧邻 `avatarSQL`（`:204-209`）的写法 | `profileFilledSQL` + `loadProfile` |
| 7 | `apps/api/app/Models/Conditions/batch.go` | `:160` 的合并 case 列表 | 加 `KeyIdentityProfile` |

### 2.1 `Definition`（逐字来自 `copy-spec §2.4`，字段顺序照 `registry.go` 既有条目）

```go
{
	Key:       KeyIdentityProfile,
	Phrase:    phraseIdentityProfile,
	ValueKind: ruleengine.ValueBoolean,
	Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
		{Name: "field", Kind: ruleengine.ParamEnum, Required: true,
			Options: []string{"bio", "signature", "website", "location"}},
	}},
	LabelZh: "资料已填写", LabelEn: "Profile filled in",
	DescribeZh: "资料里的简介、签名、个人网站或所在地是否已填写。填了就算，不看内容质量。", DescribeEn: "Whether the profile bio, signature, website or location is filled in. Any content counts; quality is not judged.",
},
```

- **无 `Compare`、无 `Unit*`**：布尔条件。这不是风格问题，`conditions_postgres_integration_test.go:274-276` 会直接判红：
  ```
  if def.ValueKind == ruleengine.ValueBoolean && len(def.Compare) != 0 {
      t.Fatalf("%s 是布尔条件但声明了 compare", def.Key)   // :275
  ```
- 四个取值**不排序、按 copy-spec 给定顺序**（`bio`/`signature`/`website`/`location`）：`options` 的顺序就是编辑器下拉顺序，且 golden 断言按取值取样本。

### 2.2 `phrases.go`（逐字来自 `copy-spec §2.4`）

```go
// profileFieldPhrases 是 identity.profile 的名词表。
// 与 authoringScopeEn 同一个理由：名词来自文案表，不来自参数取值。
var profileFieldPhrases = map[string][2]string{
	"bio":       {"简介", "bio"},
	"signature": {"签名", "signature"},
	"website":   {"个人网站", "website"},
	"location":  {"所在地", "location"},
}

func phraseIdentityProfile(params map[string]any, _ *ruleengine.Compare, _ RefNames) (string, string) {
	field := paramString(params, "field")
	labels, ok := profileFieldPhrases[field]
	if !ok {
		labels = [2]string{"资料", "profile"}
	}
	return fmt.Sprintf("已填写「%s」", labels[0]),
		fmt.Sprintf("\"%s\" filled in", labels[1])
}
```

- 中文名用 **「所在地」**（不是「地区」）：与 `admin.users.location`、`profileSettings.scope.publicFields` 同一个词。
- 未命中回落 `资料 / profile` 而非拼 slug —— 与 `phraseActivityCount`（`:149-151`）同款纪律。
- **不会撞 `rawSlugPattern`**：该正则（`Models/Badges/requirement_text_test.go:24-27`）覆盖的是 `topic_count|minDays|targetSlug|…|(activity|identity|object|content|badge|fact)\.[a-z_]+`，`bio`/`signature`/`website`/`location` 均不在其中；`astVocabularyPattern`（`:21`）是 `cmp|params|children|op|rule|type|metric|window|slug`，也不命中。

### 2.3 解析器（**这是本次唯一需要设计、不能照抄的部分**）

一条 SQL 覆盖四个取值，形状照 `oauthLinkSQL`（`:102-109`）/ `roleSQL`（`:155-162`）的 `unnest + EXISTS` 先例：

```go
// profileFilledSQL：资料行的四个文本列都是 NOT NULL DEFAULT ''，所以判据只能是
// 「非空串」——IS NOT NULL 对它们恒真，等于每个用户都满足。
//
// 用 LEFT 语义（子查询无行 → NULL）而不是直接 SELECT ... FROM user_profiles：
// 资料行是按需 upsert 的，没有行是常态（见迁移 202607070004 的说明），
// 那种情况必须判 false，不能让 QueryRow 的 ErrNoRows 变成「查不到 → unknown」。
// CASE 不写 ELSE，未知 field 得到 NULL，由调用方转成 error（fail-closed）：
// 静默 false 会让 `not` 子树反转成 true，那是 fail-open。
const profileFilledSQL = `
SELECT field,
       (SELECT CASE field
                 WHEN 'bio'       THEN profiles.bio        <> ''
                 WHEN 'signature' THEN profiles.signature  <> ''
                 WHEN 'website'   THEN profiles.website_url <> ''
                 WHEN 'location'  THEN profiles.location   <> ''
               END
          FROM user_profiles AS profiles
         WHERE profiles.user_id = $1)
FROM unnest($2::text[]) AS candidates(field)
`
```

```go
func (r *Resolver) loadProfile(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	fields := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		fields = append(fields, stringParam(leaf, "field"))
	}
	rows, err := r.pool.Query(ctx, profileFilledSQL, r.userID, fields)
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 identity.profile 失败：%w", err)
	}
	defer rows.Close()

	filled := make(map[string]bool, len(fields))
	for rows.Next() {
		var field string
		var hit *bool // 无资料行 / 未知取值 → NULL
		if err := rows.Scan(&field, &hit); err != nil {
			return nil, fmt.Errorf("conditions: 读取 identity.profile 结果失败：%w", err)
		}
		if hit == nil {
			filled[field] = false // 无资料行：确定不满足
			continue
		}
		filled[field] = *hit
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 identity.profile 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		field := stringParam(leaf, "field")
		hit, ok := filled[field]
		if !ok {
			return nil, fmt.Errorf("conditions: identity.profile 缺少 %q 的结果", field)
		}
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = boolValue(hit)
	}
	return out, nil
}
```

> ⚠️ 上面 `hit == nil → false` 与「未知 field → error」是**两个不同判断**，实现时不要合并：
> 无行 = 用户确实没资料 = 确定 false；未知 field = 规则本身脏 = 必须 unknown。
> 但两者在 SQL 里都表现为 NULL，所以**要么**在 Go 侧先按 `profileFieldPhrases` 白名单过滤未知 field 并直接返 error，**要么**把 `CASE` 换成四次 `EXISTS`（未知 field 查不到行 → `ok == false` → error）。二选一，别留「NULL 一律 false」。
> 我给的这版把「无行」的 NULL 和「未知 field」的 NULL 混在一起了——**这就是它需要一次 review 的原因**，实施方必须显式处理。标 `TODO(核实)`：`*bool` 扫描在 pgx v5 下对 `unnest` 结果是否按预期给出 nil，实施时先用一条 unit 测试钉住（`SELECT NULL::boolean` 扫描）。

更稳的写法（推荐，去掉 NULL 二义性）：列白名单 + 每列一次 `coalesce`：

```go
const profileFieldSQL = `
SELECT coalesce((SELECT bio        <> '' FROM user_profiles WHERE user_id = $1), false),
       coalesce((SELECT signature  <> '' FROM user_profiles WHERE user_id = $1), false),
       coalesce((SELECT website_url <> '' FROM user_profiles WHERE user_id = $1), false),
       coalesce((SELECT location   <> '' FROM user_profiles WHERE user_id = $1), false)
`
```

一次取回四个 bool，叶子按 `field` 从四个里挑；未知 `field` 显式返 error。SQL 条数仍是「每 type 一条」，不破 `Prepare` 的「查询数 = type 数」口径（`batch.go:78-104` 每 type 只调一次 `r.load`）。

### 2.4 `batch.go:160`

```go
case KeyIdentityAccountAge, KeyIdentityEmailVerify, KeyIdentityOAuthLink, KeyIdentityRole, KeyIdentityAvatar, KeyIdentityProfile:
```

---

## 3. 必改的同步点（漏一处即红）

「红」指**测试或门禁变红**，不是「最好改一下」。

| # | 文件:行号 | 现值 | 改成 | 红了会说什么 |
| --- | --- | --- | --- | --- |
| 1 | `apps/api/app/Models/Badges/requirement_text_test.go:168` | `if len(definitions) != 13` | `!= 14` | `条件目录应为 13 种，得到 14 种` |
| 2 | 同上 `:169` | `"条件目录应为 13 种"` | `14 种` | 同一条 fatal 的文案 |
| 3 | 同上 `:55` `phraseSamples()` | 14 个样本覆盖 13 个 type（`activity.count` 两项） | 加第 15 个样本 `identity.profile/location` | `条件 %q 没有 golden 样本：新增条件必须同时补样例句子`（`:186`） |
| 4 | 同上 `:51` / `:195` 注释 | `13 种条件` | `14 种条件` | 不红，但会误导下一个人（措辞纪律） |
| 5 | `apps/api/app/Models/Conditions/conditions_postgres_integration_test.go:264` | `!= 13` | `!= 14` | `内置条件应为 13 条，实际 14` |
| 6 | 同上 `:265` | `"内置条件应为 13 条"` | `14 条` | 同上 |
| 7 | **`apps/api/app/Models/TrustLevels/rule_catalog_test.go:61`** | `if len(keys) != 6` | `!= 7`（同时改 `:60` 注释「6 条」） | `应剩 6 条无候选依赖的条件，得到 7` |
| 8 | **`apps/api/app/Models/TrustLevels/config_rule_test.go:257`** | `if len(subset) != 11` | `!= 12`（同时改 `:250-253` 注释与 `:258` fatal 文案） | `今日子集应为 11 条…若已加 identity.profile 请同批更新本断言` |
| 9 | `apps/api/app/Models/Conditions/phrases.go:11` | `本文件是 13 种条件` | `14 种` | 不红（措辞纪律） |
| 10 | `apps/api/app/Models/Conditions/resolve_identity.go:11` | `处理五个身份类条件` | `六个` | 不红（措辞纪律） |
| 11 | `apps/api/app/Models/Conditions/batch.go:160` | 5 个 key | 6 个 key | **不红**——`Prepare` 只在运行期报 `未知条件`（`batch.go:168`），没有测试扫「每个 Definition 都能 Prepare」 |

第 7 条为什么必红（值得写进实施单，否则下一个人会以为它是相对断言）：

```
$ sed -n '58,63p' apps/api/app/Models/TrustLevels/rule_catalog_test.go
		// 剩下的是不依赖任何候选的条件：6 条。
		if len(keys) != 6 {
			t.Fatalf("应剩 6 条无候选依赖的条件，得到 %d：%v", len(keys), keys)
		}
```

子集 12 条里「无 `objectRef` 参数」的是 6 条（`activity.count`、`activity.streak`、`object.engagement`、`identity.account_age`、`identity.email_verified`、`identity.avatar`），加 `identity.profile` 成 7 条。

### 3.1 相对式断言：已核，**不要动**

| 文件:行号 | 表达式 | 加条件后 |
| --- | --- | --- |
| `conditions_postgres_integration_test.go:291` | `len(published) != len(registry.Definitions())-1` | 自动 13；**不要改成 `-2`** |
| `conditions_postgres_integration_test.go:298` | `len(withFacts) != len(registry.Definitions())` | 自动 |
| `TrustLevels/rule_catalog_test.go:86` | `if len(keys) != subset-1` | 自动（`subset` 在 `:46` 现算） |
| `Http/Controllers/TrustLevels/rule_controller_test.go:276-277` | `len(payload.Conditions) != len(shared)-3` | 自动（13−3=10 → 14−3=11） |
| `Http/Controllers/TrustLevels/rule_controller_test.go:316` | `len(trustlevels.RuleConditions()) != len(shared)-2` | 自动 |
| `Http/Controllers/TrustLevels/rule_controller_test.go:346-347` | `len(payload.Conditions) != len(shared)-2` | 自动 |

### 3.2 `/admin/badges/conditions` 有没有条数断言？

**没有。** 全仓该端点只在两处被测试提及，都不数条数：

```
$ rg -n 'badges/conditions' apps/api --glob '*_test.go'
apps/api/app/Models/Badges/admin_ref_candidates_test.go:36:// `GET /admin/badges/conditions` 失败（那会把整个规则编辑器一起打死）。
apps/api/app/Http/Controllers/Badges/route_order_test.go:87:// `/admin/badges/conditions` 与 `/admin/badges/:badgeID` 同形（同段数、同方法族）。
```

该端点的条数行为由 `Conditions` 包的 `PublishableDefinitions` 测试间接钉住（见 3.1 前两行）。→ **计划 §3.3「条件目录文档与 `GET /admin/badges/conditions` 条数断言 13 → 14」的后半句在代码里没有载体**；真正的载体是上表第 1/5/7 条。实施单里应如实写这一句，别让实施方去找一个不存在的断言。

### 3.3 前端需要改吗？

- `badgeRule.ts`：**不需要**（F12）。
- 硬编码条件类型的只有「默认选中的条件」一处逻辑，且是**按 key 查目录、查不到回落第一个**，不会因为新增条件而错：
  ```
  $ rg -n "key === 'activity.count'" apps/web/app
  apps/web/app/composables/admin/badges/useBadgeRuleDraft.ts:47
  apps/web/app/components/admin/forum/trust-levels/SFAdminTrustLevelRuleSection.vue:72
  apps/web/app/components/admin/rules/SFAdminRuleGroup.vue:80
  apps/web/app/components/admin/rules/SFAdminRuleSimpleForm.vue:149
  ```
- **但下拉文案有缺口**（不计入「必红」，但会直接影响验收观感）：`resolveParamOptionLabel`（`badgeRuleParams.ts:75-85`）按 `admin.badges.conditions.params.field.options.bio` → `admin.badges.metrics.bio` → `admin.badges.conditions.enums.bio` 依次找，三级都没有就**回落裸取值**，于是下拉显示 `bio` / `signature` / `website` / `location` 而不是「简介 / 签名 / 个人网站 / 所在地」。
  补文案的代价是**必须同批改棘轮**：`apps/web/tests/admin/adminBadgesI18n.test.ts:107` 的
  `'admin.badges.conditions.enums': ['like','bookmark','comment','category','tag']` 要加四个值，否则 `:189` 的 `extra` 断言判红（多出来的键 = 死文案）。
  若还要参数名文案 `admin.badges.conditions.params.field`，同一份 `:152-165` 的 `params` 枚举数组也要加 `field`。
  这两处属**前端写作用域**，与 task-2（`badgeRuleParams.ts` / `components/admin/rules` / locale / `tests/admin`）**直接撞车**——见 §9-3。

---

## 4. 列名与判据的坑（每条都附列定义原文）

| 断言 | 原文 | 位置 |
| --- | --- | --- |
| 列名是 `website_url`，不是 `website` | `website_url TEXT NOT NULL DEFAULT '',` | `202607070004_user_profiles.sql:8` |
| `bio` 同款 | `bio TEXT NOT NULL DEFAULT '',` | 同上 `:5` |
| `signature` 同款 | `signature TEXT NOT NULL DEFAULT '',` | 同上 `:6` |
| `location` 同款 | `location TEXT NOT NULL DEFAULT '',` | 同上 `:7` |
| 无后续 `ALTER COLUMN` / 默认值变更 | 见 F6 的空输出 | — |

**判据**：

- 必须 `<> ''`。`IS NOT NULL` 在四列上**恒真** → 每个人都能满足这条条件（把「资料已填写」变成白送的条件）。
- **不要 `trim(col) <> ''`**：`copy-spec §2.4` 原文是「填了就算，不看内容质量」。加 `trim` 会让「只填了空格」判不满足——那是对文案的一次**语义改写**，不是实现细节。如果确实想加，必须先改 copy-spec 的句子（「填了就算」→「填了非空内容才算」），否则句子与行为不一致。
- 也不要 `char_length(col) >= N`：没有门槛，只有真假。

---

## 5. 四值枚举：两处原文 + 唯一结论

### 5.1 第一处：计划 §3.3（四值）

`knowledge/plans/2026-11-14-trust-level-conditions.md:143`：

```
| `identity.profile` | `field`: enum(`bio`/`signature`/`website`/`location`) | 布尔 | `user_profiles` 对应列非空 |
```

同一份计划的 §0.1 小数修正（`:45`）：

```
小数修正：… 列名是 **`website_url`**（条件枚举值仍叫 `website`，判据是非空串）。
```

### 5.2 第二处：impl-facts 的裁决（只裁「字面量 vs 列名」，不含「删 location」）

`knowledge/reports/2026-11-14-trust-level-conditions-impl-facts.md:402`（§4 Q5）：

```
| **Q5** | `identity.profile` 的 `field` 字面量 | **枚举值 = `website`，列 = `website_url`**（计划 §0.1 小数修正已定） | 判据是三列 `TEXT NOT NULL DEFAULT ''` 的**非空串**，不是 `IS NOT NULL`（§2-10）。OpenAPI 的 enum 只写 `website`。 |
```

同文件 `:294-300`（§2-10）把计划读成三值：

```
**2-10｜`identity.profile` 的 params 取值与列名不一致（歧义）**
计划 §3.3 写 `field: enum(bio/signature/website)`，§9 假设 2 写 `website_url`。
```

—— 这段引的是**旧版计划**（v1 三值），`copy-spec §0.1 Q5` 明确记录了「已删除的写法：v1 三值」。

### 5.3 结论：**四值，无需裁决**

- 枚举四值：`bio` / `signature` / `website` / `location`。
- `website` 是**契约字面量**，落到 `user_profiles.website_url` 列。
- 判据：对应列 `<> ''`（非空串）。
- 依据（三条互相独立）：
  1. `plan §3.3:143`、`copy-spec §2.4`（四值 + 逐字代码 + golden 样本）与 `copy-spec §0.1 Q5`（`:34`，四值 + 能红断言）三者一致，且 `copy-spec` 是**定稿**（`§0.2` 表 `§2` 行在 `:42` 写「定稿」）。
  2. `copy-spec §0.1` 的 Q5 行给了**能红断言**：`identity.profile` 的 `field` Options 长度 == 4 且含 `location`；`TestPhraseGolden` 里 `location` 样本逐字命中「已填写「所在地」」。→ 三值读法会让这条断言红，所以三值读法不成立。
  3. `impl-facts` 的 Q5 表格只回答「字面量叫 `website` 还是 `website_url`」，没有处置 `location`；它引用的三值来自旧版计划，属于**引用漂移**，不是裁决。

**代价（给「万一有人仍要按三值做」的对照）**：删 `location` 要同时改三处——`copy-spec §2.4` 的 `profileFieldPhrases`（删一行）、`copy-spec §0.1 Q5` 的能红断言（断言从 4 降 3）、以及本报告 §2.2 的 golden 样本（`location` 样本失效 → 改用 `website` 或 `bio`，但那就丢了「钉住扩值」的作用）。**不建议**：`location` 的列、写入口、迁移全都现成（F6/F7），删它省不下任何成本，只丢一个运营真会用的条件取值。

### 5.4 一处措辞残留（不阻塞，但要修）

- `plan §9 假设 2`（`:387`）：`user_profiles` 的 `bio` / `signature` / `website_url` **三列**是资料编辑器的真实写入口 —— 实测是**四列**（`location` 也在，F7）。
- `impl-facts §4 Q5` 的「判据是**三列**」同源。

实施单请顺手把这两处「三列 / 三值」改成四，否则下一个人会照着三列写 SQL，`location` 取值永远判 false（保存成功、永不满足、零报错）。

---

## 6. 对信任子集的影响：11 → 12，写死「11」的断言只有一处

| 文件:行号 | 原文 | 是否需要改 |
| --- | --- | --- |
| `apps/api/app/Models/TrustLevels/config_rule_test.go:257` | `if len(subset) != 11 {` | **必改 12** |
| 同上 `:258` | `t.Fatalf("今日子集应为 11 条（共享 13 − 内容质量 2）…若已加 identity.profile 请同批更新本断言", …)` | 文案改 12 / 共享 14 |
| 同上 `:250-253` | 注释：`所以子集是 **11**，不是计划里写的 12——差的那一条是 identity.profile 还没落地…` | 改成事实陈述：子集 12 = 共享 14 − 2 |
| `apps/web/app/composables/admin/forum/trustLevels/useAdminTrustLevelConditions.ts:21` | `/** 服务端裁剪后的 12 条可用条件（14 条减去内容质量两条）。 */` | 不是门禁，但这行**现在写着「12/14」就已经是对的**——task-2 计划把它改成「13 减 2 = 11」，若两张单并行会互相打脸。见 §9-3 |

其余全部是相对式（§3.1 六行），**零改动**。

`TrustLevels/rule_catalog.go:21-24` 的排除清单只列 `content.pinned` / `content.hidden`，**不需要加** `identity.profile`——子集是「共享减这两条」，新条件自动进子集。这也是「不要写死 12」的原因：`RuleConditions()`（`:46-53`）是现算的。

---

## 7. 建议的验收断言（4 条 + 2 条加固）

均放 `apps/api/app/Models/Conditions/conditions_object_identity_integration_test.go` 的 `TestIdentityConditions`（`:67`）或紧随其后新增 `TestIdentityProfileCondition`。注意 `:117` 才插 `user_profiles` 行，新测试建议**自己开 fixture**，别依赖 `TestIdentityConditions` 的行状态。

| # | 类型 | 构造 | 断言 | 为什么它指向「真出事」 |
| --- | --- | --- | --- | --- |
| 1 | allowed | `INSERT INTO user_profiles (user_id, bio) VALUES ($1,'hi')` + 规则 `{"type":"identity.profile","params":{"field":"bio"}}` | `matched == true && !result.Incomplete` | 基线：能读、能判真 |
| 2 | denied | 该用户**没有** `user_profiles` 行 + 同一规则 | `matched == false && !result.Incomplete` | **钉住「无行 ≠ 查不到」**。若实现用 `QueryRow(... ).Scan` 或把 `ErrNoRows` 当 error，这条会 `Incomplete == true` → 红。这是本条件最可能的实现错误 |
| 3 | 列映射 | 只写 `website_url='https://x'`（其余列留默认 `''`），四条规则各判一次 | `field=website` → true；`field=bio` / `signature` / `location` → false | 钉住 `website` ↔ `website_url` 的映射。把 `CASE 'website'` 写成 `profiles.website` → SQL 报错 → 全 unknown；写成 `bio` → 第 2、4 条假真 |
| 4 | 内容质量不影响 | `bio` 只写 1 个字符（`'x'`） | `matched == true` | 钉住「填了就算」；若实现加了 `trim`/`length>=N`，这条仍绿但第 7 条会红（见下表） |
| 5 | golden phrase | `phraseSamples()` 加 `{name: "identity.profile/location", rule: leaf("identity.profile", {"field":"location"}, nil), wantZh: "已填写「所在地」", wantEn: "\"location\" filled in"}` | `TestPhraseGolden` 逐字相等 | 措辞变更不可能悄悄发生 |
| 6 | 枚举完整性 | `conditions.Core().Definition("identity.profile")` 的 `Params.Specs[0]` | `len(Options) == 4` 且含 `location` | `copy-spec §0.1 Q5` 的能红断言；三值读法会红 |
| 7 | 空格不满足（对照，可选） | `bio = '   '` | **记录当前行为**：非空串判 true | 与第 4 条配对说明「不 trim」是**有意**的；若产品要 trim，两条一起改 |
| 8 | 脏 field fail-closed（可选但推荐） | 直接调 `Resolver.Prepare`（绕过 `Validate`）传 `field: "nickname"` | 返回 error（→ 求值期 unknown） | 钉住「未知取值不能静默 false」：`false` 在 `not` 子树下会反转成 true（fail-open） |

---

## 8. 实施顺序（依赖关系）

1. `registry.go`（常量 + 定义）→ 此时 `TestEveryConditionHasPhrase` 立刻红（缺 Phrase 与 golden 样本），**这是预期的红**。
2. `phrases.go`（名词表 + 句子）→ `TestEveryConditionHasPhrase` 只剩「缺 golden 样本」一条红。
3. `requirement_text_test.go:55` 加样本 + `:168` 改 14 → 绿。
4. `resolve_identity.go`（case + SQL + loader）+ `batch.go:160` → 集成测试可跑。
5. `conditions_object_identity_integration_test.go` 加 §7 的 allowed/denied。
6. `conditions_postgres_integration_test.go:264` 14。
7. `TrustLevels/rule_catalog_test.go:61` 7、`config_rule_test.go:257` 12（这两处属 `TrustLevels` 包，可与 1–6 并行）。
8. 门禁：`cd apps/api && go build ./...`；`./scripts/test-api-isolated.sh ./app/Models/Conditions/... ./app/Models/Badges/... ./app/Models/TrustLevels/... ./app/Http/Controllers/TrustLevels/...`；`node tests/validate-architecture-boundaries.mjs`。

---

## 9. 未决 / 建议 / 协调

1. **`rule_impact.go` 的人数是「估不了」，不是 0**（F9）。信任等级的「≈ N 人」弹窗（计划 §3.3/§D8）对含 `identity.profile` 的档位会显示「估不了」。若要能估，只需一条 `SELECT count(*) FROM user_profiles WHERE <col> <> ''`（并入 `users.status='active'` 口径，照 `:186-188` 的 `KeyIdentityAvatar` 分支写）。**属实现选择，不是缺陷**；建议实施单明确「M1 不估」，避免验收时被当成 bug。
2. **下拉文案缺口**（§3.3 末）。我的建议：补 `admin.badges.conditions.enums.{bio,signature,website,location}` + 同批登记 `adminBadgesI18n.test.ts:107`；**不**补 `params.field`（参数名「资料项」收益低、还要再动 `:152-165`）。**这一条会改前端 locale 与测试，属另一张单的工作量**。
3. **与 task-2 的写作用域冲突（必须让 Lead 看到）**：
   - task-2 要改 `apps/web/app/utils/admin/badgeRuleParams.ts`（`paramOptionLabelKey` 改成三元组注入）与 `apps/web/app/composables/admin/forum/trustLevels/useAdminTrustLevelConditions.ts:21`（把「12 条（14 减 2）」改成实测「13 减 2 = 11」）。
   - 本单落地后 `useAdminTrustLevelConditions.ts:21` 的现文案**恰好变成正确**（14 − 2 = 12）。若 task-2 改成「11」或写死数字，本单落地时又会漂。→ 建议 task-2 写成**不写死数字**的形式（例如「服务端裁剪后的可用条件（共享目录减 `content.pinned` / `content.hidden`）」），与 task-2 任务描述末尾的意图一致。
   - `badgeRuleParams.ts` 的 `enums` 枚举与 locale 也是 task-2 的写作用域。**本单不要动前端**，把这条作为「后续前端单」报给 Lead。
4. **`TODO(核实)`（唯一的实现级不确定项）**：§2.3 第一版 SQL 用 `*bool` 接 NULL，需在实施时用一条最小测试确认 pgx v5 对 `unnest` 结果中 NULL 的扫描行为；这正是不推荐第一版的理由——第二版（四次 `coalesce` 子查询）没有 NULL 二义性，建议直接用第二版。
5. **本报告未做的核实**（如实声明）：
   - 没有跑任何 Go 测试（本任务只读、且改前无意义）：`TestEveryConditionHasPhrase` 现在应为绿（13 条 13 个样本），我不引用「已知绿」。
   - 没有跑 `bun test` / `typecheck`：本单零前端改动。
   - 没有真库验证 §2.3 的 SQL（未连库）。SQL 形状按同文件既有 `unnest` 先例推导，标为**建议**而非事实。

---

## 10. 一页速查

```
常量   registry.go:31 之后            KeyIdentityProfile = "identity.profile"
定义   registry.go:308 与 :309 之间    ValueBoolean + field enum(4) + Phrase
句子   phrases.go:236 与 :238 之间     profileFieldPhrases + phraseIdentityProfile
注释   phrases.go:11                  13 种 → 14 种
解析   resolve_identity.go:23 之后     case KeyIdentityProfile
SQL    resolve_identity.go:225 之后    profileFilledSQL（<> '' / 无行 = false）
注释   resolve_identity.go:11          五个 → 六个
分派   batch.go:160                    case 列表加 KeyIdentityProfile

红点   requirement_text_test.go:168     13 → 14
       requirement_text_test.go:55      补第 15 个样本
       conditions_postgres_integration_test.go:264   13 → 14
       TrustLevels/rule_catalog_test.go:61           6 → 7   ← 最易漏
       TrustLevels/config_rule_test.go:257           11 → 12

不动   contracts/**（key 是自由 string）
       apps/web/app/utils/admin/badgeRule.ts（8 行 re-export）
       所有相对式断言（len(shared)-2 / -3、subset-1、Definitions()-1）
       rule_store.go / rule_health.go / ref_names.go（default 已兜住）
```
