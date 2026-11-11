# 信任等级 — 验收执行矩阵 v3（M9–M13 增量）

Owner: pm（阿策）
Status: **已提交（2026-11-11，commit `57a3de5ad`）**。第三版，在 `2026-11-10-trust-levels-acceptance-matrix.md`（v2，124 条）基础上
**追加** J 组 14 条 + K 组 5 条 + L 组 6 条 = **25 条新增，总计 149 条**。
v2 原件**只改过一处**：I-16 因判据不可满足经 lead 裁决修订（原判据原文留档于 v2 I 组表后，新判据见本文件 §11）；其余不动，A–I 组的编号与内容继续由 v2 定义，本文件只定义 J/K/L。
**矩阵是唯一编号源**：`scripts/verify-trust-levels-acceptance.sh` 按 ID 落盘，矩阵有而脚本没有的进 §8。

> 本版最重要的三件事，先看这里：
> 1. **v2 的 jq 断言漏了信封**（§0-1）。响应是 `{code,message,data}`，照 v2 字面写 `.levels` 必红——
>    已实测产出 3 条**假 FAIL**（I-09 / I-11 / I-24）。v3 所有 jq 一律 `(.data // .)`。
>    这一条已单独立为 **L-02**：断言指错层，差点让 M8 的 0 FAIL 门槛被自己的脚本撞掉。
> 2. **默认安装下「TL 解锁能力」是空操作，这是正确行为**（§4）。判据只能是「配置生效」，不是「行为变化」。
> 3. **`eligiblePermissions` 缺失或为 `null` → J-01 判 FAIL**，不是 SKIP、不是 PEND（§2）。

---

## 0. 本版相对 v2 的变更（带实测）

### 0-1 信封：v2 的 jq 路径集体错一层（P0，影响 M8 的 0 FAIL 门槛）

宿主路由统一返回 `{code, message, data}`（`apps/api/app/Http/responses.go:13-17`），
web 侧 `useApiClient` 也是按 `envelope.data` 解包（`apps/web/app/composables/useApiClient.ts:21-25`）。
v2 §2.1 与 I 组写的 `jq '.levels'` / `jq 'has("config")'` 少了 `.data` 一层。

实测后果（2026-11-11T09:26:23，`storage/qa/trust-levels/I/`）：

| 条目 | 现有证据 | 真因 |
| --- | --- | --- |
| I-09 | `{"has_config":false,"has_levels":false,"has_eligible":false,"has_recommended":false}` HTTP=200 | 断言看顶层，字段在 `.data` 下 |
| I-11 | `PUT {"config":""}` → 422 `trust_levels.invalid_config` | `.config` 取到 `null`，回写空串 |
| I-24 | 库内 1080 字节 vs 响应 4 字节（`null`） | 同上 |

**这三条是断言 bug，不是产品缺陷。** 修法：所有响应断言改 `(.data // .)`，
`scripts/verify-trust-levels-acceptance.sh:1808/1852/1853` 三处必须同轮修掉，
否则 M13 会把 3 条假 FAIL 记进合并表。

### 0-2 测试名实测清单（本版只允许出现这里出现过的名字）

| 名字 | 实测命令 | 结果 |
| --- | --- | --- |
| `TestAdminEligiblePermissionsLiteral` | `go test -list '.*' ./app/Http/Controllers/TrustLevels/` | **存在**；`-run '^…$'` 实跑 `ok`（09:2x） |
| `TestEligiblePermissionsIsLiteralWhitelist` | `go test -list '.*' ./app/Models/TrustLevels/` | 存在 |
| `TestCoreRouteCatalogHasExactReviewedGuardParity` | `go test -list '.*' ./app/Support/Routes/` | 存在；实跑 `ok`（09:2x） |
| `TestAPIErrorCodesHaveLocalizedMessages` | `go test -list '.*' ./app/Support/Localization/` | 存在；实跑 `ok`（09:2x） |

其余条目**没有现成测试名可写**，一律标「无现成测试」并给替代手段（R1/R6）。
本版没有任何一条把「计划里打算补的测试名」当既有事实写——v2 第一版 30 个名字 29 个不存在的教训，本版按此复核。

### 0-3 与 v2 §1.2 的口径冲突（以实库为准）

v2 §1.2 写「戊 = `admin.access` + `trust.manage`」。**实库不符**：
`storage/qa/creds.txt` 的 戊 是 `zhuchunshu`（user 1，**super_admin**）。
而 `Actor.Can()` 对 super_admin 直接返回 true（`apps/api/app/Models/Identity/policy.go:26`），
所以 v2 的 I-10/H3「戊 PUT 200 / 丁 PUT 403」**从未真正验过 `trust.manage` 边界**——
戊 走的是超管短路，丁 是因为连 `admin.access` 都没有。v3 用 §1.2 的两个专用角色把这条边界补回来。

---

## 1. 公共前置

### 1.1 服务与命令

沿用 v2 §1.1 的 `API` / `ORIGIN` / `EV` / `Q()` / `login()` / `call()`，不复制。
**唯一修订**：`call()` 返回的 body 是完整信封，jq 一律 `(.data // .)`：

```bash
call /tmp/jar-wu GET /admin/trust-levels | sed '$d' | jq -e '(.data // .) | has("eligiblePermissions")'
```

### 1.2 账号矩阵 v3（实库状态 + 授权命令）

**实库实测（2026-11-11 09:2x，`Q "select id,username,status from users where id in (1,3,4,457,481,482,483,484,485,486,487)"`）**：
`qa_tl0_blank`=481 / `qa_tl1_edge`=482 / `qa_tl2_resident`=483 / `qa_tl3_veteran`=484 /
`qa_tl4_leader`=485 / `qa_tl_admin_ro`=486 / `qa_tl_admin_rw`=487 全部**已存在**；
但 486/487 **只有 `member` 角色**（各 8 个权限：`topic.create` 等 7 键 + 一个插件键），
**既无 `admin.access` 也无 `trust.manage`**。`storage/qa/seed-trust-levels-matrix.sh` 只注册、不授权，
`storage/qa/creds-trust-levels.txt` 至今不存在——所以本版把授权步骤显式写进矩阵。

| 代号 | 账号（实库 id） | 目标权限 | 用途 | 当前实测 |
| --- | --- | --- | --- | --- |
| 甲 | `seed_44888364289b` (3) | member | 被统计者、`viewer` 本人视角 | ✅ |
| 乙 | `seed_4cdff0f1dc29` (4) | member | 分档限额、`viewer.nextLevel` | ✅ |
| 丙 | `qa_badge_probe` (457) | member | 零基线对照（**不是 TL4**，v2 该行已过期） | ✅ |
| TL0 | `qa_tl0_blank` (481) | member | 零基线、`earned` 必空 | ✅ 账号在，等级待造数 |
| TL2 | `qa_tl2_resident` (483) | member | 分档配置生效 | ✅ 账号在 |
| TL4 | `qa_tl4_leader` (485) | member | 无 `nextLevel`/`bottleneck` | ✅ 账号在 |
| 丁 | `qa_tl_admin_ro` (486) | `admin.access`（**无** `trust.manage`） | 读得进、写 403：J-12 的对照 | ✅ 已授权（实测 `member + qa_tl_ro`） |
| 戊 | `qa_tl_admin_rw` (487) | `admin.access` + `trust.manage` | 正常读写：J-01/J-02/J-03/J-08 | ✅ 已授权（实测 `member + qa_tl_rw`） |
| 己 | `zhuchunshu` (1) | super_admin | **只做授权执行者与对照组**，不当写权限证据（`Can()` 短路） | ✅ |
| 庚 | `qa_tl_admin_rw2` (488) | 同 `qa_tl_rw` | **白名单外键构造者**：J-04 专用，与 戊 分开以免把 422 的现场混进 J-03 的 200 证据 | ✅ 已建并授权（实测 `member + qa_tl_rw`） |

**建号与授权（优先走真实写路径，`己` 执行）**

```bash
QA_PASS='QaPass!2026-devops'
login 'zhuchunshu999@gmail.com' "$QA_PASS" /tmp/jar-ji          # 密码见 storage/qa/creds.txt

# 1) 两个专用角色（幂等：已存在回 409，跳过继续）
call /tmp/jar-ji POST /admin/roles \
  '{"key":"qa_tl_ro","alias":"TL 只读","description":"admin.access only：验读面边界"}'
call /tmp/jar-ji PUT  /admin/roles/qa_tl_ro/permissions '{"permissions":["admin.access"]}'
call /tmp/jar-ji POST /admin/roles \
  '{"key":"qa_tl_rw","alias":"TL 读写","description":"admin.access + trust.manage：验写面边界"}'
call /tmp/jar-ji PUT  /admin/roles/qa_tl_rw/permissions '{"permissions":["admin.access","trust.manage"]}'

# 2) 绑到既有账号（保留 member，别把正常发文能力摘掉）
call /tmp/jar-ji PUT /admin/users/486/roles '{"roleKeys":["member","qa_tl_ro"]}'
call /tmp/jar-ji PUT /admin/users/487/roles '{"roleKeys":["member","qa_tl_rw"]}'

# 3) 庚（J-04 专用）：注册后绑同一个 qa_tl_rw 角色
call "" POST /auth/register \
  '{"email":"qa_tl_admin_rw2@seed.local","password":"QaPass!2026-devops","username":"qa_tl_admin_rw2"}'
Q "SELECT id FROM users WHERE username='qa_tl_admin_rw2'"
call /tmp/jar-ji PUT /admin/users/<庚id>/roles '{"roleKeys":["member","qa_tl_rw"]}'
```

**SQL 兜底（仅角色/权限四张表；**禁止**碰 `user_activity` / `user_visits`，那是 C/E 组的被测对象）**

```sql
INSERT INTO roles(key,alias,description) VALUES ('qa_tl_ro','TL 只读','admin.access only') ON CONFLICT (key) DO NOTHING;
INSERT INTO roles(key,alias,description) VALUES ('qa_tl_rw','TL 读写','admin.access + trust.manage') ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions(role_id,permission_key)
  SELECT r.id, v.key FROM (VALUES ('qa_tl_ro','admin.access'),('qa_tl_rw','admin.access'),('qa_tl_rw','trust.manage')) AS v(rk,key)
  JOIN roles r ON r.key = v.rk ON CONFLICT DO NOTHING;
INSERT INTO user_roles(user_id,role_id)
  SELECT v.uid, r.id FROM (VALUES (486::bigint,'qa_tl_ro'),(487::bigint,'qa_tl_rw')) AS v(uid,rk)
  JOIN roles r ON r.key = v.rk ON CONFLICT DO NOTHING;
```

验证（两条都要有：权限在、且只有该有的）：

```sql
SELECT u.id, u.username,
       array_agg(rp.permission_key ORDER BY rp.permission_key)
         FILTER (WHERE rp.permission_key IN ('admin.access','trust.manage')) AS gate
FROM users u
JOIN user_roles ur ON ur.user_id = u.id
JOIN role_permissions rp ON rp.role_id = ur.role_id
WHERE u.id IN (486,487) GROUP BY u.id, u.username;
-- 期望：486 → {admin.access}；487 → {admin.access,trust.manage}
```

授权后**不需要重启 API**（`LoadActor` 每请求读库），但重新 `login` 一次更干净。
**实测状态（2026-11-11 09:4x）**：486/487/488 三个账号的角色都已绑好（`member + qa_tl_ro` / `member + qa_tl_rw`），
命令保留在下面供重建环境时复用。

**备注（researcher 建议，lead 已采纳）**：要做「勾了**真的**生效」的端到端实测，选 `attachment.upload`
——它是 7 键里唯一可能被运营手工摘过的（20260705 才追加进基线）。
**但必须先摘基线**：默认安装下 7 键全在 `member` 基线（§4），不摘就测不出行为变化。
步骤见 §3 J-03 的「腿 C」。

### 1.3 证据规则增量（R6–R9，接 v2 R1–R5）

