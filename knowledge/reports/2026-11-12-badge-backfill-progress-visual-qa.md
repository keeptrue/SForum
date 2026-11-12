# 徽章回扫进度行 · 后台 390×844 视觉验收（task-11）

日期：2026-11-12 20:25–20:37（Asia/Shanghai）
执行者：ops（老运）
范围：`/control-panel/badges` 的 `SFAdminBadgeBackfillRow`（回扫进度行）+ `SFAdminExecutableTrustImpact` 能力披露段。
只读应用代码：未改 `apps/web`、`apps/api` 任何生产文件；未 commit。

## 0. 现场实况（与任务书假设不同，必须先读）

- 开工时（20:25）**3000 与 8080 都没有服务在跑**：`curl` 均返回 `000`（`Could not connect`），`ss -ltn` 无 3000/8080 监听。
- `uptime` 显示 `up 3 min`，进程起始时间集中在 20:23 —— 机器刚重启过一轮，用户手起的 dev server 与旧 API 实例都已经不在了。
- 因此本轮的 3000 与 8080 都是**我起的**：没有任何「用户的服务」被我停掉（`ps` 里开工时不存在可停的 3000 进程）。两个实例保留在跑，见 §6。

## 1. 环境与启动（原始 exit code）

依赖（已在跑）：`sforum-postgres`（127.0.0.1:15432）、`sforum-redis`（127.0.0.1:16379），`docker ps` 均 healthy。

构建：

```
cd apps/api && export TMPDIR=/home/inkedus/.cache/dsh-tmp && go build -o tmp/sforum-api ./cmd/api
→ exit 0（2.8s，产出 tmp/sforum-api 89,121,201 bytes，20:26:22）
```

启动（按 `scripts/api-dev.sh` 的方式 source `.env`、写 devguard 哨兵、走 staging 内置树，不用 `air`）：

```
set -a; . ./.env; set +a
export BUILTIN_EXTENSION_ROOT=$PWD/storage/builtin-dev
(cd apps/api && go run ./internal/testdb/cmd/devguard)   # → "devguard: public.sforum_dev_guard marked"
cd apps/api && nohup ./tmp/sforum-api &                  # PID 17717，started 2026-11-12 20:26:34
```

探测（全部 `--noproxy '*'`）：

```
GET /api/v1/badges                          → 200
GET /api/v1/admin/badges/backfill-status    → 401（未带会话）→ 证明新端点已上线
GET /api/v1/badges（带会话，Nitro 代理）     → 200
GET http://127.0.0.1:3000/                  → 200
```

web dev server（3000 当时空闲）：

```
cd apps/web && nohup bun run dev &   # nuxt dev --host 0.0.0.0 --dotenv ../../.env
→ Nuxt Nitro server built in 11013ms；GET / → 200
```

## 2. 一次性验收账号（已全部删除）

- 注册：`POST /api/v1/auth/register`（double-submit CSRF：先 GET 拿 `csrf_` cookie，回传 `X-Csrf-Token`，Origin/Referer = `http://127.0.0.1:3000`）→ **HTTP 201**，`id=496`，`qa_vis_202808`。
- 授权：`insert into user_roles(user_id,role_id) select 496, id from roles where key='super_admin'` → 1 行。
- 会话：`sforum_session` cookie（43 字符）取自同一 cookie jar，按 `tests/browser-admin-390x844.mjs` 的方式用 CDP `Network.setCookie` 注入浏览器。
- 清理（20:36）与复核：

```
delete from badge_rule_backfill_runs where badge_id=2 and error in ('','probe: simulated batch failure');  -- DELETE 1
delete from user_roles        where user_id=496;   -- DELETE 2
delete from user_sessions     where user_id=496;   -- DELETE 1
delete from user_credentials  where user_id=496;   -- DELETE 1
delete from users             where id=496;        -- DELETE 1
```

复核（全部 0）：

```
users(id=496)=0  user_roles=0  user_sessions=0  user_credentials=0  badge_rule_backfill_runs=0
```

另：为能力披露段临时给 `sforum.gamification` 的 `extension_versions.manifest.capabilities` 追加过 `badges.fact.write`，已用原 manifest 还原，`cmp` 校验 **MANIFEST_RESTORED_BYTE_EQUAL**，全库 `manifest::text like '%badges.fact.write%'` 计数回到 **0**。（该尝试未取到截图，见 §5。）

## 3. 回扫运行行的造法（说明选哪条）

先走真实链路：`POST /api/v1/admin/badges/recompute`（带会话 + CSRF）→ `200 {"granted":0,"refreshed":0,"queued":0}`，随后 `badge_rule_backfill_runs` **仍为 0 行**——本库没有需要异步回扫的目标，拿不到 running 态。

因此按任务书允许的第二条路：**直改库插入 run 行**（确定性、可回放）：

```
insert into badge_rule_backfill_runs (badge_id,status,last_user_id,failed_cursor_user_id,batch,error,started_at,updated_at,finished_at)
values (2,'running',137,0,3,'', now()-interval '2 minutes', now()-interval '10 seconds', null);   -- badge 2 = 老住户
-- 第二态：update ... set status='failed', failed_cursor_user_id=137, error='probe: simulated batch failure', finished_at=now()
```

端点核对（会话态）：`GET /admin/badges/backfill-status` → `items[0] = {badgeId:2, slug:"resident", name:"老住户", status:"running"|"failed", lastUserId:137, failedCursorUserId:137}`。

