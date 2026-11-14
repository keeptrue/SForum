# M5 验收矩阵盘点：§4.6 六条变异逐条重打

> 采集人：pm · 2026-11-14 · 本机（macOS/linux 混部环境，见各条命令）
> 采集方式：**六条全部由本人在本机重打一遍**（读代码 → 改一处 → 跑该测试 → 看红 → 还原），
> 不引用任何单的「我验过了」自述。每条都贴了实际红出来的那句断言。
> 所有变异只动本地源码副本，还原后 `sha256sum -c` 逐字节一致且 `git status` 干净（见 §5）。

## 0. 结论先说

六条全部可复现转红，且每条都能指到具体测试函数——**这六条撑得起「M5 可以收口」**。
但有一个前提：收口时把这六条的复跑命令写进模块笔记（§4），否则它们只是一次性表演，
下一次改装配层（比如有人把 `trustLevelActivityReader` 换成缓存读）没人会想到重跑。

---

## 1. 六行矩阵

| # | 变异怎么打（我实际执行的改法） | 该红的测试（文件:函数） | 我实测到的红（原文摘录） | 状态 |
| --- | --- | --- | --- | --- |
| 1 数值预筛失效 | 两处各打一次：**1a** 删 `app/Models/TrustLevels/level.go` Prepare 循环里的 `if !satisfies(...) { continue }`（合成根会把数值未过的档也装进去）；**1b** 删求值循环里同一处 `satisfies` 短路（数值未过也求值） | `level_rules_test.go:TestNumericFailureSkipsRuleEvaluation`（无库） | 1a：`Prepare 只该收到 TL1 的规则，得到 [{"v":1,"root":{"type":"identity.email_verified"}} {"v":1,"root":{"op":"all",…}}]`；1b：`TL2 数值不过，应停在 TL1，得到 2` | **已证**（本人重打） |
| 2 规则错误被吞成成立 | `level.go` 的 `evaluate` 尾部 `return outcome.Matched` → `return outcome.Matched \|\| outcome.Incomplete`（把「判不了」当成立） | `level_rules_test.go:TestRuleFailureIsFailClosed`（无库，4 个子用例） | 3/4 子用例红：`规则判不了时该档必须不成立（fail-closed）→ TL1，得到 2`（`引擎判不了（unknown）`/`脏规则`/`unknown 且 Matched=false`；`缺少求值器` 那条仍绿——它走的是 `eval == nil` 分支） | **已证**（本人重打） |
| 3 公开面泄露 AST | `Http/Controllers/TrustLevels/dto.go` 给 `publicLevelDefinition` 加 `Rule json.RawMessage`，`payload.go` 的 `toPublicLevel` 填 `policy.RuleOf(rank)` | `rule_controller_test.go:TestPublicPayloadNeverCarriesRule`（无库） | `公开响应里出现了 "rule"：{…"levelSlug":"member",…,"rule":{"v":1,"root":{"type":"activity.count",…}}}` | **已证**（本人重打） |
| 4 未声明 factKey 返回 0 | `app/Models/Conditions/resolve_facts.go` 删掉「未声明 → return error」那段（等价于按已声明处理、查不到行按 0） | `conditions_object_identity_integration_test.go:TestFactCountUndeclaredKeyIsUnknown`（**真库**，`test-api-isolated.sh`） | `未声明的 factKey 必须返 error（引擎才能转 unknown），不能返回 0` | **已证**（本人真库重打） |
| 5 incomplete 仍发事件 | `app/Models/TrustLevels/recompute.go` 删掉 incomplete 闸里的 `return outcome, nil`（仍记 warn，但流程继续读账本/写账本/发事件） | `recompute_postgres_integration_test.go:TestRecomputeIncompleteNeitherEmitsNorOverwritesLedger`（**真库**） | `incomplete 时必须不发事件，实际发了 1 条` | **已证**（本人真库重打） |
| 6 插件键绕过运行时白名单 | `app/Models/TrustLevels/registry.go` 的 `FilterGranted` 去掉 `if r.Grantable(key)` 条件（全部放行） | `earnable_registry_test.go:TestGrantsDropsInactiveAndUnknown`（无库） | `授予了不该授予的键 "acme.wiki.pages"（完整 [topic.create acme.shop.orders acme.wiki.pages acme.gone.thing]）` | **已证**（本人重打） |

### 关于第 5 条的夹具敏感性（这条值得单独记）

上一版构造是「账本写 2，而 fail-closed 的结果恰好也是 TL2」——那样「不发事件」会因为
「本来就没变化」而假绿。现在的夹具是 `fixture.seedLedger(t, 3, …)`：账本 3，TL3 的规则引用
未声明 factKey 导致 incomplete，若闸被拆掉，比较结果就是 2 ≠ 3 → 必然发事件。我实测红出来的
正是 `实际发了 1 条`，说明这条构造现在是**敏感**的。

## 2. 测试基线数