| 规则 | 内容 |
| --- | --- |
| **R6 「怎么验」列只写实测存在的东西** | 测试类只允许 `T: ^Name$ @ pkg` / `ISO: ^Name$ @ pkg`，且名字必须在 §0-2 的实测清单里；否则写「**无现成测试**」+ 替代手段。浏览器/curl/rg/jq 类条目不写任何 `Test*` 名。 |
| **R7 jq 必须解信封** | 一律 `(.data // .)`。`has()` 的极性按端点方向写，见 §2。 |
| **R8 「配置生效」≠「行为变化」** | 涉及「TL 解锁能力」的条目只断言 earnable 映射与 UI 状态；断言「勾了就能发帖」必红且无意义（§4）。 |
| **R9 修完的旧条目要回填** | I-09/I-11/I-24 修脚本后必须重跑并覆盖旧证据文件，旧 FAIL 不能留在目录里冒充当前状态。 |
| **R11 「切换后首请求」必须先判定状态的作用域** | 凡「状态切换后的首请求」类断言，必须回答：该状态是**请求级**、会话级、还是缓存级？`apphttp.LoadActor` 是**请求级**（`auth_user.go:47` 每请求 `users.LoadActor`，一次装载 4 次查询），所以任何「切换后首个请求仍是旧态」的期望**都不成立**——旧态只存在于「装载那一刻开关是关的」那个请求里。本仓 `LoadActor` 有 45 个调用点（v2 G6 口径）、每请求至少 1 次，这条判断适用面很广。反例：v2 I-16（见 §11）。 |
| **R12 动态拼接键不能按字面集合对账** | `admin.*` 里存在 `prefix.${var}` 形态的动态键（已知：`admin.badges.grantMode.${mode}`、`admin.badges.metrics.${metric}`）。朴素 `comm` 会同时报假「定义了没人用」与假「用了没定义」（前缀残片）。对账必须先把前缀残片展开成实际键值，再比集合；判据写 `missing=0 且 extra=0`，不许拿 `comm` 原始输出当结论。 |
| **R13 证据有效性：文件存在 ≠ 断言成立** | 证据文件必须写「**前置校验**」三件：**开关状态**（`features.trust_levels` / `trust.new_user_days`）、**session 可读**（登录后 `200`）、**现场已清理**（变异副本已删 / `docker unpause` 已做，且**可机械验**：`ls` 该路径应报 not found）。缺前置校验按**未实现**记。**第三项硬要求**：证据文件里的断言内容必须与被验 ID 的**字面断言一致**；**登记/转述文件不算证据**（`I/I-03.txt` 是 ops 登记、指向的是另一条断言，见 §8）。
**第四项**：证据必须带**采集时间**，且结论依赖的实现文件要能对上（sha256 或提交号）——否则「文件在、断言对」可能证明的是**旧行为**。四种真实假象：① harness 读早了 → **假 FAIL**（J-03 首落盘）；② `go test -overlay` 给**扫描类**测试加副本 → **假绿**（AST 扫描读磁盘，overlay 的文件看不见）；③ 文件在、ID 对得上，但内容验的是别的断言 → **连红都不会红**（I-03 转述件）；④ 文件对、断言对，但采集时间早于实现变更 → 证明的是**旧行为**。 |
| **R16 验收矩阵的绿 ≠ 仓库的绿** | 矩阵只证明「**声明过的条目有证据**」，不证明「**没声明的地方没问题**」。本刀就是例子：124 条全绿的同时，**整仓门禁红着，里面藏着一个 P0**（authority-gate）。**下一刀的验收必须顺带跑一次整仓门禁（`./scripts/test.sh --fast` 三线 + 五基线），`0 FAIL` 不得单独作为可提交依据。** |
| **R15 会移动的事实必须带「时点 + 可复算方式」** | 证据里凡是**会移动的东西**——**行号、`HEAD`、工作树内容、开关状态、账号档位**——都必须写明**采集时点**与**怎么复算**（`sha256sum` / `git show HEAD:<path>` / `jq` 路径 / `SELECT`）。**只带值不带时点的，按未实现记。** 收口句：**每个「当时的事实」都有有效期，而文档默认它们永久。** 本刀三层递进（行号会漂 → 要 sha256；sha256 要标哪棵树 → 工作树/提交；`HEAD` 本身会移动 → 不能当期望）都是这一条的实例。 |
| **R14 任何路径/计数写进文档前，先跑一次命令** | 路径用 `ls`/`find`，计数用 `wc -l`。**「没数就写数字」和「没 `ls` 就写路径」是同一类**——reviewer 终表路径数先写 26、实测 31，就是计数版的同一病。文档里的路径只有两种合法来源：**`ls`/`find` 的真实输出**，或**明确标注为约定命名**。把约定当事实写 = 读者照抄扑空，然后误判「证据丢了」。本刀两例：v3 §8 拿 §3 的约定命名当落点（我）；reviewer 终表写 `browser-j03-*.json` 通配（照抄会扑空，已改实名）。**通配符不是路径**，通配必须展开成实际文件名。**机械检查**：落盘后跑一次
`find "$REPO" -maxdepth 5 -type d \( -path '*/apps/api/storage/qa*' -o -path '*/storage/qa/trust-levels' \) | sort`
——**应当只出现仓库根那一份**；出现第二份就是写错树了（本刀 `apps/api/storage/qa/trust-levels/L/` 就是这么被抓出来的）。 |
| **R10 守卫条目必须有红态证据（可执行定义）** | 守卫/门禁类条目必须同时落 `<ID>-red.txt` 与 `<ID>-green.txt`，只有绿态按「未验证」记。**`-red.txt` 的合格形态**：必须**同时**含 ① 失败断言的**行号**（`file.go:NN`）② 该断言的**点名文字**（断言里写的那句话本身）。缺任一项不算红态证据——这正是「红了但红错地方」的机械判据（L-06 第五条假守卫）：`total = 21（期望 22）` 这种只有数字、没有点名句的红，说明你真正想守的那条断言根本没执行到。**第三项要求**：点名的断言必须是**本条条目要守的那一条**，不能是同一测试里的别的断言——反例样本就是 L-06 的 A 变异（总数保持 22、只改坏一个 registry ID）：它证明了「点名断言先于计数断言」，如果只跑 B 变异（22→21），红的是计数，你会误以为点名断言也被验证过。 |

### 1.4 等级判定是**两段式**的（涉及 `level` 的断言必读）

**等级 = 六项计数 + 账龄**，不是只看计数。`LevelOf`（`app/Models/TrustLevels/level.go:23-35`）分两段：

1. **账龄闸**：`policy.NewUserDays > 0 && accountAge < NewUserDays*24h` 时**最高只到 TL1**
   （活动够 → TL1，不够 → TL0）。`NewUserDays` 默认 7 天，来自 option `trust.new_user_days`；设为 **0 即关闭该闸**。
2. **活动闸**：过了账龄闸，从 TL4 往 TL1 找第一个被满足的档，**最后 `return LevelMember`**——
   也就是说「**账龄超过新人窗口、但六项计数全为 0**」的正确结果是 **TL1，不是 TL0**。

**两条硬结论（都是本刀踩过的）**：

- 任何**只用六项计数推档**的断言都是错的（I-16b 就是这么错的：用户 1 账龄 38 天 > 7 天，
  计数全 0，正确档位是 TL1，脚本算出 0）。**别在 shell 里重算 `LevelOf`**——那是写第二份 `LevelOf`，
  正是本刀第三次复发的那个坑（L-03）。档位与计数的一致性交给
  `T: ^TestLevelAgeGate$` / `T: ^TestLevelBoundaries$ @ app/Models/TrustLevels`。
- **dev 库要验 TL2+，必须先把 `trust.new_user_days` 设为 0**（或等满 7 天）。
  现库实测：`qa_tl0_blank`…`qa_tl_admin_rw`（481–487）**账龄仅 12 小时**，全部落在账龄闸内，
  **不论造多少活动都封顶 TL1**；`qa_tl_admin_rw2`（488）更小。v2 的「腿 B 阈值下探」只下探了
  `Requirements`，**漏了账龄闸**——这是同一个「期望算法漏输入」在夹具层的翻版。

**TL0 的可造性**：TL0 要求「账龄 < 7 天 **且** 六项计数未达 TL1（`days_visited≥2` 且 `comment_count≥1`）」。
481 现为 12 小时账龄 + 零活动，可作 TL0 基线；**一旦把 `trust.new_user_days` 设为 0，TL0 就不可造**
（老账号最低 TL1）。两条腿互斥，排期时别同时要。

#### 1.4.1 已落盘的 C/D 证据**不需要重审**（警报范围收窄，2026-11-11 09:5x）

lead 复核后把上一条的警报收窄，结论如下，别再扩大：

| 证据 | 实际情况 | 是否受影响 |
| --- | --- | --- |
| `C/C13` | 用的是**原始计数**（`topic=16 comment=11 like_given=0 like_received=55 days=1`），不声称「用 TL2 账号验到 X」 | **否** |
| `D/D2` | `pend`，交代写明「429 被冷却抢先，日限额未测到；需先置 `topic_cooldown_seconds=0`」，本身不是等级证据 | **否** |
| C/ 与 D/ 全部 | `rg -l "viewer.level\|level="` **零命中**——没有任何一条把「账号档位」当判据 | **否** |

**规则（对以后生效，不追溯）**：凡证据要**声称档位**，必须写**采集时实测的 `viewer.level`**，
不许写「我造的是 TL2 账号」。判据从「账号类型」改成「**实测档位**」——
某一刻的设计意图不是永久事实。这条与附录 A 第四句自检同源。

**采集头部纪律（与 `features.trust_levels` 同等对待）**：每条 C/D 证据的头部必须同时写明
**开关状态**与 **`trust.new_user_days` 的值**；改过配置的，跑完**必须还原**（`new_user_days` 回 7），
并复跑一次公开端点确认 200。

**排期（lead 定）**：① 先跑 `new_user_days=7` 的 TL0 组（C13–C16、D2/D3）；② 再设 0 跑 TL2+ 组；
③ 最后还原 7 并复跑公开端点确认 200。


---

## 2. 契约裁决：`eligiblePermissions` 是编辑器的唯一数据源

**裁决（lead，2026-11-11）**：M10 之后 `GET /admin/trust-levels` 的 `eligiblePermissions`
是 earnable 编辑器的**唯一**白名单来源。前端零硬编码（T22）；后端它是
`trustlevels.EligiblePermissions()` 的直出（`controller.go:145`），顺序即 UI 展示序。

**FAIL 判定（写死，不许解释成 SKIP）**：

| 响应形态 | 判定 |
| --- | --- |
| 键缺失 | **J-01 FAIL** |
| 键存在但为 `null` | **J-01 FAIL** |
| 键存在但是空数组 `[]` | **J-01 FAIL**（白名单非空，空数组等于「没有可勾的键」= 谎） |
| 键存在、数组、长度 7、字面集合相等 | PASS |

**极性陷阱（下一个人最容易踩的）**：同一个键名在两个端点上的断言方向**相反**。

| 端点 | 断言 | 来源 |
| --- | --- | --- |
| `GET /trust-levels`（公开） | `(.data // .) \| has("eligiblePermissions") \| not` | v2 §2.1：公开面不得泄漏白名单 |
| `GET /admin/trust-levels`（后台） | `(.data // .) \| has("eligiblePermissions")` **且** `type == "array"` **且** `length == 7` | 本条 |

两条都用 `has()`，方向相反。**复制粘贴时必须同时改端点与极性**——
把 §2.1 那条负向断言搬到后台面，就会把「字段漏了」判成通过（v2 R5 的同一个坑，换了个位置）。

后端测试已经按这个口径写死：`TestAdminEligiblePermissionsLiteral` 解 `.data.eligiblePermissions`，
缺失或 `null` 时该字段为 `nil` → `len(got) == 0 != 7` → 红。所以 Go 侧不存在「漏字段还绿」的可能；
需要单独在 HTTP 层再断言一次的原因是——J-07 的组件测试会 mock 500，
**如果后端真的漏字段，mock 分支会变成真实分支，组件测试仍然绿，而产品是错的**。

---

## 3. 新增条目（J 组 14 / K 组 5 / L 组 6）

> **「证据」列写的是约定命名（预期落点），不是实际文件。** 本刀很多条目合并到同一份证据文件
> （浏览器七条在 `K/browser-matrix.md` + `K/logs/browser-*.json`；K 组在 `K/K-0*.md`），
> **实际落点以 §8 覆盖核对为准**。别按本列的路径去 `ls`，会扑空。

