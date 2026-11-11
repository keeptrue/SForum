# 2026-11-11 信任等级 N 档改造 · 独立验收（reviewer / 小审）

工作树基准：`03fc95d78` + 未提交改动（`git diff --stat` 41 files, +3082/−1296）。
所有结论以**本次重跑**的输出为准；后端/前端自报值已逐条复现或推翻。

## 0. 结论速览

| 判据 | 结果 | 关键证据 |
|---|---|---|
| 判据 1：N=1/3/5/8 保存 → 判定 → `GET /api/v1/trust-levels` → 资料页卡片 | **部分 FAIL** | API 三组干净样本与判定一致；资料页卡片 off-by-one（P1）；N=5 样本被并发改写污染作废；N=1 的「账龄闸不再产生差异」UI 披露**未实现** |
| 判据 2：插入锚定不变量（D-N1） | **PASS（含变异红/绿）** | `TestInsertTierKeepsFollowingIdentities` 绿 → 改成按位次锚定立刻红 → 还原再绿 |
| 判据 3：v1 兼容 + 读路径不回写（D-N13） | **PASS** | 隔离 DB 集成测试 4/4 PASS；运行时 v1 库值读入无损；多次 API 重建后库值仍是 v1 |
| 判据 4：失败回落字段级 | **解码层 PASS / HTTP 层 FAIL** | 解码错误带「档位 id + 字段」；`PUT` 只返回 `trust_levels.invalid_config` 一句通用文案 |
| 零残留 | **PASS**（`LevelLeader` 仅剩 2 条注释，属预期） | 见 §6 |

P1 缺陷一条（资料页等级卡档位名/图标整体错位一档 + 多一条重复 TL0 刻度），另有 4 条 P2/P3 不一致，见 §5。

---

## 1. 判据 1：N 组配置的运行时链路

### 1.1 数据构造与取证纪律（重要）

- `trust.levels` 直接写 `web_options`（dev 库 `127.0.0.1:15432`，`docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum`）。
- **options 读面不是 Redis，而是进程内快照缓存**：`app/Models/Options/service.go:21` `defaultCacheTTL = 30 * time.Second`，`loadMapWithFreshness`（`service.go:642-658`）命中 `s.cached`。直写库后最长 30s 才可见——第一版 sweep 未等待，读到的全是上一轮值（就是这次采样被污染的根因）。修正做法：每次 `UPDATE` 后 `SELECT` 回读比对，再 `sleep 32` 后取运行时值。
- 判定链路用一个可判定构造：每档 `requirements` 全 0 + 临时把 `trust.new_user_days` 置 0（否则账龄闸把所有人压在 rank 1，见 §1.3），这样「active 档数 = 该用户 rank」可直接读出 `LastRank` 的效果。
- **N=5 那一组样本作废**：写入后 32s 读到的是 v1 四档（`newcomer,member,resident,veteran,leader`），说明同一时间另有写入者把值改回去了（老运 QA 并发）。按纪律丢弃，不记 PASS 也不记 FAIL。**这项改动本身没有「并发写保护」，仅提示**：该 option 无乐观锁/版本号，两个后台会话同时保存是后写者赢。

### 1.2 干净样本（写入 → 回读一致 → 运行时）

```
N=1: api levels=2  slugs=['newcomer','tier_6']  icons=['sprout','leaf']  newUserDays=0
N=3: api levels=4  slugs=['newcomer','tier_6','tier_7','tier_8']  icons=['sprout','leaf','home','mountain']  newUserDays=0
N=8: api levels=9  slugs=['newcomer','tier_6',...,'tier_13']  icons=['sprout','leaf','home','mountain','crown','star','sun','moon','flame']  newUserDays=0
```

- `levels` 首条是服务端合成的 TL0（`level:0`/`editable:false`），所以 `N+1` 个条目正确；`MaxTiers=8` 未被越过（**存储侧从未出现 9 个 active 档**；若出现当属缺陷，本次未出现）。
- 判定跟随 `LastRank()`：同一位用户（`seed_44888364289b`）在 N=1/3/8 下卡片 `data-level` 依次为 1 / 3 / 8，符合预期。
- `newUserDays` 在 payload 中取自既有 `trust.new_user_days`（临时置 0 时下发 0），R6 单一来源成立；验收结束后已还原为 7。

### 1.3 N=1 的「账龄闸不再产生差异」披露 —— **未实现**

