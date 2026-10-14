# 2026-10-15 AI 自主参与（autonomous triage）Handoff

## Changed

- AI 回复触发方式可配置：`settings.Reply.TriggerMode` ∈ `mention`（默认）/
  `autonomous` / `off`。未知/缺省/读取失败一律归一 mention（fail-safe）。
- Autonomous 模式：未被 @ 的新主题/评论入队 `ai.reply_triage`（River unique
  key = topic × 60s debounce 桶），worker 内 `aireply.TriageDecider` 用
  `forum.reply.triage` 用途（economy、无工具、8 tokens）判 REPLY/SKIP；
  REPLY 才入队正式生成（`ai.reply_generate`），SKIP 落日志。判定失败 =
  SKIP（fail-closed，网关故障不放大成刷屏）。被 @ / 被回复仍直达生成。
- 判定器输出解析高门槛：仅包含 REPLY（大小写不敏感）才回，其余全按 SKIP。
- `supportai`：新增 `CorePurposeForumReplyTriage`、`DefaultTriageSystemPrompt`、
  `CoreTriagePromptVersion`、`TriggerMode*` 常量与 `NormalizedTriggerMode`。
- 触发器（`aireply.Trigger`）经 `WithTriggerModes` 注入 `ai.Service` 读法；
  worker 侧 `registerAIReplyWorker` 同时注册生成与判定 worker（判定组件缺失
  时只注册生成，autonomous 退回 mention 行为）。
- 控制台：AI 设置「回复提示词」页签顶部新增「触发方式」三选块
  （`SFAdminAITriggerModeBlock`），autonomous 附成本提示；i18n 双语齐备；
  dirty 比对把 mention/缺省/未知归一后再比（`normalizedAISettingsForComparison`）。
- OpenAPI：`AIReplySettings.triggerMode`（enum mention/autonomous/off）。

## Decisions

- 判定与生成拆成两个用途、两次队列任务：失败语义相反（生成失败要重试/报错，
  判定失败要沉默）；判定永不允许工具，「回什么」的能力面不因自主模式扩大。
- 同主题 60 秒 debounce 桶进 River 唯一键（`TopicDebounce` +
  `river:"unique"`）：热帖连发合并判定，窗口内生效中的判定会读到最新内容。

## Verification

- `go test ./...` 126 包全过；`bun test` 1133 全过；typecheck / OpenAPI refs /
  architecture boundaries 全过。新增：triage 解析与用途边界测试、触发模式矩阵
  （mention/autonomous/off/缺失/故障/未知值）、worker REPLY→生成入队、
  debounce 桶、web dirty 比对归一。

## Next

- 管理台浏览器 QA：`/control-panel/ai` 桌面 + 390x844（本次未做视觉验证）。
- 可选后续：按版块排除自主参与；判定日志在管理台可见（现只在 worker 日志）。

## Open Questions

- autonomous 判定的 economy 调用是否需要单独的每分钟上限（目前吃全局
  rate limit 与触发者每日配额）。
