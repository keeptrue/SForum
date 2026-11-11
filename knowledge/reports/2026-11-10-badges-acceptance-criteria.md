# 徽章 + 用户活动统计 — 验收执行清单（M8 用）

Source: `plans/2026-11-10-badges-and-user-activity.md` §0（10 条口径）+ §6（13 行边界）
Audience: 审查（小审）在 M8 逐条执行；实施方按本清单自测
Status: 可执行。第 4 节 13 项已**全部由计划 §11「实施决断 D1–D11」拍板**，本文件已按决断校准；§11 与视觉规格冲突处以 §11 为准。

本文件不是计划的复述。每条只写四件事：**怎么摆现场 / 怎么点 / 看到什么算过 / 看到什么算不过**。
凡是计划没写死、本文件无法给出精确期望值的，一律在第 4 节列出，不在这里编数字。

---

## 1. 环境与公共前置（做一次，后面全用它）

### 1.1 服务

| 角色 | 地址 | 启动方式 |
| --- | --- | --- |
| API | `http://127.0.0.1:8080/api/v1` | `./scripts/api-dev.sh`（dev 下 API 自带 worker） |
| Web（用户自己的） | `http://127.0.0.1:3000` | 用户手动 `cd apps/web && bun run dev`，**不要 kill** |
| Web（QA 专用，可选） | `http://127.0.0.1:3001` | 另起一个 Nuxt dev，避免污染用户会话 |
| PostgreSQL | 容器 `sforum-postgres`，库 `sforum`，用户 `sforum`，密码 `sforum` | `./scripts/dev.sh` |
| Redis | 容器 `sforum-redis` | 同上 |
| Mailpit | `http://127.0.0.1:8025` | 同上（E-通知类才需要） |

容器名以 `docker compose ps` 实况为准，本文按现有 QA 脚本沿用的名字写。

### 1.2 三个 shell 函数（贴进当前 shell，全文后续都用）

```bash
API=http://127.0.0.1:8080/api/v1
ORIGIN=http://127.0.0.1:3000
EV=storage/qa/badges          # 证据目录，先 mkdir -p

Q()   { docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -tAc "$1"; }
R()   { docker exec sforum-redis redis-cli "$@"; }

login() { # $1=email $2=password $3=jar
  curl -s -c "$3" "$API/auth/session" -o /dev/null
  local t; t=$(awk '/csrf_/{v=$7} END{print v}' "$3")
  curl -s -b "$3" -c "$3" -X POST "$API/auth/login" \
    -H 'Content-Type: application/json' -H "X-CSRF-Token: $t" \
    -H "Origin: $ORIGIN" -H "Referer: $ORIGIN/" \
    -d "{\"login\":\"$1\",\"password\":\"$2\"}" -o /dev/null -w 'login=%{http_code}\n'
}

call() { # $1=jar $2=METHOD $3=/path [$4=json body]
  local t; t=$(awk '/csrf_/{v=$7} END{print v}' "$1")
  curl -s -b "$1" -c "$1" -X "$2" "$API$3" -H "X-CSRF-Token: $t" \
    -H 'Content-Type: application/json' -H "Origin: $ORIGIN" \
    ${4:+-d "$4"} -w '\nHTTP=%{http_code}\n'
}
```

CSRF 必须带：`X-Csrf-Token` 取自 `csrf_` cookie，缺了会 403 且与功能无关——不要把它误判成验收失败。

### 1.3 账号矩阵（验收开始前必须备齐）

| 代号 | 用途 | 账号 | 备注 |
| --- | --- | --- | --- |
| 甲 | 被统计者 / 徽章持有者 | `seed_44888364289b@seed.local` / `QaPass!2026-devops` | user_id=3，member，`storage/qa/creds.txt` 为准 |
| 乙 | 点赞者 / 取消者 | 另取一个 seed 用户 | 需先重置密码，见下 |
| 丙 | 零基线新用户（AC-4 幂等必须） | `seed:forum` 新生成 | 不能拿甲当丙，甲已有历史计数 |
| 管理员 | 后台 12 条路由 / AC-6 / AC-10 | operator 或 super_admin | **当前无已知账号，见第 4 节歧义 A1** |

造/修账号：

```bash
# 取管理员账号（若 dev 库已有）
Q "select u.id, u.email from users u join user_roles ur on ur.user_id=u.id
   join roles r on r.id=ur.role_id where r.key in ('operator','super_admin') order by u.id;"

# 给任意已有用户设一个已知密码（生成新 seed 用户后同样适用）
cd apps/api && go run ./cmd/sforum users:reset-password \
  --email <EMAIL> --password 'QaPass!2026-devops' --yes

# 需要新用户时（append-only，不影响既有数据）
cd apps/api && go run ./cmd/sforum seed:forum
```

### 1.4 每条用例的固定开场（不要跳）

```bash
mkdir -p storage/qa/badges
U=<被测 user_id>
Q "select user_id,topic_count,comment_count,like_received_count,like_given_count,
          bookmark_count,days_visited from user_activity where user_id=$U;"
Q "select user_id,visited_on from user_visits where user_id=$U order by visited_on desc limit 5;"
Q "select id,slug,name,tier,grant_mode,metric,threshold,enabled from badges order by id;"
Q "select id,user_id,badge_id,granted_by,granted_at,grant_reason from user_badges where user_id=$U;"
Q "select id,username,top_badge_id from users where id=$U;"
```

全部输出追加进 `$EV/baseline-<AC编号>.txt`。**所有计数断言都写成「基线 → 操作 → 基线+delta」**，不要断言绝对值：dev 库里有历史数据。

### 1.5 证据落点约定

| 类型 | 路径 |
| --- | --- |
| 接口/SQL 输出 | `storage/qa/badges/<编号>-<描述>.txt` |
| 截图（桌面 1633×810） | `storage/qa/badges/<编号>-1633.png` |
| 截图（移动 390×844） | `storage/qa/badges/<编号>-390.png` |
| 浏览器断言 JSON | `storage/qa/badges/<编号>-audit.json` |
| 开关翻转循环 | `storage/qa/badges/flag-evidence.txt` |

截图用现成工具，不要自己写新驱动：

