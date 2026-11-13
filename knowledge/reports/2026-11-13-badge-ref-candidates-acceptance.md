# 徽章 objectRef 候选：验收矩阵（可判红）+ 基线快照

- 作者：小审（reviewer）
- 日期：2026-11-13
- 锚定 commit：`7c07edf9d`（本文所有"实现前"结论以此为基准）
- 写作用域：只用 `knowledge/reports/`。业务代码只读。
- 证据时效：本文的**行为判据**不依赖具体行号；引用的源码事实均标注文件与函数名，代码重构后按函数名重新定位，不要按行号。

---

## 0. 先给结论

1. **实现已在进行中，但 A4 基线仍然成立。** 截稿时未提交改动为：`dto.go`（新增 `refCandidateDTO` / `refCandidateGroupDTO` / `toRefCandidateDTO` / `toRefCandidateSetDTO`）、`rule_controller.go`（顶层多一个 `refCandidates` 键）、`admin_service.go`（`BadgeConditionCatalog` 多一个 `RefCandidates` 面）、`useAdminBadgeConditions.ts`，以及三个新文件。**`items` 与 `factCandidates` 的拼装函数 `conditionToDTO` / `toFactCandidateDTO` 在 `git diff` 中零改动**——所以"实现前基线"与"当前 HEAD + 该 diff"的两个子面形状等价，这是我敢用当前代码做基线锚点的依据，不是猜的。
2. **可判红矩阵共 6 组、22 条子断言；变异样本 9 条**，每条都标注"红在哪条子断言"。
3. **A4 的值级 live 快照本次未抓取**：本机 API（`:8080`、`sforum-api`）在跑，但没有 admin 会话凭据（`curl /api/v1/admin/badges/conditions` 无 cookie 走不到 200），我不会伪造一份 JSON 当证据。抓取命令与 sha256 锚定法见 §1.3，由 task-4 执行者在**动手改动业务代码之前**跑一遍落盘。
4. **实现方目前零新增测试**（`git status --porcelain | grep test` = 0）。task-4 验收前必须补齐 §5 列出的测试清单，否则 A1–A3 全靠人肉点，等于没有回归保护。

---

## 1. 基线

### 1.1 键级基线（值级 schema，已验证）

`GET /api/v1/admin/badges/conditions`（`useApiClient` 自己补 `/api/v1`，见 `composables/useApiClient.ts`）：

```
顶层            : items, factCandidates            （实现后：+ refCandidates）
items[]         : key, valueKind, compare[], labelZh, labelEn,
                  params[], 以及 describeZh?/describeEn?/unitZh?/unitEn?（omitempty，按条件出现）
items[].params[]: name, kind, required, 以及 min?/max?/options?/refKind?（omitempty，按参数出现）
factCandidates[]: key, label{zh,en}, unit|null, compare[], variants[{value,label{zh,en}}]
```

（依据：`app/Http/Controllers/Badges/dto.go` 的 `conditionDTO` / `conditionParamDTO` / `factCandidateDTO` / `factVariantDTO`，以及 `fact_candidate_dto_test.go` 的键集合断言。`unit` 是唯一可空对象字段；`compare`/`variants`/`params`/`items` 恒为数组，空也是 `[]`。）

实现后新增面（`git show` 可见，`dto.go` 的 `refCandidateGroupDTO` / `refCandidateDTO`）：

```
refCandidates[]             : kind, candidates[], unavailable, truncated   （四键都无 omitempty）
refCandidates[].candidates[]: value, label, flags[]                        （flags 恒为数组）
refCandidates[].kind 顺序   : badge, category, tag, role, provider          （conditions/refcandidates.go 的 refKindOrder）
```

### 1.2 已执行的可复现证据

