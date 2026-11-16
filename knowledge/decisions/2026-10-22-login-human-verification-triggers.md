# Decision: 登录强化的人机验证触发条件

- Date: 2026-10-22
- Status: Accepted
- Scope: `apps/api/app/Models/Identity`、`apps/api/app/Models/Options`、
  `apps/api/app/Http/Controllers/Identity`、`apps/web/app/components/identity/SFLoginFormPage.vue`、
  `apps/web/app/components/admin/settings/site/tabs/SFAdminSiteVerificationTab.vue`

## Context

`login_risk` 之前只有一种触发方式：账号级失败计数达到 `max_failures * 3` 后置位
（`LoginLockout.RequiresVerification`）。`HasKnownDevice`（UA 指纹命中活跃会话）与
`privileged` 判定能力都已存在，但前者源码注释明确写着「尚未在 login 流程接通」。

运营需要的登录强化不只是「失败后」，还包括「总是」「新设备」「管理员账号」。
本轮只做登录侧，provider 仍只支持 ALTCHA。

## Decision

1. 三个独立开关，全部受 `human_verification.scenarios.login_risk` 总闸约束：
   - `human_verification.login_risk.always`（public）
   - `human_verification.login_risk.new_device`（admin-only）
   - `human_verification.login_risk.privileged`（admin-only）
2. `always` 是唯一需要前端预判的触发条件，因此设为 public；登录页据此进入即渲染
   ALTCHA。`new_device` / `privileged` 只由服务端 422 驱动，避免任何按账号预检的
   枚举信号。
3. 「新设备」= 当前 UA 指纹在该账号未撤销会话中没有命中（复用
   `HasSessionFingerprint`，`user_agent_raw` 等值匹配）。不引入 IP 前缀或 Redis
   设备记忆；代价是「登出所有设备」后下一次登录会再验一次。
4. 「管理员账号」= `IsInitialSuperAdmin` ∪ `super_admin` 角色 ∪ `admin.access` 权限。
   不依赖单一角色名。
5. 失败锁定与总闸、与显式豁免都解耦：`loginRequiresVerification` 先判
   `LoginLockout.RequiresVerification`，命中即要求验证，之后才看
   `SkipHumanVerification` 与策略总闸。Redis 侧账号失败计数因此**永远**先于策略
   判定生效，也不受任何豁免影响（P2-1 收窄，2026-11-16）。
6. 失败姿态：Options 读取失败、设备指纹查询失败一律 fail open（退化为仅失败锁定），
   空指纹视为已知设备。
7. 限流从硬编码 60 次/分/场景/IP 改为可配置
   `human_verification.rate_limit.per_minute`（1–600，默认 60）。
8. **已知取舍（豁免口径已收窄）**：携带 `issueApiToken` 的登录请求豁免**策略类**
   触发条件（`always` / `new_device` / `privileged`），**不豁免**失败锁定触发的
   验证——失败锁定排在豁免之前判定（见第 5 条）。`POST /auth/login` 先签发 cookie
   会话再签发 PAT，因此该豁免仍然放行浏览器会话，这仍是保留的取舍（不误伤原生/
   CLI 客户端）；被收窄掉的只是「连账号失败阈值触发的 `login_verify:acct` 一起
   跳过」这条超出本决策口径的副作用（P2-1，2026-11-16）。OAuth（外部登录）路径
   本轮不接入。

## Consequences

- 运营可以在后台「人机验证 → 登录强化」一处看清三个触发条件与限流；触发条件开启
  但总闸或 provider 未开时，界面显式提示「当前不会生效」。
- `post_risk` 仍是死开关，论坛发帖侧未接入；作为独立议题。
- UA 指纹可伪造，`new_device` 只提升自动化成本，不构成强身份保证；强控制仍依赖
  密码 + 会话 + 失败锁定。
- 无数据库迁移；`user_sessions` 现有索引足以支撑指纹查询。

## Alternatives considered

- **单一模式枚举**（`after_failures|always|new_device|privileged`）：无法表达
  「管理员 + 新设备」这类叠加，且新增组合要改枚举值。
- **严格度档位**（宽松/标准/严格）：对新手友好，但管理员看不清实际触发条件。
- **接入 `risk.evaluate` 扩展提供方**：最灵活，但需要先有可用的风险提供方与
  step-up 编排，本轮范围外。
- **UA + IP 前缀**：更严，移动网络换 IP 会频繁误报。
- **UA 指纹 + Redis 180 天记忆**：跨会话更准，但多一套存储与清理逻辑。