```bash
node storage/qa/shot.mjs http://127.0.0.1:3001/u/<username> \
  "$PWD/storage/qa/badges/ac5-profile-1633.png" 1633 810
node storage/qa/shot.mjs http://127.0.0.1:3001/u/<username> \
  "$PWD/storage/qa/badges/ac5-profile-390.png" 390 844
```

需要登录态或 DOM 断言的用例，复用 `storage/qa/cdp-driver.mjs`（`launch` / `setViewport` / `goto` / `evaluate` / `login` / `sleep`），照 `storage/qa/probe-login.mjs` 的形状写一个 `probe-badges-<编号>.mjs`。

---

## 2. 十条口径的测试步骤

### AC-1 发主题 +1 / 发评论 +1 / 刷新保持

**前置**：甲已登录；`features.badges` 为 enabled 或 disabled **都可以**（统计不属于徽章产品面）；已做 1.4 基线。

**步骤**

1. 记基线 `T0 = topic_count`、`C0 = comment_count`。
2. 发主题：
   `call jar 甲 POST /topics '{"categoryId":<有效分类>,"title":"AC1 badge probe <ts>","content":"probe"}'`
   期望 `HTTP=201`，响应 `data.id` 记作 `TID`。
3. `Q "select topic_count,comment_count from user_activity where user_id=$U;"` → 期望 `topic_count = T0+1`，`comment_count = C0`（未变）。
4. 发评论：`call jar 甲 POST /topics/$TID/comments '{"content":"AC1 comment probe"}'` → 期望 `HTTP=201`。
5. 同上 SQL → 期望 `comment_count = C0+1`。
6. 刷新资料页 `http://127.0.0.1:3000/u/<甲username>`：tab「主题」计数 = `T0+1`，「回复」计数 = `C0+1`（这两个数来自 profile API，不是 `user_activity`，见第 4 节歧义 A2）。
7. 再刷新一次，数值不变（"刷新后保持"）。

**取证**：`ac1-activity.txt` 含三次 SQL 输出 + 两个 `HTTP=` 行；`ac1-profile-1633.png`。

**反例（判 FAIL）**
- 计数不涨；或一次操作 +2（重复 bump）。
- 评论把 `topic_count` 也涨了（metric 串了）。
- 匿名（无 cookie）发主题也 bump 到某个用户头上。
- 第二次刷新数字回退（说明读路径读的是缓存而非库，或写入未提交）。
- 软删主题后 `topic_count` 掉回（D4：软删**不回退**；若掉了说明误接了删除路径）。

---

### AC-2 获赞 +1 / 给赞 +1 / 取消回退

**前置**：甲有一篇 0 赞主题 `TID`（没有就新发一篇，并确认 `like_count=0`）；乙已登录（D11 角色乙）；全程避免自赞——自赞口径见 D3 与步骤 7。

**步骤**

1. 基线：`R0=甲.like_received_count`、`G0=乙.like_given_count`、`L0=topics.like_count where id=TID`。
2. 乙点赞：`call jar乙 POST /topics/$TID/like` → 期望 `HTTP=200`。
3. SQL：
   `Q "select (select like_received_count from user_activity where user_id=甲) r, (select like_given_count from user_activity where user_id=乙) g, (select like_count from topics where id=$TID) l;"`
   期望 `r=R0+1`、`g=G0+1`、`l=L0+1`。
4. 乙取消：`call jar乙 DELETE /topics/$TID/like` → 期望 `HTTP=200`；SQL 三项全部回到基线（`r=R0`、`g=G0`、`l=L0`）。
5. 评论路径重复一遍：`POST /comments/$CID/like` → `DELETE /comments/$CID/like`，断言同上。
6. 重复点赞：连点两次 `POST /topics/$TID/like` → 期望计数只 +1，第二次不叠加。

**取证**：`ac2-reactions.txt`（每一步的 `HTTP=` + 一行 SQL 三列对照）。

**反例**
- 取消后 `like_received_count` 不回退（只增不减）。
- 回退成负数（`CHECK >= 0` 会炸成 500，也算 FAIL）。
- 点赞者的 `like_given_count` 涨在作者头上（主体串了）。
- 评论点赞只动 `comments.like_count`、不动 `user_activity`。

**步骤（自赞，D3）**

7. 甲给自己的主题 `TID` 点赞：`call jar甲 POST /topics/$TID/like` → 期望 `HTTP=200`；`topics.like_count` 照常 +1；但甲自己的 `like_received_count` 与 `like_given_count` **都不变**。
8. 取消后同样断言两列不变。
9. 反例：自赞把 `like_received_count` 或 `like_given_count` 涨了（等于给了刷徽章路径，D3 明令禁止）。

---

### AC-3 访问天数：同日 1 天、跨天 +1、`(user_id, visited_on)` 唯一

**前置**：甲；Redis 可用；已做 1.4。**这条最容易因为 Redis 日 key 残留而假通过/假失败**，必须按步骤 3 清 key。

**步骤**

1. 造干净现场：
   ```sql
   DELETE FROM user_visits WHERE user_id=<甲> AND visited_on >= current_date - 1;
   UPDATE user_activity SET days_visited =
     (SELECT count(*) FROM user_visits WHERE user_id=<甲>) WHERE user_id=<甲>;
   ```
   记 `N0 = days_visited`。
2. 清当天 Redis 去重 key（key 形态按计划 §2.3 为 `visit:<userID>:<YYYY-MM-DD>`；实施若改名，按第 4 节歧义 A3 同步）：
   `R --scan --pattern "visit:<甲>:*" | xargs -r -n1 docker exec sforum-redis redis-cli DEL`
3. 甲带 cookie 连续 `GET /topics/1` 两次（间隔 1s）：
   `call jar甲 GET /topics/1` ×2，期望都是 `HTTP=200`。
4. SQL 期望：
   `Q "select count(*) from user_visits where user_id=<甲> and visited_on=current_date;"` → **1**
   `Q "select days_visited from user_activity where user_id=<甲>;"` → `N0+1`（只加一次）
5. 跨天模拟：把今天那行挪到昨天，再清 Redis key，再 `GET /topics/1` 一次：
   ```sql
   UPDATE user_visits SET visited_on = current_date - 1
    WHERE user_id=<甲> AND visited_on = current_date;
   ```
   期望：`user_visits` 现有 2 行（昨天 + 今天），`days_visited` 相对步骤 4 不再 +1（因为昨天那行是移动来的，今天这行才是新增）。