### J 组 — earnable 编辑器（M10）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| J-01 | ops | `T: ^TestAdminEligiblePermissionsLiteral$ @ app/Http/Controllers/TrustLevels` | `J/J-01.txt` |
| J-02 | ops | `T: ^TestAdminEligiblePermissionsLiteral$ @ app/Http/Controllers/TrustLevels` | `J/J-02.json` |
| J-03 | rev | 浏览器：戊 勾 TL2 `topic.create` → 保存 → 重载，勾选态保持 | `J/J-03-*.png` + `J/J-03.txt` |
| J-04 | rev | 浏览器（庚，白名单外键构造者）：矩阵里无 `user.ban` 可勾 + 手造 PUT body → 422 | `J/J-04-body.txt` + `J/J-04.png` |
| J-05 | rev | 浏览器：TL0 行整行 disabled，勾不动（`aria-disabled="true"`） | `J/J-05.png` + `J/J-05-aria.txt` |
| J-06 | rev | 浏览器：`innerText` 含「及以上」（累积语义披露常驻） | `J/J-06.txt` |
| J-07 | frontend | `bun test apps/web/tests/admin/settings/trustLevelsTabMount.test.ts`（frontend 用例，非 Go 测试；4 用例 17 断言，**带对照组**） | `J/J-07.txt` |
| J-08 | ops | **无现成测试** → 戊 PUT `earnable: []` → 重载后仍 `[]`（不丢成 `null`/省略） | `J/J-08.json` + `J/J-08.txt` |
| J-09 | rev | 变异：白名单塞 `user.ban` → J-01 红；改回绿 | `J/J-09-red.txt` + `J/J-09-green.txt` |
| J-10 | rev | 浏览器 390×844：无横向溢出，7 列不裁切 | `J/J-10-390x844.png` |
| J-11 | rev | 浏览器：开关关闭 → tab 顶部 `UAlert`，文案含「保存会成功但不会生效」；支撑用例 `bun test apps/web/tests/admin/settings/trustLevelsTabMount.test.ts` | `J/J-11.png` + `J/J-11.txt` |
| J-12 | rev | 浏览器 + curl（丁）：侧栏无 tab、直连 URL 可读但保存控件 disabled + 说明；PUT 仍 403 | `J/J-12.png` + `J/J-12.txt` |
| J-13 | rev | `T: ^TestEarnableGrantsCumulative$ @ app/Models/TrustLevels` | `J/J-13.txt` + `J/J-13-red.txt` |
| J-14 | rev | `T: ^TestGrantSourceIsStaticForDefaultInstallWhitelistKeys$ @ app/Models/Identity` | `J/J-14.txt`（**红态与前置同文件**，无独立 `-red` 文件） |

**判据细节**

- **J-01**：Go 测试已含「长度 7 + 字面集合 + 顺序 + 两次读稳定」四条断言（09:2x 实跑 `ok`）。
  ops 侧必须另落一次 HTTP 证据（R7）：
  `call /tmp/jar-wu GET /admin/trust-levels | sed '$d' | jq -e '(.data|has("eligiblePermissions")) and (.data.eligiblePermissions|type=="array") and (.data.eligiblePermissions|length==7)'`。
  缺字段 / `null` / `[]` 一律 **FAIL**（§2）。
- **J-02**：顺序断言同样在 `TestAdminEligiblePermissionsLiteral` 里（`slices.Equal(got, EarnableWhitelist())`）。
  字面顺序：`topic.create, post.create, attachment.upload, topic.edit_own, post.edit_own, topic.delete_own, post.delete_own`。
  **别拿 `SeedMemberPermissions` 的顺序当期望**——两份清单顺序不同，断言同序必红。
- **J-03**：只断言「配置生效」（earnable 映射往返 + 勾选态保持），**不断言**用户能力变化（§4）。
  若要验行为变化，走**腿 C**：先用角色编辑页把 `attachment.upload` 从 `member` 基线摘掉 →
  确认该用户 `POST /attachments` 变 403 → 在编辑器给 TL2 勾上 → 确认 TL2 用户恢复 200 →
  摘勾 → 恢复 403。全程走真实写路径，跑完必须还原基线。
- **J-04**：庚 是**非 super_admin** 的 `trust.manage` 持有者。**不许用己**——
  `Actor.Can()` 对 super_admin 直接 true（`policy.go:26`），越权与白名单两条边界会同时失去意义。
  body 模板：`{"config":"<现网 config，把 TL2 的 earnable 改成 [\"user.ban\"]>"}` → 期望 422 `trust_levels.invalid_config`。
- **J-05**：`Grants()` 对 `level <= LevelNewcomer` 直接返回 nil（`registry.go:60`），TL0 不参与 earnable。
  disabled 必须是 `aria-disabled`/原生 `disabled`，不是 `pointer-events:none`。
- **J-06**：T21 —— 文案三处（矩阵标题、行内说明、`aria-label`）都要「新增」措辞。
  `innerText` 断言「及以上」只是最低门槛，三处措辞由 reviewer 目视。
- **J-07（已交付）**：`apps/web/tests/admin/settings/trustLevelsTabMount.test.ts`，4 用例 17 断言，我实跑
  `4 pass / 0 fail`。**带对照组**：`'loads the whitelist once when the switch is on'` 证明开关打开时确实发 1 次请求 +
  渲染 5 个勾选框，所以「0 次请求 / 0 勾选框」不是恒真——没有对照组的「没发生」证明不了任何事。
  另一条 `'keeps saving available: a failed read must not wipe stored capability unlocks'` 守住 T23：
  读失败不许把已存的 `earnable` 抹成空。
  **旧用例已按要求改写、未删除**：`trustLevels.test.ts` 里那条「does not draw the capability-unlock matrix before
  the M4 contract exists」已改写为 `'mounts the earnable matrix as the single owner of the earnable field'`
  （断言 tab 不再 `v-model` earnable、改由矩阵组件单一持有），git diff 226 增 4 删，不是删掉。
  关开关时这条路径会真实发生：`requireAdmin` 先判开关（`controller.go:244`），关闭即 404。
- **J-11 与 J-07 同口径（lead 拍板，方案 a 已落地）**：`maybeLoadWhitelist()` 在开关关闭时**短路不发请求**
  （`SFAdminTrustLevelsTab.vue:84-89`），矩阵 `v-if="trustLevelsEnabled"`（`:251`），关闭态 alert 恰 1 条（`:182`）。
  两套 i18n 键（`…disabled.*` 与 `…earnable.loadFailed.*`）仍在，但**互斥不共存**——满足 §9.2 的判据。
  挂载测试断言 `requestLog` 长度 **0**、alert 恰 1 条。
- **J-08**：`[]`（该档无新增）/ `null`（合法）/ 省略（422）三者语义不同（T23）。
  断言用 `jq -e '(.data.levels[]|select(.levelSlug=="member").earnable) == []'` + `Q` 读库双证。
- **J-11**：`PUT /admin/web-options` 的更新路径**不查 feature flag**（`Options/controller.go:96-110` → `UpdateMany`，
  无 `FeatureFlags` 调用），所以开关关闭时保存会成功、前台 `GET /trust-levels` 仍 404、等级不生效。
  本刀只加提示，**不改后端行为**；判据是「界面说清了这个落差」，不是「后端拦住保存」。
- **J-12**：`admin.access` 只够读（`controller.go:139`），写要 `trust.manage`（`controller.go:157`）。
  丁 直连 URL 能读到表单、点保存才 403——这是真实的 UX 缺口。
  实现参考 `SFAdminSiteUpdatesTab.vue:22` 的 `canManage`；判据：保存控件 disabled + 说明，
  **且** `PUT` 仍是 403（前端禁用不是权限边界）。

- **J-13（累积语义，`Grants` 单向上行）**：测试构造的是 **TL1/TL2 两档**
  （`Earnable{TL1:[post.create], TL2:[topic.create]}`），断言 `Grants(TL2)` **含** `Grants(TL1)` 的键、
  而 `Grants(TL1)` **不含** TL2 独有的 `topic.create`；另含 `Grants(TL0) == nil` 与越界等级 `nil`。
  我实跑 PASS（`TestEarnableGrantsCumulative`）。
  **不写 TL4/TL2 那个字面形状**（lead 裁决）：`Grants` 是同一个循环、同一条代码路径，换档位编号不加覆盖，
  正是 L-04 判为「重复用例、删掉不降覆盖」的那一类。
  **信息量在反向泄漏变异**：把循环上界从 `current <= level` 改成 `current < LevelCount`（高层键向下泄漏）
  → 红在 `registry_test.go:124: TL1 = [post.create topic.create]`，证据 `J/J-13-red.txt`。
  比换一个档位编号更能说明「反向真的有断言」。
  **本条目验的是 `Grants` 语义，不是默认安装的可观测行为。** 7 键已被 `SeedMemberPermissions` 静态授予，
  默认安装下 `earned` 来源在生效集合里**看不见**——想在默认安装上找证据的人会判 FAIL，这句话就是给他看的。
- **J-14（`GrantSource` 在默认安装下对这 7 键返回 `static`）**：**已落地**，
  `T: ^TestGrantSourceIsStaticForDefaultInstallWhitelistKeys$ @ app/Models/Identity`（我 `-list` 复核名字存在、实跑 PASS）。
  变异：把 `if a.Earned[permission]` 改成 `if true` → 红在 `default_install_grant_source_test.go:65`（「`topic.create` 的来源 = `earned`，期望 `static`」），红态与前置写在同一个 `J/J-14.txt` 里。
  **矩阵里绝对不能写「接口层可观测」**：`CurrentUser` 只有扁平 `permissions []string`，
  全仓无 `origin`/`grantSource` 字段（我实查：`rg 'grantSource|grant_source|"origin"'` 的命中都在 Forum 与 AI，不在身份载荷）。
  写「接口层可观测」会变成一条**不可执行**的条目，比没有条目更坏。
  **同时登记 residual**：「`GrantSource` 目前**无任何生产消费者**」；若将来前端要做「为什么我不能发帖」的解释，
  那时再暴露 `origin`，届时它才有消费者——在那之前别把它当可观测能力验收。

**登记说明（lead 自记）**：J-13/J-14 两个 ID 最初出现在 lead 给 reviewer 的派工里，但**没有先登记进矩阵**，
属于「脚本有、矩阵没有」的原始病灶由 Lead 自己制造的一例。本版补登记，并确认纪律双向约束：
**编号只能由矩阵产生，Lead 的派工也不例外。**


### K 组 — 上一刀（徽章）复查

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| K-01a | ops | **无现成测试** → `jq` 对比 `badges.*` 子树键集（zh vs en） | `K/K-01a.txt` |
| K-01b | ops | **无现成测试** → `jq` 对账：`admin.badges.*` 引用键集 vs 两份 locale 定义键集（双向：missing=0 且 extra=0） | `K/K-01b.txt` |
| K-02 | ops | `T: ^TestCoreRouteCatalogHasExactReviewedGuardParity$ @ app/Support/Routes` | `K/K-02.txt` |
| K-03 | ops | `T: ^TestAPIErrorCodesHaveLocalizedMessages$ @ app/Support/Localization` | `K/K-03.txt` |
| K-04 | ops | **无现成测试** → `curl /profiles/:username` + `jq 'has(...)'` 断言两个统计键 | `K/K-04.json` |

**判据细节**

- **K-01a（public `badges.*`，**已闭合**）**：实测 **15 键**（zh 15 / en 15），键集相等
  （`diff <(jq -r 'paths(scalars)|map(tostring)|join(".")' zh-CN.json | grep '^badges\.') <(… en-US.json …)` 空输出）。
  命令照抄即可，别用「有键」代替「键集相等」。
- **K-01b（`admin.badges.*`，**已闭合**）**：**2026-11-11 09:4x 复核：zh-CN 与 en-US 各 75 个叶子定义**，
  reviewer 的双向校验为 `used=75 defined=75 missing=0 extra=0`，逐个 resolve 零 `[intlify] Not found`。
  （早期实测 `apps/web/app` 引用 69 个 `admin.badges.*` 键，现已随 frontend 合入变为 **70 个字面引用**，
  两份 locale **各 0 个定义**（`jq -r 'if (.admin.badges) then "present" else "MISSING" end'` → 两次 MISSING），
  即 `/control-panel/badges` 全裸键。writer 的文案表 `reports/2026-11-11-badges-admin-i18n-copy.md`（69 键）
  与 **75 定义** 差 5 个——**这是正常的，但原因不是「少写了」**（见下）。）
  **对账陷阱（ops 必读）**：朴素的两份 key 集合 `comm` 会同时报 **7 个「定义了没人用」** 和 **2 个「用了没定义」**，
  两者都是**假阳性**，因为 `admin.badges.grantMode.${mode}` 与 `admin.badges.metrics.${metric}` 是动态拼接：
  - 「用了没定义」的两条是前缀残片 `admin.badges.grantMode.` / `admin.badges.metrics.`；
  - 「定义了没人用」的 7 条正是被这两条前缀解析出来的：`grantMode.automatic/manual` + `metrics.topic_count/comment_count/like_received_count/bookmark_count/days_visited`。
  **正确判据**：字面引用 70 − 2 个前缀残片 + 7 个动态键 = 75 = 定义数，`missing=0`；不要用 `comm` 的原始输出当结论。
  判据：`admin.badges` 子树存在，且 **69 个引用键全部命中**、zh/en 键集相等。
  **不许用「文件里出现了 admin.badges」当证据**——要对账到键级。
