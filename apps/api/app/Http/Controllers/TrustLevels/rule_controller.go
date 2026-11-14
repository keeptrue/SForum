package trustlevelscontroller

import (
	"context"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 本文件是规则编辑面的两条后台端点（M1）：
//
//	GET  /admin/trust-levels/conditions   条件目录 + 插件事实候选 + 对象候选
//	POST /admin/trust-levels/rule-impact  保存前的影响人数估算（草稿，不落库）
//
// 两条都**不写库、不入队、不发事件**：它们服务的是编辑器，不是状态变更。
// 因此权限沿用读面的 `admin.access`（估算）与写面的 `trust.manage`（见下）：
// 估算会读全站用户分布，比普通读面敏感，但语义仍是「写之前的试算」，所以给
// `trust.manage` 而不是 `admin.access`——只有能改策略的人才需要知道改完影响多少人。

// WithRuleEditingDeps 注入规则编辑面依赖（bootstrap 装配；测试注入 fake）。
//
// 三个依赖都用窄接口/闭包而不是具体 store（D17）：本包不 import `Models/Badges`，
// 真实查询留在 Badges 侧。
func (h *Controller) WithRuleEditingDeps(
	refCandidates trustlevels.RefCandidateSource,
	factCandidates func() []conditions.FactCandidate,
	impactEstimator trustlevels.RuleImpactEstimator,
) *Controller {
	h.refCandidates = refCandidates
	h.factCandidates = factCandidates
	h.impactEstimator = impactEstimator
	return h
}

// ---- GET /admin/trust-levels/conditions ----

func (h *Controller) adminListRuleConditions(c fiber.Ctx) error {
	if _, err := h.requireAdmin(c, identity.PermissionAdminAccess); err != nil {
		return err
	}
	factCandidates := h.declaredFactCandidates()
	refCandidates := conditions.UnavailableRefCandidateSet()
	if h.refCandidates != nil {
		if set, err := h.refCandidates.RefCandidates(c.Context()); err == nil {
			refCandidates = set
		}
		// 读失败保持 `unavailable` 形状：候选取不到 ≠ 候选为空。
	}
	payload := trustLevelConditionCatalog{
		// 发布面按候选过滤（事实候选全空 → 不下发 `fact.count`；某类引用候选读到为空 →
		// 不下发用到它的条件）。**校验白名单不受影响**：`RuleConditionAllowed` 仍接受全部
		// 条件，否则插件停用期间的存量规则会读不出来。判据在 `PublishableRuleConditions`。
		Conditions:     toConditionDTOs(trustlevels.PublishableRuleConditions(factCandidates, refCandidates)),
		FactCandidates: toFactCandidateDTOs(factCandidates),
		RefCandidates:  toRefCandidateGroups(refCandidates),
	}
	return apphttp.OK(c, payload)
}

// declaredFactCandidates 返回已启用插件声明的事实候选（未注入时为空集）。
func (h *Controller) declaredFactCandidates() []conditions.FactCandidate {
	if h.factCandidates == nil {
		return []conditions.FactCandidate{}
	}
	return h.factCandidates()
}

// ---- POST /admin/trust-levels/rule-impact ----

func (h *Controller) adminEstimateRuleImpact(c fiber.Ctx) error {
	if _, err := h.requireAdmin(c, identity.PermissionTrustManage); err != nil {
		return err
	}
	var request ruleImpactRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	results, err := h.estimateImpact(c.Context(), request)
	if err != nil {
		return err
	}
	return apphttp.OK(c, ruleImpactResponse{Tiers: results})
}

// estimateImpact 逐档估算。
//
// **新档也估**（草稿形态，不依赖已保存的 tierId）：运营点开弹窗时通常还没保存，
// 「只给已保存档位估算」会让这个功能在最需要它的时刻缺席。
func (h *Controller) estimateImpact(ctx context.Context, request ruleImpactRequest) ([]ruleImpactTierResult, error) {
	results := make([]ruleImpactTierResult, 0, len(request.Tiers))
	for _, tier := range request.Tiers {
		requirements := trustlevels.Requirements{}
		if tier.Requirements != nil {
			requirements = toRequirements(*tier.Requirements)
		}
		impact, err := trustlevels.EstimateTierImpact(ctx, h.impactEstimator, requirements, tier.Rule)
		if err != nil {
			return nil, fiber.NewError(fiber.StatusInternalServerError, "trust_levels.impact_unavailable")
		}
		unsupported := impact.UnsupportedConditions
		if unsupported == nil {
			unsupported = []string{}
		}
		results = append(results, ruleImpactTierResult{
			TierID:                tier.TierID,
			Estimated:             impact.Estimated,
			Method:                impact.Method,
			Partial:               impact.Partial,
			UnsupportedConditions: unsupported,
		})
	}
	return results, nil
}

// toRequirements 把 wire 形状折回领域形状。
func toRequirements(input trustlevelsRequirement) trustlevels.Requirements {
	return trustlevels.Requirements{
		DaysVisited:       input.DaysVisited,
		TopicCount:        input.TopicCount,
		CommentCount:      input.CommentCount,
		LikeReceivedCount: input.LikeReceivedCount,
		LikeGivenCount:    input.LikeGivenCount,
		BookmarkCount:     input.BookmarkCount,
	}
}

// ---- 领域形状 → wire ----

func toConditionDTOs(definitions []conditions.Definition) []trustLevelCondition {
	out := make([]trustLevelCondition, 0, len(definitions))
	for _, definition := range definitions {
		params := make([]trustLevelConditionParam, 0, len(definition.Params.Specs))
		for _, spec := range definition.Params.Specs {
			params = append(params, trustLevelConditionParam{
				Name:     spec.Name,
				Kind:     string(spec.Kind),
				Required: spec.Required,
				Min:      nonZeroInt64(spec.Min),
				Max:      nonZeroInt64(spec.Max),
				Options:  nonNilStrings(spec.Options),
				RefKind:  spec.RefKind,
			})
		}
		compare := make([]string, 0, len(definition.Compare))
		for _, op := range definition.Compare {
			compare = append(compare, string(op))
		}
		out = append(out, trustLevelCondition{
			Key:        definition.Key,
			ValueKind:  string(definition.ValueKind),
			Compare:    compare,
			LabelZh:    definition.LabelZh,
			LabelEn:    definition.LabelEn,
			DescribeZh: definition.DescribeZh,
			DescribeEn: definition.DescribeEn,
			UnitZh:     definition.UnitZh,
			UnitEn:     definition.UnitEn,
			Params:     params,
		})
	}
	return out
}

// nonZeroInt64 把「0 表示不限制」翻成「不下发」：`min: 0` 与「没有下限」在编辑器里
// 是两件事，前者会渲染成「最小 0」的提示。
func nonZeroInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

func toFactCandidateDTOs(candidates []conditions.FactCandidate) []factCandidate {
	out := make([]factCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		dto := factCandidate{
			Key:      candidate.Key,
			Label:    trustLevelName{Zh: candidate.LabelZh, En: candidate.LabelEn},
			Compare:  make([]string, 0, len(candidate.Compare)),
			Variants: make([]factVariant, 0, len(candidate.Variants)),
		}
		if candidate.UnitZh != "" || candidate.UnitEn != "" {
			dto.Unit = &trustLevelName{Zh: candidate.UnitZh, En: candidate.UnitEn}
		}
		dto.Compare = append(dto.Compare, candidate.Compare...)
		for _, variant := range candidate.Variants {
			dto.Variants = append(dto.Variants, factVariant{
				Value: variant.Value,
				Label: trustLevelName{Zh: variant.LabelZh, En: variant.LabelEn},
			})
		}
		out = append(out, dto)
	}
	return out
}

func toRefCandidateGroups(set conditions.RefCandidateSet) []refCandidateGroup {
	groups := make([]refCandidateGroup, 0, len(set))
	for _, group := range set {
		candidates := make([]refCandidate, 0, len(group.Candidates))
		for _, candidate := range group.Candidates {
			flags := make([]string, 0, len(candidate.Flags))
			flags = append(flags, candidate.Flags...)
			candidates = append(candidates, refCandidate{
				Value: candidate.Value,
				Label: candidate.Label,
				Flags: flags,
			})
		}
		groups = append(groups, refCandidateGroup{
			Kind:        group.Kind,
			Candidates:  candidates,
			Unavailable: group.Unavailable,
			Truncated:   group.Truncated,
		})
	}
	return groups
}