6. 并发同一天 10 次：
   `seq 10 | xargs -P10 -I{} curl -s -b <jar甲> -o /dev/null "$API/topics/1"`
   期望：今天仍只有 1 行；`days_visited` 不再增长。
7. 匿名对照：不带 cookie `GET /topics/1` 三次 → `Q "select count(*) from user_visits;"` 总数不变。

**取证**：`ac3-visits.txt`，含每次操作前后的 `count(*)` 与 `days_visited`，以及 Redis `--scan` 结果。

**反例**
- 同日两行（`PRIMARY KEY (user_id, visited_on)` 没生效 = 迁移错）。
- `days_visited` 一天涨 2（bump 在 `ON CONFLICT DO NOTHING` 未命中时也执行了）。
- 匿名请求产生 `user_visits` 行。
- 并发 10 次产生 2 行（Redis SETNX 未挡，且库层 upsert 写错）。
- 页面 500（采集失败污染了详情 GET 的主流程）。

---

### AC-4 规则自动授予 + `user_badges` 恰好一行

**前置**：`features.badges=enabled`；丙是零基线新用户（`topic_count=0`、`user_badges` 空）。

**步骤**

1. 取「初次发帖」徽章：`Q "select id,slug,metric,threshold from badges where slug='first-topic';"`
   期望 `metric='topic_count'`、`threshold=1`；记 `BID`。（slug 清单见 §4 A4，M0 迁移已落地。）
2. 丙发 1 主题（同 AC-1 步骤 2）。
3. 等授予可见。D2 定了 River job `badges.evaluate_user`（队列默认、`MaxAttempts:3`、去重键 `badges:evaluate:{userID}`），**判定上限 ≤10s**（队列 ≤5s；资料页不走 Forum 缓存，所以不用等 20–30s）：
   `for i in $(seq 20); do n=$(Q "select count(*) from user_badges where user_id=<丙> and badge_id=$BID;"); [ "$n" = 1 ] && break; sleep 0.5; done`
   超过 10s 仍未出现 → FAIL。
4. 期望：`count(*) = 1`；`users.top_badge_id` = `BID`（若这是丙唯一徽章）。
5. 丙再发第 2 个主题 → 期望仍是 **1 行**（不重复授予），`granted_at` 不变；该行来源是「规则评估」：`granted_by IS NULL` 且 `grant_reason=''`（与重算的 `'recompute'` 区分开）。
6. 并发重复触发：并发 10 次发帖（或用 `POST /admin/badges/recompute` 并发 10 次，看哪条先落地）→ 期望 `user_badges` 该 `(user_id, badge_id)` 仍 **1 行**，通知也只 1 条（`E4` 一并验）。

**取证**：`ac4-grant.txt`：`BID` 定义 + 每次 `count(*)` 与 `top_badge_id`。

**反例**
- 2 行（`ON CONFLICT (user_id,badge_id) DO NOTHING` 失效）。
- 10s 内没出现，但点一次后台重算就出现（说明 job 触发点没接上，D2 的四个触发点漏了）。
- 授予了但 `top_badge_id` 仍 NULL。
- 达到阈值后完全不授予（评估未接上写路径）。
- 丙零基线时就被授予（阈值判断用了 `>=` 之外的错误比较）。

---

### AC-5 资料页分组 + tier 排序；帖子旁只显示最高级 1 个

**前置**：给甲同时持有 gold、silver、bronze 三档（gold 用 manual「社区贡献」、silver 用 manual「优质解答」、bronze 由规则产生），并且**同级再来一个**（例如再 manual 授予一个 gold，验证同级按 `position` 再按 `granted_at` 降序）。

**步骤**

1. `call jar甲 GET /profiles/<甲username>/badges` → 期望 `HTTP=200`。
2. 结构断言（写进 `ac5-api.json`）：
   - 分组按 `badge_groups.position` 升序；组内 tier 权重 `gold > silver > bronze`；同级按 `badges.position` 升序，再 `granted_at` 降序。
   - 未获得的 automatic 徽章带 `progress.current` / `progress.threshold`，且 `current < threshold`。
   - 已获得项带 `granted_at`。
3. 匿名同一 URL → 期望 `HTTP=200`（对应 E1）。
4. 资料页 `http://127.0.0.1:3000/u/<甲username>`：tab 数为 **5**，顺序为 `topics → replies → bookmarks → badges → about`（**D6**：徽章在「收藏」之后、「关于」之前；bookmarks 关闭时 badges 前移一位；扩展 widget tab 仍在 about 之后），badges tab **不带数字 count**；点开后分组标题、组内顺序与步骤 2 完全一致。
5. 帖子旁：`call jar甲 GET /topics/<甲有主题的id>` → `data.topBadge.slug` 必须等于该用户 tier 最高的那个徽章 slug（gold 优先，同级按 position）。
6. SSR 计数断言（chip 只能 1 个）：
   `curl -s http://127.0.0.1:3000/t/<TID> | grep -o 'sf-badge-chip' | wc -l` → **1**
   并断言 tier 与 slug 落在属性上（D7 冻结契约）：
   `grep -o 'sf-badge-chip[^>]*data-tier="[a-z]*"'`、`grep -o 'data-badge-slug="[a-z0-9-]*"'`
   → `data-tier` 必须是该用户最高的那档，`data-badge-slug` 必须是该徽章 slug。
7. 评论旁同样验一次：取甲在某主题下的评论，`grep` 该评论块内 chip 数 = 1。

**取证**：`ac5-api.json`、`ac5-profile-1633.png`、`ac5-topic-1633.png`、`ac5-chip-count.txt`。

**反例**
- 帖子旁出现 2 个及以上 chip。
- 显示 bronze 而用户有 gold（`top_badge_id` 维护错）。
- 组内 tier 顺序乱（前端没排序、依赖后端返回顺序）。
- 未获得的自动徽章不显示进度，或显示 `current=0/threshold=0`。
- 已停用徽章仍出现在列表里（对应 E3）。

---

### AC-6 手动授予/收回生效、记录可查、自动徽章不可收回