```
$ cd apps/api && go test ./app/Http/Controllers/Badges/ \
    -run 'TestFactCandidateWireShape|TestFactCandidateEmptyCollectionsAreArrays' -v -count=1
=== RUN   TestFactCandidateWireShape
--- PASS: TestFactCandidateWireShape (0.00s)
=== RUN   TestFactCandidateEmptyCollectionsAreArrays
--- PASS: TestFactCandidateEmptyCollectionsAreArrays (0.00s)
PASS
ok  github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Badges  0.030s
```

这条只证明"当前 DTO 的既有契约测试仍然绿"，**不证明实现后的新字段对**。它的价值是：实现方若为了塞新字段而改弱/改坏 `TestFactCandidateWireShape`，那么从这一刻起的绿都是假的——所以该测试文件在本次验收里**只准增不准改**（A6-a 里用 `git diff --stat` 钉住）。

**顺手抓到的一个假绿（本文档自身的第一条教训）**：我第一次执行时 `-run` 里还写了 `TestConditionWireShape`——**这个测试不存在**，包内没有同名函数，而 `go test` 对"regex 一条都不匹配"仍然返回 `ok`。也就是说 `ok … 0.030s` 完全可能来自"一条测试都没跑"。所以本矩阵里凡是引用测试证据，都必须 `-v` 并要求输出里出现 `--- PASS` 行（A6-a 同此要求），不接受裸 `ok`。

`git diff` 事实（用于判定"items/factCandidates 未被碰"）：

```
apps/api/app/Http/Controllers/Badges/dto.go        | 54 ++++++++++++++++++++++   （纯新增类型与映射函数）
apps/api/app/Http/Controllers/Badges/rule_controller.go | 11 ++++-              （return 的 map 多一个键）
apps/api/app/Models/Badges/admin_service.go        | 38 +++++++++++++++++++--   （catalog 多一个面）
apps/web/app/composables/admin/badges/useAdminBadgeConditions.ts | 21 +++++++    （前端接新面）
```

### 1.3 值级快照（未执行，task-4 必须先跑）

**为什么必须落盘**：形状断言（§2 A4）只能抓"键/类型变了"，抓不到"某个 condition 少了 `options`""某个 `refKind` 拼错成 `provider_id`"这类值级漂移；而后者的症状是运营下拉静默变空，比 500 更难查。

```bash
API=http://127.0.0.1:8080/api/v1
J=/tmp/lcs-admin.jar
# 凭据由执行者提供；登录模式同 scripts/verify-badges-acceptance.sh:403
curl -s -c "$J" -X POST "$API/auth/login" -H 'content-type: application/json' \
  -d "{\"login\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" -o /dev/null -w '%{http_code}\n'

OUT=knowledge/reports/baseline-2026-11-13-conditions.json
curl -s -b "$J" "$API/admin/badges/conditions" -o "$OUT"
sha256sum "$OUT" | tee "$OUT.sha256"

# 形状指纹（只取 items / factCandidates 两个子面，逐路径 + 类型）
shape() { jq -S '[paths as $p
  | select($p[0]=="items" or $p[0]=="factCandidates")
  | {path: ($p|map(tostring)|join(".")), type: (getpath($p)|type)}]' ; }
shape < "$OUT" > knowledge/reports/baseline-2026-11-13-shape.json

# 计数基线（环境相关，必须记下来而不是假设）
jq -c '{items: (.items|length), factCandidates: (.factCandidates|length)}' "$OUT"
```

**环境相关性（不要当成缺陷）**：`items` 条数在本机是 12（Core 12 条内置条件）还是 13（外加 `fact.count`）取决于**是否装有声明 `badgeConditions[]` 的扩展**——`PublishableDefinitions` 只过滤"objectRef 无候选"的条件，而 `hasCandidates` 当前只对 `RefKind=="fact"` 生效。所以基线必须记录实测条数，任何"应该等于 12"的硬编码断言都会在装了夹具插件的环境假红。

---

## 2. 断言矩阵（A1–A6，22 条）

统一前置：

```bash
API=http://127.0.0.1:8080/api/v1 ; J=/tmp/lcs-admin.jar   # 登录同 §1.3
```

### 2.0 数据库断言通道