- **K-02**：tripwire 在（09:2x 实跑 `ok`）。它同时是「新增 core 路由必须过评审」的守卫，
  J 组不改路由，所以本刀它应保持绿；变红说明有人加了未评审的路由。
- **K-03（**已闭合**，`f63ad2cf9` 起）**：六个码 `forum.badge_not_found` / `forum.badge_slug_taken` /
  `forum.badge_already_granted` / `forum.badge_manual_only` / `forum.badge_rule_invalid` / `forum.badges_disabled`
  （`apps/api/app/Models/Badges/errors.go:24-29`）的文案在 **Go 侧 `Localization/messages.go`**：
  zh `:61-66`、en `:415-420`，各 6 条，代码里还带着「跨域修复：徽章那刀漏了这六个错误码的双语文案…」的注释。
  **API 错误码的文案本来就该在 Go 侧，web locale 里是 0 个才对。**
  判据：`rg -n 'forum\.badge' apps/api/app/Support/Localization/messages.go` 在两个 map 里各命中 6 条
  （全文件 12 条），且 `TestAPIErrorCodesHaveLocalizedMessages` PASS。
  **本条首稿判成 FAIL，是「在错的地方找」——与 L-02 同一个病，见 L-02 的变体 2。**
- **K-04（**已闭合**）**：**2026-11-11 09:4x 复核：backend 已加字段**（`Profile/types.go:75-76`：
  `LikeReceivedCount int64 \`json:"likeReceivedCount"\`` / `DaysVisited int64 \`json:"daysVisited"\``），
  reviewer 实读 `GET /api/v1/profiles/seed_44888364289b` →
  `{"topicCount":16,"commentCount":69,"likeReceivedCount":55,"daysVisited":1}`，SSR 页 `获赞` 与 `<strong>55</strong>` 各命中。
  保留下面这段「为什么它是 payload 问题」的原始诊断——**判据仍是「payload 有字段」而不是「页面有数字」**，
  因为 `?? 0` 兜底会让「字段缺失」和「真的是 0」长得一模一样。
  诊断原文：**不是 UI 缺字段，是 payload 没字段**。
  `PublicProfile`（`apps/api/app/Models/Profile/types.go:68-71`）只有 `topicCount`/`commentCount`；
  **profile 装载路径零生产者**（`rg 'likeReceivedCount|daysVisited' apps/api --glob '*.go'` 的命中全部在
  别处：`bootstrap/trust_levels_wiring.go:71/74` 是 **TrustLevels 的 activity 适配器**，
  `TrustLevels/*` 是等级判定的输入，`Badges/visits_test.go` 是徽章测试的局部变量——
  **没有一处喂给 `PublicProfile`**）；`contracts/` 未声明；
  前端 `SFProfileFeedTab.vue:74-79` 用 `?? 0` 兜底 → 页面**恒显示 0 获赞 / 0 访问天数**。
  已派 backend 加字段 + OpenAPI + 断言。
  判据（两个都要）：
  `curl -s $API/profiles/<甲> | jq -e '(.data//.)|has("likeReceivedCount") and has("daysVisited")'`，
  且浏览器资料页数值 ≠ 0（甲 有 68 条评论带来的获赞，见 `creds.txt`）。
  **只截一张图不算证据**——0 是合法渲染值，截图看不出「字段缺失」与「真的是 0」的区别。

### L 组 — 声明≠执行（门禁类）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| L-01 | ops | `T: ^TestAPIErrorCodesHaveLocalizedMessages$ @ app/Support/Localization` | `L/L-01.txt` |
| L-02 | ops | **规则型**（判据是检查验收脚本本身）→ 机械检查全部 jq 落点，结论落 `L/L-02-rule.md` | `L/L-02-rule.md` |
| L-03 | ops | `T: ^TestOptionsErrorMappingHasSingleOwner$ @ app/Http` | `L/L-03.txt` + `L/L-03-red.txt` + `L/L-03-green.txt` |
| L-04 | reviewer | **无现成测试** → 代码审阅：用例名与构造是否一致（见判据细节） | `L/L-04-review.md` |
| L-05 | ops | `T: ^TestUpdateAdminStoreFailureIsServerError$ @ app/Http/Controllers/TrustLevels` | `L/L-05.txt` |
| L-06 | backend | `T: ^TestQueryCatalogHTTPReturnsSealedCatalogForViewers$ @ app/Http/Controllers/Extensions`（**已闭合**） | `L/L-06.txt` + `L/L-06-red.txt` |

**判据细节**：该测试遍历全 API 的 `const Code*` 常量，用 `Message(zh/en, code) == code` 判「文案缺失」
（`locales_test.go:126-155`），新增常量不补双语就是红。
M12-a 新增的三个 reason `options.invalid_value` / `options.invalid_value_set` / `options.store_failure`
（`Options/types.go:267-273`）**当前已有双语**（`Localization/messages.go:98-100` / `501-503`），
2026-11-11 09:2x 实跑 `ok`。**本条保留为回归门禁**：它绿不代表文案好，只代表没裸码；
哪天有人加常量不补文案，这条会红——这正是它存在的意义。
（注：它只扫 `const`，内联字符串字面量是盲区，别以为写了这条就万无一失。）

**L-02 判据细节：断言 bug —— 验收矩阵与验收脚本之间也需要一条契约**

v2 的 I-09/I-11/I-24 在 2026-11-11T09:26:23 产出 3 条 FAIL，产品是对的，脚本是错的：
它在顶层找 `config`/`levels`/`eligiblePermissions`，而宿主响应是 `{code,message,data}`
（`responses.go:15-19`）。**这条 bug 差点让 M8 的「0 FAIL」门槛被自己的验收脚本撞掉**——
最坏的结果不是漏测，是**用假 FAIL 把一条正确的实现判死**，然后有人去「修」没坏的代码。

判据（重跑 I-09/I-11/I-24 后逐条核）：
1. `I-09` 的日志里必须出现 `has_config:true` / `has_eligible:true`（在 `.data` 层命中），
   且 `eligible_n == 7`；
2. `I-11` 的 PUT body 里的 `config` 长度 > 2（不是 `null` 两个字），HTTP 200；
3. `I-24` 的「库内值 == 响应 config」为真，两侧长度相等。

**这一类病已有三个变体，写在这里防复发**：

| 变体 | 症状 | 判据 |
| --- | --- | --- |
| 1 | 矩阵写了 30 个测试名，29 个不存在 | `-run` 匹配不到时退出码 0、输出 `ok`（v2 R1/R2） |
| 2 | 在错的地方找东西（web locale 找 API 文案 / 顶层找信封内字段） | 断言必须写明「哪个文件、哪一层」；K-03 首稿就是此例 |
| 3 | 断言层错位：值对、位置错 | 本 L-02 |

**结论**：矩阵条目与脚本实现之间需要一条显式契约——**每条断言必须写明「在哪个响应的哪一层、哪个文件的哪一段」**，
否则「绿」只证明脚本和脚本一致。

**归类（lead 裁决，2026-11-11 10:0x）**：L-02 记 **「规则型」**，不记「未实现」——它的判据就是检查验收脚本本身，
不是一条要靠造数跑出来的行为断言。机械检查结果在 `storage/qa/trust-levels/L/L-02-rule.md`：
脚本里 16 处 jq 落点逐处核过，**没有一处「信封盲」**（把 `.levels`/`.viewer` 写在根上）；
`(.data // .)` 宽容式 5 处、硬 `.data.…` 9 处、深搜 2 处（与信封无关）。

**L-03 判据细节：同一份逻辑存在两个副本（本刀第三次复发同一纪律）**

**最终状态（2026-11-11 09:3x，lead 亲跑变异，非转述）**：**副本已收口 + 守卫已落地 + 变异已验证**。

| 判据 | 结果 | 证据 |
| --- | --- | --- |
| `rg -n "func mapOptionsError" apps/api --glob '*.go'` | **0 行** | 我实跑 |
| `rg -n "func MapOptionsError" apps/api --glob '*.go'` | **1 行**（`app/Http/options_error.go:32`） | 我实跑 |
| `T: ^TestOptionsErrorMappingHasSingleOwner$ @ app/Http` | **PASS**（1.08s） | 我实跑 |
| 变异：造第二副本 → 必须红 | **FAIL**，列出两个 owner | lead 亲跑 |
| 删掉探针 → 必须绿 | **ok** | lead 亲跑 |

两个控制器都改成 `apphttp.MapOptionsError(err)`（`Options/controller.go` **7 处**、`TrustLevels/controller.go:176/188` **2 处**，共 9 个调用点），
老副本那段「已知缺陷（residual，本刀不修）」注释已随之删除。守卫自带**反假绿检查**：
`options_error_tripwire_test.go:82-83` 的 `scanned < 500 → t.Fatalf("只扫到 %d 个生产 .go 文件，扫描根或过滤条件写错了")`；
根目录放行靠 `:41` 的 `path == root`。

**收口前**的样子（留作证据，也是这次差点被记成「待修」的中间态）：

| 副本 | 位置 | 当时行为 |
| --- | --- | --- |
| A | `app/Http/Controllers/Options/controller.go:183` | M12-a 已拆四类 reason；store 失败 → **500** `options.store_failure` |
| B | `app/Http/Controllers/TrustLevels/controller.go:296` | 老版：除权限外一律 **422** `validation.invalid` |

**后果**：真正踩到症状的路径就是 `PUT /admin/trust-levels`——它走的是 B。M12-a 修完 A，
**B 依然是坏的，而且两份行为已经不一致**；B 的注释块（`:287-296`）还写着「已知缺陷（residual，本刀不修）」，
这句话**既过期、又成了「已经修完」的假证据**。

**判据升级为两半，缺一不可**：① **当前绿**；② **加回副本必红**（红态输出必须归档为 `L/L-03-red.txt`）。
**变异工具要按测试类型分两类（别一刀切）**：

| 测试类型 | 例子 | 变异方式 | 理由 |
| --- | --- | --- | --- |
| **扫描类**（AST/文件遍历） | L-03 | **必须落盘**（`zz_*` 前缀写进包目录，验完删） | 扫描走 `filepath.WalkDir` 读真实文件系统，`-overlay` 的副本它**看不见** → 假绿（reviewer 首验踩过） |
| **编译输入类**（类型/常量/映射） | L-05、J-14 | `-overlay` **更安全**（零写工作树） | 变异进入编译单元即可，不必污染磁盘 |

判据：改完先确认「变异真的被测试读到了」——扫描类看 `scanned` 计数或失败信息里的文件名；
编译类看编译是否真的用到了变异版本。**没被读到的变异，绿和红都不算证据。**

**方法论（本轮最好的一句，写进正文而不是备注）**：

> 一个永远红的守卫会被当成「还没修完」，一个永远绿的守卫会被当成「已经修完」——**两种都比没有守卫更坏**。

**可执行检查步骤（新守卫测试交付的硬要求）**：任何新守卫交付时必须同时给「**它红过的样子**」。

```bash
# 1) 造一个故意违反守卫的探针（不提交，放 zz_* 前缀）
# 2) 必须红，且失败信息能定位到探针
go test -count=1 -v -run '^<GuardName>$' ./<pkg>/ | tee storage/qa/trust-levels/L/<ID>-red.txt
# 3) 删探针 → 必须绿
go test -count=1 -v -run '^<GuardName>$' ./<pkg>/ | tee storage/qa/trust-levels/L/<ID>-green.txt
```

**本刀实收五条假守卫/假断言，形状不同、病根同一个**：

| # | 形状 | 为什么它看起来是绿的 |
| --- | --- | --- |
| 1 | `level_purity_test.go` 的不可证伪纯度 | 断言不随实现变化，删掉被测逻辑也不红 |
| 2 | `TestEarnableValidateCoversWholeCatalog` 只断言自己一致 | 白名单与 Validate 一起动，永远相等 |
| 3 | `-run` 匹配不到 → 退出码 0、输出 `ok` | 「没跑」和「跑了且通过」长得一样 |
| 4 | 扫描器跳过自己的根（本 L-03 的中间态） | 扫 0 个文件也能得出「0 个副本」这个结论 |
| 5 | 断言顺序使点名断言不可达（L-06 修复前） | 测试**确实会红**，但红在「总数 19≠22」上；那句「哪条徽章被吞了」从未执行过——绿和红都发生了，被点名的那句却从没出现过 |

**病根：没有人见过它红。** 所以证据规则再加一条（R10）：**守卫类条目必须同时有 `-red` 与 `-green` 两份证据**，
只有 `-green` 的守卫条目按「未验证」记，不计 ok。

