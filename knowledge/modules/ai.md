# AI Module

Core owns a provider-neutral AI gateway (配置、成本闸门、用量、trace、诊断) and a
admin console at `/control-panel/ai`. Provider/vendor behavior and model catalogs
live in configuration, not in Core code.

## Gateway

- `Support/AI` holds the contracts: `Settings`（策略层全部可配 + 受限层有硬天花板），
  `Profile`，`GateSettings`，`Gateway`，`CredentialWriter`，`DiagnoseResult`。
- 边界层（记账、trace、密钥处理、排序规则、契约版本）不可配置。
- Settings 是单例文档（`ai_settings`），`revision` 由列权威，文档内始终写 0。
- 成本等级 economy/standard/premium 只声明用途，用哪个模型由
  `costClassProfiles` 绑定决定。控制台是单选语义：选中的供应商被写入三档绑定。

## Outbound policy

- 供应商调用与模型列表探测共用同一个 `HTTPInvoker`，因此共用同一套出站守卫
  （`Support/OutboundHTTP`）：scheme、userinfo、解析后的 IP 全部校验，连接时再校验
  一次，重定向次数受限，响应体上限 1 MiB。
- `AllowHTTP` 沿用仓库统一约定 `!strings.EqualFold(cfg.AppEnv, "production")`
  （见 `bootstrap/api_assembly_domains.go` 的装配；webhooks / HostHTTP 同一规则）。
  `.env` 默认 `APP_ENV=development`，所以本地可以用 http 端点；生产只允许 https。
- **私网、回环、link-local、保留网段默认拒绝**，只由 `SFORUM_AI_ALLOW_PRIVATE_ENDPOINT`
  显式放行，并且生产环境恒为 false（`config.AllowPrivateAIEndpoint`）。放行的是
  本机/内网模型服务（Ollama、vLLM 等）：`outboundhttp.Options.AllowPrivate` 只跳过
  「地址是否公网」这一条判断，scheme / userinfo / 重定向 / 超时全部照旧，而且只有
  AI 的 invoker 用它，不是全局开关。决策见
  `decisions/2026-10-11-ai-private-provider-endpoints.md`。
- 出站失败分类为稳定原因码：`provider_failed`（请求已发出但失败）与 `unsafe_target`
  （守卫拒绝地址）。两者对运营者的动作不同——前者查网络/密钥，后者改地址——因此
  前端有两份文案（`admin.ai.modelFetchReasons.*` 与 `admin.ai.diagnose.reasons.*`）。
- 生产要接内网模型服务时没有直连路径：需要在前面放一个提供 HTTPS 的反向代理
  （见 `docs/zh-CN/deployment.md` 与 `docs/en-US/deployment.md` 的配置章节）。

## Credentials

- 设置文档只保存 `sforum.secret://` 引用；明文只在 PUT
  `/admin/ai/profiles/:profileId/credential` 这一次请求里存在。
- **密钥写入 fail closed**：`CredentialWriter.Put` 在没有可用加密密钥时返回
  `ErrCredentialEncryptionRequired`。透明 cipher 只服务单元测试，运行期不接受明文。
- 加密能力来自 `APP_OPTION_ENC_KEY`（32 字节 hex，AES-256-GCM）。未配置时
  `GET /admin/ai/settings` 返回 `encryptionEnabled=false`，控制台显示警告并禁用
  密钥输入与保存按钮。
- `scripts/dev.sh` 首次生成 `.env` 时自动补本地密钥。

## Admin console

- 页面壳 `pages/admin/ai.vue` 是唯一数据所有者：数据经 `ADMIN_AI_KEY` 注入，可编辑
  草稿经 `ADMIN_AI_DRAFT_KEY` 注入（`useAdminAISettingsForm`）。页签只渲染，不重新拉取。
- 四个固定页签：providers / **replyPrompt** / diagnose / observability。页签用
  `router.replace` 同步 query（`push` 会触发完整导航并白屏）。
- **草稿由页面壳持有**，因为回复提示词与供应商是同一次保存的内容：两个页签各持一份
  副本的话，在提示词页签里的改动切回供应商页签就消失了。保存/重置操作条也放在页面壳
  （任一页签都能就地保存），并显示未保存提示。
