# Decision: 审计 §7 的 9 条噪声不占缺陷档期，各自写死复位条件

## Status

Accepted（2026-11-15，审计总表 §7 噪声剥离 / §9 F-D2 核对）

## Context

审计总表把同一批发现记了两本账：一本是 113 条的 P0–P3 分母，一本是 §7「这些不该占修复档期」。
两本账并行之后，**分母继续把已经定性为「不修」的条目当待修算**，剩余数虚高，每轮排期都要把
同一批条目重新争论一遍。本记录把两本账合成一本：这 9 条正式摘出待修池，并逐条写死判据与
复位条件，让「为什么不算」和「什么时候再算」都能被第三人复核，而不是靠在场者的记忆。

**判据标准**。一条要占缺陷档期，必须同时成立三条：

1. **有可复现的受害面**：存在某个主体，在不具备既有特权的前提下会因为这段代码而变差
   —— 数据错、权限错、内容错、服务停。
2. **实现与声明的意图不一致**：写错的代码、漏掉的守卫、骗人的注释、半截的迁移。
3. **没有任何既有层兜住它**：失败会直接变成错误结果，而不是被下一层挡住。

任何一条不成立，它就不属于「待修缺陷」，无论它读起来多像。9 条落进四类：

| 类别 | 条目 | 共同点 |
|---|---|---|
| 产品决策 | RV-51 | 豁免是写下来的决定，不是漏写的检查 |
| 已提权前提下的纵深防御 | INC-01、INC-02 | 需要先具备 DB 写权限才能触发，不提供越权阶梯 |
| 显式取舍 + 已分层缓解 | INC-03 | 风险已被更敏感的那一层 fail closed 兜住 |
| 无运行时路径 | RV-47、RV-48、RV-40、RV-44、OPS2-11 | 行为正确 / 产品缺口 / 只影响 dev 与打包 |

**引用纪律**。本记录的仓库引用一律写成「相对路径 + 符号名」，不写行号。这个规矩是上一轮吃过
亏才立的：RV-49 本身就是行号漂移，再拿行号当锚点只会养出下一条 RV-49。

**范围**。只这 9 条。§7 里其余行（文档行号漂移组、契约完整性组）的账不在本记录里动，那两组
各自有归属：契约组已有常设门，文档漂移组仍是独立待办。

## Decision

以下 9 条摘出待修池，F-D2 另计为「已关闭」（见下一节）。分母 **71 → 61**。

### RV-51 `issueAPIToken` 的登录路径豁免人机验证

**可复核锚点**：`apps/api/app/Http/Controllers/Identity/auth_handlers.go` 里 `Login` 组装
`identity.LoginInput` 时写 `SkipHumanVerification: req.IssueAPIToken != nil`；
`apps/api/app/Models/Identity/login_verification.go` 的 `loginRequiresVerification` 第一句就是
`if input.SkipHumanVerification { return false, nil }`，短路过「总是 / 新设备 / 特权账号」三条
策略与失败计数升级。决策记录 `knowledge/decisions/2026-10-22-login-human-verification-triggers.md`
的 `Status` 为 `Accepted`，源码注释同样自认「这是已记录的取舍」，并主动写明该请求仍会签发
浏览器会话、等同保留一处可绕过的验证豁免面。

**判据（产品决策）**。三个条件全不成立：豁免面是**声明式**的 —— 不是攻击者让服务器误信
「已验证」，而是服务器按调用方给的字段自愿不查，所以不存在「实现与意图不一致」；阻断型锁定
仍然在验证之前，`Service.Login` 第一步就是 `isLoginLocked` → `ErrLoginLocked`，被锁账号照旧
登不进，所以最坏效果被限定在「已泄露密码的验证成本降为零」而不是「任意接管」；这条取舍同时
写在代码注释和一份 `Accepted` 决策记录里，是可复核的书面决定。缺陷池排的是「实现辜负了意图」，
这里意图本身就是豁免。

**复位条件**（任一成立即回池）：

1. 该登录路径不再同时签发浏览器会话 —— 那时它变成纯 token 铸造面，代价结构也就变了。
2. PAT 的默认寿命或默认 scope 被放宽到「一次成功即长期高权」，使「多给一枚 PAT」从边际收益
   变成主要收益。
