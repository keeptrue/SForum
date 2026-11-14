package badges

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 本文件是后台面对「自动生成文案」的两个入口：
//   - 已保存徽章的 `requirementText`（列表/编辑弹窗展示）；
//   - `POST /admin/badges/rule-text` 的只读预览（前端草稿实时刷新）。
//
// 两者与公开面共用 `BuildRuleRequirementText`，因此「预览看到的」与「保存后公开的」
// 是同一句话——这是本刀唯一的口径来源，前端不许自己拼句子。

// fillRequirementTexts 给一批徽章填上自动文案（一次批量解析名字）。
//
// 就地修改入参切片（调用方刚构造出来、没有共享），返回同一份以便链式调用。
func (a *AdminService) fillRequirementTexts(ctx context.Context, items []Badge) []Badge {
	if len(items) == 0 {
		return items
	}
	names := BadgeRefNames(ctx, RefNameLoaderOf(a.store), items)
	for index := range items {
		items[index].RequirementText = BuildRequirementText(items[index], names)
	}
	return items
}

// PreviewRequirementText 预览一条**尚未保存**的规则会生成什么说明。
//
// 只读、无副作用：不写库、不审计、不入队。校验复用保存路径的同一套判定
// （`ruleengine.Validate` + 引用存在性 + 徽章依赖环），因此「预览通过、保存 422」
// 这类不一致在结构上不存在。空规则（`null`）返回手工徽章的固定句——
// 前端把「还没写规则」与「请求失败」区分开，才不会把空状态画成错误态。
func (a *AdminService) PreviewRequirementText(ctx context.Context, actor identity.Actor, raw json.RawMessage) (RequirementText, error) {
	if err := requireBadgePermission(actor, PermissionBadgeView); err != nil {
		return RequirementText{}, err
	}
	if a == nil || a.store == nil {
		return RequirementText{}, ErrBadgesDisabled
	}
	node, err := decodePreviewRule(raw)
	if err != nil {
		return RequirementText{}, err
	}
	if node == nil {
		return BuildRuleRequirementText(nil, conditions.NewRefNames()), nil
	}
	if err := a.validateRuleForPreview(ctx, *node); err != nil {
		return RequirementText{}, err
	}
	names := ResolveRefNames(ctx, RefNameLoaderOf(a.store), CollectRefs(node), conditions.NewRefNames())
	return BuildRuleRequirementText(node, names), nil
}

// decodePreviewRule 解码预览请求里的规则文档；空文档返回 (nil, nil) 表示「没有规则」。
func decodePreviewRule(raw json.RawMessage) (*ruleengine.Node, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	node, err := ruleengine.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadgeRuleInvalid, err)
	}
	return &node, nil
}

// validateRuleForPreview 复用保存路径的校验链。
//
// store 若实现了 `ValidateBadgeRule`（PostgresStore 就是），校验口径与保存完全一致；
// 否则退回纯 AST 校验（单测里的假 store 不需要实现引用查询）。
func (a *AdminService) validateRuleForPreview(ctx context.Context, rule ruleengine.Node) error {
	type ruleValidator interface {
		ValidateBadgeRule(context.Context, int64, ruleengine.Node) error
	}
	if validator, ok := a.store.(ruleValidator); ok {
		return validator.ValidateBadgeRule(ctx, 0, rule)
	}
	if err := ruleengine.Validate(rule, conditions.Core()); err != nil {
		return fmt.Errorf("%w: %s", ErrBadgeRuleInvalid, err)
	}
	return nil
}