- providers 页签按职责拆出 `admin/ai/blocks/`：`SFAdminAIProviderPicker`、
  `SFAdminAIAutoActionBlock`、`SFAdminAIRedactionBlock`、`SFAdminAIModelField`；
  回复提示词独立为 `admin/ai/tabs/SFAdminAIReplyPromptTab.vue`。
- 「模型」字段支持一键获取模型列表：`SFAdminAIModelField` 在拿到列表后切换为
  下拉选择（当前值始终在选项里），并保留「手动输入」出口。
- 一键重置只重置策略字段，**保留 profiles 与成本绑定**（密钥引用与 Secret Store
  中的值不受影响）。
- 诊断按 economy → standard → premium 解析实际命中的 profile，并在结果里回传
  `costClass`。

## Model list probe

- `POST /admin/ai/profiles/:profileId/models`（`ai.manage`）用一次只读 `GET /models`
  探测端点可用的模型。路由 id `core.route.ai.list_profile_models`。
- 它**不走网关**：没有 token、没有费用、不写执行记录，因此不会污染成本口径与用量
  看板。出站仍复用同一个 `HTTPInvoker`（`Execute` 暴露原始状态码+响应体），SSRF
  校验、超时与响应体上限只有一处实现。
- 请求体 `{baseUrl?, protocol?, apiKey?}` 三项都可缺省：缺省时用已保存的 profile
  与 Secret Store 里的密钥。带上它们，运营者才能在保存之前就看到列表；`apiKey`
  只在这一次请求里存在，不写库、不记录、不回传（写入密钥仍只走 credential 端点）。
- 尚未保存的 profile（例如刚选中的「自定义」）允许用请求里的地址做一次性探测；
  没带地址则返回 `ai.profile_not_found`。
- `Profile.WithProbe` 只校验探测必需字段（协议 + 地址）：**model 允许为空**，因为
  获取列表恰恰是为了挑模型。
- 供应商层面的失败作为 200 结论返回（`reason=provider_failed` + `error` 说明），
  由界面就地解释；只有配置层问题（缺密钥、地址非法、找不到 profile）才是 4xx。
- 上限 `MaxModelListEntries=500`：超出或供应商自身分页未取完时 `truncated=true`，
  界面必须说明「这不是全部」。模型顺序保持供应商给出的顺序（最新/最常用通常在前）。

## Reply bot and built-in read-only tools

- `app/Models/AIReply` owns the trigger (comment.created → enqueue), the thread
  snapshot (`ReplyContext`), the prompt assembly and the generator;
  `app/Models/AITools` owns the six Core built-in tools
  (`forum-search`, `forum-topic-read`, `forum-topic-list`, `user-profile-read`,
  `user-search`, `time-now`). The step budget, gates, accounting and truncation
  live in `Support/AI.Orchestrator`, not in the tools.
- **Identity, not just nicknames, reaches the prompt.** Every quoted utterance
  carries `AuthorRef{Username, DisplayName}` and renders as
  `显示名（@用户名）`. Without the handle the model could name the member it was
  replying to but had nothing to pass to `user-profile-read`, so the tool was
  unreachable for exactly the users in the conversation.
- `user-search` resolves a nickname to a handle; `user-profile-read` then reads
  the public profile. Both are in the `forum.reply` purpose declaration
  (`supportai.CoreReplyToolAllowlist`); an injected prompt cannot reach a tool the
  purpose never advertised.
- `profile.Service.SearchPublicUsers` is the only user directory read path the
  bot has: non-empty query required (an empty query would become `%%` and list
  every member), LIKE metacharacters escaped, active accounts only, bounded
  results, and a projection type with no email/status/role field. See
  `decisions/2026-10-11-ai-reply-endpoint-identity-and-user-search.md`.

## Multimodal image input (2026-10-15)

- `Settings.Multimodal.Enabled`（默认 false）是站点级图片理解开关；控制台
  providers 页签的 `SFAdminAIMultimodalBlock` 提供 checkbox。Core 不维护模型
  能力名单，运营者自行确认模型支持图片理解；配错降级为纯文本或按失败姿态
  处理，不会崩溃。决策：`decisions/2026-10-15-ai-multimodal-image-input.md`。
- 契约 `Part` 增加 `PartImage`（base64 `ImageContent`），只允许用户消息；
  上限每次 4 张、单张 2MB（`MaxImagesPerMessage` / `MaxImageBytes` 硬边界）。
  `sforum.ai.completion@1` 版本不变（新增字段向后兼容）。
