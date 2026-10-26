package main

import (
	"context"
	"fmt"
	"math/rand/v2"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
)

// 本文件为种子用户补齐公开资料（简介 / 签名 / 所在地 / 个人站点）。
//
// 走 Profile 领域服务而不是直接写 SQL：资料更新会做长度与 URL 校验，
// 并与真实用户编辑资料走同一条 upsert 路径。

// seedProfileService 是 seed 场景用到的资料写入子集。
type seedProfileService interface {
	UpdateMyProfile(ctx context.Context, actor identity.Actor, input profile.UpdateProfileInput) (profile.PublicProfile, error)
}

// 内置资料语料。内容刻意中性、无指向性，避免假数据里出现真实人物或品牌。
var (
	seedProfileBios = []string{
		"写代码，也写点别的。长期潜水，偶尔冒泡。",
		"后端工程师，日常和数据库、队列打交道。",
		"前端搬砖，喜欢折腾各种构建工具。",
		"半路出家，正在补计算机基础，请多指教。",
		"运维转开发，对稳定性和可观测性格外上心。",
		"学生党，业余时间做点小项目练手。",
		"喜欢把踩过的坑写下来，免得下次再踩。",
		"关注开发效率，能自动化的绝不手动。",
		"兴趣广泛，什么都不精通，什么都想学。",
		"一线业务开发，最近在研究性能优化。",
		"自由职业，接一些小项目，时间比较自由。",
		"喜欢安静地读文档，不太喜欢加班。",
		"资深潜水员，看到有意思的帖子会回两句。",
		"做数据相关的工作，日常和 SQL 纠缠。",
		"刚开始学编程，希望在这里认识一些朋友。",
		"产品经理，来技术社区学习一下大家的思路。",
	}

	seedProfileSignatures = []string{
		"简单、直接、可维护。",
		"先跑起来，再跑得快。",
		"任何足够复杂的配置都等同于 bug。",
		"写文档就是写代码的一部分。",
		"慢即是快，少即是多。",
		"复制粘贴一时爽，重构火葬场。",
		"能用工具解决的，就不要靠自觉。",
		"不要过早优化，也不要永远不优化。",
		"理解问题比写代码更重要。",
		"保持好奇，保持记录。",
		"把复杂留给自己，把简单留给用户。",
		"今天的随手一记，是明天的救命稻草。",
	}

	seedProfileLocations = []string{
		"北京", "上海", "广州", "深圳", "杭州", "成都", "武汉", "西安",
		"南京", "长沙", "苏州", "重庆", "厦门", "青岛", "远程办公", "海外",
	}
)

// seedProfileInputFor 为第 index 个种子用户生成一份资料输入。
// 站点只有约六成用户填写，空缺用 nil 表示「不改动」。
func seedProfileInputFor(rng *rand.Rand, index int) profile.UpdateProfileInput {
	bio := pickString(rng, seedProfileBios)
	signature := pickString(rng, seedProfileSignatures)
	location := pickString(rng, seedProfileLocations)
	input := profile.UpdateProfileInput{
		Bio:       &bio,
		Signature: &signature,
		Location:  &location,
	}
	if rng.IntN(10) < 6 {
		website := fmt.Sprintf("https://example.com/u/%d", index+1)
		input.WebsiteURL = &website
	}
	return input
}

// applySeedProfiles 逐个用户写入公开资料。失败即中断，避免留下半截数据。
func applySeedProfiles(
	ctx context.Context,
	svc seedProfileService,
	actors actorLoader,
	userIDs []int64,
	rng *rand.Rand,
	logf func(format string, args ...any),
) (int, error) {
	if svc == nil || len(userIDs) == 0 {
		return 0, nil
	}
	updated := 0
	for i, userID := range userIDs {
		if err := ctx.Err(); err != nil {
			return updated, err
		}
		actor, err := actors.LoadActor(ctx, userID)
		if err != nil {
			return updated, fmt.Errorf("load actor for seed profile %d: %w", userID, err)
		}
		if _, err := svc.UpdateMyProfile(ctx, actor, seedProfileInputFor(rng, i)); err != nil {
			return updated, fmt.Errorf("update seed profile for user#%d: %w", userID, err)
		}
		updated++
		if updated%20 == 0 {
			logf("  seeded %d/%d profiles\n", updated, len(userIDs))
		}
	}
	return updated, nil
}