**前置**：**戊**（持 `badge.manage`）已登录；**丁**（持 `badge.view`）已登录；准备 `manual` 徽章 `MID`、`automatic` 徽章 `AID`（D11 角色表）。

**步骤**

1. 授予：`call jarAdm POST /admin/badges/$MID/grants '{"userId":<甲>,"reason":"AC6 probe"}'`
   期望 `HTTP=200/201`。
2. SQL：`Q "select user_id,badge_id,granted_by,grant_reason from user_badges where user_id=<甲> and badge_id=$MID;"` → **1 行**，`granted_by` = 管理员 user_id（非 NULL），`grant_reason='AC6 probe'`。
3. 重复授予同一用户同一徽章 → 期望 `HTTP=409`，错误码 `forum.badge_already_granted`；行数仍 1。
4. 授予记录可查：`call jarAdm GET /admin/badges/$MID/grants` → 期望 `HTTP=200`，列表含步骤 2 那行（含 `grantedAt`）。
5. 收回：`call jarAdm DELETE /admin/badges/$MID/grants/<甲>` → 期望 `HTTP=200`；该行消失；`users.top_badge_id` 被重算（若这是最高级则变次高，否则不变）。
6. 自动徽章收回：`call jarAdm DELETE /admin/badges/$AID/grants/<丙>` → 期望 `HTTP=422`，错误码 `forum.badge_manual_only`；该行**仍在**。
7. 权限两侧（D11：丁只读 / 戊可写）：
   - 丁（`badge.view`）：`GET /admin/badges`、`GET /admin/badge-groups`、`GET /admin/badges/{id}/grants` → **200**；`POST/PATCH/DELETE /admin/badges*`、`POST .../grants`、`DELETE .../grants/{uid}`、`POST /admin/badges/recompute` → **403**。
   - 戊（`badge.manage`）：上述全部 → 200/201/204。
   - 甲（member）：`GET /admin/badges` → **403**。

**取证**：`ac6-grants.txt` 全码路径表（method / path / http / code）+ 步骤 2 的 SQL 输出。

**反例**
- 重复授予返回 200（幂等当成功）。
- 收回自动徽章成功（违反 `forum.badge_manual_only`）。
- 收回后 `top_badge_id` 仍指向已收回徽章。
- moderator 能授予（权限键用错，把 `manage` 写成了 `view`）。
- `granted_by` 为 NULL（没记操作者）。

---

### AC-7 `features.badges` 关闭：徽章面消失、写端点 403、统计照常

**前置**：甲已有徽章 + 已有统计数字；`storage/qa/` 下有可复用的翻转模式（`run-flag-cycle.sh` / `bookmarks-flag-evidence.sh`）。

**步骤**

1. 记录 enabled 态基线：`curl -s $API/web-options/features.badges` → 期望 `{"value":"enabled"}`（字段名以实际响应为准）；tab 数 5；帖子页 chip 数 1；`Q "select * from user_activity where user_id=<甲>;"` 存一份。
2. 关闭：
   `Q "update web_options set value='disabled' where name='features.badges';"`
3. **等进程内缓存过期**（Options 缓存 TTL=30s）：
   `for i in $(seq 30); do curl -s $API/web-options/features.badges | grep -q disabled && break; sleep 2; done`
4. 端点断言（全部期望 403 且 `code=forum.badges_disabled`）：
   - `GET /profiles/<甲username>/badges`
   - `POST /admin/badges`、`PATCH /admin/badges/1`、`DELETE /admin/badges/1`
   - `POST /admin/badges/1/grants`、`DELETE /admin/badges/1/grants/<甲>`
   - `POST /admin/badges/recompute`
   逐条记 `HTTP=` 与响应 `code`。
5. 资料页：tab 数从 5 变 **4**，不存在 `badges` tab（`document.querySelectorAll('[role=tab]')` 里没有 badges）。
6. 帖子页：chip 计数 **0**（同 AC-5 步骤 6 的 grep 方式）。
7. **统计照常**：`Q "select * from user_activity where user_id=<甲>;"` 与步骤 1 完全一致（行没被删、值没被清零）；再发 1 主题 → `topic_count` 仍 +1（统计不受开关控制）。
   前端侧（**D1** 定稿为 **5** 项）：资料摘要区共 5 格 —— 主题数、回复数、**分类数**、获赞数、访问天数。
   - 前三项是既有数字（`profile.topicCount` / `profile.commentCount` / `profile.categoryCount`，容器 `.sf-profile-summary`，`aria-label="profile statistics"`），**本刀只增不减**：删掉分类数等于回退一个已上线的公开数字。
   - 新增的是「获赞数」与「访问天数」，走 `user_activity`。
   - 几何：`.sf-profile-summary--quint`（≥768 一行 5 格，390 两列、末项跨满），断言 `document.querySelectorAll('.sf-profile-summary__item').length === 5` 且 390 下无横向溢出。
   - 关闭开关后，**获赞数与访问天数必须仍然显示且数值不变**；`给赞数` / `收藏数` **本来就不该出现在资料页**（5 项里没有它们，出现即 FAIL，D1 明令不上）。
   双源是设计：主题/回复走 Profile 实时 `COUNT(*)`，获赞/访问天数走 `user_activity`。**不要因为两者在软删后不一致就判 FAIL**（D1 + D4）。
8. 还原：`Q "update web_options set value='enabled' where name='features.badges';"` + 轮询回 enabled；复验 tab 5、chip 1、写端点 200。

**取证**：`storage/qa/badges/flag-evidence.txt`（三态：enabled → disabled → restored）、`ac7-disabled-1633.png`、`ac7-restored-1633.png`。

**反例**
- 403 但错误码是别的（例如通用 `forum.forbidden`）。
- 关开关后 tab 还在（前端读的是构建期常量而非运行时 options）。
- 关开关后 `user_activity` 行被清空或统计停摆。
- 还原后徽章不回来（缓存没失效）。

---

### AC-8 迁移 up/down 在一次性 PG 集群上实机可逆

**前置**：Docker 可用；`docker info` 正常；**不要**在导出了 `DATABASE_URL` 的 shell 里直接跑库测试（`internal/testdb` 会拒绝）。

**步骤**