3. 失败锁定被移除或阈值被放宽，人机验证成为唯一剩余刹车。
4. 产品决定收回豁免 —— 那时它是功能改动工单，不是缺陷条目，仍不该留在这里。

### INC-01 PAT `scopes` 解析失败被吞，令牌收窄被跳过

**可复核锚点**：`apps/api/app/Models/APITokens/postgres_store.go` 三处
`_ = json.Unmarshal(scopesRaw, &rec.Scopes)`：`Create` 的 `RETURNING` 分支、`scanRecord`
（`GetByPublicID` / `GetByIDForUser` 共用）与 `scanRecords`（`ListByUser` 用）；`rec.Scopes`
为 `nil` 时归一成空切片。`apps/api/app/Http/auth_user.go` 与
`apps/api/app/Http/Controllers/Identity/session_handlers.go` 都以 `len(scopes) > 0` 作为是否
调用 `RestrictActor` 的条件。写入侧只有 `Service.Create` → `normalizeScopes`（保证非空且逐项
`actor.Can`）→ `json.Marshal`；本轮复核全仓 `UPDATE api_tokens` 只有 `revoked_at` 与
`last_used_at` 两处，没有 `SET scopes`。

**判据（已提权前提下的纵深防御）**。触发链要求「有效 PAT + 能直接写 `api_tokens.scopes`」。
第二个前提是一个**已经提权**的位置：能写这张表的主体本来就能改 `user_roles` 或 `token_hash`，
所以这条不提供任何新的越权阶梯 —— 它降低的是「已经进来的敌人被抓住的概率」，不是
「能不能进来」的概率。受害面条件不成立，因此不进缺陷池；它是 fail-open 的纵深防御项。

**复位条件**（任一成立即回池）：

1. 出现任何新的 `scopes` 写入面：后台编辑、令牌导入、插件 `raw_core` 授权、迁移脚本给列加
   默认值或放松 `NOT NULL`。
2. 某条路由只靠「`len(scopes) == 0` 即视为无限制」这一条语义做授权，且不在 `RestrictActor`
   的覆盖范围内 —— 那时前提 2 被绕开，它立刻变成远程可达。
3. `LoadActor` 出现新调用方用于 PAT 却跳过收窄，或 `ScopesFromContext` 的消费点增加而无人守
   空切片。
4. 列定义失去数组形状约束（`jsonb_typeof` / 长度检查），使坏值能在 DB 层静默落入。

### INC-02 `ScopedActorStore` 只有定义、零构造点

**可复核锚点**：`apps/api/app/Models/APITokens/scoped_store.go` 的 `ScopedActorStore`；本轮
`rg` 复核全仓仅该定义文件命中，`bootstrap` 的 `ActorStore` 装配全部直连 `LoadActor`。它与
`auth_user.go` 里那段手写判断是同一判据的两份拷贝，其中一份没接线。

**判据（未装配的代码不改变任何结果）**。装配与否不改变一条权限判定，因此它当前产生零个结果；
它的价值是结构提示（「收窄本该只有一个点」），属于契约债而不是缺陷。注意这与 RV-50 的
`CanReplay` 零调用方不同：那一条是**有断言需求却没人调用**，这一条是**没人调用也没有断言
需求**。

**复位条件**（任一成立即回池）：

1. 它获得构造点（进 `bootstrap` 装配）—— 那一刻它从契约债变成活的授权路径，必须同时 fail
   closed，否则就是 INC-01 的结构性版本。
2. `auth_user.go` / `session_handlers.go` 的手写判断被删掉而 store 仍未装配，收窄点就真空了。
3. 出现「PAT 收窄已集中在 `ScopedActorStore`」这类判断 —— 误解本身要回池纠正，否则它会成为
   下一次重构的错误前提。

### INC-03 十余处审计写入 best-effort，失败无 warn / metric