本机没有 `psql` 客户端，Postgres 在 Compose 里。取连接串与账号按 `.env` 的 `DATABASE_URL`，执行方式二选一：

```bash
docker compose exec -T postgres psql "$DATABASE_URL" -tAc 'select 1'      # Compose 内
psql "$DATABASE_URL" -tAc 'select 1'                                       # 有客户端时
```

**SQL 断言一律 `-tAc`（无表头单值）**，并在期望值里写死具体值，不要写"非空"就完事。

---

### A1 简单模式「已拥有徽章」

前置：两枚徽章 `B_SELF`（被编辑）、`B_OTHER`（被引用），都取 `enabled=true` 且 `slug` 已记录。

| 子项 | 判据 | 执行 | 期望 | 失败含义 |
| --- | --- | --- | --- | --- |
| A1-a | 候选存在且值是真实 slug | `curl -s -b $J $API/admin/badges/conditions \| jq -c '[.refCandidates[] \| select(.kind=="badge") \| .candidates[].value]'` | 数组含 `B_OTHER.slug`；`unavailable==false`；`truncated` 与条数自洽（`length<500` 时 `truncated` 必须 `false`） | 候选没接上 / store 未实现 `RefCandidateStore` → 控件仍是死下拉 |
| A1-b | 自身被排除（自环入口判死） | 同 A1-a 的 value 集合，与前端 DOM（A5 步骤里的 `[role=option]` 集合）各查一次 | 两处都 ∌ `B_SELF.slug` | 自身可选 → 保存要么 422 `forum.badge_rule_cycle`，要么存成永假规则；属实现缺陷，不许"文档里提醒运营别选自己" |
| A1-c | 保存成功且**值级正确** | `PATCH $API/admin/badges/$B_SELF_ID`，body `{"rule":{"v":1,"root":{"type":"badge.owned","params":{"slug":"<B_OTHER>"}}}}` | HTTP 200 | 前端"缺少必填参数。"消失但后端仍拒 → 契约没对齐 |
| A1-d | 落库值等于所选 | `select rule #>> '{root,params,slug}' from badges where id=<B_SELF_ID>;` | 精确等于 `<B_OTHER>` 的 slug（**不是**"非空"、不是 `""`） | 存了占位串（如 `candidatesPending`）或空串 → §13 假绿 #20 的经典形态：键在值空 |
| A1-e | 健康面不受影响 | `curl -s -b $J $API/admin/badges/rule-health` → 找 `badgeId==B_SELF_ID` 的项 | 该徽章**不出现**，或出现时 `issues` 里无 `missing_badge_slug` | 存进了候选表里不存在的值 → 说明前端把 label 当 value 提交 |

> A1-d 故意比任务书写得更严（要求"等于所选值"而不是"非空"）。理由：本刀最可能的真实缺陷是"控件能选了，但写进去的是 label / 空串"，只断言"非空"会漏掉 `params.slug=""`（键在值空，`jq 'has("slug")'` 照样绿）。

### A2 高级模式四条 objectRef 条件

对每一条独立 PATCH 到一枚专门的测试徽章（`B_AUTH` / `B_OAUTH` / `B_ROLE` / `B_OWNED`），body 里 `cmp` 只在计数型条件出现：

```json
{"v":1,"root":{"type":"object.authoring","params":{"targetKind":"category","targetSlug":"<category-slug>","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}
{"v":1,"root":{"type":"identity.oauth_link","params":{"providerId":"<provider-id>"}}}
{"v":1,"root":{"type":"identity.role","params":{"roleKey":"<roles.key>"}}}
{"v":1,"root":{"type":"badge.owned","params":{"slug":"<other-badge-slug>"}}}
```

