package conditions_test

import (
	"fmt"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

func TestObjectEngagement(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "engager", 30)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (id, category_id, author_user_id) VALUES (7, (SELECT id FROM categories WHERE slug='general'), $1)`, userID)
	f.exec(t, `INSERT INTO content_reactions (user_id, target_type, target_id, reaction) VALUES ($1,'topic',7,'like')`, userID)
	// 私密收藏：与 bookmark_count 同口径，不算「收藏过」。
	f.exec(t, `INSERT INTO content_bookmarks (user_id, target_type, target_id, visibility) VALUES ($1,'topic',7,'private')`, userID)
	// 自己评论自己的主题（P-11：算）。
	f.exec(t, `INSERT INTO comments (topic_id, author_user_id, status) VALUES (7, $1, 'deleted')`, userID)

	// 私密收藏也算「收藏过」：写路径与 Recompute 都不按 visibility 过滤（task-6 ②）。
	cases := map[string]bool{
		`like`:     true,
		`bookmark`: true,
		`comment`:  true,
	}
	for action, want := range cases {
		rule := fmt.Sprintf(`{"v":1,"root":{"type":"object.engagement","params":{"action":%q,"topicId":7}}}`, action)
		matched, result := f.eval(t, userID, rule)
		if matched != want || result.Incomplete {
			t.Fatalf("%s 期望 %v，得到 %+v", action, want, result)
		}
	}
}

func TestObjectAuthoring(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "writer", 30)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general'), ('news')`)
	f.exec(t, `INSERT INTO tags (slug) VALUES ('go')`)
	f.exec(t, `INSERT INTO topics (id, category_id, author_user_id, created_at) VALUES
	  (1, (SELECT id FROM categories WHERE slug='general'), $1, now()),
	  (2, (SELECT id FROM categories WHERE slug='general'), $1, now() - interval '40 days'),
	  (3, (SELECT id FROM categories WHERE slug='news'), $1, now())`, userID)
	f.exec(t, `INSERT INTO topic_tags (topic_id, tag_id) VALUES (1, (SELECT id FROM tags WHERE slug='go'))`)

	categoryRule := `{"v":1,"root":{"type":"object.authoring","params":{"targetKind":"category","targetSlug":"general","window":{"kind":"all"}},"cmp":{"op":"gte","value":2}}}`
	if matched, result := f.eval(t, userID, categoryRule); !matched || result.Incomplete {
		t.Fatalf("general 分类累计 2 篇应满足: %+v", result)
	}
	categoryWindow := `{"v":1,"root":{"type":"object.authoring","params":{"targetKind":"category","targetSlug":"general","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":2}}}`
	if matched, result := f.eval(t, userID, categoryWindow); matched || result.Incomplete {
		t.Fatalf("general 分类 30 天内只有 1 篇，不应满足: %+v", result)
	}
	tagRule := `{"v":1,"root":{"type":"object.authoring","params":{"targetKind":"tag","targetSlug":"go","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, userID, tagRule); !matched || result.Incomplete {
		t.Fatalf("go 标签 1 篇应满足: %+v", result)
	}
	// 引用不存在的 slug：返回 0（不是错误）——存在性由保存路径负责。
	missingRule := `{"v":1,"root":{"type":"object.authoring","params":{"targetKind":"tag","targetSlug":"rust","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, userID, missingRule); matched || result.Incomplete {
		t.Fatalf("不存在的标签应为 0 且确定: %+v", result)
	}
}

func TestIdentityConditions(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "ident", 10)
	f.exec(t, `INSERT INTO user_activity (user_id) VALUES ($1)`, userID)

	ageRule := `{"v":1,"root":{"type":"identity.account_age","cmp":{"op":"gte","value":10}}}`
	if matched, result := f.eval(t, userID, ageRule); !matched || result.Incomplete {
		t.Fatalf("注册 10 天应满足 gte 10: %+v", result)
	}
	ageDenied := `{"v":1,"root":{"type":"identity.account_age","cmp":{"op":"gte","value":11}}}`
	if matched, result := f.eval(t, userID, ageDenied); matched || result.Incomplete {
		t.Fatalf("注册 10 天不应满足 gte 11: %+v", result)
	}

	verified := `{"v":1,"root":{"type":"identity.email_verified"}}`
	if matched, result := f.eval(t, userID, verified); matched || result.Incomplete {
		t.Fatalf("未验证邮箱不应满足: %+v", result)
	}
	f.exec(t, `UPDATE users SET email_verified_at = now() WHERE id = $1`, userID)
	if matched, result := f.eval(t, userID, verified); !matched || result.Incomplete {
		t.Fatalf("已验证邮箱应满足: %+v", result)
	}

	// oauth_link：只认 active。
	f.exec(t, `INSERT INTO identity_external_links (user_id, provider_id, status) VALUES ($1,'sforum.auth-github','unlinked')`, userID)
	oauth := `{"v":1,"root":{"type":"identity.oauth_link","params":{"providerId":"sforum.auth-github"}}}`
	if matched, result := f.eval(t, userID, oauth); matched || result.Incomplete {
		t.Fatalf("unlinked 的绑定不应算已绑定: %+v", result)
	}
	f.exec(t, `UPDATE identity_external_links SET status='active' WHERE user_id = $1`, userID)
	if matched, result := f.eval(t, userID, oauth); !matched || result.Incomplete {
		t.Fatalf("active 的绑定应算已绑定: %+v", result)
	}

	// role：停用角色不算持有。
	f.exec(t, `INSERT INTO roles (id, key, is_enabled) VALUES (1,'moderator',TRUE),(2,'legacy',FALSE)`)
	f.exec(t, `INSERT INTO user_roles (user_id, role_id) VALUES ($1,1),($1,2)`, userID)
	role := `{"v":1,"root":{"type":"identity.role","params":{"roleKey":"moderator"}}}`
	if matched, result := f.eval(t, userID, role); !matched || result.Incomplete {
		t.Fatalf("持有 moderator 应满足: %+v", result)
	}
	disabledRole := `{"v":1,"root":{"type":"identity.role","params":{"roleKey":"legacy"}}}`
	if matched, result := f.eval(t, userID, disabledRole); matched || result.Incomplete {
		t.Fatalf("停用角色不应算持有: %+v", result)
	}

	avatar := `{"v":1,"root":{"type":"identity.avatar"}}`
	if matched, result := f.eval(t, userID, avatar); matched || result.Incomplete {
		t.Fatalf("没有头像不应满足: %+v", result)
	}
	f.exec(t, `INSERT INTO user_profiles (user_id, avatar_attachment_id) VALUES ($1, 42)`, userID)
	if matched, result := f.eval(t, userID, avatar); !matched || result.Incomplete {
		t.Fatalf("有头像应满足: %+v", result)
	}
}