`rg -n "newUserDays|new_user_days|账龄|新人期" apps/web/app apps/web/i18n/locales/zh-CN.json` 只命中：

- `apps/web/app/composables/profile/useTrustLevel.ts:133`（响应类型字段）、`:55`（注释）；
- `apps/web/app/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue`（新人限制页本身）；
- 后台信任等级页 i18n 里只有「TL0 由新人限制页决定，此处只读」「当前启用 X 档 / 上限 8 档」两类说明，**没有任何一句告诉运营「只剩 1 档时 `trust.new_user_days` 不再产生任何可观察差异」**。列进不一致清单 #2。

---

## 2. 判据 2：插入锚定 + 变异证据

基线（`-count=1`，不吃缓存）：

```
cd apps/api && go test ./app/Models/TrustLevels/ -run 'TestInsertTierKeepsFollowingIdentities|TestRetireThenRestoreKeepsRankAndConfig' -count=1
→ ok
```

变异（把身份改成由位次决定，即在 `DecodeConfig` 第二遍里 `id := BuiltinTierID(Level(activeCount+1))` 覆盖客户端回传的 id，模拟「按位次锚定」）：

```
--- FAIL: TestInsertTierKeepsFollowingIdentities (0.00s)
    tier_identity_test.go:94: 新档 id = "resident"，期望 tier_6（内置占 1..5）
--- FAIL: TestRetireThenRestoreKeepsRankAndConfig (0.00s)
    tier_identity_test.go:138: 停用 resident 后 rank 2 = "resident"，期望顺移上来的 veteran
FAIL
```

还原后 `md5sum app/Models/TrustLevels/config.go` = `ac25dbfe9d0fcc2d9b3b48e8373e2cc2`（与变异前备份逐字节相同）、`grep -c MUTATION` = 0、测试重新为 `ok`。

补充：该测试断言的不只是身份，还逐项比对插入后每一档的 `requirements` / `limits` / `earnable` 仍挂在**同一个 id** 上（`tier_identity_test.go:100-118`），并且校验新档的 `Earnable.Grants(2)` 会跟着位次移动。变异只改身份就已变红，说明「位次锚定」这条退化路径被真正守住。

---

## 3. 判据 3：v1 兼容 + 读路径不回写（D-N13）

### 3.1 隔离 DB 集成测试（本地直跑会静默 SKIP，必须走脚本）

先证伪一个容易误记的 PASS：`go test ./app/Models/Options/` 报 `ok` 时，两个写路径测试其实是 **SKIP**：

```
--- SKIP: TestUpdateTrustLevelsPersistsNormalizedConfig
    trust_levels_options_write_integration_test.go:54: SFORUM_TEST_DATABASE_URL is not set; ...
```

改走 `./scripts/test-api-isolated.sh`（真实 disposable PG 集群）：

```
./scripts/test-api-isolated.sh -count=1 -v -run 'TestUpdateTrustLevelsPersistsNormalizedConfig|TestReadPathDoesNotUpgradeStoredConfig|TestRestoreDefaultsPersistsRecommendedConfig|TestInvalidTrustLevelsWriteIsAtomic' ./app/Models/Options/
--- PASS: TestUpdateTrustLevelsPersistsNormalizedConfig (0.11s)      # 合法写入 → 读回=归一化 v2
--- PASS: TestReadPathDoesNotUpgradeStoredConfig (0.07s)             # D-N13：读路径不回写
--- PASS: TestRestoreDefaultsPersistsRecommendedConfig (0.06s)
--- PASS: TestInvalidTrustLevelsWriteIsAtomic (0.06s)                # 未知字段/schemaVersion/省略 limits 三子例，且非原子半写
ok  	.../app/Models/Options	0.326s
```

### 3.2 运行时：dev 库本来就是 v1，读入无损

验收开始时 dev 库值就是 `{"schemaVersion":1,"levels":[...member/resident/veteran/leader...]}`（1192 B，四档 + 全 null limits + 空 earnable）。此时在线 API 输出：

```
GET /api/v1/trust-levels → 5 levels, slugs=[newcomer,member,resident,veteran,leader]
requirements = days 3/15/45/100、topic 0/2/8/25、comment 5/20/250/80、like_recv 0/5/30/100、like_given 0/3/20/80、bookmark 0/0/5/20
```