| 子项 | 判据 | 执行 | 期望 | 失败含义 |
| --- | --- | --- | --- | --- |
| A2-a | 四条各自 200 | 上面 4 条 PATCH，逐条记 `http_code` | 全 200 | 任一 422 → 值是假的（手填了不存在的 slug/key）或控件提交了 label |
| A2-b | 落库值逐条值级正确 | `select rule #>> '{root,params,targetSlug}' …` 等四条 | 分别等于所选 category slug / provider id / role key / badge slug | 同上，键在值空 |
| A2-c | 健康清单不新增 `missing_*` | PATCH 前后各抓一次 `rule-health`，按 `badgeId` + `kind` 做差集 | 差集为空 | 引用失效 → 保存期校验与健康检查口径不一致（这条才是"能不能真用"的判据） |
| A2-d | **负向对照**（防"永远不报"的假绿） | 把 `identity.oauth_link` 的 `params.providerId` 改成 `__nonexistent__` 再 PATCH | 期望 PATCH 被拒（422 `forum.badge_rule_reference_invalid`）；若被接受，则 `rule-health` **必须**出现该徽章的 `missing_provider` | 负向对照都绿（既存得下又不报）= 健康检查根本没跑，A2-c 的绿是假的 |

> A2-d 不是可选项。没有它，A2-c 恒真：`RuleHealth` 直接 `return []` 也能让 A2-c 通过。

### A3 候选不可用时的手填兜底

分三层，前两层不需要动 dev 库。

| 子项 | 判据 | 执行 | 期望 | 失败含义 |
| --- | --- | --- | --- | --- |
| A3-a | 后端降级不 500、标记准确 | Go 单测：`AdminService` 注入**不实现** `RefCandidateStore` 的 fake store → `BadgeConditions` | `len(RefCandidates)==5`，五项 `Unavailable==true`，`Candidates` 长度 0（不是 nil 语义问题：DTO 层必须序列化成 `[]`）；HTTP 层仍 200 | 端点 500 → 候选故障把主流程打死（这是本设计明确禁止的形态） |
| A3-b | 单类故障只影响该类 | 临时 `ALTER TABLE roles RENAME TO roles__qa_tmp;` → curl conditions → 立刻 `ALTER TABLE roles__qa_tmp RENAME TO roles;` | role 组 `unavailable==true`、`candidates==[]`；其余四组 `unavailable==false` 且条数不变 | 一类失败导致五项全空 → 局部失败被放大，运营失去全部候选 |
| A3-c | 前端渲染文本框而非空下拉 | 浏览器在 A3-b 窗口内打开编辑 → 条件 `identity.role` | `[data-param="roleKey"]` 内存在 `input[type=text]`（不是只有 placeholder option 的 `select`），且有提示文案节点；手填合法 `roles.key` → 保存 200；A1-d 式 SQL 断言 `params.roleKey` 等于手填值 | 仍渲染空下拉 / 保存按钮被禁用 → 「此前必失败的死路」原样保留 |

**A3-b 的故障注入窗口纪律**（必须写进执行记录）：
- `roles` 被改名期间，**任何读 roles 的请求**（权限判定、后台用户页）都会 500。只在单人 QA 窗口执行，窗口目标 < 60s。
- 回收必须与注入成对执行；若 `ALTER TABLE roles__qa_tmp RENAME TO roles;` 失败，**立即停手报 Lead**，不要在坏状态下继续跑其它断言。
- 有并发同事在跑测试时不要用这条；改用 A3-a 的 fake store 路径 + 前端手工断网法（DevTools 把 `/admin/badges/conditions` 的响应改写为 `refCandidates:[{kind:"role",candidates:[],unavailable:true,truncated:false}, …]`），并在报告里注明"前端判据用了响应改写，不是真实故障"。

### A4 响应形状与既有面不变

