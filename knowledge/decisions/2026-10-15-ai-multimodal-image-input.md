# 2026-10-15 AI 多模态图片输入（按站点开关启用）

## 背景

2026-10-02 的 AI Assist Platform 决定把多模态输入整体排除在 Core 之外。本次按
运营者需求重开其中最小的一片：AI 回复机器人的**图片理解**——模型能看到主题与
被回复评论里的图片附件并据此回答。embeddings、图片生成、图片审核、alt-text
生成仍然保持排除。

## 决定

1. **站点级开关，不建模型能力名单。** `Settings.Multimodal.Enabled`（默认
   false）是唯一的开关面。Core 不内置「哪些模型支持视觉」的名单：名单永远
   滞后于新模型，自建网关的重命名模型根本覆盖不到，误报比漏报更有害。运营者
   自行确认所选模型支持图片理解；配错的后果是「图片被跳过或供应商报错」，
   按既有失败姿态降级，不会崩溃。
2. **契约向后兼容扩展。** `Part` 增加 `PartImage`（base64 `ImageContent`），
   `sforum.ai.completion@1` 版本不变：旧调用方不带图片字段，行为不变。图片
   只允许出现在用户消息；上限每次 4 张、单张 2MB（`MaxImagesPerMessage` /
   `MaxImageBytes`，硬边界不可调）。
3. **收集范围刻意收窄。** 只收集主楼 + 被回复评论的公开图片附件；近期讨论
   窗口里的图片不收集——10 条评论全带上会把单次请求打到不可解释的规模。
4. **读取走与普通用户相同的授权路径。** 生产适配器
   `providers.AIReplyImageReader` 以机器人账号身份调用
   `attachments.Service.OpenContent`；机器人不因「系统调用」获得任何额外可见
   性。SQL 先收窄到 active + public + 图片 MIME，再由附件授权链复核。
5. **读取失败降级为纯文本。** 候选查询失败、单张读失败、单张超限都跳过，
   不让整次回复失败。开关读取也按「关」处理——fail closed 到文本回复。
6. **两套协议都翻译。** openai-chat 用混合 content 数组
   （`image_url` + data URI；纯文本消息保持字符串形态以最大化兼容面），
   anthropic-messages 用 `image` block + base64 source。
7. **图片计入成本。** 图片调用照常走网关闸门/配额/trace（既有路径自动覆盖），
   提示词末尾标注「本条发言附带 N 张图片」，让模型知道自己在看图。

## 前端

- 控制台 providers 页签新增 `SFAdminAIMultimodalBlock`：一个 checkbox +
  说明文案（数量/体积上限、需配合视觉模型）；旧文档缺 `multimodal` 字段时
  前端先补全再绑定。
- Controller `updateSettingsRequest` 增加 `multimodal` 字段，保存走同一
  `Normalized()` 路径。

## 权限与测试

- 无新权限键：开关修改随既有 `ai.manage`；附件读取复用附件授权链。
- 测试：`wire_image_test.go`（两套协议序列化 + 契约校验拒绝非法图片形态）、
  `images_test.go`（收集上限/降级/标注）。

## 未做（保持排除）

- 图片生成、图片审核、alt-text 自动生成、外链图片 URL 抓取、近期评论图片。