与库里的 v1 值逐字段一致（无损），TL0 合成条目 `displayName.zh="新成员"`、`icon="sprout"`，`newUserDays` 取自 `trust.new_user_days`(=7)。

### 3.3 启动路径不写库（D-N13）

- 期间 API 被 `air` 重建过（`apps/api/tmp/sforum-api` mtime `17:57:51`，进程 `17:57:44` 之后重启），此后 `trust.levels` 仍是 v1 → **启动只读不写**。
- 另用 `select value ... | md5sum` 在 `touch` 触发重建前后比对：`before == after == 846bc597147b0bf7bf8e7af30654130c`。需诚实标注：这一次 `touch` 没在 12s 内触发新 pid（air 侧未重建成功），所以「不写库」的**决定性**证据是 §3.1 的 `TestReadPathDoesNotUpgradeStoredConfig` + 代码路径（`WebOption`→`Get`→`loadMap`→`store.List`，写路径仅 `normalizeTrustLevelsOption`/后台 PUT）。

### 3.4 保存后升为 v2 且不丢配置

隔离测试覆盖；另在运行时以「v2 五档默认值」写库并回读（见 §7 还原记录），`GET` 输出 5 档内置 id 不变、阈值与默认策略一致 → v1→v2 的对外契约（id/阈值/档数）不变。

**未直接验证**：后台读面 `GET /api/v1/admin/trust-levels` 的「显示与今天一致」。该端点需要 `admin.access` 会话，本次没有可用凭据（未擅自重置任何账号密码）。替代证据：`controller_test.go:TestAdminPayloadCarriesTierEditorMetadata` + 读代码（`getAdmin` 返回 `EncodeConfig(policy)`，即 v2 规范化形状 + 同形 `levels[]`）。这一条标记为「handler 级 + 代码级证据，非 live curl」。

---

## 4. 判据 4：失败回落是否落到「档位 + 字段」

### 4.1 解码层：是（PASS）

`apps/api/app/Models/TrustLevels/config.go` 的错误文案带具体档位与字段，例如：

- icon 非法：`档位 "tier_6" 的 icon "xxx" 不在白名单内`（`config.go:203`）
- id 重复：`id "member" 重复`（`:173`）；id 非法：`第 2 档的 id "x" 不合法`（`:165`）
- 档数越界：`tiers 至少要有 1 档`（`:150`）/ `active 档位最多 8 档`（`:246`）
- 省略 limits：`档位 "member" 缺少 limits 字段；省略会让该档限额被静默重置为「全继承」`（`:213`）

### 4.2 HTTP 层：否（FAIL）

`app/Http/Controllers/TrustLevels/controller.go:183-186` 把上面的精确错误直接丢弃：

```go
policy, err := trustlevels.DecodeConfig(request.Config)
if err != nil {
    return fiber.NewError(fiber.StatusUnprocessableEntity, "trust_levels.invalid_config")
}
```

对外只剩一句（`app/Support/Localization/messages.go:68`）：
> 信任等级配置无效：每档必须给出 requirements、limits 与 earnable；未知字段会被拒绝。

即运营看到的是「配置无效」，**无法知道是哪一档、哪个字段**；与判据要求「错误落到具体档位与字段」不符。同时 `controller_test.go:TestUpdateRejectsInvalidConfig` 只断言 `status == 422`，不断言任何字段级信息 —— 这条测试无法守住「字段级」这件事。列不一致清单 #1。

「后台读面不可用」一项：前端 `useTrustLevelsForm.ts:353` 读面失败时 `status:'failed', error:'invalid_response'`，只做整体失败态，没有逐档回落；资料页侧 `parseTrustLevelCatalog` 失败时回落 `FALLBACK_TIERS`（静默），属既定设计（卡片不因目录坏掉不渲染），可接受。

### 4.3 「不得静默改写已存配置」：PASS

`TestInvalidTrustLevelsWriteIsAtomic`（隔离 DB）证明非法写入整笔拒绝、库值不变；运行时也观察到：我写入的非法形状从未出现在读面。

---

## 5. 不一致清单

### P1 — 资料页等级卡整体错位一档，且多渲染一条重复 TL0 刻度（**缺陷**）

`SFProfileShowPage.vue:110` 从 `useTrustLevel` 取到的 `ladder` **已经包含 TL0**（`useTrustLevel.ts:447` → `trustLevelLadder()` 前面拼了 `TL0_TIER`），却又原样当 `:ladder` 传给卡片；卡片 `SFTrustLevelCard.vue:39` 再调用一次 `trustLevelLadder(props.ladder)`，于是 TL0 被拼两遍，索引整体后移一位。