| 子项 | 判据 | 执行 | 期望 | 失败含义 |
| --- | --- | --- | --- | --- |
| A4-a | 顶层键集合精确 | `curl … \| jq -S 'keys'` | `["factCandidates","items","refCandidates"]` | 多一个 `refKinds`/少一个 `factCandidates` → 契约漂移 |
| A4-b | `items`/`factCandidates` 形状与基线逐路径相等 | §1.3 的 `shape()` 输出与 `baseline-2026-11-13-shape.json` `diff` | 空 diff；且 `jq '.items\|length'`、`.factCandidates\|length` 与基线相等 | 任一非空 diff → 老编辑器会被打断（这正是"additive"承诺的反面） |
| A4-c | 五 kind、固定顺序 | `jq -c '[.refCandidates[].kind]'` | `["badge","category","tag","role","provider"]`（精确顺序，不是集合相等） | 顺序变了 → 前端按下标取组的实现静默错位 |
| A4-d | 每组四键、类型正确 | `jq -c '[.refCandidates[] \| {keys:(keys\|sort), c:(.candidates\|type), u:(.unavailable\|type), t:(.truncated\|type)}]'` | 每组 `keys==["candidates","kind","truncated","unavailable"]`、`c=="array"`、`u=="boolean"`、`t=="boolean"` | `candidates:null`（漏 make）会让编辑器把"没候选"读成"没下发"，两种降级混淆 |
| A4-e | `flags` 是值级真实的标记，不是摆设 | 先把一枚徽章置 `enabled=false`（后台停用），再 curl | 该 badge 候选的 `flags` 含 `"disabled"`；`flags` 类型为数组；正常徽章 `flags==[]` | flags 恒空 → A4-e 红；这是"改一行删掉 flags"的唯一哨兵 |
| A4-f | 候选值非空且组内唯一 | `jq -c '[.refCandidates[] \| [.kind, ([.candidates[].value] \| {empty: (map(select(.==""))\|length), dup: ((length) - (unique\|length))})]]'` | 每组 `empty==0`、`dup==0` | 空 value 被写进 `params` → 保存成功、永不命中（最难查的一类） |
| A4-g | `flags` 只标记不过滤（契约如此） | 同上那枚 disabled 徽章 | 它**仍然出现在** candidates 里（带 `"disabled"` 标记） | 被过滤掉 → 历史规则回显时下拉找不到自己的值，且与 `refcandidates.go` 的契约注释矛盾 |

### A5 390×844 弹窗内弹层不被裁切

判据全部是 DOM 度量，不用"看起来正常"。

```js
// 每个断点各跑一遍；desktop 用 1440×900，mobile 用 390×844
await page.setViewportSize({width:390, height:844})
await page.goto('/control-panel/badges')
// 打开编辑弹窗 → 规则区 → 高级模式 → 触发 objectRef 控件的弹层
const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
// 期望 overflow <= 0
const box = await page.locator('[role=listbox]').first().boundingBox()
// 期望 box.x >= 0 && box.x + box.width <= 390 && box.y >= 0 && box.y + box.height <= 844
```

| 子项 | 判据 | 期望 | 失败含义 |
| --- | --- | --- | --- |
| A5-a | 页面无横向溢出 | `document.documentElement.scrollWidth <= innerWidth`（390 与 1440 各一次） | 弹层撑破容器，移动端不可用 |
| A5-b | 弹层完全在视口内 | `[role=listbox]` 的 boundingBox 四条边都在视口内 | `USelectMenu` 未用 teleport / 未做翻转 |
| A5-c | 弹层可滚动到末项（候选 > 视口高度时） | 弹层 `scrollHeight > clientHeight` 时，`el.scrollTop = el.scrollHeight` 后末项仍 `boundingBox` 在视口内 | 弹层被 `overflow:hidden` 的祖先裁掉，长列表只能看到前几项 |
| A5-d | 弹窗本体不横向滚动 | 编辑弹窗容器 `el.scrollWidth <= el.clientWidth` | 参数行 `flex` 未 `min-width:0`（现有 `.sf-rule-field` 已有 `min-width:0`，回归会红） |
| A5-e | 键盘可达 | Tab 到控件后 Enter 打开、方向键移动、Esc 关闭；`aria-expanded` 翻转 | 可搜索选择换了控件后丢了键盘路径（`aria-label`/`title` 必须存在，见 AGENTS.md 窄屏折叠条款） |