**这是本刀第三次复发同一条纪律**：

| 次 | 表现 | 教训 |
| --- | --- | --- |
| T6/T7 | fixture 的建表 schema 第二份 | 复制一份 schema = 埋一个静默漂移 |
| L-02 | 断言层第二份（脚本 vs 矩阵各解释「字段在哪层」） | 绿只证明脚本和脚本一致 |
| **L-03** | 错误映射第二份 | 同一份错误语义两处实现，**修一处等于修一半** |

**下一刀开工第一件事**：扫「还有没有别的第二份」。粗筛
`rg -n "func \w+" apps/api --glob '*.go' | sed 's/.*func //' | sort | uniq -d`，
重点看四处：**error→HTTP 映射、DTO 编码、权限判定、迁移建表**。

**L-04 判据细节：用例名与构造不符（reviewer 的活）**

`apps/api/app/Models/Identity/trust_levels_merge_test.go:121`（所在函数 `TestGrantSourceMatchesCan`，`:88` 起）的用例名是「同时被静态与等级授予 → static」，
但构造是 `Permissions: {"topic.create": true}` + **`Earned: map[string]bool{}`（空）**——
**根本没构造「同键既静态又 earned」**。真实实现里 `GrantSource` 在同一分支内先看 `a.Earned[permission]`，
为真即返回 `earned`（**earned 分支在 static 之前**，`permission_origin.go`），
而装载期不变量保证不会出现「同键既静态又在 `Earned`」，所以那个组合**不可达**。

判据（两条都要）：
1. 用例名必须**改名**（如「静态授予 → static（Earned 为空）」），或补一行说明「本用例只覆盖 static 分支」；
2. **禁止**为了「覆盖」去构造 `Permissions[键] && Earned[键]` 并断言结果——断言不可达状态等于钉死实现细节，
   与 `level_purity_test.go` 的假纯度断言是同一类病（E5 已把它记 PEND）；
3. 反向检查：`Earned` 分支的可达性证据必须仍在——`permission_origin.go` 的注释点名「已挣得独占」用例是
   它**唯一**的证明，删掉即红。改名时别把这层关系改丢。

**L-05 判据细节：422 → 500 是「修正」，不是回归（正面条目）**

`PUT /admin/trust-levels` 的 **store 写失败**：**422 `validation.invalid` → 500 `options.store_failure`**。
这是**本刀唯一有意改变 HTTP 状态码的地方**，方向是「之前就在说谎，本刀改说真话」——
写库失败是服务端错误，伪装成 422 会让运营去改参数而不是查库。

单独立这条的**唯一目的**：将来有人 `git diff` 到状态码变化，能在这里查到「有意为之」，
不会当成回归回滚掉。**别把它并进 L-03 的「不影响」清单里**——那是审计结论，这是变更记录。

判据：
1. `T: ^TestUpdateAdminStoreFailureIsServerError$ @ app/Http/Controllers/TrustLevels` → PASS
   （我实跑；日志含 `WARN trust_levels save failed … connection refused`）；
2. 同一语义在 Options 侧由 `T: ^TestMapOptionsErrorReasons$ @ app/Http/Controllers/Options` 的子用例
   「写库失败 → 服务端错误，不是 422」守住（我实跑，4 个子用例全 PASS）；
3. HTTP 层对照（可选，走真实写路径）：`docker pause sforum-postgres` → 戊 PUT → 期望
   **500 `options.store_failure`** → `docker unpause` → **必须再复跑一次 200** 确认恢复。
   不 unpause 就跑下一轮，会把下一个人 200 的证据污染成 500。

**L-06 判据细节：既有红 —— 目录总数被写死在测试里（非本刀引入）**

实测（2026-11-11 09:37，我实跑）：`T: ^TestQueryCatalogHTTPReturnsSealedCatalogForViewers$ @ app/Http/Controllers/Extensions`
→ **FAIL**，两个子用例（`viewer` / `legacy_manager`）都红，响应 `Total:22` 而断言写死 **19**。

多出的三条来自徽章那一刀的基线提交 `f63ad2cf9`：
`core.query.public_badges.list` / `core.query.public_user_badges.list` / `core.query.public_user_activity.list`
（该测试文件最后一次改动是 `82a6b014e 2026-11-10`，backend 未碰那个包）。

**归属**：**不是本刀引入**，但它**会撞 M8 的 0 FAIL 门槛**，所以必须在本刀内处理——
不许记「既有问题不修」，那等于让合并表带着一条红交付。
**写域**：`apps/api/app/Http/Controllers/Extensions/query_catalog_test.go` 由 lead 授权给 **backend**，
**ops 不得同时修改该文件**（同一文件两个写手会互相覆盖）；ops 只负责落 `L/L-06.txt` 的复跑证据。

判据（修的时候按这个方向，**别只把 19 改成 22**）：
1. **优先**把总数改成从目录源计算（`Total == len(Queries)` 或 Go 侧 catalog 常量），
   写死字面数字的测试下次加查询还会红——这就是「测试与产品一起漂移」的又一次；
2. 保留可证伪性：另加一条断言「这三条 badges 查询**在列**」（按 QueryID 精确匹配），
   否则总数改成动态后就失去了「徽章查询没被吞掉」的守卫；
3. 反向验证（R10）：临时删掉一条 badges 查询 → 必须红 → 恢复 → 必须绿，两份证据落 `L/L-06-red.txt` / `L/L-06-green.txt`。

**实现复核（2026-11-11 09:38 实读 backend 的在改版本）**：三条判据都已落地，而且比最低要求更强——
`sealedQueryCatalogIDs` 是 22 条**字面**切片（15 stable 含 1 条 consent-gated + 7 shortcode），
第三条断言用 `slices.Equal(sorted(got), sorted(want))` 做**双向集合相等**，不是只比数字。

**但断言顺序有一个必须改的问题**：现在是「① 总数 → ② 三条徽章在列 → ③ 集合相等」。
删掉一条徽章查询时，**总数断言会先触发**（22→21），第 ② 条永远轮不到执行——
**这条点名断言对它唯一要防的那个回归是不可达的**，正好是本轮第五条「写了但没人见过它红」。
修法二选一：
- 把「三条徽章在列」提到最前（推荐：它是这条测试存在的理由）；
- 或红态证据里补一次「总数不变、只换掉一条徽章查询」的变异（保持 22 条但把某个徽章 ID 换成别的合法 ID），
  让第 ② 条真的执行到。

**已修（2026-11-11 09:4x，我实读 + 实跑确认）**：顺序改为 **① 三条徽章点名（`:126`）→ ② 总数（`:134`）→ ③ 双向集合相等**。
两份红态**都停在点名句**：
- A 变异（总数不变 22，只改坏 `core.query.public_badges.list` 的 registry ID）→ 点名红；
- B 变异（整条徽章查询从目录摘掉，22→21）→ **仍点名红**（改顺序前它红在 `total = 21`）。
  **B 是唯一能证明「顺序修复真的生效」的证据**——没有它，「我改了顺序」只是一句声明，
  因为 A 变异（总数不变）在改顺序前后都会红在点名句，区分不了两者。
复原 → `ok Controllers/Extensions`。

**两条口径（写进本条，避免下一版再错）**：
1. 期望集合是 **22 条字面切片**（15 stable 含 1 条 consent-gated 的 `core.query.identity.self_email` + 7 shortcode），
   **不是** `12+len(ShortcodeProjectionQueryIDs())`。原文注释「11 条 stable」与代码「12+」两处都错，已一并订正——
   这正是「文档写的数字与代码算的数字各错一半」的样本。
2. 守卫能力覆盖三种漂移：**少一条徽章 / 多一条别的 / 总数不变**。A 变异（22→22，只有点名与集合断言能抓）证明第三种的覆盖是真的。

---

## 4. 口径声明：默认安装下「TL 解锁能力」是空操作，这是正确行为

researcher 实测（2026-11-11）：7 个白名单键**全部已在 `member` 基线**（`Identity/seeds.go:157-165` + 三层迁移），
所以默认安装下 `Actor.Earned` **恒为空**，`GrantSource` 对这 7 键**永远返回 `static`**。

因此：

1. **凡涉及「TL 解锁能力」的条目，判据只能是「配置生效」，不是「行为变化」。**
2. **禁止**写「勾了 TL2 用户就能发帖」「TL4 比 TL2 多一个权限」这类断言——它必红，且红得没意义（R8）。
3. 「默认空集 + 勾选是空操作」**不是 bug**，是 D17 的既定设计：机制可配，默认零行为变化。
4. 真要验行为变化，只能走 §3 J-03 的**腿 C**（先摘基线再勾），并跑完还原。

---

## 5. 明确**不**计入 FAIL

| 项 | 归属 | 理由 |
| --- | --- | --- |
| 戊（super_admin）的写权限证据 | 本版作废 | 超管短路 `Can()`，不能证明 `trust.manage` 边界；改用 丁/戊/庚 三角 |
| J-07 的组件用例 | frontend 自测 | M10 交付内容，未落地前记 PEND 并写明「待落地：frontend M10」 |
| I-09 / I-11 / I-24 的现存 FAIL | 断言 bug | §0-1，修脚本后重跑，不计产品缺陷 |
| `badges.*`（public）键集 | 已闭合 | K-01a 保留为回归，不再计入上一刀欠账 |
| 六个徽章错误码的文案 | 已闭合（`f63ad2cf9`） | K-03 在 Go 侧 `messages.go:61-66`/`:415-420`，不是 web locale；首稿判 FAIL 是「找错位置」，已更正 |

---

## 6. 证据目录

```
storage/qa/trust-levels/
  A/ … I/                     # v2 的 124 条（不改名、不重排）
  J/ J-01.txt … J-12-*.png    # 本版新增
  K/ K-01a.txt … K-04.json
  L/ L-01.txt
  <ID>-pend.md                # PEND 的交代（环境限制 / 待落地给谁）
  <ID>-red.txt / -green.txt   # 变异红绿两态
  _summary.txt                # 脚本生成：每 ID 一行 PASS/FAIL/SKIP/PEND + 原因
```

规则：一条 ID 一个文件；浏览器证据带视口尺寸；**修过的旧证据必须覆盖**（R9）；
`_summary.txt` 里出现矩阵没有的 ID，视为脚本越界，要回填矩阵或删断言。

---

## 7. 派修

| 派给 | 事项 |
| --- | --- |
| ops | ① 修 `verify-trust-levels-acceptance.sh:1808/1852/1853` 的信封（§0-1），重跑 I-09/I-11/I-24 并覆盖旧证据；② 按 §1.2 授权 486/487、建庚；③ 落 J-01/J-02/J-08、K-01a/K-01b/K-03/K-04、L-01 |
| frontend | J-07 用例（并**改写** `'does not draw the capability-unlock matrix…'`）；J-11 关闭态 `UAlert`；J-12 `canManage` 禁用 + 说明 |
| backend | K-04 的 `PublicProfile` 两字段 + OpenAPI + 断言；K-03 的六码双语（或明确裁决「按码取文案」的落点） |
| writer | K-01b 的 `admin.badges.*` 69 键文案表（zh/en 成对） |
| reviewer | J-03/J-04/J-05/J-06/J-10/J-11/J-12 浏览器实测；J-09 变异红绿；K-01b 键级对账；J-04 必须用庚不用己 |

---

## 8. 覆盖核对（矩阵 149 条 vs 证据，**未实现 0**）

> **本节核对的是磁盘上的实际文件，不是 §3 证据列里的约定命名。**
> §3 的「证据」列写的是**预期落点**（如 `J/J-01.txt`）；本刀实际有很多条目**合并到同一份证据文件**，
> 浏览器七条全在 `K/browser-matrix.md` + `K/logs/browser-*.json`，K 组四条全在 `K/K-0*.md`。
> 以本节为准。**这是 R13/R14 在自己文档上的一次应用：路径必须 `ls` 得到，不能靠想象。**
> 这两类错（本节最初的编路径、以及 `I/I-03.txt` 的转述件）**同一个根：把「约定」当成了「事实」**。

