package ai_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// moderationProbeSystem 是试跑用的审核提示词草稿。它刻意把「合理的批评」与
// 「激烈但不涉人身的争论」写进不需要审核的清单——这是审核系统最容易误伤的
// 两类内容，也是本次试跑最想观察的指标。
const moderationProbeSystem = `你是中文技术社区的内容审核助手。判断一条内容是否需要人工审核。

需要人工审核：
- 广告、推广、引流、售卖服务或账号
- 包含外部联系方式（微信、QQ、手机号、外链）
- 人身攻击、辱骂、羞辱他人
- 色情、擦边、求取资源
- 违法违规（盗版、破解、外挂、代考）
- 无意义灌水、刷屏

不需要人工审核（重要）：
- 合理的技术提问与技术讨论
- 对产品、项目、社区的正常批评与不满
- 激烈但只针对技术的争论
- 新手的重复提问或基础问题

严格输出 JSON，字段为 action（allow 或 review）、confidence（0 到 1 的小数）、reason（不超过 20 字的中文理由）。`

type moderationProbeCase struct {
	ID          string
	Expectation string
	Content     string
}

func moderationProbeCases() []moderationProbeCase {
	return []moderationProbeCase{
		{"normal_question", "allow", "请教一下，PostgreSQL 的 VACUUM FULL 和 pg_repack 在锁表行为上有什么区别？我这边有个 200GB 的表需要整理，但停机窗口只有 10 分钟。"},
		{"spam_ad", "review", "【推广】专业代做毕业设计，Java/Python/前端全包，包过答辩，价格优惠，需要的加微信 xiaoming_2024，QQ 88888888，长期有效。"},
		{"soft_promo", "borderline", "最近换了个新的云服务器，用的是星空云，感觉性能比之前那家好太多，延迟低价格也便宜，有兴趣的可以去了解一下。"},
		{"resource_bait", "review", "分享一个网站，上面资源很全，各种小姐姐写真都有，懂的都懂，链接在个人签名里。"},
		{"low_effort", "review", "顶顶顶顶顶顶顶顶顶顶顶顶顶顶顶顶"},
		{"personal_attack", "review", "楼主你是不是脑子有问题？这种低级问题也来问，浪费大家时间，赶紧滚回去再学两年。"},
		{"fair_criticism", "allow", "说实话这个版本改得挺让人失望的。边栏在小屏上完全没法用，我在官方论坛反馈了三个月也没人理，不知道开发组到底有没有在用自己做的产品。"},
		{"piracy_request", "review", "求个 PS 2024 的破解版下载地址，或者有没有注册机，谢谢大佬。"},
		{"technical_dispute", "allow", "你这个说法完全没有依据。Go 的 GC 在现代硬件上早就不是瓶颈了，拿 2015 年的 benchmark 说事没有意义。你要是有数据就拿出来，没有就别在这误导新人。"},
		{"newbie_repeat", "allow", "第一次发帖，想问问大家这个网站怎么改头像啊？我找了好久没找到，谢谢。"},
	}
}

type moderationProbeVerdict struct {
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

func TestLiveModerationPromptProbe(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("SFORUM_AI_LIVE_KEY"))
	if apiKey == "" {
		t.Skip("SFORUM_AI_LIVE_KEY is not set; moderation probe skipped")
	}
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	settings.Profiles[0].Enabled = true
	settings.Profiles[0].Defaults.MaxTokens = 128
	settings.Profiles[0].Defaults.TimeoutMS = 45_000

	gateway := ai.NewGateway(ai.GatewayConfig{
		Settings: &fakeSettings{settings: settings},
		Creds:    &fakeCreds{value: apiKey},
		Invoker:  ai.NewHTTPInvoker(ai.HTTPInvokerOptions{Timeout: 50 * time.Second}),
		Clock:    fixtureNow,
	})

	ctx := context.Background()
	agree, total := 0, 0
	for _, item := range moderationProbeCases() {
		result, err := gateway.Execute(ctx, ai.CompletionRequest{
			Purpose:   "moderation.probe",
			CostClass: ai.CostClassEconomy,
			System:    moderationProbeSystem,
			Messages: []ai.Message{
				{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "内容：" + item.Content}}},
			},
			MaxTokens:      128,
			ResponseFormat: ai.ResponseFormatJSON,
			PromptVersion:  "moderation-probe@1",
			Metadata:       ai.Metadata{CallerExtensionID: "sforum.moderation-probe"},
		})
		if err != nil {
			t.Fatalf("%s: %v", item.ID, err)
		}
		var verdict moderationProbeVerdict
		if err := json.Unmarshal([]byte(strings.TrimSpace(result.Text)), &verdict); err != nil {
			t.Logf("%-18s RAW %q", item.ID, result.Text)
			continue
		}
		total++
		if item.Expectation != "borderline" && verdict.Action == item.Expectation {
			agree++
		}
		t.Logf("%-18s want=%-10s got=%-7s conf=%.2f reason=%s",
			item.ID, item.Expectation, verdict.Action, verdict.Confidence, verdict.Reason)
	}
	t.Logf("agreement on decidable cases: %d/%d", agree, total)
}