1. 结构断言（快）：
   `bash scripts/test-api-isolated.sh ./database/migrations/`
   期望 exit 0；其中 `badges_migration_test.go` 的 Up/Down 结构断言、`stable_core_views_test.go` 的三视图断言全 PASS。
2. 实机 Up：用隔离脚本注入的一次性集群跑集成测试（脚本会把 `SFORUM_TEST_DATABASE_URL` 指向一次性集群）：
   `bash scripts/test-api-isolated.sh ./app/Models/Badges/ ./database/migrations/ ./app/Support/HostAPI/`
3. 手工结构核对（在一次性集群里，或 dev 库只读核对）：
   ```sql
   \dt user_activity, user_visits, badge_groups, badges, user_badges
   \dv sforum_core_v1.forum_badges, sforum_core_v1.forum_user_badges, sforum_core_v1.forum_user_activity
   \d users          -- 必须有 top_badge_id
   select count(*) from badge_groups;              -- 期望 3
   select count(*) from badges;                    -- 期望 12
   select count(*) from notification_type_descriptors where type='badge_granted'; -- 1
   select count(*) from notification_type_policies where type='badge_granted';    -- 3
   ```
4. Down + 再 Up 的实机链路：**用 M0 已交付的专用脚本，不要手搓**（`cmd/migrate` 本身只有 up 与 `--check-*`，没有 down 参数）：
   ```bash
   ./scripts/verify-migration-reversible.sh; echo "exit=$?"
   ```
   期望：`exit=0`，且最后一行是
   `全部通过：202611100002 可 down 回干净状态并重新 up 重建。`
   脚本会自建一次性集群、探测 `sforum_dev_guard` 哨兵、跑完整链 up → 单步 down → 对象断言 → 再 up → 对象断言，退出时销毁集群。失败时保留现场：`SFORUM_VERIFY_KEEP_CLUSTER=1 ./scripts/verify-migration-reversible.sh`。
   退出码语义见 `knowledge/runbooks/2026-11-10-badges-migration-reversibility.md`：2 预检 / 4 up 失败 / 5 down 失败 / 6 down 后残留 / 7 再 up 失败 / 8 重建不全。
5. 脚本已内置的 Down 后断言（核对它确实覆盖）：五张表（`user_activity` / `user_visits` / `badge_groups` / `badges` / `user_badges`）、`users.top_badge_id` 列、三个稳定视图、`badge_granted` 描述符与三条通道策略全部消失，goose 版本回到 `202611100001`；再 up 后全部重建，12 徽章 + 3 分组（`activity`/`content`/`honor`）在位，版本回到 `202611100002`。
6. OnlineSafe 声明：`cd apps/api && DATABASE_URL=<一次性集群> go run ./cmd/migrate --check-online-safe` → 期望 exit 0（迁移头部声明了 `-- +sforum OnlineSafe`）。结构断言已由步骤 1 覆盖，不重复跑。

**取证**：`ac8-up.txt` / `ac8-down.txt`（`\dt` `\dv` `\d users` 输出前后对照）、隔离脚本 exit code。

**反例**
- Down 报错（外键顺序错，例如先删 `badges` 后删 `user_badges`）。
- Down 后残留任一对象。
- 二次 Up 冲突。
- 迁移未声明 `-- +sforum OnlineSafe` 导致 `--check-online-safe` 红。

---

### AC-9 门禁全绿

**前置**：干净工作区；`apps/web` 只用 bun。

**步骤与期望**（逐条记 exit code，任一非 0 即 FAIL）

| # | 命令 | 期望 |
| --- | --- | --- |
| 1 | `cd apps/api && go build ./...` | exit 0 |
| 2 | `bash scripts/test-api-isolated.sh` | exit 0，无 SKIP 掉的库测试 |
| 3 | `cd apps/web && bun test` | exit 0；pass 数 ≥ 当前基线（2026-11-10 收藏那一刀为 1944 pass / 0 fail） |
| 4 | `cd apps/web && bun run typecheck` | exit 0 |
| 5 | `node tests/validate-architecture-boundaries.mjs` | exit 0 |
| 6 | `ruby scripts/validate-openapi-refs.rb` | exit 0 |
| 7 | `node scripts/v3-catalog/generate.mjs --check` | exit 0，无 diff |
| 8 | `./scripts/test.sh --fast` | 三线（api / web / static）全 exit 0 |

**取证**：`ac9-gate.txt`，每行 `cmd -> exit=N`；`test.sh --fast` 的 `tests/.logs/` 路径一并记下。

**反例**
- 第 5 条红：`SFTopicShowPage.vue` 抽取后未下调基线，或 `postgres_store_ops.go` 净增。
- 第 7 条红：路由数 +13 未进账本，或 UI 身份未登记（`SFBadgeChip` / `SFProfileBadgeTab` / 后台四组件）。
- 第 2 条有 SKIP：说明 `SFORUM_TEST_DATABASE_URL` 没注入，等于没测。
- 第 3 条 pass 数比基线少：有测试被删。

---

### AC-10 浏览器 QA（1633×810 + 390×844）

**前置**：QA 专用 web 实例（建议 3001，避免与用户 3000 会话互相污染）；三个目标页：
- 资料页：`http://127.0.0.1:3001/u/<甲username>` → 切到 badges tab
- 帖子页：`http://127.0.0.1:3001/t/<TID>`（含甲的主题作者块 + 甲的评论）
- 后台：`http://127.0.0.1:3001/control-panel/badges`（**D9**）。前缀受 `NUXT_PUBLIC_ADMIN_ROUTE_PREFIX` 重写，dev 实际值由前端确认并写进 QA 记录。**API 侧是 `/api/v1/admin/badges`（不带前缀），两者不要混。**

**步骤**（每页 × 两视口 = 6 组）。aria 判定用 **D8 四条规则**：① chip `aria-label` 含徽章名 + tier；② tab 容器 `role=tablist`、每项 `role=tab` + `aria-selected` 且唯一；③ 进度条 `role=progressbar` + `aria-valuenow`/`aria-valuemax`/`aria-valuetext`；④ 可点元素必须是 `button`/`a` 或有 `tabindex` + `role`。