func TestContentAndBadgeConditions(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "content", 30)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (category_id, author_user_id, status, is_pinned) VALUES
	  ((SELECT id FROM categories WHERE slug='general'), $1, 'active', TRUE),
	  ((SELECT id FROM categories WHERE slug='general'), $1, 'hidden', FALSE),
	  ((SELECT id FROM categories WHERE slug='general'), $1, 'deleted', FALSE)`, userID)

	pinned := `{"v":1,"root":{"type":"content.pinned","cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, userID, pinned); !matched || result.Incomplete {
		t.Fatalf("有置顶主题应满足: %+v", result)
	}
	hidden := `{"v":1,"root":{"type":"content.hidden","cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, userID, hidden); !matched || result.Incomplete {
		t.Fatalf("有隐藏主题应满足: %+v", result)
	}
	noHidden := `{"v":1,"root":{"op":"not","children":[{"type":"content.hidden","cmp":{"op":"gte","value":1}}]}}`
	if matched, result := f.eval(t, userID, noHidden); matched || result.Incomplete {
		t.Fatalf("有隐藏主题时 not(hidden) 不应满足: %+v", result)
	}

	f.exec(t, `INSERT INTO badges (slug) VALUES ('veteran')`)
	owned := `{"v":1,"root":{"type":"badge.owned","params":{"slug":"veteran"}}}`
	if matched, result := f.eval(t, userID, owned); matched || result.Incomplete {
		t.Fatalf("未持有时不应满足: %+v", result)
	}
	f.exec(t, `INSERT INTO user_badges (user_id, badge_id) VALUES ($1, (SELECT id FROM badges WHERE slug='veteran'))`, userID)
	if matched, result := f.eval(t, userID, owned); !matched || result.Incomplete {
		t.Fatalf("持有时应满足: %+v", result)
	}
}

func TestFactCountCumulativeWindowAndClamp(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "facts", 30)
	// 累计：允许为负，读侧钳零。
	f.exec(t, `INSERT INTO user_fact_counters (user_id, fact_key, variant, total) VALUES ($1,'wiki.edit','',-3)`, userID)
	// 声明过的 factKey 才能判定；未声明的一律 unknown（见文件末尾那条测试）。
	// 钳零只能在解析器层验证：规则引擎的 cmp.value 下界是 1（`lte 0` 本身非法），
	// 所以「负值会不会漏到规则层」必须在 Value 上断言，不能靠 Eval。
	value, err := f.resolver(t, userID, "wiki.edit").Resolve(f.ctx, ruleengine.Leaf{
		Type:   conditions.KeyFactCount,
		Params: map[string]any{"factKey": "wiki.edit", "window": map[string]any{"kind": "all"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value.Kind != ruleengine.ValueCount || value.Count != 0 {
		t.Fatalf("负累计应钳成 0，得到 %+v", value)
	}
	denied := `{"v":1,"root":{"type":"fact.count","params":{"factKey":"wiki.edit","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, userID, denied, "wiki.edit"); matched || result.Incomplete {
		t.Fatalf("钳零后不应满足 gte 1: %+v", result)
	}

	// 窗口：40 天前 +2、今天 +1。
	f.exec(t, `INSERT INTO badge_facts (user_id, fact_key, variant, delta, occurred_at) VALUES
	  ($1,'wiki.edit','',2, now() - interval '40 days'),
	  ($1,'wiki.edit','',1, now())`, userID)
	windowed := `{"v":1,"root":{"type":"fact.count","params":{"factKey":"wiki.edit","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":2}}}`
	if matched, result := f.eval(t, userID, windowed, "wiki.edit"); matched || result.Incomplete {
		t.Fatalf("30 天窗口内只有 1，不应满足 gte 2: %+v", result)
	}
	windowedOne := `{"v":1,"root":{"type":"fact.count","params":{"factKey":"wiki.edit","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, userID, windowedOne, "wiki.edit"); !matched || result.Incomplete {
		t.Fatalf("30 天窗口内 1 应满足 gte 1: %+v", result)
	}
	// 完全没有事实行 → 0（不是 unknown）。
	// 已声明但没有事实行 → 0（确定值，不是 unknown）。variant 省略即默认变体：
	// M1 的 enum 选项为空，显式写 variant 会被校验拒绝。
	noFacts := `{"v":1,"root":{"type":"fact.count","params":{"factKey":"wiki.edit","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}`
	if matched, result := f.eval(t, userID, noFacts, "wiki.edit"); matched || result.Incomplete {
		t.Fatalf("没有事实行应为 0 且确定: %+v", result)
	}
}

// TestFactCountUndeclaredKeyIsUnknown 是 task-6 ③ 的回归：未声明的 factKey 属于
// 「无法判定」，必须让引擎转 unknown（fail-closed），不能当 0。
//
// 为什么这条重要：M3 卸载插件后，一条 `lte` 规则会把 0 判成满足、`not` 会把 false
// 判成 true，两者都是 fail-open。M1 传空声明集，所以任何 fact.count 叶子都是 unknown。
func TestFactCountUndeclaredKeyIsUnknown(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "undeclared", 30)
	f.exec(t, `INSERT INTO user_fact_counters (user_id, fact_key, variant, total) VALUES ($1,'wiki.edit','',5)`, userID)

	// 不声明任何 factKey（等价于 M1 的装配）。
	resolver := f.resolver(t, userID)
	_, err := resolver.Resolve(f.ctx, ruleengine.Leaf{
		Type:   conditions.KeyFactCount,
		Params: map[string]any{"factKey": "wiki.edit", "window": map[string]any{"kind": "all"}},
	})
	if err == nil {
		t.Fatal("未声明的 factKey 必须返 error（引擎才能转 unknown），不能返回 0")
	}

	// 端到端：引擎把 error 收成 unknown，结论是不授予且 incomplete。
	rule := `{"v":1,"root":{"type":"fact.count","params":{"factKey":"wiki.edit","window":{"kind":"all"}},"cmp":{"op":"lte","value":100}}}`
	node, err := ruleengine.Decode([]byte(rule))
	if err != nil {
		t.Fatal(err)
	}
	if err := ruleengine.Validate(node, conditions.Core()); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Prepare(f.ctx, node); err != nil {
		t.Fatalf("Prepare 不应因单条件失败而报错: %v", err)
	}
	result, err := ruleengine.Eval(f.ctx, node, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched || !result.Incomplete {
		t.Fatalf("未声明 factKey 必须 fail-closed 成 unknown，得到 %+v", result)
	}
}

// TestObjectEngagementBookmarkIgnoresVisibility 是 task-6 ② 的回归：私密收藏也算
// 「收藏过」——写路径与 Recompute 都不按 visibility 过滤，resolver 是错的那一方。
func TestObjectEngagementBookmarkIgnoresVisibility(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "private-bookmark", 30)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (id, category_id, author_user_id) VALUES (3, (SELECT id FROM categories WHERE slug='general'), $1)`, userID)
	f.exec(t, `INSERT INTO content_bookmarks (user_id, target_type, target_id, visibility) VALUES ($1,'topic',3,'private')`, userID)

	rule := `{"v":1,"root":{"type":"object.engagement","params":{"action":"bookmark","topicId":3}}}`
	if matched, result := f.eval(t, userID, rule); !matched || result.Incomplete {
		t.Fatalf("私密收藏也应算「收藏过」: %+v", result)
	}
}

// TestBookmarkEngagementAndCountIgnoreVisibility 是 task-6 ② 的完整夹具：
// 同一用户 3 条收藏（2 private + 1 public），收藏判定必须全算；把可见性全改成 public
// 后判定不变（可见性不是「收藏过」的维度）。计数列 `bookmark_count` 由写路径维护，
// 这里用同一张表的行数证明「计数侧本来就不看 visibility」。
func TestBookmarkEngagementAndCountIgnoreVisibility(t *testing.T) {
	f := newFixture(t)
	userID := f.user(t, "bookmarks", 30)
	f.exec(t, `INSERT INTO categories (slug) VALUES ('general')`)
	f.exec(t, `INSERT INTO topics (id, category_id, author_user_id) VALUES (5, (SELECT id FROM categories WHERE slug='general'), $1)`, userID)
	f.exec(t, `INSERT INTO content_bookmarks (user_id, target_type, target_id, visibility) VALUES
	  ($1,'topic',5,'private'), ($1,'topic',6,'private'), ($1,'topic',7,'public')`, userID)

	rule := `{"v":1,"root":{"type":"object.engagement","params":{"action":"bookmark","topicId":5}}}`
	if matched, result := f.eval(t, userID, rule); !matched || result.Incomplete {
		t.Fatalf("私密收藏也必须算「收藏过」: %+v", result)
	}
	var count int64
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM content_bookmarks WHERE user_id = $1`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("计数侧应数全部 3 条（不看 visibility），得到 %d", count)
	}
	f.exec(t, `UPDATE content_bookmarks SET visibility='public' WHERE user_id = $1`, userID)
	if matched, result := f.eval(t, userID, rule); !matched || result.Incomplete {
		t.Fatalf("改可见性不应改变收藏判定: %+v", result)
	}
}