| 组 | 条目 | 实际证据落点（已 `ls` 验） |
| --- | --- | --- |
| J | J-01 / J-02 | Go 断言实跑（`TestAdminEligiblePermissionsLiteral`），**无独立落盘文件**，见下方缺口说明 |
| J | J-03 / J-04 / J-05 / J-06 / J-10 / J-11 / J-12 | `K/browser-matrix.md`（结论表）+ `K/logs/browser-j03-*.json`、`browser-j04-*.json`、`browser-assert-*.json`、`browser-j11-*.json`、`browser-j12-*.json` |
| J | J-07 | `J/J-07.txt`（10:06:15，4 pass / 17 expect，带对照组；头部含三个实现文件 sha256） |
| J | J-08 | `K/J-04-J-08-api.md`（PUT→200，重载 `earnable` 长度 0） |
| J | J-09 | 变异红绿两次（09:31 / 09:52），**无独立落盘文件**，见下方缺口说明 |
| J | J-13 | `J/J-13-red.txt`（干净 PASS；循环上界改 `LevelCount` → `TL1 = [post.create topic.create]` 红） |
| J | J-14 | `J/J-14.txt`（`-list` 复核 + PASS；`if true` 变异红在 `default_install_grant_source_test.go:65`；红态与前置同文件） |
| K | K-01a / K-01b | `K/K-01-badges-i18n.md` + `K/logs/K-01-keyset.txt`、`K-01-resolve.txt`、`K-01b-resolve.txt` |
| K | K-02 | `K/K-02-route-tripwire.md` + `K/logs/K-02-tripwire.txt` |
| K | K-03 | `K/K-03-error-codes.md` + `K/logs/K-03-localization*.txt` |
| K | K-04 | `K/K-04-profile-stats.md` + `K/logs/K-04-payload.txt`、`K-04-after-fix.txt` |
| L | L-01 | `K/K-03-error-codes.md` + `K/logs/K-03-localization-after-fix.txt` |
| L | L-02 | `L/L-02-rule.md`（规则型：16 处 jq 落点机械核过，零「信封盲」） |
| L | L-03 | `L/L-03.txt`（含 `ls` 清理校验与变异文件名） |
| L | L-04 | `K/L-04-review.md` |
| L | L-05 | `L/L-05.txt` |
| L | L-06 | `L/L-06.txt` |

### 8.1 三个「有运行、无落盘」的条目（**已闭环 2026-11-11T10:09:57**）

| ID | 现状 | 补法（**绝对路径，见下方陷阱**） |
| --- | --- | --- |
| J-01 / J-02 | 我 09:28 实跑 PASS，但**没有把 `-v` 输出落盘** | `cd $REPO/apps/api && go test -count=1 -v -run '^TestAdminEligiblePermissionsLiteral$' ./app/Http/Controllers/TrustLevels/ 2>&1 \| tee "$REPO/storage/qa/trust-levels/J/J-01.txt"` |
| J-09 | 变异红绿两次实跑，**未落盘**（reviewer 表里只有时间戳描述） | 红态（`-overlay` 塞 `user.ban`）→ `J/J-09-red.txt`；绿态（撤 overlay 复跑）→ `J/J-09-green.txt`，两者都用 `$REPO` 绝对路径 |

**⚠️ 落盘路径陷阱（本刀第三次「第二份」）**：`cd apps/api` 之后，相对路径 `storage/qa/trust-levels/…`
**不是**仓库根的那份——`apps/api/storage/qa/trust-levels/` **真实存在**（`L/` 目录已在），
所以：
- `tee storage/qa/trust-levels/J/J-01.txt` → `apps/api/storage/…/J/` 不存在 → **报错**（reviewer 栽过两次）；
- 但 `tee storage/qa/trust-levels/L/…` → **静默写进另一个树**，被 `.gitignore:118 storage/` 吞掉，
  验收脚本按仓库根路径去查会**查不到**，看起来像「证据丢了」。

也就是说：**同一个仓库里有两个 `storage/qa/trust-levels` 树**——这正是 L-03 那族「第二份」在目录层的复现。
**规矩**：证据落盘一律用 `$REPO/storage/...` 绝对路径；跑完 `ls` 确认文件在**仓库根**那份树里。

**已闭环（ops 10:09:57 落盘，我逐份读过内容）**：

| 文件 | 实读结论 |
| --- | --- |
| `J/J-01.txt` | 头部四项齐全 + `=== RUN TestAdminEligiblePermissionsLiteral` + `--- PASS` |
| `J/J-09-red.txt` | `controller_test.go:312: eligiblePermissions 长度 = 8，期望 7：[… user.ban]` + `--- FAIL`；变异方式写明 `-overlay`（编译输入类，合规） |
| `J/J-09-green.txt` | 撤 overlay 后 `--- PASS`，注明「现场零改动」 |

**实现基线 sha256 已补（2026-11-11T10:1x，我独立复算一致）**：三份头部现有**五项**——
采集时间 / 采集环境（开关 + 账龄闸）/ 服务探测（health·login·session 全 200）/
实现基线（`controller_test.go=fa3f075a…1695`、`registry.go=b15add0f…0a0f`，与 `sha256sum` 逐字符一致；
**注意前者是工作树快照**，提交后需按 §8.2 回填 HEAD 哈希 `5ec2409b…7716`）/
现场已清理（`git status` 0 行 · `/tmp` 0 残留）。

**R14 机械检查同步通过**：`find "$REPO" -maxdepth 5 -type d \( -path '*/apps/api/storage/qa*' -o -path '*/storage/qa/trust-levels' \)`
→ 只有 `/home/inkedus/Code/github/SForum/storage/qa/trust-levels` 一份，**无第二棵树**。

**§8.1 的「落盘缺口」已闭环**：三份文件在、内容合格、头部五项齐全（含来源标注）。
**已完全闭环（2026-11-11T10:34，commit `57a3de5ad`）**：基线可由该提交复现——
`git show 57a3de5ad:apps/api/app/Http/Controllers/TrustLevels/controller_test.go | sha256sum`
= `fa3f075a…1695` = 当前工作树（工作树干净）。证据头部仍保留「提交后回填：… 期望 fa3f075a…」那一行；
**commit id 记在本节与 §10**（ops 若要补一行 `# 回填（57a3de5ad）：实测 fa3f075a…` 更完整，但不影响可复现性）。

**口径澄清（保留）**：`未实现 0` 与「全部有文件」不是一回事。本节闭环后两者一致；
但下次若再出现「有运行、无落盘」，仍按本节形态单列，别混进 ok 也别混进未实现。

### 8.2 基线一律用 sha256 清单（reviewer 建议，已采纳）

证据头部记「实现基线」时，**写被验文件的 sha256，不写提交号**：工作树有未提交改动时提交号根本对不上，
而 sha256 永远对得上，下一个人 `sha256sum` 一比就知道证据是不是旧的。
J-07 已经这么做（三个实现文件 sha256），后续照此。

**基线必须写明来源：工作树 / 提交（本刀唯一差点静默过期的元数据）**：
裸哈希不写来源会骗人——`controller_test.go` 当前是**未提交**状态（`git status` = `M`，+102 行），
所以 `fa3f075a…1695` 是**工作树快照**，它的 **HEAD 版本是 `5ec2409b…7716`**；两个都「对」，取决于哪棵树。
`registry.go` 干净，工作树与 HEAD 同为 `b15add0f…0a0f`。
若 backend 在提交前再改一次测试文件，`fa3f075a…` 立刻失效，而下一个人 `sha256sum` 一比就会判「证据过期」、
白查一轮。**写法**：

```
# 基线（采集时）：工作树 sha256 fa3f075a…1695（未提交）· registry.go b15add0f…0a0f（=HEAD）
# 提交后回填：git show HEAD:<path> | sha256sum
```

提交后按计划 §8「提交后回填提交级证据」补一次 HEAD 哈希，基线就变成可复现的。
**只写裸哈希不写来源，是 R13 第四项目前唯一的漏洞。**

**post-commit 回填（一次跑完，别逐条手敲）**：

```bash
REPO=/home/inkedus/Code/github/SForum
for p in apps/api/app/Http/Controllers/TrustLevels/controller_test.go \
         apps/api/app/Models/TrustLevels/registry.go \
         apps/web/tests/admin/settings/trustLevelsTabMount.test.ts \
         apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue \
         apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelEarnableMatrix.vue; do
  echo "$(git -C "$REPO" show HEAD:"$p" | sha256sum | cut -d' ' -f1)  $p  (HEAD)"
done
```

**顺序硬约束**：`git show HEAD:<path>` 对**未跟踪**（`??`）文件会报错——`SFAdminTrustLevelEarnableMatrix.vue`
与 `trustLevelsTabMount.test.ts` 现在还是 `??`，**提交后才有 HEAD 版本**。所以回填必须在提交**之后**跑，
不能在提交前。回填时把证据头部「期望」改成实测值并附 commit id。

**⚠️ 回填期望值不能写「HEAD 现值」（本刀最深一层坑）**：`HEAD` 是**会移动的树**，`git show HEAD:path`
在采集时给出的是**旧提交**里的版本。以 `controller_test.go` 为例：

| 时点 | `sha256sum`（工作树） | `git show HEAD:path` |
| --- | --- | --- |
| 采集时（未提交） | `fa3f075a…1695` | `5ec2409b…7716`（**旧提交**，两者不同正是「未提交」的证据） |
| 提交后 | `fa3f075a…1695` | **`fa3f075a…1695`**（HEAD 装进了工作树内容） |

所以**回填期望值是 `fa3f075a…1695`**（提交时的工作树内容），`5ec2409b…7716` 只能作「提交前 HEAD 旧值」留档。
把它写成「期望」会让回填的人按旧值核对，得到一个**错值**。

**正确写法**：

```
# 基线（采集时）：工作树 fa3f075a…1695（未提交）· registry.go b15add0f…0a0f（=HEAD）
# 提交前 HEAD 旧值：5ec2409b…7716（仅留档，不是回填期望）
# 提交后回填：git show HEAD:<path> | sha256sum → 期望 fa3f075a…1695
#             （若 backend 提交前又改，以提交时工作树为准，并把新值同时写进本行）
```

**回填时的防错校验（两条必须同时成立）**：

```bash
git -C "$REPO" show HEAD:apps/api/app/Http/Controllers/TrustLevels/controller_test.go | sha256sum
sha256sum "$REPO/apps/api/app/Http/Controllers/TrustLevels/controller_test.go"
```

两行相同 ⇒ 工作树内容已进 HEAD、基线可复现；**不同 ⇒ 还有未提交改动，回填无效**。

**提交前预演（reviewer 实跑，我复核一致）——避免提交后才发现命令有问题**：

| 文件 | 当前 git 状态 | 现在 `git show HEAD:<path>` 的表现 |
| --- | --- | --- |
| `app/Models/TrustLevels/registry.go` | 干净 | 正常，`b15add0f…0a0f` ✅ 与证据一致 |
| `app/Http/Controllers/TrustLevels/controller_test.go` | `M` | 正常，给的是**旧提交**的 `5ec2409b…7716` |
| `SFAdminTrustLevelsTab.vue` | `M` | 同上（旧值） |
| `SFAdminTrustLevelEarnableMatrix.vue` | `??` | **必然报错**：`fatal: 路径 … 在磁盘上，但是不在 'HEAD' 中` |
| `trustLevelsTabMount.test.ts` | `??` | 同上 |

**报错是预期，不是命令写错**——这两份提交后才有 HEAD 版本。
**提交后判定（每个文件两条都要成立）**：`git show HEAD:<path> | sha256sum` == 证据里记的工作树哈希；
且 `sha256sum <path>` == 同一值。**任一条不等 ⇒ 还有未提交改动，回填无效，不许写进证据。**

**一句话判据（ops 归纳，采纳）**：基线 = **哪棵树 + 那一刻的内容哈希**；
回填的期望值 = **提交后的 HEAD**，也就是**提交前的工作树**。
三层里第三层最隐蔽，因为 `HEAD` 看起来像个稳定锚点，其实是**会移动的树**。

**状态（2026-11-11T10:1x）**：`J/J-01.txt`、`J/J-09-red.txt`、`J/J-09-green.txt` 三份的**来源标注已补**
（第 4–5 行：工作树快照 + `git status=M` + 提交后回填命令与期望值 `5ec2409b…7716`），我逐份实读确认。
`J/J-07.txt` 与 `K/logs/00-env.txt` 由 reviewer 自行补了同款来源标注（他先在自己文件上生效，再要求别人）。
**现在只差「提交 → 回填」这一步。**

**为什么连行号也不能当基线（实证）**：同一条断言，reviewer 09:31 记的是 `controller_test.go:310`，
我 09:5x 跑出来是 `:312`——**backend 加了两行就漂了**。当前该文件 sha256 `fa3f075a…1695`。
所以证据里的「失败位置」只能当**线索**，基线必须是 **sha256**；按行号核会误判「证据过期」。

**核对纪律**：下次重跑后若某 ID 没有对应文件，必须在 `_summary.txt` 里写 `未实现` + 理由，
**不许省略该行**——省略等于宣称覆盖了。