- 收集范围：主楼 + 被回复评论的公开图片附件（`PostgresReader.ReplyImageAttachments`
  可选接口，fake 不实现即纯文本）。字节由 completer 在开关开启时按需读取，
  生产适配器 `providers.AIReplyImageReader` 以机器人账号身份走
  `attachments.Service.OpenContent`——授权链与普通用户一致。
- openai-chat 翻译成混合 content 数组（`image_url` + data URI；纯文本消息
  保持字符串）；anthropic-messages 翻译成 `image` block + base64 source。
  图片调用照常过闸门/配额/trace；提示词末尾标注图片计数。

## Purposes

- **用途是声明，不是调用点的细节。** `app/Support/AI/purpose.go` 的
  `PurposeDeclaration` 记录 id、双语名称/说明、成本等级、失败姿态、默认工具白名单、
  资源类型、提示词版本与影响范围（single/batch/site）；`PurposeRegistry` 拒绝重复 id
  与任何不合规声明，装配期构造后只读。`forum.reply` 是当前唯一条目；
  `DefaultPurposeRegistry()` 是进程单例，保证「回复用途声明了什么」在一个进程里
  只有一个答案。
- **解析优先级固定为「站点按用途覆盖 > 用途声明默认值」**，且不可反转：声明是 Core
  （后续是插件）写下的能力边界，运营者只能在已声明的用途上做选择。
  `PurposeResolver` 由网关（拒绝未声明用途）、服务层（回复参数）与控制台（用途清单）
  共用，因此「控制台显示的生效值」与「运行期实际使用的值」不可能分叉。
- **未声明的用途必须失败关闭**：`Gateway.Execute` 在总开关之后、闸门之前校验用途，
  未声明即 `purpose_unknown` + denied trace + 零出站；站点关闭时答案仍然只有一个
  `gateway_disabled`。`GatewayConfig.Purposes` 可缺省（老部署/单测兼容），装配路径
  `Providers.NewAIProvider` 始终注入。
- **站点配置 `settings.purposes`** 是登记表的镜像：缺省沿用声明默认；`tools` 出现
  （含空列表）即运营者接管该用途的工具白名单；保存只校验形状，存在性在请求期解析
  （用途集合会随版本与后续插件用途变化）。旧的 `reply.tools` 在 `Normalized()` 里
  迁移进 `purposes["forum.reply"].tools`，旧字段保留可读（回滚不丢配置），新配置优先。
- 回复生成器不再自带工具常量、成本等级或提示词版本：三者由一次
  `ReplyParameterSource.ReplyPurposeParameters` 解析，杜绝「成本等级来自 A、工具来自
  B」。解析失败回退到声明默认值（声明成本等级 + Core 工具列表 + 声明提示词版本），
  绝不回退成「无白名单」或「全部工具」。
- 控制台 `GET /admin/ai/settings` 返回 `purposes` 清单（声明与生效参数、覆盖标记），
  `PATCH` 接受 `purposes`；`/control-panel/ai?tab=purposes` 的用途页签
  （`blocks/SFAdminAIPurposesBlock.vue`）按用途提供失败姿态与工具白名单覆盖，
  「跟随声明」与「自定义」是两种事实，声明默认与生效值并列展示，一键恢复声明默认。
  页面壳 `await ai.ready` 后才渲染——AI 控制台所有页签共享的三个 useAsyncData 必须在
  SSR 渲染前完成，否则服务端按空兜底渲染、客户端按真实载荷水合，hydration 必然
  mismatch（2026-10-12 修复）。**窄屏渲染 QA 待做**（390x844）。决策：
  `decisions/2026-10-12-ai-purpose-registry.md`。

## Defaults semantics

- `gates.toolCallsPerReply` 是指针字段：nil = 按 `DefaultToolCallsPerReply`(3)
  生效，0 = 停用工具调用。前端归一化显式区分两者（`JSON.stringify` 会抹平
  undefined，不能直接比较）。
- `reply.systemPrompt` 为空表示使用内置默认；`ReplyPromptSafetyAppendix` 始终追加
  且不可配置。提示词暂时留在 `reply.*`：只有回复一个用途时，把提示词做成按用途字段
  是在猜接口；第一个非回复用途落地时它移入用途声明（`purposes[id].systemPrompt`），
  安全尾注对所有用途保持不可配置。