**可复核锚点**：`apps/api/app/Support/Audit/audit.go` 的 `Append` 返回 error，而同文件
`IDWriter` 的注释明确「既有 best-effort 调用方可以继续只依赖 `Writer`」；审计口径记 15 处
`_ = …Append(`（代表位置 `apps/api/app/Http/bearer.go`、
`apps/api/app/Models/APITokens/service.go`、`apps/api/app/Models/Extensions/trust_service.go`、
`apps/api/app/Models/Options/service.go`、`apps/api/app/Models/Extensions/activation.go`），本轮
按单行形态复核到 14 行 / 12 文件，与 15 的差在跨行写法，量级一致，不逐处重列。最敏感的那条链
在 `apps/api/app/Models/Extensions/activation.go`：审计写入失败时只返回原 ctx，租约层因此退回
host 分支，精确授权缺失则 fail closed。

**判据（显式取舍 + 已分层缓解）**。条件是「没有既有层兜住它」，而这里兜住了：需要审计锚点才
能成立的授权链已经 fail closed，其余 best-effort 写入失败不改变任何授权结果。剩下的只是可
观测性缺口（失败没有 warn 或 metric），运维看不见丢行 —— 那是运维体验问题，不是正确性问题。
仓库还把「是否把 best-effort 升级成阻断式」明确标为产品决策。

**复位条件**（任一成立即回池）：

1. 新的授权或信任判定开始把「有没有审计行」当作前置条件，却不检查写入是否成功。
2. `activation.go` 的 fail-closed 分支被移除、被降级成 warn，或被包进一个吞错的 wrapper。
3. 出现外部合规 / 留存要求，需要证明审计完整（那时缺口从体验变成合同义务）。
4. 生产上确实观测到丢行 —— 先补 metric 定位，再判断是否升级为阻断式。

### RV-47 `POST /admin/badges/rule-text` 挂读权限 `badge.view`

**可复核锚点**：`apps/api/app/Http/Controllers/Badges/controller.go` 注册
`admin.Post("/rule-text", h.adminBadgeRuleText)`，handler 在
`apps/api/app/Http/Controllers/Badges/rule_controller.go`，注释写明「只读，无副作用」，并专门
禁止按 method 一刀切改成 `badge.manage`；`apps/api/app/Support/Routes/core_catalog_gen.go` 的
`core.route.badges.admin_badge_rule_text` 声明 `POST /api/v1/admin/badges/rule-text` +
`badge.view`。handler 只做「解码 → 调域层 → 编码」，不写库、不审计、不入队。

**判据（行为正确，且当前权限就是对的）**。POST 在这里只是承载草稿规则的请求体，语义仍是读；
同一批管理员本来就能从 `GET /admin/badges` 读到同样的 `rule` 原文，预览没有扩大任何信息面。
把读权限叫「读权限」是正确的：硬套「POST 必须 manage」只会把只读管理员挡在自己有权读的内容
之外。所以这不是「将错就错的命名」，而是「按方法一刀切才会错」。

**复位条件**（任一成立即回池）：

1. 该端点开始产生副作用（写草稿、写审计、入队）—— 权限必须同步升到 `badge.manage`。
2. `badge.view` 被授予了读不到 `GET /admin/badges` 的角色，此时预览成为绕过读面门的信息通道。
3. 预览开始返回比 `rule` 原文更多的内容（引用对象的实测值、其他用户的数据、未脱敏事实）。

### RV-48 无自助改邮链路

**可复核锚点**：`apps/api/app/Http/Controllers/Identity/routes.go` 只有邮箱**验证**流程
（`/email-verification/request`、`/email-verification/confirm`）与管理端
`PUT /users/:userID/email-verification`，没有任何自助修改邮箱地址的端点。

**判据（产品缺口，不是缺陷）**。第二条判据不成立：这里既没有谎报也没有半成品，功能就是没写。
改邮箱不是一个字段的 UPDATE，而是一整条链 —— 改地址、给新地址发验证、通知旧地址、撤销会话与
PAT；没人写就是没写。把它留在缺陷池的实际代价是让「未实现的功能」和「实现错的代码」抢同一份
档期，这正是分母虚高的主要来源之一。

**复位条件**（任一成立即回池，但改走不同通道）：