运行时证据（真实 SSR HTML，`curl http://127.0.0.1:3000/u/seed_44888364289b`，逐字取 `sf-trust-level-card`）：

```
N=1（active 档 = [第1档 leaf]）  卡片: data-level=1 name=新用户 icon=sprout rungs=3   ← 期望 name=第1档/leaf，rungs=2
N=3（active 档 = [第1/2/3档]）   卡片: data-level=3 name=第2档 icon=home  rungs=5   ← 期望 第3档/mountain，rungs=4
N=8（active 档 = [第1..8档]）    卡片: data-level=8 name=第7档 icon=moon  rungs=10  ← 期望 第8档/flame，rungs=9
```

三个 N 一致的偏移量（name/icon 取到 rank−1 的档、刻度数多了 1 条），不是采样噪声。TL1 用户在默认五档站点上也复现：`/u/zhuchunshu` → `data-level=1 name=新用户 icon=sprout`（应 `正式用户/leaf`）。

现有测试为何没红：`tests/profile/trustLevelCard.test.ts` 的 `mountCard()` 传的是**原始 tiers**（`FALLBACK_TIERS`），符合卡片自述契约（"TL0 不在数组里"），所以单测绿；缺的是「页面 → 卡片」这层的接线断言/SSR 断言。

建议最小修法（二选一，避免两边各修一半）：卡片自己负责拼 TL0（页面传原始 tiers），或页面传已拼好的 ladder 且卡片不再二次 `trustLevelLadder()`。**我没有改代码。**

### P2 — 失败回落不落到字段（见 §4.2）

`controller.go:185` 丢弃 `DecodeConfig` 的精确错误。建议把 `err.Error()`（或结构化 `{tierId, field, reason}`）透出到 `data`，并把 `TestUpdateRejectsInvalidConfig` 从「断言 422」升级为「断言具体档位/字段」。

### P3 — 前端兜底图标短名与后端白名单不同名

- 后端唯一白名单：`sprout, leaf, home, mountain, crown, star, sun, moon, flame, shield, gem, compass, zap, anchor, medal, trophy`（`tier_icons.go:24-41`）。
- 前端 `DEFAULT_TIER_ICON_NAMES`：`sprout, leaf, house, mountain-snow, crown, medal, trophy, gem`（`useTrustLevel.ts:25-34`）；`FALLBACK_ICON_NAMES`：`leaf, house, mountain-snow, crown`。

`house` / `mountain-snow` 不在后端白名单里（对应 `home` / `mountain`），rank 5–7 也不一致（前端 medal/trophy/gem vs 后端 star/sun/moon）。当前只在「读面不可用 / 图标为 null」时生效（后端解码时会把空 icon 填成默认值，所以线上主路径不会踩到），但它是**第二份真相**，一旦有人把兜底值写回配置就是 422。建议换成后端同名字面量。

### P4 — 「`min(rank,4)` 收敛」是装饰性的，不是图标重复（**口径已确认，非缺陷**）

- 三处 `Math.min(4, level)` 只写进 `data-step` 属性（`SFTrustLevelCard.vue:93`、`SFAdminTrustLevelsOverviewTab.vue:200`、`SFAdminTrustLevelThresholdsTab.vue:106`），只被 CSS 渐变选中使用；真实图标走 `:name="levelIcon(level)"` / `tierIcon(tier)`，即服务端下发的 `icon`。
- N=8 实测 `GET /api/v1/trust-levels` 的 icon 序列：`sprout, leaf, home, mountain, crown, star, sun, moon, flame` —— rank 1..8 一一不同，**没有重复**。
- 但「后端默认序列已覆盖到 8 个：芽/叶/屋/山/冠/星/火/盾」这句口径与代码不符：`tierIcons` 第 6/7 项是 `sun`/`moon`，`flame` 是第 8 项（rank 8），`shield` 是第 9 项（rank 9，`MaxTiers=8` 下不可达）。即 8 档站点看到的是 冠/星/日/月/焰，不是 冠/星/火/盾。仅口径问题，行为正确。

### P5 — `trust.levels` 无并发写保护（观察项，非本次改造引入）

