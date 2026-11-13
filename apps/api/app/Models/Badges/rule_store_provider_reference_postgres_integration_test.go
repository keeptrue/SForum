package badges

import (
	"context"
	"errors"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// provider 引用在**保存期**的判据是「绑定表 ∪ identity registry 已声明」——
// 与 `rule_health` 的 `knownProviders` 同一口径。两个方向都必须钉住：
//
//   - 已声明但零绑定 → 必须能保存。候选面（`refCandidates`）取自 registry 快照，
//     只查绑定表会让界面上唯一可选的 provider 保存必 422，候选与保存自相矛盾；
//   - 既未声明也无绑定 → 仍必须 422 `ErrBadgeRuleReferenceInvalid`。放宽的对象是
//     「合法来源」，不是「拼错的 id」。
func TestBadgeRuleProviderReferenceMatchesRefCandidates(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	decode := func(raw string) ruleengine.Node {
		t.Helper()
		node, err := ruleengine.Decode([]byte(raw))
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		return node
	}
	providerRule := func(providerID string) ruleengine.Node {
		return decode(`{"v":1,"root":{"type":"identity.oauth_link","params":{"providerId":"` + providerID + `"}}}`)
	}

	const declared = "sforum.connect.auth"
	store := NewPostgresStore(fixture.pool)
	store.SetDeclaredProviders(func(context.Context) ([]string, error) {
		return []string{declared}, nil
	})

	// 方向一：registry 已声明、绑定表 0 行 → 保存通过。
	if err := store.ValidateBadgeRule(ctx, 0, providerRule(declared)); err != nil {
		t.Fatalf("已声明但零绑定的 provider 必须允许保存（候选面会给它），得到 %v", err)
	}

	// 方向二：既未声明也无绑定 → 422 的领域错误，不是「放行」也不是 500。
	err := store.ValidateBadgeRule(ctx, 0, providerRule("sforum.connect.authx"))
	if !errors.Is(err, ErrBadgeRuleReferenceInvalid) {
		t.Fatalf("未声明且无绑定的 provider 必须判引用失效（422），得到 %v", err)
	}
	// 把两侧的领域结果打进 -v 输出：报告引用的是可复跑的原始文本，不是转述。
	t.Logf("已声明零绑定 → ValidateBadgeRule error = <nil>；拼错 → %v", err)

	// 装配早期 / 部分测试没有注入 declaredProviders：退化为「只认绑定表」，
	// 既不能 panic，也不能把 knownProviders 变成错误路径。
	bare := NewPostgresStore(fixture.pool)
	if err := bare.ValidateBadgeRule(ctx, 0, providerRule(declared)); !errors.Is(err, ErrBadgeRuleReferenceInvalid) {
		t.Fatalf("未注入 registry 时已声明 provider 应退化为未命中（不是报错），得到 %v", err)
	}
	userID := fixture.user(t, "provider-bound", "active")
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO identity_external_links (user_id, provider_id, status)
		VALUES ($1, 'sforum.connect.bound', 'active')`, userID); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := bare.ValidateBadgeRule(ctx, 0, providerRule("sforum.connect.bound")); err != nil {
		t.Fatalf("未注入 registry 时绑定表里存在的 provider 仍必须可保存，得到 %v", err)
	}
}