## 4. 断言与实测值（真实 Chrome 154 headless + CDP `Emulation.setDeviceMetricsOverride` 390×844 dpr3）

探针：`knowledge/reports/qa-2026-11-12-badge-backfill-progress/probe-390.mjs`（复用 `tests/browser-admin-390x844.mjs` 的 CDP/`Network.setCookie` 手法，Chrome 加 `--no-proxy-server`）。

| 断言 | running 实测 | failed 实测 | 结论 |
|---|---|---|---|
| 视口 override 生效 | `innerWidth=390, innerHeight=844, dpr=3` | 同 | PASS |
| `documentElement.scrollWidth <= innerWidth` | `390 <= 390`，`horizontalOverflow=false` | 同 | PASS（无横向溢出） |
| 页面级被裁控件 | `0`（另有 48 个位于表格横向滚动容器内，属于预期滚动） | `0` | PASS |
| 进度行出现 | `true` | `true` | PASS |
| 行文案 | `回扫进行中：已扫描到用户 #137` / `涉及徽章：老住户` | `回扫失败于用户 #137` / `涉及徽章：老住户` | PASS（三态中的两种） |
| 位于筛选区上方 | `rowBox.bottom=439 <= 搜索框 top=461`（`rowBox {top:363,left:32,width:326,height:76,right:358}`） | 同 | PASS |
| 单行紧凑（不换行错位） | `height=76`（alert 两行标题+说明），`rowScrollWidth=326 <= rowClientWidth=326` | 同 | PASS |
| 刷新按钮可访问名 | `aria-label="刷新回扫进度"`，`title="刷新回扫进度"`，宽 32px | 同 | PASS |
| 点击确实发请求 | `backfill-status` 请求 1 → 2，`delta=1` | `delta=1` | PASS |
| 控制台错误/异常 | `[]` | `[]` | PASS |

调用与原始 exit code：

```
SFORUM_SESSION_COOKIE=… SFORUM_STATE=running SFORUM_QA_DIR=… node probe-390.mjs  → EXIT=0
SFORUM_SESSION_COOKIE=… SFORUM_STATE=failed  SFORUM_QA_DIR=… node probe-390.mjs  → EXIT=0
```

截图（`knowledge/reports/qa-2026-11-12-badge-backfill-progress/`）：

- `badge-backfill-390x844-running.png`（整屏 390×844）
- `badge-backfill-row-390x844-running.png`（进度行裁切）
- `badge-backfill-390x844-failed.png`
- `badge-backfill-row-390x844-failed.png`

关于 `tests/browser-admin-390x844.mjs`：**没有用它跑本页**。它的断言是硬编码的 route-inspector 契约（`[data-testid="admin-route-inspector"]`、`route-incidents` 面板），拿 `/control-panel/badges` 去跑只会产出与本次验收无关的 FAIL，属于误导性证据；本轮改为复用它的同一套 CDP 手法写专用探针（上表 exit code 均为探针原始值）。

## 5. 能力披露段：**pend（未取到证据）**

事实与卡点：

1. 全库 `select count(*) from extension_versions where manifest::text like '%badges.fact.write%'` = **0**：现有任何扩展包都没有声明 `badges.fact.write`，而该披露段只在**可执行扩展的启用/安装信任弹窗**里渲染（`SFAdminExtensionEnableDialog.vue`、`SFAdminPageInstallDialog.vue`）。
2. 尝试路径：挑一个 disabled 且带能力的扩展（`sforum.gamification`，「SForum Gamification」），临时给它的 manifest 追加 `badges.fact.write`，打开 `/control-panel/extensions` 点「启用」以触发弹窗。两次尝试都**没定位到该行**：列表按页展示 23 个插件，目标行不在首屏，页内搜索输入（`input[type=search]/input[placeholder]`）设值 + `input` 事件后仍未出现该行，`click = {ok:false, reason:'row-not-found'}`。
   探针 `probe-trust-390.mjs` → **EXIT=1**（`section=false caps=0`），截图按「不许留误导证据」原则已删除，只保留脚本本身。
3. 已还原：见 §2 末，manifest 字节级还原、`badges.fact.write` 全库计数 0。

结论：**布局断言（390 下不溢出、label 与披露句可见）本轮没有可信实测值**，记为 pend。要补这一项，需要在「扩展列表可达目标行」的前提下重跑，或直接构造一个声明 `badges.fact.write` 的合成扩展包（dev watcher 会自动 enable，得先 disabled 才看得到启用弹窗）——这会产生额外 fixture 状态，超出本刀范围，需要 Lead 决定是否做。

## 6. 当前进程与回滚

- API：`./tmp/sforum-api`，PID **17717**，启动 20:26:34（新二进制，含 `/admin/badges/backfill-status`），监听 8080。
- Web：`bun run dev`（PID 19773 外层 bun）→ `nuxt dev`（PID 19775），监听 3000，非用户实例。
- 两者都是本轮为验收起的；停法：`kill 17717`（API）与 `kill 19773 19775`（web）。保留运行是为了让 Lead/用户能直接打开验证；要恢复「开工时空闲」状态就说一声，我停掉。
- Chrome profile 全部落在 `/home/inkedus/.cache/dsh-tmp/sforum-qa/chrome/`（不在 `/tmp`），`/tmp/probe-*`、`/tmp/qa-*`、`/tmp/sforum-admin-cdp-*` 已清理。