截图落盘（文件名固定，便于 diff 与引用）：

```
knowledge/reports/2026-11-13-badge-ref-candidates-qa/
  desktop-1440x900-01-editor-open.png
  desktop-1440x900-02-badge-menu-open.png
  desktop-1440x900-03-selected-saved.png
  390x844-01-editor-open.png
  390x844-02-badge-menu-open.png
  390x844-03-fallback-text.png
```

### A6 三道门禁

| 子项 | 命令 | 期望 |
| --- | --- | --- |
| A6-a | `cd apps/api && go test ./app/Http/Controllers/Badges/ ./app/Models/Badges/... -v -count=1` | exit 0，且输出里出现 `--- PASS: TestFactCandidateWireShape` 与 `--- PASS: TestFactCandidateEmptyCollectionsAreArrays`（**裸 `ok` 不算**，见 §1.2 的教训）；**且 `fact_candidate_dto_test.go` 未被改动**（`git diff --stat -- '*fact_candidate_dto_test.go'` 为空） |
| A6-b | `node tests/validate-architecture-boundaries.mjs` | exit 0（新增 `refcandidates.go` / `ref_candidates.go` / `badgeRefCandidates.ts` 后必须跑，文件数与目录扁平的 ratchet 会管到） |
| A6-c | `ruby scripts/validate-openapi-refs.rb` | exit 0（`refCandidates` 进契约后必须跑；只加 Go DTO 不改 OpenAPI 也算契约漂移） |
| A6-d | `./scripts/test.sh --fast` | exit 0，日志在 `tests/.logs/`，把日志尾部贴进报告 |

---

## 3. 变异样本（9 条，逐条指定判红点）

做法：在本地副本上改一处 → 重跑对应断言 → 必须**红** → 还原。**不许**为了让它绿去放宽断言。

| # | 变异 | 必须红的断言 | 若没红说明什么 |
| --- | --- | --- | --- |
| M1 | 把 badge 组 `Unavailable` 强制 `true` | **A1-a**（候选集合不再含 `B_OTHER`）、A1-b 前置失效；A4-d 仍绿 | A1 只断言了"控件存在"而不是"候选含真实值" |
| M2 | `toRefCandidateDTO` 里 `Flags` 恒 `[]`（丢掉 disabled/hidden） | **A4-e** | flags 没有值级哨兵，前端标记逻辑随时可腐坏 |
| M3 | `refKindOrder` 只留 `badge` 一项 | **A4-c**（顺序/长度）、**A4-d**（组数） | 只断言了"含 badge"而不是"恰好五项固定顺序" |
| M4 | 删掉 `refCandidates` 顶层键（回到两键返回） | **A4-a** + **A1-a**（整体降级 → 候选无法从接口取到） | 前端有兜底就等于后端可以不下发——这是契约与实现脱钩 |
| M5 | 去掉前端"排除自身"的过滤（把 `B_SELF.slug` 放回可选项） | **A1-b**（DOM 选项集含自身） | A1 只看了接口，没看 DOM；或"自身排除"被当作后端职责从未实现 |
| M6 | `candidates` 由 `[]` 变 `null`（删掉 `make(...)` 初始化） | **A4-d**（`type!="array"`）、A3-a | 空集合与"未下发"的区分没人守 |
| M7 | 漏掉 `truncated` 键（给它加 `omitempty`） | **A4-d**（键集合不等） | 键存在性断言写成了"包含"而不是"相等" |
| M8 | 给某条 condition 的 `params[]` 加一个键（或给 `conditionDTO` 加 `refKinds`） | **A4-b**（路径 diff 非空） | 基线只记了顶层键，没记整棵路径树 |
| M9 | `RuleHealth` 直接返回空 `items` | **A2-d**（负向对照不再报 `missing_provider`）；A2-c 仍绿 | A2-c 的"不新增 `missing_*`"是真空绿——这正是 #20 的同族 |