| 范围 | 命令 | 数字（2026-11-14 本机实测） |
| --- | --- | --- |
| web 全量 | `cd apps/web && bun test` | **3007 pass / 0 fail / 288 files / 26182 expect() / 24.54s** |
| web 信任等级切片 | `bun test tests/admin/forum/trustLevel tests/profile/trustLevel` | 364 pass / 0 fail / 17 files |
| api 信任等级切片（无库单测） | `go test ./app/Models/TrustLevels ./app/Http/Controllers/TrustLevels ./app/Jobs/TrustLevels -count=1` | 3 包全 ok |
| api 全模块静态规模 | `rg -n "^\s*func Test" --glob '*_test.go' apps/api \| wc -l` | 7416 个测试函数 / 1454 个 `*_test.go` 文件 |
| 本刀新增测试文件 | `git log --since=2026-11-13 --diff-filter=A --name-only -- '*_test.go' '*_test.ts'` | 62 个（含徽章 / profile / settings surface 邻域；信任等级域内为 `Models/TrustLevels` 12 个 + `Controllers/TrustLevels` 1 个 + `Jobs/TrustLevels` 1 个） |

**为什么 api 只给「静态计数」而不给 `go test ./...` 的通过数**：数据库包只认
`SFORUM_TEST_DATABASE_URL`（`internal/testdb` 的设计），在本机裸跑会整包失败；
那个失败数不是基线，是环境噪声。真库口径要用 `./scripts/test-api-isolated.sh`
（整条链约 662 秒，见脚本注释），属于 M5 收口门禁该跑的东西，不适合当「盘点数字」。

## 3. 「永远绿的断言」清单

判据统一为一句：**「这条断言在什么单行改法下会红？」** 答不出来的，就是装饰。

### 3.1 已确认、且已在本刀内修掉的（3 条）

| 形态 | 为什么永远绿 | 现在怎样 |
| --- | --- | --- |
| H6 旧判据 `keys\|length >= 5` | 少一个内置 slug 时，别的键照样能把数补回 5 → 断言继续绿而兜底链已断 | 已改为逐 slug 存在且非空。实测：删 `member` + 加 `operator_custom`（键数仍 5）→ 旧判据绿 / 新判据红（`task-18`, commit `0a83926bb`） |
| H17 的 `h17="$(jq …)"` 读失败 | jq 报错时值集合是错误文本 → 交集恒空 → 恒 ok | 已改为「jq 读失败直接判 FAIL」并显式注释（同 commit） |
| 徽章侧 `row.text()` 看不见回落被删 | 断言只比渲染文本，回落链被砍掉时文本仍是另一条合法文案 | reviewer 在 F1 抓到，已由该单修（本条为引用，未由我复打） |

### 3.2 结构性弱判据（不是永远绿，但挡不住它想挡的事）

- **源码文本断言 12 处**：`expect(page).toContain("t('admin.forum.trustLevels.<key>')")` 形态
  （`trustLevels.test.ts`、`trustLevelsOverview.test.ts` 等）。它测的是「源码里有这串字符」：
  把 `t()` 调用换个引号/挪进变量就红，而把功能删掉、留下这串字符串仍然绿。
- **相对式断言**：`trustLevelI18nDeadKeys.test.ts` 断言 `errors.code.*` 条数 ==
  `SPECIFIC_ERROR_CODES.length`。两边同源：只加 locale 会红、只加码表会红，
  但「码表与 locale 一起加一条错码」仍绿——它挡的是单边漂移，不是错误增加。
- **记录器断言**：`evaluator.prepared` / `matched` 这类依赖 fake 记录行为的断言
  （第 1、2 条变异用的就是它们）。它们能挡「明显删代码」，但生产侧换成真实 evaluator 实现时
  这些断言不会跟着走。

### 3.3 阳性样本：本仓唯一主动检测自身灵敏度的写法

`tests/admin/forum/trustLevelAccentAttribute.test.ts` 末尾有一条
`the guard would fail if the attribute were dropped (mutation check)`——它读源码、**在测试内
做一次变异**、断言护栏确实报红。这是「证明断言不是装饰」的正确模板，建议 M5 收口时把它写成
纪律：**凡是读源码/读字面量的护栏，都要自带一条自检变异**。

## 4. 复跑命令（收口门禁用）

```bash
# 无库四条（1/2/3/6）：每条约 0.02s
cd apps/api
go test ./app/Models/TrustLevels/ -run 'TestNumericFailureSkipsRuleEvaluation|TestRuleFailureIsFailClosed|TestGrantsDropsInactiveAndUnknown' -count=1
go test ./app/Http/Controllers/TrustLevels/ -run 'TestPublicPayloadNeverCarriesRule' -count=1

# 真库两条（4/5）：起一次性集群
cd ../..
./scripts/test-api-isolated.sh ./app/Models/Conditions/ ./app/Models/TrustLevels/ \
  -run 'TestFactCountUndeclaredKeyIsUnknown|TestRecomputeIncompleteNeitherEmitsNorOverwritesLedger' -v

# 每次变异后必须还原并核字节
sha256sum -c <备份>.sha && git status --porcelain -- <被改文件>   # 期望：OK + 空
```

## 5. 变异纪律与还原证据

- 六次变异共涉及 6 个文件：`level.go`（两次）、`registry.go`、`recompute.go`、`resolve_facts.go`、
  `dto.go` + `payload.go`；全部 `cp -p` 备份 + `sha256sum` 前后比对。
- 还原后复核：`sha256sum -c` 6/6 `OK`，`git status --porcelain` 对这些路径为空。
- **没有**任何 dev 库写操作；两条真库测试走 `test-api-isolated.sh` 的一次性集群（用完即销毁）。

---

## 6. 一句话结论

**六条变异都能在本机复现转红，且都指到具体测试函数——可以支撑「M5 可以收口」；
但「能不能一直守住」取决于把 §4 的复跑命令与 §3.3 的自检变异纪律写进模块笔记，而不是这份报告。**