1. 产品决定提供自助改邮 —— 转为功能需求，并带上「旧地址通知 + 会话撤销 + PAT 撤销 +
   验证前不生效」的安全清单；它不该再留在这份记录里。
2. 现状产生安全事故面：用户邮箱失窃却没有任何轮换路径，或管理端能改邮箱但绕过验证与审计。
   那时回池的是那条具体路径（一个真缺陷），不是「缺自助入口」本身。

### RV-40 `Dockerfile` dev 阶段 `go install …@latest`

**可复核锚点**：`apps/api/Dockerfile` 的 `AS dev` 阶段执行
`go install github.com/air-verse/air@latest`；生产目标是 `api` / `worker` / `migrate`，
生产向 compose（`compose.yaml`、`compose.prod.yaml`）取的是运行阶段，全仓只有
`compose.dev.yaml` 取 `target: dev`。

**判据（dev 路径不进生产）**。`@latest` 的真实问题是不可复现（同一份源码在不同日期构建出不同
工具），不是「可被利用」。它出现在一个只在本机隔离环境运行的构建阶段，既不进发布镜像，也不进
任何一次生产 `docker build`。受害面条件不成立。

**复位条件**（任一成立即回池）：

1. 任何非隔离环境使用 `target: dev`：共享 CI runner 的构建缓存、发布流水线、他人可访问的
   dev 主机。
2. dev 镜像被发布成 tag 或制品。
3. 供应链门禁把「所有 `go install` 必须钉版」立为硬规则 —— 那时它是规则违反，按规则处理，
   而不是按缺陷排期。

### RV-44 内置插件把 `httptest` 测试替身编进发布二进制

**可复核锚点**：`extensions/builtin/plugins/sforum-auth-github/backend/fake_github.go` 是
`package main` 的**非测试文件**（无 build tag），定义了 `FakeGitHub` 与 `NewFakeGitHub`；
`apps/api/Dockerfile` 的 base 阶段对该目录执行 `go build`，发布镜像再把整个
`extensions/builtin` 复制进 `api` / `worker` 阶段 —— 所以 `net/http/httptest` 确实进了发布的
插件二进制。本轮复核非测试代码对 `NewFakeGitHub` / `FakeGitHub` 的引用为 0，只有插件 README
提到它。

**判据（可达性为零的打包整洁性问题）**。`httptest.Server` 只有被构造才起监听，而没有任何非
测试调用方，所以它是死代码；死代码不产生行为。今天它影响的是二进制的字节数，以及「制品里带着
一份假 OAuth 提供方实现」这件事本身的心理成本。

**复位条件**（任一成立即回池）：

1. 任何非测试代码出现 `NewFakeGitHub(` —— 尤其当它挂到 debug 开关或环境变量上时，立刻升级为
   真缺陷：一个可被启动的假 OAuth 端点。
2. 出现「制品不得包含测试替身」的构建或发布门禁。
3. 该插件后端不再随发布镜像分发（那时问题自然消失，条目也就不需要了）。

### OPS2-11 `.dockerignore` 未排除本地脚手架与大目录

**可复核锚点**：`.dockerignore` 未列 `knowledge/`、`docs/`、`tests/`、`scripts/`、`deploy/`、
`contracts/`、根级 `tmp/`，也未列 `extensions/dev/`（该目录被 `.gitignore` 忽略，但忽略规则
不影响构建上下文）；所有 compose 的 `build.context` 都是仓库根。两个 Dockerfile 的复制都是
白名单式（`apps/api/`、`apps/web/`、`extensions/builtin`），上述目录没有任何一条进入镜像；
`.env` 与 `.env.*` 已被排除。

**判据（只影响构建上下文体积，不进运行路径）**。既然没有一条 `COPY` 选中它们，运行时就拿不到
它们；受害面条件不成立。真实代价是每次构建要把这些目录（含未跟踪的本地构建产物，例如插件
目录下的 `plugin` 二进制）打包发送给 builder —— 慢、占盘，仅此而已。

**复位条件**（任一成立即回池）：

1. 任一 Dockerfile 改成整体 `COPY . .`，或显式复制上述目录。
2. 引入远程 / 托管 builder，构建上下文离开本机 —— 那时「多带什么进上下文」变成数据外流问题。
3. 上述目录里开始出现密钥、凭据或用户数据。