**「文件存在」不等于「断言成立」（reviewer 实遇两例，收进本节当样本）**：

| 形态 | 现象 | 根因 |
| --- | --- | --- |
| **假 FAIL** | J-03 首落盘：文件在、JSON 也在，却判失败 | harness **读早了**，页面/接口还没就绪 |
| **假绿** | L-03 首验：用 `go test -overlay` 加一份副本，测试仍绿 | AST 扫描走 `filepath.WalkDir` **读磁盘**，overlay 里的文件它**看不见**——变异根本没被扫到 |
| **旧证据覆盖新结论** | 文件在、断言对、内容也对，但**采集时间早于实现变更**（如 K-04 修复前的 payload 证据） | 读者看到「文件在、断言对」就信了，实际它证明的是**旧行为**。防法：头部必须有**采集时间**，且结论所依赖的实现文件 **sha256 能对上**（K-04 那份 09:37 采集时 `types.go` 字段已存在，对得上） |
| **转述件冒充证据** | 终表引用 `I/I-03.txt` 作「匿名无 `viewer`」，但那份是 **ops 的登记文件**，内容指向 `I-03-I-04-otherview.txt`，而后者验的是「匿名 SSR 载荷无 progress/bottleneck」——**另一条断言** | 文件在、ID 也对得上，**连红都不会红**。已处置：新写 `I/I-03-reviewer.txt`（带前置：admin 读面 200 证开关 enabled；匿名 200 + 无 `viewer` + 5 档），终表改指该文件，并注明「`I/I-03.txt` 是登记，两者不可互相替代」 |

**「变异真的被读到了」的自证手法（按测试类型分）**：

| 类型 | 自证 | 实例 |
| --- | --- | --- |
| 扫描类 | 失败信息里**出现变异文件名** | L-03 红：`实际 2 个：[app/Http/options_error.go::MapOptionsError app/Http/zz_dup_options.go::duplicateMapOptionsError]`——文件名直接出现 ⇒ 读到了；同文件第 4 行 `ls: cannot access …` 同时证明现场已清理 |
| 编译输入类（`-overlay`） | **先做一次「必然编译失败」的 overlay**（把标识符改成不存在的名字），确认 `go build -overlay` 真的报错且错误指向被替换的文件路径 | 若这一步是绿的，说明 overlay 根本没接上，**后面所有语义变异都不算证据**。J-14 的红指向被测断言本身（`default_install_grant_source_test.go:65`），属间接证明 |

**这个收窄有数据支撑（不是拍脑袋）**：全量口径下 `storage/qa/trust-levels/*/*.txt` **共 73 个，
含 `trust.new_user_days` 采集头的 60 个**；缺头的多为变异红/绿（前提就是变异本身，写在文件里）、
reviewer 本轮已补的 4 个，以及 `accounts/roles.txt`、`B0`、`I-03-I-04-otherview.txt` 这类登记/说明文件。
**声称 `viewer.level` 的 4 个文件（C13/D3/H11/H12）全部带开关态头**——R13 三件里最要命的那件
在旧证据里已基本满足，追溯重开的收益接近零。

---

## 9. 状态回填与执行顺序（reviewer 按此跑浏览器矩阵）

### 9.1 状态回填（2026-11-11 09:2x，逐条实测）

| 条目 | 上一版状态 | 现在 | 依据 |
| --- | --- | --- | --- |
| J-01, J-02 | 待建测试 | **可验** | `TestAdminEligiblePermissionsLiteral` 已存在且实跑 ok |
| J-03, J-05, J-06, J-10 | 待 M10 | **可验** | `SFAdminTrustLevelEarnableMatrix.vue` 已交付（590 行） |
| J-04 | 待 M10 + 庚 | **可验（需先授权）** | 组件已交付；庚 待 §1.2 建号 |
| J-12 | 待 M10 | **可验（需先授权 丁）** | 组件内有 `!canManage` 的 `UAlert` + `:disabled` |
| J-07 | 待 frontend 用例 | **已交付** | `trustLevelsTabMount.test.ts` 4 用例 17 断言，实跑 4 pass / 0 fail；带对照组 |
| J-11 | 部分达成 | **已闭合（方案 a）** | `maybeLoadWhitelist()` 开关关闭短路（`:84-89`）+ 矩阵 `v-if`（`:251`）；测试断言 `requestLog` 长度 0、alert 恰 1 条 |
| J-08 | 待 ops | **可验（需先授权 戊）** | 组件把 `earnable[level] ?? []` 写进 payload |
| J-09 | 待 rev | **可验** | J-01 已稳定，可做变异 |
| K-01a | 未闭合 | **已闭合** | `badges.*` 15/15 键集相等 |
| K-01b | 在修 | **已闭合** | zh/en 各 **75** 叶子定义；reviewer 双向 `used=75 defined=75 missing=0 extra=0`，零 `[intlify] Not found` |
| K-02 | 未验 | **可验** | 实跑 ok |
| K-03 | 首稿误判未闭合 | **已闭合** | Go 侧 `messages.go:61-66`/`:415-420` |
| K-04 | 未闭合 | **已闭合** | `Profile/types.go:75-76` 两字段；实测 payload `likeReceivedCount:55 / daysVisited:1`；SSR 命中 |
| L-01 | 待验 | **可验（当前绿）** | 实跑 ok |
| L-02 | 新增 | **可验** | 修脚本后重跑 I-09/I-11/I-24 |
| L-03 | 新增 | **已闭合** | 副本 0 / 真源 1 / 守卫 PASS / 变异已验（lead 亲跑，非转述） |
| L-04 | 新增 | **可验（reviewer 审阅）** | 用例名与构造不符，行号 `:121` |
| L-05 | 新增 | **可验（当前绿）** | 422→500 的正面断言 `TestUpdateAdminStoreFailureIsServerError` 实跑 PASS |
| L-06 | 新增 | **已闭合** | 字面 22 集合 + 三条徽章点名（前置）+ 双向集合相等；我实跑 `ok 0.134s`；A/B 两份红态都停在点名句 |
| J-13 | 新增（补登记） | **已闭合** | `TestEarnableGrantsCumulative` PASS；反向泄漏变异红在 `registry_test.go:124` |
| J-14 | 新增（补登记） | **已闭合** | `TestGrantSourceIsStaticForDefaultInstallWhitelistKeys` PASS；`if true` 变异红 |
| L-02 | 新增 | **规则型** | `L/L-02-rule.md`：16 处 jq 落点机械核过，零「信封盲」 |

### 9.2 J-11 与 J-07 的「同口径」：**已按方案 a 达成**（lead 拍板项）

现状是**两套 i18n 键**：

| 触发条件 | 实现位置 | 键族 | 颜色 |
| --- | --- | --- | --- |
| `featureFlag === false` | `SFAdminTrustLevelsTab.vue:169` | `admin.settings.trustLevels.disabled.title/description` | `warning` |
| `status === 'failed'` | `SFAdminTrustLevelEarnableMatrix.vue:153` | `admin.settings.trustLevels.earnable.loadFailed.*` | `error` |

同一个 `UAlert` 组件（✅），但不是同一份 i18n 键（❌）。**后果**：开关关闭时白名单请求必然 404
（`requireAdmin` 先判开关），于是页面**同时**出现「功能未启用」和「白名单加载失败」两条 alert，
一个原因、两种说法。修法二选一，由 frontend 定，但必须在 J-11 验收前定：

1. 开关关闭时不发白名单请求（`enabled === false` 直接短路），只留一条「功能未启用」；
2. 或让失败分支在 `enabled === false` 时复用 `disabled.*` 键。

判据：**同一原因只出现一条 alert**，且两条路径的键族要么相同、要么互斥不共存。

**结论（2026-11-11 09:4x）**：frontend 选了**方案 1（短路不发请求）**，因此两条路径**互斥不共存**，判据满足。
键族仍分两套（`…disabled.*` / `…earnable.loadFailed.*`）——**这是允许的**，因为它们不再同时出现；
真正要防的是「一个原因两种说法同时出现在屏幕上」，不是「文案必须共用同一个键」。
（下表是修复前的状态，留作证据。）

### 9.3 执行顺序（按「谁能先验」排，避免空跑）

> **执行完毕（2026-11-11 10:0x）**：下表保留为**复跑顺序**，不是待办清单。
> 终值见 §10：`149 = ok 145 · FAIL 0 · pend 2 · 规则 1 · 作废 1 · 未实现 0`。

| 序 | 条目 | 归属 | 前置 |
| --- | --- | --- | --- |
| 0 | L-06 | ops | 已闭合；ops 只需复跑并落 `L/L-06.txt`（该文件写域只归 backend，**ops 不得改测试**） |
| 1 | K-02, K-03, L-01 | ops | 无（现在就跑） |
| 2 | J-01, J-02 | ops | 无 |
| 3 | L-02 | ops | 修 `verify-trust-levels-acceptance.sh` 三处信封后重跑 I-09/I-11/I-24 |
| 3b | L-03 | ops | 守卫已绿；ops 只需复跑并落 `L/L-03-green.txt`（红态由 lead 的变异证据 `L/L-03-red.txt` 归档） |
| 3c | L-05 | ops | 无（`TestUpdateAdminStoreFailureIsServerError` 现绿，落 `L/L-05.txt`） |
| 4 | §1.2 授权（486/487 + 庚） | ops | 己 的 super_admin 会话 |
| 5 | J-08 | ops | 序 4 |
| 6 | K-01a | ops | 无 |
| 7 | J-03, J-05, J-06, J-10 | reviewer | 序 4（戊 可写） |
| 8 | J-04 | reviewer | 序 4（庚 建好）；**禁用 己** |
| 9 | J-12 | reviewer | 序 4（丁 授权） |
| 10 | J-09 | reviewer | 序 2 |
| 11 | K-01b | ops | **已闭合**；ops 只需落 `K/K-01b.txt` 的双向对账（75/75） |
| 12 | K-04 | ops | **已闭合**；ops 只需落 `K/K-04.json`（payload 两键 + 页面数值） |
| 13 | J-11 | reviewer | **已闭合**；reviewer 只补关闭态浏览器截图（组件测试已覆盖 0 请求 / 1 alert） |
| 14 | J-07 | frontend | **已交付**；reviewer 复核挂载测试的对照组是否真的在跑 |

**纪律**：序 7–9 的浏览器条目必须在**同一 API 实例、同一开关状态**下连续跑完，
中途切开关会让 J-03 的勾选态证据与 J-11 的关闭态证据互相污染；
每条落盘前先 `jq` 确认 `(.data // .)` 命中（L-02），再截图。

### 9.4 L-03 落地后的状态码变更（**本刀唯一有意改变 HTTP 状态码的地方**）

`PUT /admin/trust-levels` 的 **store 写失败**：**422 `validation.invalid` → 500 `options.store_failure`**。
既有条目逐条复核（2026-11-11 09:2x）：

| 条目 | 断言 | 是否依赖旧状态码 |
| --- | --- | --- |
| I-10 | 丁 PUT → 403 | 否（权限先于 store） |
| I-11 | 戊 PUT 合法 config → 200 | 否（成功路径） |
| I-12 | 省略 limits/earnable → 422 `trust_levels.invalid_config` | 否（decode 在 save 之前，且 reason 不同） |
| I-13 | `restore-defaults` → 200 | 否 |
| I-24 | PUT 后库内值 == 响应 config | 否 |
| H3 | 戊 PUT 200 / 丁 PUT 403 | 否 |
| v2 §0-3 | bind 失败 → 422 `validation.invalid` | 否（bind 在 decode 之前） |

**结论：没有既有条目依赖「store 失败 → 422」。** 这次变更本身记在 **L-05**（正面条目，不是回归）。另外两件事必须做：

1. 新增/复核断言时，凡是「造 store 失败」的用例一律期望 **500 `options.store_failure`**，不许再写 422；
2. 老副本 B 删除时，注释里「422 不等于校验失败、排查要看原始 err」那段排查知识**要搬进
   `apphttp.MapOptionsError` 的注释**，不许跟着副本一起消失——它是运营排查的唯一线索。

### 9.5 证据隔离（补 lead 的口径）

「浏览器条目在同一 API 实例、同一开关状态下连跑」这条规则**同样适用于 J-03 与 L-03**：
L-03 改的是**同一个端点**的错误路径，别让 J-03 的 200 证据混进 L-03 的 500 现场。
做法：J-03 落盘 → 结束该轮会话 → 单独起一轮做 store 失败注入 → 两个证据目录不同、时间戳不同。

---

## 10. M13 合并口径（沿用 M8，不另发明）

