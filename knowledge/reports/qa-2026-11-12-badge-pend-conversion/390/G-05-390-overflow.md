# G-05（390 无横向溢出）pend → ok · 实测证据

日期：2026-11-12 21:30–21:40（Asia/Shanghai）
执行者：小前（frontend）
范围：`/control-panel/badges?tab=badges` 的 `SFAdminBadgeBackfillRow`（回扫进度行）在 390×844 下的三条断言
+ 默认档案回归（route-inspector）。
产物目录：`knowledge/reports/qa-2026-11-12-badge-pend-conversion/390/`（本文件所在目录）。

只读/只动两处：
- 改 `tests/browser-admin-390x844.mjs`（加 `--no-proxy-server` + `SFORUM_ADMIN_PROFILE=badges` 档案；默认档案断言逐字未动，见 §5）。
- dev 库临时写：1 个一次性超管账号 + 1 行 `badge_rule_backfill_runs` 夹具；跑完单事务删净（见 §4）。
未改三份验收矩阵，未 commit/add/stash。

## 1. 环境与新鲜度（跑前自证）

```
pid=219173  lstart=四 11月 12 21:29:56 2026   （旧 17717/20:26:34 已由 backend 换掉）
最新 apps/api 源码 mtime = 2026-11-12 21:09:08  → 进程晚于源码，二进制新鲜
GET /api/v1/health → 200
GET /api/v1/badges → 200（3000 与 8080）
GET /api/v1/admin/badges/backfill-status（无会话）→ 401
GET /control-panel/badges（无会话）→ 302（admin middleware）
web dev server（3000）全程未动；`air` 未用；Chrome 加 `--no-proxy-server`（本机 http_proxy=127.0.0.1:7897）
```

## 2. 一次性超管账号（已删除）

- `POST /api/v1/auth/register` → **201**，`id=498`，`username=qa_pend_mwdl6414`
  （double-submit CSRF：先 GET 落 `csrf_` cookie，回传 `X-Csrf-Token`，Origin/Referer = http://127.0.0.1:3000；
  `HUMAN_VERIFICATION_PROVIDER=disabled` 所以机器验证直接过）。
- 授权：`insert into user_roles(user_id,role_id) select 498, id from roles where key='super_admin'` → INSERT 0 1；
  复核 `roles=2 has_super=true`。
- 会话：`sforum_session`（43 字符，httpOnly），按 `tests/browser-admin-390x844.mjs` 的 CDP `Network.setCookie` 注入。
- 复核 `GET /admin/badges/backfill-status` 带会话 → **200**。
- **坑（诚实记录）**：注册即发的会话在授 super_admin **之前**创建也不影响（权限按请求解析），实测 `auth/session`
  返回 `roleKeys:["member","super_admin"]` 且权限表含 `admin.access`/`badge.view` 全家桶。

## 3. 失败态夹具（已删除）

真实链路走 `POST /admin/badges/recompute` 在本库拿不到 running/failed（无待回扫目标，上轮已验），
按任务书允许的第二条路直改库插一行（`badges.id=2` 存在、`error` 非 NULL、`status` 命中 CHECK）：

```sql
insert into badge_rule_backfill_runs
  (badge_id,status,last_user_id,failed_cursor_user_id,batch,error,started_at,updated_at,finished_at)
values (2,'failed',137,137,3,'qa-pend: simulated batch failure',
        now()-interval '2 minutes', now(), now())
returning id,status,last_user_id,failed_cursor_user_id,error;
→ 3|failed|137|137|qa-pend: simulated batch failure
```

端点核对（带会话，原始响应见 `backfill-status-failed.json`）：

```json
{"badgeId":2,"slug":"resident","name":"老住户","status":"failed",
 "lastUserId":137,"failedCursorUserId":137,"error":"qa-pend: simulated batch failure"}
```

## 4. 断言与实测值

调用（原始 exit code 见各 `.log`）：

```
SFORUM_ADMIN_PROFILE=badges \
SFORUM_SESSION_COOKIE=<43 字符> \
SFORUM_ADMIN_PATH='/control-panel/badges?tab=badges' \
SFORUM_QA_DIR=…/390 SFORUM_EVIDENCE_OUTPUT=…/390/evidence-390-failed.json \
node tests/browser-admin-390x844.mjs      → EXIT 0
```

| 断言 | 实测 | 判据 |
|---|---|---|
| 视口 override 生效 | `innerWidth=390 innerHeight=844 dpr=3` | 硬判 |
| 进度行**只有一行** | `rowCount=1`（`[data-testid="badge-backfill-progress"]`） | 硬判，`!==1` 红 |
| 行在筛选区**上方** | `rowBeforeFilter=true`（DOM 顺序）+ `rowBottom=439 ≤ filterTop=451`（几何） | 双向，任一不满足红 |
| 行本身不被横向裁切 | `rowWithinViewport=true`、`rowScrollFits=true` | 硬判 |
| 390 无横向溢出 | `scrollWidth=390 ≤ innerWidth=390`，`horizontalOverflow=false` | 硬判 |
| 越界控件（排除横向滚动容器） | `clippedPage=0` | 硬判 |
| 越界控件（**不排除**，原始计数） | `clippedRaw=48`（全部位于表格横向滚动容器内，属预期滚动） | 只记录，不判红；与上一条并排防「排除法吃掉真溢出」 |
| 失败态文案（值级） | `回扫失败于用户 #137` + `涉及徽章：老住户` | 值级 `includes`，不做键存在断言 |
| 控制台错误/异常 | `[]` | 硬判 |

整屏与行裁切两张图（Chrome 154 headless，dpr3 → 1170×2532 / 1026×276）：