后台两个会话同时保存 = 后写者赢，本次采样即被并发改写污染（见 §1.1）。如计划里没有版本号/ETag 的裁决，建议记一笔；不在本刀范围内。

---

## 6. 零残留复核（真实输出）

```
$ rg -n "LevelCount" apps/ -g '!*_test.go' | wc -l
0
$ git diff --stat -- apps/api/database/ | wc -l
0
$ rg -n "trust_level" apps/api/database/ | wc -l
0
$ rg -n "LevelLeader\s*(Level)?\s*=" apps/api/app/Models/TrustLevels/types.go
（零命中：常量声明已删）
$ rg -n "LevelLeader" apps/api/app/Models/TrustLevels/*.go -g '!*_test.go'
app/Models/TrustLevels/resolver.go:108   // 最高档的判据是 policy.LastRank() …… 而不是内置的 LevelLeader：
app/Models/TrustLevels/registry.go:79    // … 这里曾经写死 Grants(LevelLeader)，于是 8 档站点第 5–8 档…
$ cd apps/api && go build ./...
BUILD_OK (exit 0)
```

**注释命中是预期**：两条都是事故史注释，删注释去迎合正则等于销毁证据；真正的判据是「常量声明零命中 + `go build` 通过」（Go 里被删掉的标识符不可能被引用）。

前端五档常量的残留解释：

```
apps/web/app/composables/profile/useTrustLevel.ts:15  TRUST_LEVEL_SLUGS 定义（仅回退锚点）
                          :19  TRUST_LEVEL_MAX = len-1
                          :165 FALLBACK_TIERS（读面不可用兜底）
                          :204 builtinTierI18nKey（id 命中才有 i18n 回退名）
                          :277-278 / :286-287 trustLevelSlug() / trustLevelIcon()（rank clamp 到 4）
apps/web/app/composables/admin/forum/trustLevels/useTrustLevelsForm.ts:27/:473  re-export + v1→v2 内存升级按内置顺序对回
```

`rg -n "TRUST_LEVEL_SLUGS|TRUST_LEVEL_MAX" apps/web/app apps/web/tests | grep -v useTrustLevel.ts` → 仅有上述 admin composable 的两处与两条测试；**生产组件里零调用点**。`trustLevelSlug()` / `trustLevelIcon()` 属导出但无调用的兜底助手，建议后续标注或删除，避免下一个人当权威源使用。

---

## 7. 其他复核项

- **资料页每读一次的额外解码开销（lead 指定项）**：`Providers/profile.go:36-56` `trustTierCatalog.TierIdentity` 每次调用都 `options.WebOption` + `trustlevels.DecodeConfig`；`Profile/service.go:700-717` 每个资料请求调用一次（只解当前档）。
  - 解码成本（`go test ./app/Models/TrustLevels/ -bench BenchmarkDecodeConfig -benchmem -run '^$'`）：
    `BenchmarkDecodeConfigDefault  42292 iter  32314 ns/op  12706 B/op  68 allocs/op`
    `BenchmarkDecodeConfigEightTiers 34242 iter 31038 ns/op 14108 B/op 108 allocs/op`
  - 读的是**进程内快照**（30s TTL，`service.go:21/642-658`），不是 Redis：Redis 里没有 `*option*` 键；直写库后运行时最长 30s 才变（实测两轮均为 32s 睡眠后稳态）。
  - 端到端（本机 dev，n=60，median）：`/api/v1/trust-levels` 1.4ms vs `/api/v1/health` 1.2ms（Δ≈0.2ms 含一次 32µs 解码）；资料页 `/u/zhuchunshu` 215ms vs 无卡片页 `/guidelines` 104ms —— 页级差异被资料页自身查询主导，**解码开销不可在页级观测**，≈32µs/次、12.7KB/次。