M8 的合并规则（`reports/2026-11-10-trust-levels-m8-final-status.md:3`）：
**reviewer 独立证据优先 → 运维 `_summary.txt`（矩阵 ID 口径）→ 两边都无则 `未实现` + 理由。**

**终值（2026-11-11 10:0x，reviewer 定稿 + 我独立对账；提交 `57a3de5ad`）**：

| 分类 | 数 | 来源/校验 |
| --- | --- | --- |
| ok | **145** | = v2 侧 121 + 本刀新增 24 |
| FAIL | **0** | `_summary.txt` 10:00:56 版已 FAIL 0（H-5/I-09/I-16 三条全部消除） |
| pend | **2** | v2 侧遗留（限流类） |
| 规则型 | **1** | L-02（判据是检查脚本本身，不靠造数） |
| 作废 | **1** | v2 的 H13（由 H15 取代） |
| 未实现 | **0** | 字面意思：**既无证据缺口，也无落盘缺口**；见 §8 覆盖核对 |
| **合计** | **149** | 145+0+2+1+1+0 |

**对账**：`_summary.txt`（`2026-11-11T10:00:56`，矩阵 124 口径）为 **ok 121 · FAIL 0 · pend 2 · 未实现 0 · 作废 1**；
本刀新增 25 条 = **24 ok + 1 规则型**。两边相加正好 149，四类互斥、无空白行。
**头部纪律已生效**：该版 `_summary.txt` 第 1 行写明了采集时的开关状态（`features.trust_levels=enabled`）。

**注解已撤（2026-11-11T10:34）**：三个信号齐了——头部第 5 行改正、提交完成（`57a3de5ad`）、
回填校验 PASS（`git show HEAD:…controller_test.go` = `fa3f075a…` = 工作树）。
所以 `未实现 0` 恢复**字面意思**，不再读作「无证据缺口」。
**但注意 R16**：这份 `0 FAIL` 只覆盖**声明过的 149 条**，不等于整仓绿。

M13 合并表要能对上一刀的 124 条逐条说清（四栏，缺一栏就是漏账）：

| 栏 | 含义 | 预期 |
| --- | --- | --- |
| 上一刀 ok（75）→ 本刀仍 ok | 复跑确认 | 大部分 |
| 上一刀 ok → 本刀 FAIL/变化 | 行为被本刀有意改变 | **预期 0 条**（L-03 只改错误路径，不动成功路径；若出现，必须逐条解释） |
| 上一刀「未实现」（49）→ 本刀 ok | 收口 | A11–A12、C13–C16、C20、D2–D3、D10、E5、E6、F3、F5、F8、F9、G1–G6、H1–H3/H6–H12/H14/H16/H17、I 组 |
| 仍「未实现」 | 写明**环境限制**还是**待落地（谁、哪个里程碑）** | 逐条 |

数字口径：**124 + 25 = 149**，且 `ok + FAIL + SKIP + 未实现 = 149`，四类互斥、不许有空白行。
提交信息里的数字必须与 `…-m13-final-status.md` 一致（v2 §8 的纪律不变）。

---

## 11. I-16 修订（v2 的 I-16 判据不可满足，本条取代它）

**结论（2026-11-11 09:4x，lead 读脚本 + 实测）**：I-16 原判据「关→开切换后首个请求**没有** `viewer`」**永远不可能绿**，
因为产品行为是**正确的**：

```
恢复 enabled 后甲首个 GET /trust-levels → 200 · viewer=true
```

`viewer` 由 `apphttp.LoadActor` **每请求**从库重新装载（`auth_user.go:47` → `users.LoadActor`，4 次查询），
开关一恢复，下一个请求的 actor 就是**已解析态**。所谓「未解析」只存在于「装载时开关是关的」那**一个**请求里，
而那个请求正是 F3/I-15 已在验的「关闭期间 404」。

**这是本刀第六条「写了但没人见过它绿」**：前五条是红不了/红错地方，这条是**绿不了**——
判据把「请求级状态」当成了「会话级状态」。据此立为通用判据类别 **R11**（§1.3）。

**计数口径**：I-16 仍是**一个 ID**，内部拆成 `I-16a`/`I-16b` 两条断言，所以总数不变
（v2 124 + 本刀 25 = **149**），不要因为拆成两条就把总数再加一。

### 11.1 新判据（两条正面断言，取代 v2 的 I-16）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| I-16a | ops | **无现成测试** → 引用 F3/I-15 的关闭期证据，断言关闭期间**任何**请求都无 `viewer`（不重复造数） | `I/I-16a.txt`（引用 `I/I-15.txt`、`F/F3.txt`） |
| I-16b | ops | **无现成测试** → 恢复开关后首个 `GET /trust-levels`：`viewer` 存在 + `level ∈ {0..4}` + 若 `bottleneck` 存在则 `current < threshold` | `I/I-16b.json` |

**I-16b 的判据细节（2026-11-11 修订：不再在 shell 里重算档位）**：

**为什么改**：首版判据要求「`level` == 按 `DefaultPolicy()` 与库内**六项计数**算出的档」，
ops 据此算出 **0**，而实际返回 **1**。**返回 1 是对的**——`level.go:24-27` 的账龄闸之外**至少 TL1**：
用户 1 账龄 38 天（`created_at 2026-10-04`）远超 `NewUserDays=7`，过了窗口就落到活动闸的
`return LevelMember`。**错的是期望算法：它只用计数、漏了账龄**（第七条判据错，见附录 A）。

新判据（不复刻领域逻辑）：

```bash
call /tmp/jar-wu PUT /admin/features '{"options":[{"name":"features.trust_levels","value":"enabled"}]}'
call /tmp/jar-jia GET /trust-levels | sed '$d' > /tmp/i16b.json
jq -e '(.data|has("viewer"))
       and (.data.viewer.level|type=="number")
       and (.data.viewer.level >= 0 and .data.viewer.level <= 4)
       and (if (.data.viewer|has("bottleneck")) then (.data.viewer.bottleneck.current < .data.viewer.bottleneck.threshold) else true end)' \
   /tmp/i16b.json
```

**档位与计数的一致性不在这里验**，交给 `T: ^TestLevelAgeGate$` / `T: ^TestLevelBoundaries$`
（它们已守六指标边界与账龄闸）。理由：在 shell 里重算 `LevelOf` = 写第二份 `LevelOf`。

```bash
# 1) 恢复开关
call /tmp/jar-wu PUT /admin/features '{"options":[{"name":"features.trust_levels","value":"enabled"}]}'
# 2) 同一账号（甲）首个请求
call /tmp/jar-jia GET /trust-levels | sed '$d' > /tmp/i16b.json
# 3) 两个断言：viewer 在 + 档位与库内计数一致
jq -e '(.data|has("viewer")) and (.data.viewer.level|type=="number")' /tmp/i16b.json
Q "SELECT topic_count,comment_count,like_received_count,like_given_count,bookmark_count,days_visited
   FROM user_activity WHERE user_id=3;"
```

**也不许**拿「上一次响应里的 level」当期望——那是自证；同样不许「照 Requirements 写期望」——
`accountAge` 不是 `Requirements` 字段，它不进阈值配置，是独立账龄闸的输入（模块笔记原话）。

**I-16a 的口径**：关闭期间路由直接 404（`controller.go:244`），所以「无 `viewer`」在 HTTP 层是**必然**的，
它的价值在于把「关闭期没有等级面」这件事钉进矩阵，而不是重复造一份 404 证据。

### 11.2 v2 原判据留档（历史证据，不再执行）

> `I-16` ｜ ops ｜ 关→开切换后首个 GET：`has("viewer")==false` ｜ `I/I-16.txt`
>
> 原脚本 `:1999-2004`：恢复开关后断言 `jq -r '.data|has("viewer")' == false`。
> **jq 层是对的（`.data` 没错），错的是期望值。** 留此记录以免下一个人以为「又是信封 bug」。

---

## 附录 A · 七种让绿变假的方式（本刀在验收层最值钱的产出）

**共性**：七条都**不是产品错了**，是**断言的前提从没被验证过**。每一条都对应一个真实代价——
要么把正确实现判死（假 FAIL），要么让错误实现在绿里躺着（假 PASS）。

| # | 形状 | 本刀实例 | 机械判据 / 防线 |
| --- | --- | --- | --- |
| 1 | **测试名不存在** | 第一版矩阵 30 个名字 29 个不存在；`-run` 匹配不到时退出码 0、输出 `ok` | 名字必须来自 `go test -list '.*'`；必须见 `=== RUN`（R1/R2） |
| 2 | **在错的地方找** | web locale 找 API 文案（K-03 首稿）；顶层找信封内字段（I-09/L-02） | 断言必须写明「哪个文件、哪一层」（R7） |
| 3 | **断言层错位** | 值对、位置错：`has("config")` 查顶层，实际在 `.data` | 同 #2 族，但后果是**假 FAIL**：差点让 0 FAIL 门槛被自己的脚本撞掉（L-02）。**同族变体**：① 转述件冒充证据——文件对、ID 对，内容验的却是别的断言，后果是**连红都不会红**（`I/I-03.txt`）；② 旧证据覆盖新结论——文件对、断言对，但采集时间早于实现变更，证明的是**旧行为**。两者都在 §8 与 R13 有防法 |
| 4 | **不可证伪** | `level_purity_test.go` 的纯度断言（删掉被测逻辑也不红） | 守卫必须附「它红过的样子」（R10） |
| 5 | **红了但红错地方** | L-06 断言顺序：删一条徽章查询时，计数断言先红，点名句从未执行 | `-red.txt` 必须含**行号 + 点名文字**，且点名的是本条要守的断言（R10） |
| 6 | **绿不了** | I-16 把**请求级**状态（`LoadActor` 每请求装载）当成会话级，断言「切换后首请求仍是旧态」 | 先判定状态作用域：请求级 / 会话级 / 缓存级（R11） |
| 7 | **期望算法漏输入** | I-16b 只用六项计数推档、漏了**账龄闸**；算出 0 而正确是 1 | 期望值不许在 shell 里重算领域逻辑（那是第二份 `LevelOf`）；等级 = 计数 + 账龄两段式（§1.4） |

**给下一刀的用法**：写完任何断言，先问四句——
1. 「这个名字/路径/层，我**实测**过它存在吗？」
2. 「它**红过**吗？红在**我守的那句**上吗？」
3. 「我的期望值是**从哪算出来的**，那个算法和产品的算法是**同一份**吗？」
4. 「这条证据采集时，被测对象的**实际状态**是什么？是**实测的**还是**我以为的**？」

第 4 句是**夹具级漏输入**的机械判据：它把「设计意图」和「采集时的事实」分开——
本刀 I-16b（以为计数全 0 就是 TL0）与「以为 483 是 TL2 账号」都是这一句能当场抓住的。

---

## 12. 本刀遗留 residual（下一刀处理，不属未实现）

| # | residual | 现状 | 下一刀必须做什么 |
| --- | --- | --- | --- |
| 1 | **密封 Core Query 目录的 22 条清单有两份** | `bootstrap/sealed_query_catalog_test.go` 的 `sealedCoreQueryCatalogIDs` 与 `app/Http/Controllers/Extensions/query_catalog_test.go` 的 `sealedQueryCatalogIDs`——**两份手维护的字面清单**，靠两处注释互相指路，**未抽共享包**（刻意：bootstrap 断言装配产物、controller 断言 HTTP 响应，两个包各自必须能独立红，共用会耦合两个失败面） | **新增 stable query 时两处都要改**；`-run` **两个包都跑**（`./app/Http/Controllers/Extensions/` 与 `./bootstrap/`）。若下一刀决定抽共享常量包，须先证明「独立红」不被牺牲 |
| 2 | 本刀共抓到**四次「第二份」** | T6/T7 fixture schema、`mapOptionsError` 两份（已收口）、目录条数两份（上表）、**证据目录树两份**（`apps/api/storage/qa`，已删） | 开工第一件事按 L-03 的粗筛扫一遍：error→HTTP 映射、DTO 编码、权限判定、迁移建表、**目录清单** |
| 3 | `LoadEffectiveActorTx` 不展开父→子继承 | 已知缺口（父计划 residual 11） | 另开一刀 |
| 4 | `sqlc generate` 不可安全运行 | 已知（模块笔记 residual 14） | 另开一刀 |
| 5 | `trust.new_user.forbid_attachments` 仍是死配置 | D20 不变 | 另开一刀（或接线或删除） |

**注意**：这些**不是**矩阵里的「未实现」——它们是**已声明、已交代、本刀不做**的事项。
矩阵的 `未实现 0` 不包含它们；把 residual 记成「未实现」会让下一刀误以为有 5 条证据欠账。
