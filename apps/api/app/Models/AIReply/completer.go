package aireply

import (
	"context"
	"fmt"
	"log/slog"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// GatewayCompleter 把生成器的请求转成一次网关调用。
//
// 成本等级来自用途声明（生成器解析后放进 CompletionInput.CostClass）：它是用途
// 的结构性事实——回复是高频、低难度的任务，声明里的等级就是 economy。调用方
// 想换模型时改的是成本等级到 profile 的绑定，而不是这里的代码；这里只在用途
// 没有声明等级时兜底，绝不接受调用方自选模型。
//
// 工具循环由 Host 的编排器执行（步数预算、逐步闸门、逐步执行记录都在那里）。
// orchestrator 为 nil 时退化为单次补全，与工具能力引入前完全一致——一个还没有
// 注册表的部署不会因此失去回复功能。
type GatewayCompleter struct {
	gateway      *supportai.Gateway
	orchestrator *supportai.Orchestrator
	// images 可为 nil：没有附件读取能力的部署退化为纯文本补全。
	images ImageReader
}

func NewGatewayCompleter(gateway *supportai.Gateway, orchestrator *supportai.Orchestrator) *GatewayCompleter {
	return &GatewayCompleter{gateway: gateway, orchestrator: orchestrator}
}

// WithImages 装配附件图片读取能力。多模态开关在运行期按次读取（gateway 的
// Settings 有缓存），而不是装配期定死——控制台改开关必须立即生效。
func (c *GatewayCompleter) WithImages(reader ImageReader) *GatewayCompleter {
	c.images = reader
	return c
}

// resolveCostClass 取本次调用的成本等级：用途声明优先，缺省回退到经济档。
// 这个回退不是「允许调用方选模型」，而是「声明缺失时选最便宜的一档」。
func resolveCostClass(input CompletionInput) string {
	if supportai.ValidCostClass(input.CostClass) {
		return input.CostClass
	}
	return supportai.CostClassEconomy
}

// resolveImageParts 决定本次调用要带上的图片。开关关闭、无读取能力或无候选
// 附件时都是空——三种情况对运营者是同一个事实「这次回复不带图」。
func (c *GatewayCompleter) resolveImageParts(ctx context.Context, input CompletionInput) []supportai.Part {
	if c.images == nil || len(input.Images) == 0 {
		return nil
	}
	settings, err := c.gateway.Settings(ctx)
	if err != nil {
		slog.WarnContext(ctx, "aireply: load settings for multimodal check failed; sending text only", "err", err)
		return nil
	}
	if !settings.Multimodal.Enabled {
		return nil
	}
	return collectImages(ctx, c.images, input.ActorUserID, input.Images)
}

// buildUserParts 组装用户消息的内容部件：文本在前，图片在后。带图时在
// 文本末尾追加一句图片计数说明——不告诉模型「这里有图」，不少模型会直接
// 忽略数组里的图片块。
func buildUserParts(text string, images []supportai.Part) []supportai.Part {
	if len(images) > 0 {
		text += fmt.Sprintf("\n\n（本条发言附带 %d 张图片，请结合图片内容回答。）", len(images))
	}
	parts := make([]supportai.Part, 0, len(images)+1)
	parts = append(parts, supportai.Part{Type: supportai.PartText, Text: text})
	return append(parts, images...)
}

func (c *GatewayCompleter) Complete(ctx context.Context, input CompletionInput) (CompletionOutput, error) {
	if c == nil || c.gateway == nil {
		return CompletionOutput{}, ErrGeneratorUnavailable
	}
	costClass := resolveCostClass(input)
	imageParts := c.resolveImageParts(ctx, input)
	if c.orchestrator != nil && len(input.Tools) > 0 {
		result, err := c.orchestrator.Run(ctx, supportai.RunInput{
			Purpose:       input.Purpose,
			CostClass:     costClass,
			System:        input.System,
			User:          input.User,
			Images:        imageParts,
			MaxTokens:     input.MaxTokens,
			PromptVersion: input.PromptVersion,
			SubjectUserID: input.SubjectUserID,
			ToolNames:     input.Tools,
			ToolContext:   input.ToolContext,
		})
		if err != nil {
			// 部分结果照样透出：已经完成的工具调用与 token 用量属于这次 run 的
			// 事实，丢掉它们会让「上次做到哪」无法观察，重试只能从零开始。
			return partialOutput(result), err
		}
		return CompletionOutput{
			Text:       result.Text,
			StopReason: result.StopReason,
			Reasoning:  result.Reasoning,
			ToolTrace:  result.ToolTrace,
			Usage:      result.Usage,
			ModelCalls: result.ModelCalls,
			ToolCalls:  result.ToolCalls,
			// run id 由编排器生成，与每一步写进 ai_executions 的归属相同：
			// 调用方据此把回复与执行记录（以及 B4 的引用、B5 的状态）串起来。
			RunID: result.RunID,
		}, nil
	}
	result, err := c.gateway.Execute(ctx, supportai.CompletionRequest{
		Purpose:       input.Purpose,
		CostClass:     costClass,
		System:        input.System,
		Messages:      []supportai.Message{{Role: supportai.RoleUser, Parts: buildUserParts(input.User, imageParts)}},
		MaxTokens:     input.MaxTokens,
		PromptVersion: input.PromptVersion,
		// SubjectUserID 让触发者的每日配额能被正确计量：机器人花的是提问者的额度。
		SubjectUserID: input.SubjectUserID,
	})
	if err != nil {
		// 失败路径同样透出可解释事实（停止原因、思考有无、用量）：网关对空补全
		// 会带回部分结果，丢掉它会让「为什么没回」在日志与结果表里都变成一句
		// 笼统的失败。正文仍然不发：空补全的判定前提就是没有正文。
		return CompletionOutput{
			Text:       result.Text,
			StopReason: result.StopReason,
			Reasoning:  result.Reasoning,
			Usage:      result.Usage,
			ModelCalls: 1,
		}, err
	}
	return CompletionOutput{
		Text:       result.Text,
		StopReason: result.StopReason,
		Reasoning:  result.Reasoning,
		Usage:      result.Usage,
		// 非工具分支只有一次模型调用：这个 1 是事实，不是兜底值。
		ModelCalls: 1,
		// RunID 刻意留空。单次 gateway.Execute 不生成 run id（没有「run」这个
		// 概念，也没有内层调用要聚合），编一个出来就是第二个真相：消费方会以为
		// 它对应某组执行记录，而 ai_executions 里根本没有那一行。
	}, nil
}

// partialOutput 把编排器的部分结果转成补全输出。它只在错误路径使用：调用方
// 拿到 error 的同时能看见这次 run 已经走过的工具与花掉的 token。
func partialOutput(result supportai.RunResult) CompletionOutput {
	return CompletionOutput{
		Text:       result.Text,
		StopReason: result.StopReason,
		Reasoning:  result.Reasoning,
		ToolTrace:  result.ToolTrace,
		Usage:      result.Usage,
		ModelCalls: result.ModelCalls,
		ToolCalls:  result.ToolCalls,
		// 失败路径同样要带回 run id：这次 run 已经产生了真实调用与执行记录，
		// 「失败了所以没有 id」会让那些记录变成查不回来的孤立行。
		RunID: result.RunID,
	}
}