- **他人视角隐私（D1）**：`Profile/types.go:110-113` `PublicTrustLevel` 只有 `level` / `levelSlug` / `displayName`（`displayName` 为 `LocalizedName`，`omitempty`），`service.go:693-719` 里 `TierIdentity` 只携带 `ID + Name`，策略其余字段（阈值、限额、阶梯）在适配器层就被丢弃；SSR 里他人资料只有 `TL{n}` + 一个档位名，无进度条。`like_given_count` / `bookmark_count` 不出现在资料页信任卡。契约 `contracts/openapi/schemas/trust_levels.yaml:154` 亦写明「只对本人返回进度」。
- **契约与实现三方对拍（前端自报三处）**：
  1. `displayName`：后端公开/后台 wire 均为 `displayName`（`payload.go:toTrustLevelName`），OpenAPI `TrustLevelDefinition.displayName → #/TrustLevelName` 一致，前端 `parseTrustLevelCatalog` 用 `item.displayName ?? item.name` 兼容存储形状 —— **三方一致，后端为权威**。
  2. 公开目录里合成的 TL0 被跳过：后端在 `buildPayload` 首条合成 `level:0/editable:false`（`payload.go:24-30`），前端 `useTrustLevel.ts:367-369` 按 `level===0 || editable===false` 跳过，本地 `trustLevelLadder` 再补 TL0 —— **一致**。（注意：卡片二次补 TL0 是另一个问题，见 P1。）
  3. `namePool`：后端 `toTrustLevelNamePool{zh[],en[]}`（平行数组）、OpenAPI `TrustLevelNamePool required:[zh,en]` 两个数组、前端 `parseNamePool` 首选平行数组分支 —— **一致**，前端另兼容「对象数组」旧形状属宽容解析。
- **Notifications `users_pkey` 偶发**：`./scripts/test-api-isolated.sh -count=1 ./app/Models/Notifications/` 连跑两次均 `ok`（0.588s / 0.639s，river migrations applied=7），**已复跑两次不现，判定既有偶发**，与本次改动无关（该包不 import trustlevels/options 改动面）。

---

## 8. 本次验收对 dev 环境做了什么（可回滚记录）

- 只改 `web_options` 两行：`trust.levels`、`trust.new_user_days`；未改任何账号/密码/角色/会话。
- 试验期间的注意点：任意一次写入都在 30s 内可能被并发写者覆盖（本报告 N=5 样本因此作废）。
- **最终状态（已还原并验证）**：`trust.levels` = `EncodeConfig(DefaultPolicy())` 的规范 v2 五档（1431 B，`member/resident/veteran/leader`，阈值与推荐默认一致，`limits` 全 null，`earnable` 空），写入后回读逐字节相等、35s 后仍相等；`trust.new_user_days = 7`；`GET /api/v1/trust-levels` = 5 档 `newcomer,member,resident,veteran,leader`、`newUserDays=7`。
- 起点是 v1 四档（存量形状），按 lead 要求最终落成 v2 默认态，而不是回滚成 v1。

## 9. 未验证 / 空白

- 后台读面的 live 契约形状（`GET /api/v1/admin/trust-levels`）与后台表单的浏览器 QA：本次无 admin 会话，未擅自重置密码；仅有 handler 测试与代码证据。
- `PUT /api/v1/admin/trust-levels` 的 live 422 响应体：同上（无会话），以 `controller_test.go` + 代码为证。
- 资料页在 1/3/5/8 档下的**视觉**阶梯（data-step 渐变）未做像素级核对，只核 `data-step`/图标类名与档数。
- `N=5` 运行时样本缺失（并发污染），N=5 的等价证据来自 N=3/N=8 与隔离测试；如需补齐，请在无并发写者时重跑 §1.2。

---

# 复验（P1/P2/N=1/保存确认/P3）

第二轮窄复验，只针对这五项，不重跑全量矩阵。上面正文（含「`go test ./app/Models/Options/` 里两个写路径测试其实 SKIP」的证伪）原样保留。

环境：API 8080 / Web 3000 由 lead 恢复运行；库起点 `trust.levels` = 默认 v2 五档（1431 B）、`trust.new_user_days=7`、`GET /api/v1/trust-levels` = 5 档。

## R1（P1）资料页卡片错位 —— **PASS**

