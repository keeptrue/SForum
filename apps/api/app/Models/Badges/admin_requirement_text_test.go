package badges

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 预览端点（POST /admin/badges/rule-text）的域层测试。
//
// HTTP 层证明状态码与信封；这里证明三件事：权限在领域边界挡住、非法规则给
// 与保存路径同一个错误、预览**不产生任何副作用**。

func previewActor() identity.Actor {
	return identity.Actor{ID: 3, Permissions: map[string]bool{PermissionBadgeView: true}}
}

func TestPreviewRequirementTextRendersRule(t *testing.T) {
	store := newFakeStore()
	admin := NewAdminService(store)
	before := len(store.badges)

	raw := json.RawMessage(`{"v":1,"root":{"type":"activity.count",` +
		`"params":{"metric":"topic_count","window":{"kind":"all"}},` +
		`"cmp":{"op":"gte","value":12}}}`)
	text, err := admin.PreviewRequirementText(context.Background(), previewActor(), raw)
	if err != nil {
		t.Fatalf("PreviewRequirementText: %v", err)
	}
	if text.Zh != "累计发布主题 ≥ 12" || text.En != "topics published ≥ 12" {
		t.Fatalf("preview text = %+v", text)
	}
	// 只读：没有新增/修改任何徽章。
	if len(store.badges) != before {
		t.Fatalf("preview must not mutate catalog: %d -> %d", before, len(store.badges))
	}
}

// 未登录 / 缺 badge.view：权限拒绝在领域层发生（不依赖路由守卫）。
func TestPreviewRequirementTextRequiresBadgeView(t *testing.T) {
	admin := NewAdminService(newFakeStore())
	raw := json.RawMessage(`{"v":1,"root":{"type":"identity.avatar"}}`)

	for _, actor := range []identity.Actor{
		{},
		{ID: 4, Permissions: map[string]bool{PermissionBadgeManage: true}},
	} {
		if _, err := admin.PreviewRequirementText(context.Background(), actor, raw); !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("actor %+v: err = %v, want permission denied", actor, err)
		}
	}
}

// 非法规则：错误类型与保存路径同源（`ErrBadgeRuleInvalid` → 422）。
func TestPreviewRequirementTextRejectsInvalidRule(t *testing.T) {
	admin := NewAdminService(newFakeStore())

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"v":1}`),                                   // 缺 root
		json.RawMessage(`{"v":1,"root":{"type":"nope.condition"}}`),  // 未知条件
		json.RawMessage(`{"v":2,"root":{"type":"identity.avatar"}}`), // 版本不支持
		json.RawMessage(`{"v":1,"root":{"op":"all","children":[]}}`), // 空组
	} {
		_, err := admin.PreviewRequirementText(context.Background(), previewActor(), raw)
		if !errors.Is(err, ErrBadgeRuleInvalid) {
			t.Fatalf("rule %s: err = %v, want ErrBadgeRuleInvalid", raw, err)
		}
	}
}

// 空规则：手工徽章的固定句（不是错误）。
func TestPreviewRequirementTextNullRuleIsManual(t *testing.T) {
	admin := NewAdminService(newFakeStore())
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		text, err := admin.PreviewRequirementText(context.Background(), previewActor(), raw)
		if err != nil {
			t.Fatalf("PreviewRequirementText(%s): %v", raw, err)
		}
		if text.Zh != manualRequirementZh || text.En != manualRequirementEn {
			t.Fatalf("null rule = %+v, want manual sentence", text)
		}
	}
}