1. 打开页面，等 `settle` 1.2s，执行断言脚本（复用 `cdp-driver.mjs`）：
   ```js
   // 判定口径
   const overflow = document.documentElement.scrollWidth > window.innerWidth + 1;
   const clipped = [...document.querySelectorAll('.sf-badge-chip, .sf-profile-badge-card, .sf-admin-badge-table td')]
     .filter(el => { const r = el.getBoundingClientRect();
                     return r.right > window.innerWidth + 1 || r.left < -1; }).length;
   ```
   期望：`overflow === false`、`clipped === 0`、`consoleErrors.length === 0`。
2. 资料页 tab：`[role=tab]` 共 5 个，`aria-selected="true"` 恰 1 个；点击 badges 后内容面板切换、无整页重载。
3. 帖子旁 chip：`document.querySelectorAll('.sf-badge-chip').length === 1`；`aria-label` 非空且**同时含 tier 名与徽章名**（D7 模板 zh `{tierName}徽章：{name}`、en `{tierName} badge: {name}`）；`data-tier` ∈ `bronze|silver|gold`；`data-badge-slug` 与 API 返回一致。
4. 后台页：标题与工具栏左对齐（`h1` 与 toolbar 的 `left` 差值 ≤ 1px）；列表 8 列不换行挤压；点「新建」打开对话框；表单校验（slug 冲突 / threshold ≤ 0 / icon 前缀错）给出字段级错误且 Toast 不自动消失。
5. 390×844 重复 1–4；英文文案（切 en-US）下 chip 与表格表头不裁切。

**取证**：6 张截图 + `ac10-audit.json`（每页每视口的 `overflow` / `clipped` / `consoleErrors` / `tabs` / `chips`）。

**反例**
- 任意视口 `scrollWidth > innerWidth`。
- chip 被父容器裁掉（`clipped > 0`）。
- `aria-label` 为空或只有图标名。
- 后台页出现两条并存的标题/工具栏（违反 AGENTS.md 后台几何契约）。
- console 有 hydration mismatch 报错。

---

## 3. §6 边界与失败模式的测试步骤（E1–E13）

| # | 场景 | 前置 | 操作 | 期望 | 取证 | 反例 |
| --- | --- | --- | --- | --- | --- | --- |
| E1 | 匿名访问资料页徽章 | 甲有徽章；无 cookie | `curl -s $API/profiles/<甲username>/badges`；再匿名打开 `/u/<甲username>?tab=badges` | API 200 且返回公开徽章；页面正常渲染，不跳登录；5 个统计数字（D1）对匿名可见 | `e1-anon.txt` + `e1-anon.png` | 401/403；页面被重定向到 /login；统计数字对匿名隐藏 |
| E2 | `features.badges` 关闭 | 同 AC-7 | 见 AC-7 全流程 | 见 AC-7 | 见 AC-7 | 见 AC-7 |
| E3 | 用户被 ban / disabled | 甲有徽章；管理员把甲 `status` 置 `banned`（或 disabled） | `select count(*) from user_badges where user_id=<甲>;` → 行**保留**；`select * from sforum_core_v1.forum_user_badges where user_id=<甲>;` → **0 行**；`select * from sforum_core_v1.forum_user_activity where user_id=<甲>;` → **0 行**；帖子页 chip 应消失 | 行保留、视图过滤、前端不展示 | `e3-banned.txt` | `user_badges` 行被删（误以为要清）；或视图仍返回该用户（视图漏 `status='active'` 谓词） |
| E4 | 徽章停用（`enabled=false`） | 甲持有 `BID` 且它是 `top_badge_id` | `Q "update badges set enabled=false where id=$BID;"`；等 ≤10s（D2 的 job 会重算，不必等 35s）；`select top_badge_id from users where id=<甲>;`；`GET /profiles/<甲>/badges`；帖子页 grep chip | 已授予行保留；列表与 chip 都不再出现；`top_badge_id` 被重算为次高或 NULL | `e4-disabled.txt` + 截图 | 停用后仍展示；`top_badge_id` 仍指向停用徽章（§6 要求停用时重算受影响用户，未实现即 FAIL） |
| E5 | 徽章被删除 | 甲持有 `BID` | `Q "delete from badges where id=$BID;"`；查 `user_badges`、`users.top_badge_id` | `user_badges` 行级联删除；`top_badge_id` 变 NULL（或重算为次高）；页面无 500 | `e5-delete.txt` | 删除报外键错；`top_badge_id` 悬空导致详情页 500 |
| E6 | 自动徽章手动收回 | 丙有自动徽章 `AID` | `DELETE /admin/badges/$AID/grants/<丙>` | 422 `forum.badge_manual_only`；行仍在 | `e6-manual-only.txt` | 收回成功；或返回 500 |
| E7 | 手动徽章重复授予 | 甲已持 `MID` | 再次 `POST /admin/badges/$MID/grants` | 409 `forum.badge_already_granted`；行数 1 | `e7-duplicate.txt` | 200；或 500（唯一约束裸奔到 SQL 层） |
| E8 | 规则阈值下调不追溯，重算才追溯 | 丙 `topic_count=2`；某自动徽章阈值从 10 改 3 | `PATCH /admin/badges/$BID '{"threshold":3}'`；等 15s（> D2 上限）；查 `user_badges`；再 `POST /admin/badges/recompute`；看响应 `granted`/`refreshed`；再查 `user_badges` | 改规则后**不自动**授予（行数 0）；重算后丙获得该徽章，且响应 `granted >= 1`；新行 `granted_by IS NULL` 且 `grant_reason='recompute'` | `e8-recompute.txt` | 改规则立即追溯（违反"不自动追溯"）；或重算后 `granted=0` 且行仍不存在；或补的行 `grant_reason` 不是 `recompute`（来源不可区分） |
| E9 | Redis 不可用（访问采集降级） | 甲当天无 `user_visits` 行 | `docker stop sforum-redis`；甲 `GET /topics/1` 两次；查 `user_visits`；`docker start sforum-redis` | 详情页仍 200（不失败）；`user_visits` 今天恰好 1 行（`ON CONFLICT DO NOTHING` 幂等）；`days_visited` 只 +1 | `e9-redis-down.txt` | 页面 500；或产生 2 行；或 `days_visited` +2 |
| E10 | 统计列漂移 + 重算（**D5：本刀不做 CLI**） | 甲有若干历史计数；丙零基线 | ① 漂移：`Q "update user_activity set topic_count=999 where user_id=<丙>;"`；② 按模块笔记的对账 SQL 记录差异（文档化 SQL 必须真实可跑）；③ `POST /admin/badges/recompute`，记响应 `granted`/`refreshed`；④ `Q "select count(*) from user_badges where user_id=<丙> and grant_reason='recompute';"`（比按时间窗猜可靠） | 重算把徽章与统计对齐到规则（丙应得 `first-topic` 等）；**统计列本身不保证被重算修正**（D5 只做徽章重算 + 对账 SQL），对账 SQL 能指出差异即 PASS | `e10-reconcile.txt`（含对账 SQL 原文与输出） | 模块笔记没有对账 SQL、或 SQL 跑不通；或声称重算会修统计列却修不了（口径造假） |
| E11 | 用户硬删 | 甲有 `user_activity`/`user_visits`/`user_badges` 行 | `Q "delete from users where id=<甲>;"` | 三张表的甲行全部 CASCADE 消失；无孤儿行 | `e11-cascade.txt` | 删除报外键错；或残留孤儿行 |
| E12 | 列表查询无 N+1 | 首页/版块列表有 ≥10 个不同作者 | 打开列表页；抓 SQL 日志（`APP_ENV=development` 下 API 日志）或 `pg_stat_statements` | 整页徽章只来自一次 `users.top_badge_id` join；不出现按作者逐条的 badge 查询；无 `DISTINCT ON` | `e12-nplus1.txt`（SQL 计数） | 每行一次 badge 查询（N+1）；或对 `user_badges` 做 `DISTINCT ON` 全表扫 |
| E13 | 缓存导致的可见延迟 | 甲无徽章；`topicDetail` 缓存 TTL 30s | 先打开 `/t/<TID>` 一次（填缓存）；再给甲授予徽章；**立即**再打开同一页；等 35s 后再打开 | 资料页徽章 tab ≤10s 出现（D2，不走 Forum 缓存）；帖子旁 chip 立即打开可能仍无（**这是预期**）；35s 后必须出现 | `e13-cache.txt` + 两张截图 | 验收把"帖子旁立即无 chip"判成 FAIL（口径错）；或 35s 后仍无 chip；或资料页 tab 也超过 10s |