- 修复接缝：阶梯组成权收在 `useTrustLevel`（`useTrustLevel.ts:278` `FALLBACK_LADDER = trustLevelLadder(FALLBACK_TIERS)`、`:447` 的 `ladder` computed）；卡片 `SFTrustLevelCard.vue:37-41` 变成 `rungsSource = computed(() => props.ladder ?? [])` + `displayLadder = hasLadder ? rungsSource : FALLBACK_LADDER`，**不再自己拼 TL0**。
- 接缝断言确实落在接缝上，不是卡片内部自嗨（`tests/profile/trustLevelCard.test.ts:297-327`）：构造**真实公开目录**（含服务端合成的 `level:0 / editable:false` 条目）→ `parseTrustLevelCatalog` → `trustLevelLadder(catalog)` → 挂载卡片，断言 `ladder.filter(id==='newcomer')` 只有 1 条、`rungs.length === ladder.length`、`data-level === '1'`、`TL1`、名字 `正式用户`、图标 `i-lucide-leaf`、最高档按阶梯长度（2）而非内置 4。另有源码级守卫（`:336-340`）：`expect(source).not.toContain('trustLevelLadder(')` + `toContain('ladder: () => []')`。
- 运行时（真 SSR `curl http://127.0.0.1:3000/u/zhuchunshu`）：`data-level=1` / `TL1` / `正式用户` / `leaf` / `rungs=5` / `aria-label="信任等级 正式用户，已获得"`。5 = TL0 一格 + 4 个 active 档，与「TL0 + active 档数」一致。
- 复跑：`cd apps/web && bun test tests/admin/forum/trustLevels.test.ts tests/admin/forum/trustLevelsTabMount.test.ts tests/admin/forum/trustLevelNameFallback.test.ts tests/profile/trustLevelCard.test.ts tests/admin/forum/trustLevelsOverview.test.ts` → **149 pass / 0 fail**。

## R2（P2）字段级明细 —— **PASS（handler 级 + 前端映射级；live 422 仍缺会话）**

- 422 真的带明细（`go test ./app/Http/Controllers/TrustLevels/ -run TestUpdateConfigErrorDetails -v -count=1` → PASS）：测试在**复刻生产错误信封** `{code,message,data:{reason,details:{errors:[…]}}}` 上断言（`controller_test.go:25-31` 明确写着不复刻就会变成「测一份测试专用形状」）：
  - 非法图标 → `hasDetail(details,"veteran","icon",CodeIconNotWhitelisted)` 且 `details[0].Params["icon"]=="rocket"`（可插值）；
  - 缺 limits → `hasDetail(details,"resident","limits",CodeLimitsMissing)`；
  - id 重复 → 明细指出重复的那个 id；
  - 上界 → 造 `MaxConfigErrorDetails+4` 条错，断言 `len(details) == MaxConfigErrorDetails`。
- 29 条稳定枚举：`TestConfigErrorReasonCodesAreStable` PASS（钉住字面量表，供 OpenAPI enum + 前端 i18n 对拍）；`TestDecodeConfigReturnsFieldDetails` PASS。
- 前端按 code 拼文案、未知 code 回落人读 prose：i18n 有 `icon_not_whitelisted`「图标「{icon}」不在服务端白名单内…」、`id_duplicate`「存在重复的档位 id「{id}」…」、`limits_missing`（`zh-CN.json:5459-5465`）；`reasonCode wins over backend prose and interpolates params` PASS，未知 code `brand_new_code` → 渲染 `reason`（"名称过长"）而不是 `[key]` 字面；29 码覆盖（14 具体 + 15 归桶）测试 PASS。
- **未验证边界（同上轮）**：无 admin 会话，没 curl 出真实 422 响应体；证据是真实 fiber handler 响应（测试内复刻生产信封）+ 代码 `controller.go:311-327` 的投影逻辑 + 前端真实映射测试。

## R3 N=1 披露 —— **PASS（组件挂载 + 配置侧运行时；admin DOM 需会话）**

- 实现：`SFAdminTrustLevelTiersTab.vue:140-152`，`v-if="activeCount === 1"` 的常驻 `UAlert` + `data-sf-tier-single-disclosure`（非 Toast、不自动消失）。
- 文案说清了后果（`admin.forum.trustLevels.tiers.singleTier`）：title「只剩一档：账龄闸不再产生差异」；description「启用档位只有 1 档时，新人信任期的账龄闸（trust.new_user_days）不再产生任何差别——所有用户要么是 TL0，要么就已经是这一档。想让成长曲线重新有意义，至少再加一档。」
- 挂载测试 `tests/admin/forum/trustLevelsTabMount.test.ts:528-532`：1 档 → 属性存在且标题/描述命中；默认档数 → 属性不存在。本次复跑包含该文件，全绿。
- 配置侧运行时：真把库推到 1 档（`readback_equal=True`），`GET /api/v1/trust-levels` 读到 `[newcomer,tier_6]`（TL0 + 1 档）；已还原。
- **边界**：`/control-panel/forum/trust-levels` 无会话 302 到登录，未取到 live DOM 中的该属性；「出现/消失」由真实组件挂载（同一 tab 组件、同一 form 形状）证明。