**判定纪律**：每条变异都要留下"变异 diff 片段 + 红的断言名 + 红的具体输出"。只写"跑过了，红了"不算证据。

---

## 4. 浏览器 QA 步骤（桌面 + 390×844）

1. 起环境：`./scripts/dev.sh`（依赖 + 迁移）；API 由 `./scripts/api-dev.sh` 跑（`:8080`）；web dev server 用户自己跑（`:3000`，**不要杀**）。
2. 登录后台，确认侧栏进得去 `/control-panel/badges`。
3. 桌面 1440×900：编辑 `B_SELF` → 规则区 → 简单模式 → 「已拥有徽章」→ 打开弹层 → 截图 `desktop-…-02`。
4. 断言 A1-a/b/c/d/e（DOM 选项集合 + `refCandidates` 接口集合 + PATCH + SQL）。
5. 高级模式：依次构造 A2 四条 → 每条保存 → 截图 `desktop-…-03`。
6. 切 390×844（`Emulation.setDeviceMetricsOverride` 或 playwright `setViewportSize`）→ 重跑 3–5 的 A5 度量断言 → 截图三个 `390x844-*`。
7. A3：按 §2 A3-b 的窗口纪律注入 role 故障 → 刷新编辑器 → 断言文本框兜底 → 手填保存 → **立即回收** → 截图 `390x844-03-fallback-text.png`。
8. 收口：`rule-health` 差集为空；停用/启用的徽章状态复原；把 `baseline-*.json.sha256` 与本次 conditions 响应 sha256 并列写进报告。

---

## 5. task-4 验收前必须补的测试（当前为零）

1. `toRefCandidateSetDTO` 键集合与类型（含 `candidates:[]` 而非 null、`flags:[]` 而非缺键）——对照 `fact_candidate_dto_test.go` 的写法。
2. `NewRefCandidateGroup`：去重、空 value 丢弃、按 label 排序稳定、`Truncated` 只在超 `RefCandidatesLimit` 时为真。
3. `PostgresStore.RefCandidates` 单类失败 → 该类 `Unavailable`、其余不受影响、返回 error 但集合可用（这条是 A3-b 的自动化版本，替代人肉改表）。
4. `BadgeConditions` 在 store 不实现 `RefCandidateStore` 时五项全 unavailable 且不返回错误（A3-a）。
5. 前端：`useAdminBadgeConditions` 对缺失/畸形 `refCandidates` 的归一化（老后端两键响应 → 全量兜底而不是崩），以及"自身排除"纯函数。

没有 3、4 两条，A3 只能靠改 dev 库表名来验，长期不可持续（§13 #22 的教训：一次性手工证据会过期）。

---

## 6. 开放问题 / 需要 Lead 裁决

1. **A4 的值级 live 快照我抓不了**（无 admin 凭据）。选项：Lead 提供一次性凭据，或把 §1.3 指定给 task-4 执行者并要求"改动业务代码前先跑"。这是本矩阵唯一的证据缺口，其余 21 条子断言都可执行。
2. **自身排除写在哪一层没有定论**。当前实现里 badge 候选是全局列表（`SELECT slug, name, enabled FROM badges`），没有 badge 上下文；接口按 `badgeId` 过滤需要改 endpoint 签名。我的 A1-b 把判据钉在 DOM 上（前端过滤即可满足），但如果实现方认为该由后端做，请明确，否则两层都不做 = A1 红。
3. **provider 组"未注入"与"没配置登录方式"必须能区分**。`UnavailableRefCandidateSet()` 与"查询成功但零条"在 wire 上是 `unavailable=true` vs `unavailable=false, candidates=[]`。建议实现方补一条测试钉住，否则运营会把"没配 OAuth"读成故障。
4. **A3-b 的故障注入窗口**需要 Lead 确认没有并发测试在跑（`roles` 改名期间权限判定会 500）。若不行，A3 的前端判据降级为"响应改写"，并在报告里如实标注手段。
