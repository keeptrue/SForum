# Decision: 后台 7 条 admin.access 粗门全部保留，判据是「同一读面一致」

## Status

Accepted（2026-11-15，第 12 轮 RV-46 逐条裁决；代码与钉子见提交 `82034bf2e` 及本轮审查返工追加提交）

## Context

435 条路由权限矩阵（仓库外审计产物，只读）把 7 条后台路由标成「只有 `admin.access` 粗门」。
审计口径的默认解法是「统一收紧到细粒度键」，但这批里有两组是**刻意**读宽写窄的
（`system-updates`、`trust-levels` 读面），收紧会直接打红既有契约；其余若按单条响应的
敏感度逐条猜，会得到「页面 200、面板 403」的半坏页面。所以本轮交付不是收紧，而是逐条
裁决，并把裁决钉成测试。

**判据（本记录的立论基础）：一条后台路由的门由它所属的读面决定，不由单条响应的敏感度决定。**

- 读面 = 一张后台页（首屏数据 + 面板 + 列表 + 行操作）或一组同源端点。
- 同读面内的端点必须同门。否则一个 200 页面里会长出 403 面板，而前端没有 per-panel
  错误面，退化形态是「面板静默消失」——比宽门更难被发现。
- 宽门只有在它只是**下界**时才合法：数据级权限（`AdminSurfaceContract.Permission`、
  实体写权）必须在门之后再判一次。只宽不判等于提权。

## Decision

7 条全部保留 `admin.access`，本轮零门收紧。

| # | 路由 | 门 | 保留理由（一句话） |
|---|---|---|---|
| 1 | GET `/api/v1/admin/admin-surfaces` | `admin.access` | 目录按 `AdminSurfaceContract.Permission` 逐条过滤，门只是下界；收成 `extension.view` 会整片挡掉 operator 的仪表盘小部件与列表列（operator 无该键） |
| 2 | POST `/api/v1/admin/admin-surfaces/:surfaceId/invoke` | `admin.access` | `invokeAdminSurface` 复检 `contract.Permission` 并把 actor 权限整包转发给插件；路由门再细就与 surface 声明打架 |
| 3 | GET `/api/v1/admin/overview` | `admin.access` | 与后台仪表盘路由壳同门，是本页首屏聚合数据 |
| 4 | GET `/api/v1/admin/overview/resources` | `admin.access` | 同一张页的实时面板（`useAdminOverviewLive` 轮询）；唯一更细键 `settings.site.manage` 造成**权限倒挂**：operator 持该键、tech_admin 不持 |
| 5 | GET `/api/v1/admin/system-updates` | `admin.access` | `Service.Status` 注释明写「available to every actor allowed into the admin area」；出站检查 `CheckNow` 才是 `settings.site.manage` |
| 6 | GET `/api/v1/admin/trust-levels` | `admin.access` | 刻意读宽写窄：只读管理员得到策略镜像（编辑器只读渲染），改它需要 `trust.manage` |
| 7 | GET `/api/v1/admin/trust-levels/conditions` | `admin.access` | 与 #6 同一张编辑器页；写面（`PUT` / `restore-defaults` / `rule-impact`）才是 `trust.manage` |

**刻意保留的两处读宽写窄**（不属缺陷，改动会打红既有契约）：

| 读面 | 写面 | 既有钉子 |
|---|---|---|
| `GET /admin/system-updates` | `POST /admin/system-updates/check` = `settings.site.manage` | `SystemUpdates/controller_test.go` 的 `TestStatusReturnsReleaseState` / `TestCheckNowRequiresSiteSettingsPermission` |
| `GET /admin/trust-levels`（读 200） | `PUT` / `restore-defaults` / `rule-impact` = `trust.manage`（写 403） | `TrustLevels/controller_test.go` 的 `TestAdminRoutesRequirePermissions` |

**裁决钉在何处**（相对路径 + 符号名，不写行号）：

- `apps/api/app/Http/Controllers/AdminOverview/coarse_gate_adjudication_test.go`
  —— 7 条目录声明、读宽写窄差额、内置角色可达性矩阵、技术运维宿主遥测可达性。
- `apps/api/app/Http/Controllers/Extensions/admin_surfaces.go` 的 `adminSurfaceActor` 注释
  —— 两层授权（门 + surface.Permission）的口径。
- `apps/api/app/Models/AdminOverview/service.go` 的 `Overview` / `Resources` 注释
  —— 与外壳同门、以及 `settings.site.manage` 倒挂的具体推导。

## Consequences

- 内置岗位可达性（离线矩阵，已钉进 `TestBuiltinRolesRetainAdminCoarseGateReachability`）：
  moderator / operator / tech_admin / super_admin 对 7 条**全部可达**；本轮没有角色失去页面。
- 读面继续加宽不需要动这 7 条；但**改读面结构**（拆分仪表盘、给 status 加技术运维字段）
  必须回来重审，见复位条件。
- 没有新增权限键。要按敏感度细分宿主遥测，得先有独立键 + 种子角色 + 迁移，属产品能力变更。

## 复位条件（出现任一条，对应条目回池重审）

1. 出现能区分「宿主遥测 / 版本状态」敏感度的**独立权限键**，且已进种子角色与迁移 → #4、#5 回池。
2. 某个内置岗位的权限集变更导致倒挂消失或反转（例如 tech_admin 获得 `settings.site.manage`、
   或 operator 失去 `admin.access`）→ 7 条全部回池，重算可达性矩阵。
3. `AdminSurfaceContract` 新增「目标可见性」语义，或 `invokeAdminSurface` 补上 `TargetID` 复检
   → #1、#2 回池（下界论证的前提变了）。
4. 后台仪表盘拆成多张页（`overview` 与 `resources` 不再同壳）→ #3、#4 回池。
5. `system-updates` 状态响应新增仅技术运维可见的内容（主机信息、仓库凭据、内部构建路径）→ #5 回池。
6. 信任等级读面新增可识别到个人的数据（逐用户分布、逐用户进度）→ #6、#7 回池。
7. **证据侧复位（不是裁决复位）**：权限矩阵工具能静态识别 helper 间接门
   （`adminSurfaceActor`、`TrustLevels` 的 `requireAdmin`）后，这 7 条会从「无运行时证据」
   名单里消失；那不需要改门，只需在矩阵里补证据行。

## 本轮未闭环（不在本裁决口径内）

- `invokeAdminSurface` 不复检 `TargetID` 可见性（`listAdminSurfaces` 有）：
  归插件契约口径，兜底是转发过去的 actor 权限。
- `POST /admin/seo/assets` 的权限检查在 `AssetService.Replace` 内、位于 `FormFile` 之后：
  缺权限且缺文件时回 422 而非 403（未执行任何动作、零泄露面）；改顺序是对外可见行为变更，
  单独一刀。