## R4 结构变化保存确认 —— **PASS（含反向那条）**

- 判据 `tierStructureChanged(saved,next)` 只比 active 档位的 id 序列（`useTrustLevelsForm.ts:627-629`）。
- 正向：插序 / 重排 / 停用 → `true`（`trustLevels.test.ts:682-697`）。
- **反向（重点）**：只改阈值 `requirements.topic_count = 99` → `false`（`:700-703`）；改名 / 图标 / earnable → `false`（`:705-716`）。保存入口是 `if (structureChanged.value) { structureConfirmOpen.value = true; return }; await performSave()`（`useTrustLevelsAdminPage.ts:279-282`），所以阈值-only 保存**不弹**。
- 弹窗内容：`/control-panel/forum/trust-levels` 页 shell 的 `UModal data-sf-trust-levels-structure-confirm`，文案命中两条硬要求——`structureConfirm.title`「档位结构已改变：保存后所有用户按新阶梯重新判定」、`structureConfirm.noMigration`「等级是每次请求实时算出来的，没有迁移过程，也不需要等待或补偿」；取消走 `cancelStructureConfirm`，不会静默落库。
- **残余风险（非缺陷，记录用）**：正向/反向的**行为**证据是谓词单测；「保存按钮 → 打开弹窗」这一步目前是**源码级**断言（`trustLevelsOverview.test.ts:438-448` 正则匹配 `if (structureChanged.value) { structureConfirmOpen.value = true; return }` 与两条文案 key），没有「点保存 → 断言弹窗/未弹窗」的挂载级测试。与 P1 的教训同型（只断源码不断行为），建议补一条挂载测试。

## R5（P3）兜底图标名与后端逐字一致 —— **PASS（含「文件缺失要红」的变异）**

- 前端 `DEFAULT_TIER_ICON_NAMES` 现在是**16 项**且与 `tier_icons.go` 的 `var tierIcons` 逐字相同：`sprout, leaf, home, mountain, crown, star, sun, moon, flame, shield, gem, compass, zap, anchor, medal, trophy`；`FALLBACK_ICON_NAMES` 也已改为 `leaf, home, mountain, crown`（不再是 `house` / `mountain-snow`）。
- 测试 `tests/admin/forum/trustLevelNameFallback.test.ts:139-157` 直接读 Go 源码离线比对：`expect(goPath).not.toBeNull()`、`backendIcons.length === 16`、`expect([...DEFAULT_TIER_ICON_NAMES]).toEqual(backendIcons)`。**PASS**。
- 变异（确认不是静默跳过）：把 `apps/api/app/Models/TrustLevels/tier_icons.go` 移出仓库后跑该文件 →
  `Received: null` @ `trustLevelNameFallback.test.ts:149` → `(fail) the frontend fallback sequence equals tier_icons.go verbatim`，14 pass / **1 fail**；移回后 `md5sum -c` = `OK`，测试恢复全绿。

## R6 本轮新增不一致 / 观察项

- **无新增 P 级缺陷**。R4 的「保存确认只有源码级断言」记为**残余风险**（建议补挂载级行为测试），不算缺陷：谓词与接线两侧都有断言，只差一次行为级串联。
- **并发写者仍在动这份配置**（复验期间再次复现）：我在写默认值后的第一次轮询里读到库被别人改成 1 档（`db_is_mine=False, db_levels=1`）。按 lead 的取证纪律重写后，第 2 次轮询收敛：`db_is_mine=True`、API = `[newcomer,member,resident,veteran,leader]`、`newUserDays=7`。**最终态已还原为默认 v2 五档 + 7 天**，回读逐字节一致。若后面还有人写这份配置，建议先沟通时段再采样。

## R7 本轮写库记录（已还原）

- 只写 `web_options.trust.levels` 与 `trust.new_user_days`；未动账号/会话/角色。
- 试验序列：默认 → N=1（验证 1 档生效）→ 并发被改成 1 档（他人写入）→ 重写默认 → 收敛确认。
- 终态：`trust.levels` = `EncodeConfig(DefaultPolicy())` 默认 v2 五档 1431 B（回读一致）、`trust.new_user_days=7`、`GET /api/v1/trust-levels` = 5 档 `newcomer,member,resident,veteran,leader` + `newUserDays=7`。