- `2026-10-16-admin-390x844-control-panel-badges-tab-badges.png`（sha256 `888e0818…0604`）
- `2026-11-12-badge-backfill-row-390x844-control-panel-badges-tab-badges.png`（sha256 `d16d6b0c…d45b`，行裁切）

目视核对：进度行（红底 alert + 刷新按钮）在搜索框与四个下拉的**上方**，同屏无横向滚动条，
表格在自身容器内滚动。

**首跑红过，原因与修法（不留假绿）**：第一次跑 `rowCount=0`、列表「0 枚徽章」——dev 页首屏 2.5s 内还没拉到
`/admin/badges` 与 `/admin/badges/backfill-status`（冷编译 + 图标请求排队）。修法不是放宽断言，而是让
`badges` 档案**轮询到进度行出现**（最多 15s，300ms 一次）后再测量；等不到仍照常测量 → `rowCount=0` 会红。
复跑即绿（证据即上表），`debug-page-state.mjs` 留有当时的现场（页面内 fetch 200、12 枚徽章、`items` 1 条）。

## 5. 默认档案回归（变异纪律：改前改后各一次）

改前字节快照：`browser-admin-390x844.orig.mjs`（sha256 与改前工作树一致 `44ebc4fd…1705`，已在本目录留档）。

```
改前（orig 快照，默认路径 /control-panel/extensions/route-inspector）
  → EXIT 0；`all admin 390x844 acceptance assertions passed`
  实测 inspector=true incidentsPanel=true overflow=false clipped=0 consoleErrors=0
改后（工作树最终版，同路径同档案）
  → EXIT 0；`all admin 390x844 acceptance assertions passed`
  实测 inspector=true incidentsPanel=true overflow=false clipped=0 consoleErrors=0
```

断言行数一致：默认分支 6 条，逐字节比对通过——
`diff <改前 assert 块> <改后 else 分支(去掉 2 空格缩进)>` → **DEFAULT_BRANCH_ASSERTS_BYTE_IDENTICAL**。
两份原始输出：`route-inspector-baseline-pre-edit.log` / `route-inspector-after-edit.log`。

改动内容（`tests/browser-admin-390x844.mjs`，136+/11-）：

1. Chrome 参数加 `--no-proxy-server`（本机代理下 127.0.0.1 会被送进代理 → ERR_CONNECTION_REFUSED）。
2. 新增 `SFORUM_ADMIN_PROFILE`（默认 `route-inspector`，原断言一字未改；`badges` 走 §4 的 9 条断言）。
3. `badges` 档案：进度行轮询等待、行/筛选区几何测量、`clippedRaw` 与 `clippedPage` 并排、行裁切截图。
4. `finish()` 的证据 JSON 多记 `adminProfile`；控制台摘要按档案分两行。

## 6. 清理（单事务）与复核

`cleanup.log` 原文：

```
BEGIN
DELETE 1   -- badge_rule_backfill_runs (badge_id=2, error='qa-pend: simulated batch failure')
DELETE 2   -- user_roles(=498)  （member + super_admin）
DELETE 1   -- user_sessions(=498)
DELETE 1   -- user_credentials(=498)
DELETE 1   -- users(id=498)
COMMIT
=== 复核计数（全部必须 0）===
 users(id=498)              | 0
 user_roles(user=498)       | 0
 user_sessions(user=498)    | 0
 user_credentials(user=498) | 0
 backfill_runs(qa-pend:%)   | 0
 user_roles_orphans         | 0
=== 回扫表总数（期望回到 0）===
 badge_rule_backfill_runs_total | 0
```

另：删除后拿旧 cookie 打 `GET /admin/badges/backfill-status` → **401**（会话确实失效）；
`account.json` 里的会话值已抹掉（只留长度与用户名）。`/tmp` 下的临时响应体已清。

## 7. 交付给验收矩阵的 G-05 证据句（可直接粘贴）

> **G-05 390 无横向溢出 · ok** — 真实 Chrome 154 headless + CDP `Emulation.setDeviceMetricsOverride`
> （390×844 dpr3）跑 `tests/browser-admin-390x844.mjs` 的 `SFORUM_ADMIN_PROFILE=badges` 档案（exit 0）：
> `documentElement.scrollWidth = 390 ≤ innerWidth = 390`（`horizontalOverflow=false`）、页面级越界控件 0
> （原始计数 48 全部位于表格横向滚动容器内，两项并排记录）；`[data-testid="badge-backfill-progress"]`
> 恰 1 行且位于筛选区**上方**（DOM 顺序 `rowBeforeFilter=true` + 几何 `rowBottom 439 ≤ filterTop 451`）；
> 失败态值级文案 `回扫失败于用户 #137` / `涉及徽章：老住户`；consoleErrors `[]`。
> 证据：`knowledge/reports/qa-2026-11-12-badge-pend-conversion/390/evidence-390-failed.json` +
> 整屏/行裁切两张 PNG。默认档案（route-inspector）改前改后各跑一次均 exit 0、断言行数一致（6 条逐字节相同）。
> 一次性超管账号（id=498）与夹具行已单事务删除，复核计数全 0（含 `badge_rule_backfill_runs` 总数回到 0）。

## 8. 未覆盖 / 边界

- 只测了 `status=failed` 这一态；`running`/`completed` 的 390 布局本轮没重拍（上轮 `probe-390.mjs` 有历史截图，
  但那是独立探针、非本脚本档案）。
- 本档案只认 `/control-panel/badges`；其他管理页仍走默认 route-inspector 断言。
- `SFORUM_ADMIN_PROFILE` 之外的档案（如公开页）不在本脚本范围内。
