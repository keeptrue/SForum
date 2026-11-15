# 决策：dev 零命令自动激活（watcher 入 host，无登录无授权交互）

日期：2026-10-17。取代本文件链上此前「凭据缓存/一次性登录」的折中方向。

## 操作者拍板（原话级语义）

运行 `./scripts/dev-plugin-loop.sh <pkg>`（以及任何 agent 会话）不经过任何验证：
无登录、无密码、无确认，保存即生效；**包括首次安装**——即使数据库里 0 个管理员
这条链也要工作。第二轮升级为 **B 方案：彻底 0 命令**——watcher 从 CLI 挪进 host
dev 装配，host 启动即自动生效，连 wrapper 本身也删除。

## 实现

- `app/Models/Extensions/dev_auto_runtime.go`：`DevAutoRuntime`——重扫外部源 →
  对每个包：精确保制品 grant → installed 自动启用 / enabled 且有 staged 自动
  upgrade（disabled 尊重操作者状态）。失败只记日志，不影响其余包。
- `app/Support/ExtensionPackage/dev_build.go`：host 进程内构建管线
  （go build backend/plugin + digest 写回含 SRI 重算），与 CLI 共用。
- `bootstrap/dev_plugin_watch.go`：dev 装配 fsnotify watcher（去抖 600ms，
  排除 dist/node_modules/.git 等产物目录），启动即对账一次。
- 合成身份：`dev-auto` 系统账号（无密码凭据不可登录；lifecycle V2 authority
  与 audit 需要合法 UserID，故不用 ID=0）。管理端可见，禁用/删除即关闭通道。
- CLI 侧：wrapper 删除；`dev_trust.go` 的登录提示/凭据缓存/PAT 层全部移除，
  回落 env-only（dev-trust CLI 变成遗留辅助路径）。

## 拒绝过的方向（留档）

- 保留门 + 凭据缓存/浏览器导入：被操作者否掉（「避免人工去授权」≠ 把登录做得
  更顺手）。
- dev 无认证 admin 路由：会撕开第二条激活路径；本轮实际未采用任何新 HTTP 面——
  自动化全在 host 进程内（watcher→runtime→lifecycle 内部调用），路由/守卫零新增。

## 边界（如实）

- dev 机器上「能写 dev 来源根的进程」等效宿主全权执行体（含任意包安装、路由
  注册、own_schema DB grant）。这是操作者明确接受的语义。
- 生产不受影响：dev watcher/DevAutoRuntime 仅 development 非 Safe Mode 装配；
  HTTP 激活路由的委托管理门（`extensions.plugin.manage` / `extensions.manage`）
  原样，可执行信任面与前端导入面的 `super_admin` 门也在原处。**更正（2026-11-15）**：
  路由门自 `b4315ef72`（细粒度权限）起就是委托管理权限，不是 `super_admin`；
  原文「所有 HTTP 激活路由的 super_admin 门原样」把权限边界写宽了。
- CLI `extension dev --auto-trust/--auto-upgrade` 成为遗留路径（无凭据时提示
  指向 host 自动化）。

## 附加（同日）：前端制品的两处补齐

1. **dev 构建管线补前端**：`BuildFrontend`——`frontend/admin/package.json` 存在时
   执行 `bun run build`（跳过依赖安装）。此前 dev 管线只构建 Go 后端，改前端源码
   不会进 dist 产物，只能手工构建。
2. **组件契约版本自动晋升**（⚠️ 已被下方「更正（2026-10-18）」推翻并移除）：
   组件注册表拒绝「同 contractVersion 下声明/内容变化」
   （要求每次改 L2 前端手动 bump）。dev 通道在传给 upgrade 的**内存副本**上把
   `components[].contractVersion` 改写为 `dev-<stagedDigest 前 8 位>`：制品文件与
   真实版本号不变，registry 把每次摘要变化视为新契约发布。`UpgradeInput.Extension`
   新增非序列化内存载体（HTTP 恒 nil），仅在 ID/version/staged digest 与 store
   staged 精确一致时采信。
3. **公开资产注册表重发布**（⚠️ 已被下方「更正二（2026-10-18）」推翻并移除）：
   Host 只在启动时 `RestoreRoutePublications`；自动
   upgrade 后必须显式 `RepublishPublicAssetsForDev`（与启动恢复同一确定性构建、
   完整图替换），否则浏览器按启动快照旧 digest 请求资产（404/旧内容）——
   这就是「前端要走生命周期切换制品才生效」的完整根因。
4. **升级健壮性**：V2 upgrade 要求 stable Idempotency-Key——dev 通道用
   `dev-auto/<id>/<stagedDigest>` 确定性键；上一次 failed upgrade 留下的 fence
   （requires-explicit-retry）由 dev 通道自动执行一轮 decision=retry recovery 后
   重试。promote 失败写 `extension_events` 台账（air 场景日志可能进交互终端）。
   非 V2 包（无 lifecycle 声明）如实报错指向管理端 restart bridge。

## 更正（2026-10-18）：放弃「组件契约版本自动晋升」

本轮发现 L2 组件改前端内容（CSS/JS）时，注册表规则「同 contractVersion 不允许
声明变化」要求手动提版。此前两轮尝试过两条自动化的路线，均不可行：

1. **宿主侧内存改写视图**（grant/authority 各层拦截，四次连环冲突后被判不可行）；
2. **作者侧自动 bump 写入源 manifest**（构建时改写 contractVersion）——
   技术上可行，但**语义上不对**：契约版本是「接口变了，宿主需重新审批准入」的
   作者承诺，自动提版等于让系统替作者签这份承诺，掩盖真正的契约漂移信号。

**最终结论：不做自动晋升**。规则保留；你改 CSS/JS 内容时**不触发**该规则
（组件声明未变），正常自动升级生效；只有改接口声明（props/schema/引用换组件）
才需手动提版——这是正确的人工介入点，不应自动化。手册已在
`docs/zh-CN|en-US/extensions/build-and-load.md` 写明触发条件。

## 更正二（2026-10-18）：移除 RepublishPublicAssetsForDev（round 6 结论被推翻）

round 6 曾判断「Host 只在启动时重建公开资产注册表，升级后必须显式重发布」，并加了
`FrontendService.RepublishPublicAssetsForDev`（无 fence 的 `ReplaceAll`）。**这个结论
是错的**，实测推翻：

- `bindAssetRegistryConsumers` 把 lifecycle 的 `AssetRegistry` 同时绑定给
  `frontendService.publicAssets`（`WithPublicAssetRegistry`）——**二者是同一个注册表
  实例**。
- lifecycle 升级本身就通过 `ReplaceAllIfRevision`（fenced）发布公开资产到该注册表
  （错误日志 "apply asset registry graph" 即此步骤）。
- 我的 `ReplaceAll`（无 fence）是**第二个写者**：它移动 revision，使下一次升级的
  fenced 写入读到陈旧 revision → "asset registry revision changed during
  replacement" + 补偿失败。round 6 观察到的「资产缺失」其实是当时升级因
  contractVersion 规则**失败**（根本没发布资产），republish 强行发布**掩盖了失败**。

contractVersion 摩擦移除后（CSS 编辑不再触发组件规则），升级正常成功并自己发布资产，
republish 成了纯竞态源。**已整体移除**（接口/字段/方法/bootstrap 参数/测试）。
实测：CSS-only 编辑 touch → 13 秒自动 staged→upgraded，浏览器经 runtime 资产端点
拿到含最新标记的内容（digest=active 12528）。