## F-D2：已关闭，不计入噪声

F-D2 与上面 9 条不是一类。那 9 条是「定性为不修」，F-D2 是「问题已经真的消失了」，所以它从
待修池里减掉的理由是**关闭**，不是**接受**。

**关闭依据**：审计总表 §9 用解析结果核对 —— `apps/web/bun.lock` 里
`vue-i18n@11.4.20`、`@intlify/core-base@11.4.20`，都在 GHSA-83rm-6c8r-g728 的影响区间
（`>=11.1.10 <11.4.14`）之外。本轮复核同一处：`bun.lock` 两个包都钉在 `11.4.20`。

**一个值得写下来的细节**：F-D2 当初的可执行项是「在 `apps/web/package.json` 的 `overrides` 里
加 `"vue-i18n": "^11.4.14"`」。实际闭环不是靠 override，而是靠把 `@intlify` 系整体抬到
11.4.20。因此 `overrides` 里现在没有 `vue-i18n` 是**正确的**，不要补 —— 补一条只会把已经满足
的约束再用 override 说一遍，并在上游继续升级时变成不必要的钉死。校验「关闭」要看解析结果，
不要看 override 文本。

**剩余 5 条 critical 全在 dev 链**：`@nuxt/devtools`、`seroval`、`shell-quote`、`simple-git`、
`@simple-git/argv-parser`，全部经 `nuxt → @nuxt/devtools` / `vite-builder` 传递；
`apps/web/nuxt.config.ts` 的 `devtoolsEnabled` 只在 `NUXT_DEVTOOLS === 'true'` 时为真，默认关。
它们的处理面是 OPS2-09（把 `bun audit` 进 CI 并在扫描时指定 registry），不是 F-D2。所以
「F-D2 关闭」不等于这 5 条消失，而是「F-D2 作为一条独立待修条目不再成立」。

## 分母影响

四个数必须自洽，否则这份记录只是另一种口径漂移：

| 项 | 变化 | 结果 |
|---|---|---|
| 本轮起点（总表 §11 第四轮） | 113 − 42 已闭 | **71**（P1 0 / P2 35 / P3 36） |
| 减 9 条噪声 | RV-51 属 P2，其余 8 条属 P3 | −9 |
| 减 F-D2（§9 已判关闭） | 属 P3 | −1 |
| 本轮终点 | 71 − 10 | **61**（P1 0 / P2 34 / P3 27） |

反向校验：`113 = 42 已修 + 10 本轮摘出（9 噪声 + F-D2）+ 61 待修`。三块相加回到 113，说明
没有条目被重复计入或被悄悄丢掉。

**口径说明**：61 里不含这 10 条，但也不代表它们「永不再做」。这份记录的判据是「今天是否需要
占用修复档期」，不是「是否值得做」。

## Consequences

- **这 9 条各自留了回家路**。复位条件不是免责声明：任何一条被触发时，它带着当时的证据回到
  待修池，按正常优先级重新评估，不需要重新辩论定性。
- **整洁性改动仍然欢迎，只是不进分母**。RV-40、RV-44、OPS2-11 都是半小时级的顺手工程，谁
  在改相邻代码时顺手做掉都可以，但不再拿「剩余 71」给它要档期。
- **分母从此只有一个来源**。以后再加「不该占档期」的条目，走同一份记录的同一张表：判据、
  复位条件、分母算术，三样缺一不算。
- **本记录不改任何既有决定**。RV-51 的依据来自
  `knowledge/decisions/2026-10-22-login-human-verification-triggers.md`，INC-03 的取舍依据来自
  `knowledge/decisions/2026-11-12-badge-plugin-facts.md`；本记录只做记账。若那些记录将来被
  `Replaced` 或降级，对应条目自动回池 —— 这是这份记录额外的一条通用复位条件。
- **行号不是锚点**。本记录全部用「相对路径 + 符号名」定位。若某天某个符号被改名，跟着改符号
  名即可；不会出现「引用还在、指向的却是另一段代码」这种静默腐烂。
