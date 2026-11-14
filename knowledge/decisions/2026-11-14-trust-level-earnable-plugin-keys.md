# 决策：可挣得权限白名单从「解码期硬拒」改为「运行期过滤 + 写期拒绝」

日期：2026-11-14
状态：已采纳（M4a/M4.2 落地，M5 收口前补记决策记录）

## 背景

M4a 要让插件能把自己声明的权限键交给信任等级授予：`permissionDefinitions[].trustLevelEarnable`
（`app/Support/ExtensionManifest/v3_platform_types.go:224-229`，缺省 `false` 且刻意如此）。
这带来一个原方案没考虑到的时序问题。

**原方案（解码期硬拒）**：`trust.levels` 里出现不在白名单的键 → `DecodeConfig` 直接报
`earnable_not_whitelisted`。当时的白名单是核心七键的硬编码字面量
（`app/Models/TrustLevels/registry.go:29-40`），所以插件键永远过不去；而一旦把插件键接进白名单，
**插件停用那一刻整份配置就解不开了**。

这不是理论风险，链路每一段都能指到行号：

1. 插件停用 → 它声明的键不再「已声明」，按原方案即「不在白名单」。
2. `DecodeConfig` 对白名单键报错并整份返回失败（`app/Models/TrustLevels/config.go:376-390`；
   同一函数用 `DisallowUnknownFields` 严格解码，可见「宁可整份拒绝」本来就是这套解码的取向）。
3. `trustLevelPolicySource.Policy()` 把 `DecodeConfig` 的错误直接上抛
   （`bootstrap/trust_levels_wiring.go:43-45`）。
4. `Resolver.inputs` 把策略错误继续上抛（`app/Models/TrustLevels/resolver.go:181-186`）。
5. `identity.resolveTrustGrants` 落到 `case err != nil` 分支，返回 `level=0, earned=nil, ok=false`
   并记一条 warn（`app/Models/Identity/trust_levels.go:48-60`）。
6. 装载器把 `ok=false` 当「未解析」：已挣得集合为空（权限面等同 **TL0**），资料页
   `publicTrustLevel` 返回 nil，等级卡不渲染（`app/Models/Profile/service.go:757-764`）。

结论：**一次插件停用 = 全站等级面归零，且要等有人修好配置才恢复。** 这条链路就是本决策的全部理由。

## 裁决

拆成三层，各自只挡自己该挡的那一段：

1. **运行期白名单 = 核心七键 ∪ 已启用插件声明键。** `EarnableRegistry.Grantable`
   （`app/Models/TrustLevels/registry.go:269`）判单键，`FilterGranted`
   （同文件 `:297-308`）在授予前批量筛掉非 `valid` 的键。核心七键是字面量而不是按规则筛：
   `user.permission_override` 曾因后缀是下划线而被规则漏掉（同文件 `:27-30` 的注释记着）。
2. **解码期只判结构。** 空键仍然拒（`app/Models/TrustLevels/config.go:834-839`），白名单判断移出解码期——
   `validateEarnableInto` 的注释逐条写明新旧分工（`app/Models/TrustLevels/config.go:841-851`）。
   `CodeEarnableNotWhitelisted` 保留在码表里（`app/Models/TrustLevels/config.go:93`、`:146`）：它是契约稳定项，
   解码期不再产出它。
3. **写期拒绝未声明键。** `ValidateEarnableDeclared`（`app/Models/TrustLevels/registry.go:326`）对「从未被任何扩展声明、
   也不在核心白名单」的键报 `earnable_not_declared`（`app/Models/TrustLevels/config.go:98`）→ HTTP 422。
   停用插件的键**允许保存**（它是 `inactive` 不是 `unknown`）：「先停插件、稍后修配置」是合规运维顺序。
4. **读面标三态。** `valid` / `inactive` / `unknown`（`app/Models/TrustLevels/registry.go:174-185`）。
   收集声明时必须**包含停用扩展**（`bootstrap/badges_fact_catalog.go:179-206` +
   `app/Models/TrustLevels/registry.go:186-192` 的注释）：只传已启用的声明，`inactive` 与 `unknown` 会被抹平成同一个状态。

## 被否方案

**沿用 `orphan` 作为状态名。** 既有文案 `earnable.orphan.title` / `.description` 的含义是
「已存键**不在白名单内**，会被后端拒绝、保存时从档位移除」（`apps/web/i18n/locales/zh-CN.json`
的 `admin.forum.trustLevels.earnable.orphan.*`；消费点
`apps/web/app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableMatrix.vue:219-220`），
也就是说它描述的是 `unknown` 那一态的修法：**取消勾选**。

而 `inactive` 的真实语义是「配置保留、当前不授予、插件恢复后自动生效」，修法是**等插件**。
两个域共用一个词，错位在两个方向都会发生：

- 把 `inactive` 叫 `orphan` → 运营读到「会被后端拒绝、保存时从档位移除」，于是去删配置，
  把本可自愈的配置删掉；
- 把 `unknown` 叫 `orphan`（代码注释 `app/Models/TrustLevels/registry.go:174-175` 记的方向）→
  `unknown` 的提示会被写成「等插件启用」，而它的正确修法是取消勾选。

所以三个状态名各锁死一种修法：`valid` 无需动作、`inactive` 等插件或换键、`unknown` 取消勾选。
这条与 D22 的用词裁决一致，代码注释也把理由留在原地（`app/Models/TrustLevels/registry.go:174-177`）。

## 降回条件

满足任一条即应**退回解码期硬拒**（届时 `validateEarnableInto` 恢复 `IsWhitelisted` 检查，
`CodeEarnableNotDeclared` 与三态读面同批撤掉）：

- 出现「配置里的未声明键一定来自篡改或数据损坏」的证据（例如 `trust.levels` 出现非本系统写路径
  写入的痕迹、或审计里出现越权写入告警）。这条成立即说明本决策的前提「配置只由自家写路径写入」
  不成立，宽进严出就不再安全。
- 出现「插件停用后该键永远回不来、却长期占着勾选」的运营实例：此时应把 `inactive` 再细分
  （例如「声明来源已消失」与「声明来源暂时停用」两态），而不是回到硬拒。
- 退回必须同批改四处，缺一处会留下两套真值源：码表（`CodeEarnableNotDeclared` 去留）、
  `validateEarnableInto`、前端矩阵三态文案、以及「一次停用 → 全站未解析」那条回归测试。

## 后果与已知取舍

- **保存面比解码面宽**：运营能存下「当前不授予」的配置。这是刻意留的运维余地
  （停用插件、改配置、再启用插件，中间不需要清空配置）。
- **`inactive` 与 `unknown` 的区分依赖目录快照包含停用扩展**：漏传即抹平。
  已有测试钉住（`bootstrap/trust_levels_fact_catalog_test.go` 的
  `TestFactCatalogSkipsDisabledForCandidatesButKeepsEarnables`）。
- **`app/Models/TrustLevels/config.go:845` 的注释措辞待更正**：那里写「全站等级掉回默认策略」，实测口径是
  「整站按未解析处理（等同 TL0）+ 资料页不渲染等级卡 + 每次求值一条 warn」，不涉及
  `DefaultPolicy()` 替换（`DefaultPolicy()` 只在配置为空串时兜底，`app/Models/TrustLevels/config.go:376-379`）。
  本记录的措辞为准，该注释与其所属注释块同批修正。