---

## 4. 13 项问题的处置（计划 §11 已全部拍板）

初稿列的 A1–A13 已由 `plans/2026-11-10-badges-and-user-activity.md` §11「实施决断 D1–D11」逐条解决。下表是索引；**执行时按本文件正文的期望值，不要回看计划前文**（前文与 §11 冲突处已作废）。

| 编号 | 原问题 | 决断 | 验收落点 |
| --- | --- | --- | --- |
| A1 | 账号矩阵缺管理员 | D11：甲/乙/丙/丁/戊五角色，密码统一 `QaPass!2026-devops`，写进 `storage/qa/creds.txt` | AC-6 步骤 7、AC-10 后台 |
| A2 | 统计数字无 UI 落点 | D1：**5 项**（主题/回复/分类/获赞/访问天数）；分类数是既有公开数字，只增不减；给赞、收藏不上资料页；双源有意 | AC-7 步骤 7、E1 |
| A3 | 自动授予触发时机 | D2：River job `badges.evaluate_user`，四个触发点，去重键 `badges:evaluate:{userID}`，**≤10s** | AC-4 步骤 3、E4、E8、E13 |
| A4 | 12 个 slug 未定义 | M0 迁移已落地（见 4.1） | AC-4 步骤 1 |
| A5 | 自赞口径 | D3：**不计入**获赞/给赞；内容 `like_count` 照常 +1 | AC-2 步骤 7–9 |
| A6 | DOM 契约 | D7：`sf-badge-chip` + `data-tier` + `data-badge-slug`；tab value `badges`；卡片 `sf-profile-badge-card` / `data-earned` | AC-5 步骤 4/6、AC-10 步骤 3 |
| A7 | 迁移 down 入口 | M0 交付 `scripts/verify-migration-reversible.sh` | AC-8 步骤 4 |
| A8 | 缺 `badge:reconcile` CLI | D5：**本刀不做 CLI**，只做后台重算 + 文档化对账 SQL | E10 |
| A9 | 统计回退语义 | D4：取消点赞/收藏回退；软删主题/评论、审核驳回、ban **不回退** | AC-1 反例、AC-2 |
| A10 | tab 插入位置 | D6：`topics/replies/bookmarks/badges/about`（badges 在 about **之前**） | AC-5 步骤 4 |
| A11 | aria 无判定标准 | D8：四条可断言规则 | AC-10 步骤 1 |
| A12 | 后台路由两套前缀 | D9：前端 `/control-panel/badges`，API `/api/v1/admin/badges` | AC-10 前置 |
| A13 | 并发口径 | D10：并发 recompute 10 次 + 并发发帖 10 次**都跑** | AC-4 步骤 6 |

### 4.1 slug 清单（M0 迁移实值，断言用它）

| 分组 | slug | tier | 规则 |
| --- | --- | --- | --- |
| activity | `regular-visitor` | bronze | `days_visited >= 7` |
| activity | `resident` | silver | `days_visited >= 30` |
| activity | `veteran` | gold | `days_visited >= 180` |
| content | `first-topic` | bronze | `topic_count >= 1` |
| content | `topic-author` | bronze | `topic_count >= 10` |
| content | `discussant` | bronze | `comment_count >= 25` |
| content | `rising-star` | bronze | `like_received_count >= 10` |
| content | `well-received` | silver | `like_received_count >= 100` |
| content | `widely-acclaimed` | gold | `like_received_count >= 500` |
| content | `collector` | bronze | `bookmark_count >= 10` |
| honor | `community-contribution` | gold | manual |
| honor | `quality-answer` | silver | manual |

以库内实值为准（运营可改）；断言写 `where slug='<上表值>'`。若运营已删该徽章，本行判 SKIP 并注明。

### 4.2 三个最容易误判的地方（审查必读）

