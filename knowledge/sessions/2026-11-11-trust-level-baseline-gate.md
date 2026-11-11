# 2026-11-11 Session Handoff — 信任等级基线门（D34）

Status: **已提交 `185be5701`（代码）+ `69f455a42`（决策/计划/矩阵）+ `429945c22`（运营手册）**。
整仓门禁 `./scripts/test.sh --fast` 三线 exit 0（api 283s / web 40s / static 15s，2026-11-11T12:08:53）。
验收判定由 reviewer 独立给出（`storage/qa/trust-levels-z3/VERDICT.md`，210 行，**本地产物、重跑即得**）。
**计数不抄进本文件**（会变），编号源见 `../reports/2026-11-11-trust-levels-baseline-narrowing-matrix.md`。

## Changed

- **减法门（Z3-b″）**：`member` 角色行与迁移**一行未动**，装载期只对「已解析 + 等级 < TL1 +
  非 bot + 无显式 allow」的有效权限扣 `topic.create` / `attachment.upload`。扣减与已挣得合并
  收进唯一实现 `Identity/trust_level_grants.go`；池路径与事务路径各一个三行适配器，
  旧的 `mergeEarnedPermissions` 循环删除。键表与等级下界在 `TrustLevels/baseline_grants.go`。
- **前置缺陷修复**：`LoadEffectiveActorTx` 从未给 `actor.CreatedAt` 赋值（SELECT 只取
  status/kind）⇒ 账龄闸必过 ⇒ 插件命令路径永远不低于 TL1。补读 `created_at` 后两条 loader
  对同一用户给出相同权限键集合。
- **读面**：`GET /admin/trust-levels` 新增 `baselinePermissions`（`nonNilStrings`，永远 `[]`）。
- **后台矩阵**：基线键不再渲染勾选框，改为同尺寸静态门控标记（`role=img` + title/aria，
  无 tabindex）；可操作勾选框 5 键 × TL1–TL4，全表 7 键 × 5 档；**存值原样回写**。

## Decisions

**D34-a（YG 裁决）**：默认安装下 TL0 不能开新主题、不能传附件；能回帖、能改删自己的内容；
TL1 起两者都有。**D34-b（机制）**：不采用「收窄角色行 + 由 TL1 授予」（加法版），改减法门。
三条理由：失败方向 fail-open（少改一条 loader 是 TL0 多一个键，而不是会员发不了帖）／
零迁移／契约与 F 组不动。偏差单列在 `../decisions/2026-11-11-trust-level-baseline-narrowing.md`。

## 本刀换来的四条硬纪律

1. **「红在别处」与「我没弄红它」是两个断言。** 后者必须给 `R_now == R_head` 的集合差分
   （干净副本 vs 工作树，逐条含测试名与失败文案）。本刀首跑 `FAIL api` 31 条，就是这个流程定性的。
2. **哨兵文案指向 A，真实原因常在 B。** `identity: session policy store is unavailable`
   实际是「夹具的 `users` 表缺 `created_at` 列」。看到哨兵先 `rg` 它的**全部**返回点。
3. **手写夹具 schema 是第五类「两份」。** `CREATE TABLE users` 在 `_test.go` 里有 34 文件 /
   35 个 DDL 块；生产查询加一列会静默拖红一批无关测试。判据：按**生产调用方**定范围
   （本刀只有 `Models/Identity` 与 `Support/HostAPI`），不是按「红过的包」。
   正向守卫比清单耐久：直接跑一次 loader 断言 `CreatedAt` 非零。
4. **并发窗口里的红绿分布不可当因果证据，但也不能因此不做 A/B。** 串行化 + 干净副本才是判据；
   上一刀的纪律 4 又踩了一次 ⇒ 要落成流程：**谁跑隔离库先在群里说一声**。

## Next

1. **`LoadEffectiveActorTx` 不展开父→子继承**：插件命令路径**今天就在误拒** `extension.manage`
   持有者（不是将来时）。与本刀刚修的双 loader 一致性同一面，趁热改最省。
2. **`/badges` 公共目录页**：Page Registry 全套（catalog / route shell / ViewModel / Host island /
   主题模板 / 完整性测试），独立一刀。
3. **注册响应绕过减法门（M-02）**：修它要把 trust source 接进 `LoadCurrentUserAccess`（L5），
   正是本刀在防的「再加一条装载路径」。矩阵里那条断言写的是「两处键集合**不相等**」，
   修好会自动翻成「相等」——修不修都看得见。
4. **`sqlc generate` 不可安全运行**（`models.go` 84 → 约 2481 行并重命名 param）。
5. **`SFComment.vue` 537 行拆分**；`trust.new_user.forbid_attachments` 死配置（接线还是删键）。

## Open Questions

- **互动四刀（点赞 / 收藏 / 徽章+统计 / 信任等级）已全部 landed**，父计划
  `plans/2026-07-12-iteration-a-engagement-loop.md` 是 completed。下一个产品纵列是什么，
  需要拍板——现有 `plans/README.md` 里还有一批 `active`（AI 回复平台、插件平台 v3、
  公开导航、图片贴纸、GitHub 登录复核），但都不属「论坛产品面」这条线。
- 限额的默认值仍是「TL1–TL4 全部继承站点默认」（D27），所以默认安装下 TL1 与 TL4 的差别
  只有等级展示。要不要给默认分档限额（会让升级改变存量站点行为），未决。