- `autoAction.allowedActions` 只接受 hide/tag/reject；删除类动作永不进入白名单。

## Permissions

- **插件面（M1/M1.5/M2，2026-10-11）**：`sforum.ai.complete@1`（`ai.invoke`，
  service 模式，`Metadata.CallerExtensionID` 由 runtime 身份注入）是插件发起补全的
  唯一入口；`sforum.events.emit@1`（`events.emit`）让插件发布自身命名空间事件；
  清单 `aiTools[]` 让插件贡献工具，走保留 slot `sforum.ai.tool` 的 versioned
  ProviderCall，装配期登记进同一张 Core 工具表（登记不等于暴露，插件工具不进任何
  Core 用途白名单，也永不覆盖 Core 工具名）。任何插件调用宿主命令都需要清单声明
  `database.grants: ["host_commands"]`，否则命令体之前即被拒为
  `host.command_authority_denied`。子进程 fixture：
  `extensions/fixtures/plugins/sforum-ai-command-reference/`（命令四路径）与
  `sforum-ai-tool-reference/`（工具派发）。事件发布的子进程 fixture 是
  `extensions/fixtures/plugins/sforum-events-command-reference/`；它的引入抓到了
  事件命令「Preview 缺 Policy、Prepare 只带 private」导致无法通过 broker 规划的缺陷。
- **出站脱敏**：`app/Support/AI/redaction.go` 的 `RedactText`/`ApplyRedaction` 在
  `Gateway.Execute` 里于缓存键与出站之前执行，覆盖 system、消息正文与工具调用参数
  （参数按 JSON 字符串值递归脱敏）；占位符 `[redacted-email]` / `[redacted-phone]`
  / `[redacted-ip]`；匹配保守（@提及、订单号、版本号、时间戳、裸数字不动）；关闭或
  未命中时零改写。决策：`decisions/2026-10-11-ai-outbound-redaction.md`。
- **插件工具暴露**：用途的模型可见工具白名单来自站点设置
  `purposes["<id>"].tools.allowed`（旧的 `reply.tools.allowed` 仍被接受并迁移；缺省＝
  该用途声明的 Core 列表；显式列出即覆盖，空列表＝不允许任何工具）。插件贡献的工具
  必须被运营者点名才会出现；保存只校验形状，存在性在请求期由登记表解析（未注册名
  跳过）。决策：`decisions/2026-10-12-ai-plugin-tool-exposure-policy.md` 与
  `decisions/2026-10-12-ai-purpose-registry.md`。
- **结果缓存**：`app/Support/AI/result_cache.go` 把 `Support/Cache` 接到网关的
  `ResultCache` 面上，生产用 Redis 装配（`Providers/ai.go`），默认 TTL 10 分钟；
  `CacheKey` 含调用方/用途/profile/模型/提示词版本/配置 revision/工具/脱敏后正文，
  因此改配置自然换键；命中不记账、不消耗额度，并在返回结果上写 `cacheHit: true`。
  决策：`decisions/2026-10-11-ai-result-cache-wiring.md`。
- 插件 job 可声明 `queue: "ai"`，进入与 Core AI 任务同一条低并发通道
  （`JOB_QUEUE_AI_WORKERS`）；取值集合封闭，其余队列不向插件开放。决策：
  `decisions/2026-10-12-plugin-job-queue-declaration.md`。
- 所有 `/admin/ai/*` 路由要求 `ai.manage`。前端 `can('ai.manage')` 只是体验辅助，
  服务层校验才是权威。

## Extension surface

- 扩展通过 Host API 与 provider slot 使用 AI 能力；用途（purpose）声明成本等级，
  配额按站点/扩展/用户三个作用域分别结算。插件调用宿主命令（含 `sforum.ai.complete`）
  需要清单声明 `database.grants: ["host_commands"]`。
- 插件 AI 工具的执行器适配层在 `app/Support/AI/toolplugin`（`PluginToolInvoker`、
  `PluginTool`、`PluginToolRegistry`）。它曾是 `Support/AI` 的一个文件，而
  `Support/AI` → `Models/Extensions` 与 `Models/Extensions` 测试 → `Support/HostAPI`
  → `Support/AI` 构成测试期 import 环，导致 `Models/Extensions` 测试包完全无法编译；
  移到子包即修复，依赖方向也正确（适配层依赖 AI 契约，反之不然）。