1. **D1 双源不是 bug。** 主题/回复走 Profile 实时 `COUNT(*)`，获赞/访问天数走 `user_activity` 快照。软删内容后两者会不一致——**这是设计**。别把它写成缺陷。
2. **D4 回退表不是对称的。** 取消点赞/收藏要回退；软删主题/评论、审核驳回、ban 都**不回退**。断言写反了会得出一堆假 FAIL。
3. **D2 的时间上限是 10s，不是 35s。** 资料页徽章 tab 不走 Forum 缓存，10s 内必须出现；只有帖子旁的 chip 受 20–30s 缓存影响（E13 已分开写）。

---

## 5. 建议的执行顺序（省时间）

1. 先按 D11 备齐甲/乙/丙/丁/戊五个账号并写进 `storage/qa/creds.txt`（旧脚本里的过期密码一并改掉），否则 AC-6 / AC-10 后台部分跑不了。
2. 跑 AC-9 第 1、5、6、7 条（快，几秒级，先卡住架构与目录问题）。
3. 跑 AC-1 → AC-2 → AC-3（写路径，纯 SQL 断言，不依赖前端）。
4. 跑 AC-4 → AC-6（授予域，依赖 A3/A4 已定）。
5. 跑 AC-5 → AC-7（前端 + 开关）。
6. 跑 AC-8（迁移，独立，可与 5 并行）。
7. 跑 AC-10 + E1–E13（浏览器与边界，最后做，最费时）。
8. 最后跑 AC-9 全量 `./scripts/test.sh --fast`（三线），一次性拿总账。

---

## 6. 契约对照（M3 已落地，按 wire 实况校准）

初稿这一节按计划里的领域类型写；M3 落地后按 `Http/Controllers/Badges/dto.go` 的实际 JSON 重写。**契约跟 wire 走，不跟领域结构走**——领域是嵌套的，HTTP 层是摊平的。

### 6.1 领域类型 → wire 形状

| 领域类型（`Models/Badges/types.go`） | wire 结构（`dto.go`） | 差异 |
| --- | --- | --- |
| `badges.Badge` | `adminBadgeResponse` | **少 `groupKey`**（领域有，DTO 不发）；`grantedCount` 有（`LEFT JOIN` 子查询一次带出） |
| `badges.PublicBadge` | 不上线 | 被 DTO 摊平进每个条目，没有独立的公开 badge 对象 |
| `badges.UserBadge` | `profileBadgeItem` | 摊平：`badge.{slug,name,tier,icon,position}` 提升到条目顶层；`progress` 摊成 `current`/`threshold` |
| `badges.BadgeGroupSection` | `profileBadgeGroup` | **少 `description`** |
| `badges.BadgeRef` | `forum.yaml` 的 `topBadge` | 一致（4 字段，actor 无关，可进缓存） |
| `badges.BadgeGroup` | `adminBadgeGroupResponse` | 一致（含 `badgeCount`，SQL 已实现） |
| `badges.BadgeGrantRecord` | 同名 | **无 `id`**；`(userId, badgeId)` 才是标识 |

### 6.2 公开面 vs admin 面（字段级）

| 字段 | 公开面（`ProfileBadgeItem`） | admin 面（`Badge`） |
| --- | --- | --- |
| `slug` / `name` / `tier` / `icon` / `position` | 有 | 有 |
| `earned` | 有 | — |
| `current` / `threshold` | 未获得自动徽章才有 | — |
| `grantedAt` | 已获得才有 | — |
| `id` | 无 | 有 |
| `description` | 无 | 有 |
| `groupId` / `groupName` | 无 | 有（nullable） |
| `grantMode` / `metric` / `threshold` | 无（进度用 `current`/`threshold` 表达） | 有 |
| `enabled` | 无 | 有 |
| `grantedCount` | 无 | 有 |
| `createdAt` / `updatedAt` | 无 | 有 |

### 6.3 收口时发现的四处「实现 ≠ 计划」（已按实现写契约，需各方对齐）

| # | 事项 | 计划/初稿 | 实现 | 影响 |
| --- | --- | --- | --- | --- |
| 1 | 分组路由 | `/admin/badge-groups`、`/admin/badge-groups/{groupID}` | **`/admin/badges/groups`、`/admin/badges/groups/{groupID}`** | 前端按计划写会 404。契约已改按实现，**前端需确认用的是哪个** |
| 2 | 分组 PATCH 语义 | 与 badge 一样「部分更新」 | **已修复**：`badgeGroupBody` 改为全指针 + `mergeInto`，与 badge PATCH 同语义 | 契约已同步为部分更新（`BadgeGroupUpdateInput` 去掉 required，描述改为「省略即保持」） |
| 3 | 授予/收回响应 | 201 + 授予记录 | **200 + `{"granted": true}`**；记录里**没有 `id`**；重复授予是 **409 `forum.badge_already_granted`**（不是幂等成功）；收回不存在的授予是 **200 + `{"revoked": false}`**（幂等，且 `revoked` 表示「确实删了一行」而非「请求成功」） | 前端若按 201/`data.id` 解析会失败；`revoked:false` 不能当失败提示；两个方向幂等性相反，别写成统一处理 |
| 4 | `Badge.groupKey` | 初稿有 | **DTO 不发**（只有 `groupId`/`groupName`） | 前端若读 `groupKey` 会 undefined |

### 6.4 M8 可直接执行的断言

- `curl -s $API/profiles/<u>/badges` 的响应里 grep 不到 `grantedCount` / `enabled` / `grantedBy` / `grantReason` / `createdAt` —— 出现即 FAIL。
- 未获得条目**必须没有** `grantedAt`；已获得条目**必须没有** `current`/`threshold`（DTO 用 `omitempty` 显式区分）。
- `GET /admin/badges` 每行必须有 `grantMode`/`metric`/`threshold`/`enabled`/`grantedCount`。
- 13 个 `operationId` 必须与 `controller.go` 的 handler 方法名逐字相同（生成器靠它反推路由 ID）；当前 13/13 一致。
- `POST /admin/badges/recompute` 响应必须是 `{granted, refreshed}` 两个计数；对已全对的社区再跑一次，两者都必须是 **0**（幂等证据）。
- `user_badges` 三种来源可区分，M8 用它精确筛而不是按时间窗猜：规则评估 → `granted_by IS NULL` 且 `grant_reason=''`；管理员手动 → `granted_by=<actor>` 且 `grant_reason=<理由>`；批量重算 → `granted_by IS NULL` 且 `grant_reason='recompute'`。
