package routes

import (
	"slices"
	"testing"
)

// TestCatalogGuardDeclarationsMatchRuntimePolicy 把 PRD2-01/02/03 的十条路由声明钉在目录里。
//
// 为什么需要这条测试：`core_catalog_gen.go` 是生成物，生成器的策略表（
// scripts/v3-catalog/generate.mjs）才是人手改的地方。只跑 `--check` 只能证明
// 「生成物与生成器一致」，证明不了「生成器写对了」。这里直接断言目录里的
// kind / permissions（有序）/ evaluatorId，任何一侧被改松都会红：
//
//   - 分类/标签读：运行时 canManageForumSettings 是三选一 → permission_any 三键、无求值器；
//   - 后台内容读：运行时 admin.access AND (edit_any OR revision_view_any) → contextual；
//   - 用户级上传策略：运行时 manage AND user.view → contextual。
//
// 同时断言每个 id 在目录里只出现一次：同一 id 重复会让「按 id 查守卫」变成随机结果。
func TestCatalogGuardDeclarationsMatchRuntimePolicy(t *testing.T) {
	type expectation struct {
		kind        CoreGuardKind
		permissions []string
		evaluatorID string
	}
	expected := map[string]expectation{
		// 3 条分类/标签读：三键任一。
		"core.route.forum.admin_categories": {
			kind:        CoreGuardPermissionAny,
			permissions: []string{"category.manage", "tag.manage", "forum.settings.manage"},
		},
		"core.route.forum.admin_category_groups": {
			kind:        CoreGuardPermissionAny,
			permissions: []string{"category.manage", "tag.manage", "forum.settings.manage"},
		},
		"core.route.forum.admin_tags": {
			kind:        CoreGuardPermissionAny,
			permissions: []string{"category.manage", "tag.manage", "forum.settings.manage"},
		},
		// 4 条后台内容读：admin.access 且（编辑任意 或 查看任意修订）。
		"core.route.forum.admin_content_topics": {
			kind:        CoreGuardContextual,
			permissions: []string{"admin.access", "topic.edit_any", "topic.revision.view_any"},
			evaluatorID: "core.guard.forum.content_read",
		},
		"core.route.forum.admin_content_topic": {
			kind:        CoreGuardContextual,
			permissions: []string{"admin.access", "topic.edit_any", "topic.revision.view_any"},
			evaluatorID: "core.guard.forum.content_read",
		},
		"core.route.forum.admin_content_comments": {
			kind:        CoreGuardContextual,
			permissions: []string{"admin.access", "post.edit_any", "post.revision.view_any"},
			evaluatorID: "core.guard.forum.content_read",
		},
		"core.route.forum.admin_content_comment": {
			kind:        CoreGuardContextual,
			permissions: []string{"admin.access", "post.edit_any", "post.revision.view_any"},
			evaluatorID: "core.guard.forum.content_read",
		},
		// 3 条用户级上传策略：manage 且 user.view。
		"core.route.attachments.get_user_upload_policy": {
			kind:        CoreGuardContextual,
			permissions: []string{"attachment.upload_policy.manage", "user.view"},
			evaluatorID: "core.guard.attachments.upload_policy",
		},
		"core.route.attachments.set_user_upload_policy": {
			kind:        CoreGuardContextual,
			permissions: []string{"attachment.upload_policy.manage", "user.view"},
			evaluatorID: "core.guard.attachments.upload_policy",
		},
		"core.route.attachments.delete_user_upload_policy": {
			kind:        CoreGuardContextual,
			permissions: []string{"attachment.upload_policy.manage", "user.view"},
			evaluatorID: "core.guard.attachments.upload_policy",
		},
	}

	seen := map[string]int{}
	for _, route := range CoreRouteCatalog() {
		seen[route.ID]++
		want, ok := expected[route.ID]
		if !ok {
			continue
		}
		guard := route.Guard
		if guard.RouteID != route.ID {
			t.Fatalf("%s: guard.RouteID = %q", route.ID, guard.RouteID)
		}
		if guard.Kind != want.kind {
			t.Fatalf("%s: kind = %q, want %q", route.ID, guard.Kind, want.kind)
		}
		// 有序比较：权限顺序是生成器的输出契约，变了就是有人改了策略表。
		if !slices.Equal(guard.Permissions, want.permissions) {
			t.Fatalf("%s: permissions = %v, want %v", route.ID, guard.Permissions, want.permissions)
		}
		if guard.EvaluatorID != want.evaluatorID {
			t.Fatalf("%s: evaluatorId = %q, want %q", route.ID, guard.EvaluatorID, want.evaluatorID)
		}
	}

	for routeID := range expected {
		if seen[routeID] != 1 {
			t.Fatalf("%s appears %d times in the catalog, want exactly 1", routeID, seen[routeID])
		}
	}
}

// TestRoleScopedUploadPoliciesKeepSinglePermission 反向钉子：角色级上传策略仍然只要 manage。
//
// 这一条防止「为了让用户级通过，把整个前缀都改成 manage + user.view」这类过度收敛：
// 角色级读写在运行时只查 manage（UploadPolicy.ListRoles/SetRole/DeleteRole），
// 多要一个 user.view 会把合法的角色运维挡在外面。
func TestRoleScopedUploadPoliciesKeepSinglePermission(t *testing.T) {
	want := map[string]string{
		"core.route.attachments.list_role_upload_policies": "GET",
		"core.route.attachments.set_role_upload_policy":    "PUT",
		"core.route.attachments.delete_role_upload_policy": "DELETE",
	}
	seen := map[string]bool{}
	for _, route := range CoreRouteCatalog() {
		method, ok := want[route.ID]
		if !ok {
			continue
		}
		seen[route.ID] = true
		if route.Method != method {
			t.Fatalf("%s: method = %q, want %q", route.ID, route.Method, method)
		}
		if route.Guard.Kind != CoreGuardPermissionAny {
			t.Fatalf("%s: kind = %q, want permission_any", route.ID, route.Guard.Kind)
		}
		if !slices.Equal(route.Guard.Permissions, []string{"attachment.upload_policy.manage"}) {
			t.Fatalf("%s: permissions = %v, want [attachment.upload_policy.manage]", route.ID, route.Guard.Permissions)
		}
		if route.Guard.EvaluatorID != "" {
			t.Fatalf("%s: evaluatorId = %q, want empty", route.ID, route.Guard.EvaluatorID)
		}
	}
	for routeID := range want {
		if !seen[routeID] {
			t.Fatalf("%s is missing from the catalog", routeID)
		}
	}
}
