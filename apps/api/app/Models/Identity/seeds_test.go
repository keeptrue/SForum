package identity

import (
	"slices"
	"testing"
)

func TestSystemRoles(t *testing.T) {
	if RoleSuperAdmin != "super_admin" {
		t.Fatalf("unexpected super admin role key: %q", RoleSuperAdmin)
	}
	if RoleMember != "member" {
		t.Fatalf("unexpected member role key: %q", RoleMember)
	}
	if RoleModerator != "moderator" || RoleOperator != "operator" || RoleTechAdmin != "tech_admin" {
		t.Fatalf("unexpected template role keys: %q %q %q", RoleModerator, RoleOperator, RoleTechAdmin)
	}
	if RoleBuiltinAuto != "builtin_auto" {
		t.Fatalf("unexpected builtin auto role key: %q", RoleBuiltinAuto)
	}
	for _, key := range []string{RoleSuperAdmin, RoleMember, RoleModerator, RoleOperator, RoleTechAdmin, RoleBuiltinAuto} {
		if !IsBuiltInSystemRole(key) {
			t.Fatalf("expected %s to be a built-in system role", key)
		}
	}
	if IsBuiltInSystemRole("custom_group") {
		t.Fatal("custom roles must not be treated as built-in system roles")
	}
}

func TestSeedRoleTemplatesPermissionPacks(t *testing.T) {
	catalog := map[string]bool{}
	for _, permission := range SeedPermissions {
		catalog[permission.Key] = true
	}

	byKey := map[string]SeedRoleTemplate{}
	for _, template := range SeedRoleTemplates {
		if template.Key == "" || template.Alias == "" || template.Description == "" {
			t.Fatalf("template must declare key/alias/description: %#v", template)
		}
		if _, exists := byKey[template.Key]; exists {
			t.Fatalf("duplicate template role key: %s", template.Key)
		}
		byKey[template.Key] = template

		seen := map[string]bool{}
		for _, key := range template.PermissionKeys {
			if !catalog[key] {
				t.Fatalf("template %s references unknown permission %s", template.Key, key)
			}
			if seen[key] {
				t.Fatalf("template %s has duplicate permission %s", template.Key, key)
			}
			seen[key] = true
		}
		if !slices.Contains(template.PermissionKeys, PermissionAdminAccess) {
			t.Fatalf("template %s should include admin.access for admin chrome", template.Key)
		}
	}

	moderator, ok := byKey[RoleModerator]
	if !ok {
		t.Fatal("expected moderator template")
	}
	for _, key := range []string{
		PermissionModerationReview,
		PermissionModerationViewIP,
		PermissionTopicLock,
		PermissionTopicPin,
		PermissionTopicEditAny,
		PermissionTopicRevisionViewAny,
		PermissionTopicDeleteAny,
		PermissionPostEditAny,
		PermissionPostRevisionViewAny,
		PermissionPostDeleteAny,
		PermissionUserBan,
		PermissionBadgeView,
	} {
		if !slices.Contains(moderator.PermissionKeys, key) {
			t.Fatalf("moderator missing %s", key)
		}
	}
	// 版主不应拿到站点设置 / 技术根能力。
	for _, key := range []string{
		PermissionSettingsSiteManage,
		PermissionDatabaseManage,
		PermissionUserPermissionOverride,
		PermissionRoleManage,
	} {
		if slices.Contains(moderator.PermissionKeys, key) {
			t.Fatalf("moderator should not include %s", key)
		}
	}

	operator, ok := byKey[RoleOperator]
	if !ok {
		t.Fatal("expected operator template")
	}
	for _, key := range []string{
		PermissionUserView,
		PermissionUserManage,
		PermissionSettingsSiteManage,
		PermissionSettingsMailManage,
		PermissionSettingsAvatarManage,
		PermissionSettingsAppearanceManage,
		PermissionSettingsNotificationsManage,
		PermissionForumSettingsManage,
		PermissionSEOManage,
		PermissionCategoryManage,
		PermissionTagManage,
		PermissionAttachmentManage,
		PermissionAttachmentSettings,
		PermissionAttachmentUploadPolicyManage,
		PermissionBadgeView,
		PermissionBadgeManage,
	} {
		if !slices.Contains(operator.PermissionKeys, key) {
			t.Fatalf("operator missing %s", key)
		}
	}
	// 运营默认不持有个人权限例外与技术发布类能力。
	for _, key := range []string{
		PermissionUserPermissionOverride,
		PermissionDatabaseManage,
		PermissionJobsManage,
		PermissionRoleManage,
	} {
		if slices.Contains(operator.PermissionKeys, key) {
			t.Fatalf("operator should not include %s", key)
		}
	}

	tech, ok := byKey[RoleTechAdmin]
	if !ok {
		t.Fatal("expected tech_admin template")
	}
	for _, key := range []string{
		PermissionExtensionView,
		PermissionExtensionPluginManage,
		PermissionExtensionThemeManage,
		PermissionJobsView,
		PermissionJobsManage,
		PermissionSearchManage,
		PermissionDatabaseManage,
		PermissionAttachmentSettings,
	} {
		if !slices.Contains(tech.PermissionKeys, key) {
			t.Fatalf("tech_admin missing %s", key)
		}
	}
	// 技术管理不默认拿用户改组与内容审核全能。
	for _, key := range []string{
		PermissionUserManage,
		PermissionUserPermissionOverride,
		PermissionModerationReview,
		PermissionSettingsSiteManage,
	} {
		if slices.Contains(tech.PermissionKeys, key) {
			t.Fatalf("tech_admin should not include %s", key)
		}
	}
}

func TestSeedMemberPermissionsStayNarrow(t *testing.T) {
	catalog := map[string]bool{}
	for _, permission := range SeedPermissions {
		catalog[permission.Key] = true
	}
	for _, key := range SeedMemberPermissions {
		if !catalog[key] {
			t.Fatalf("member seed references unknown permission %s", key)
		}
	}
	for _, key := range []string{
		PermissionTopicCreate,
		PermissionTopicEditOwn,
		PermissionTopicDeleteOwn,
		PermissionPostCreate,
		PermissionPostEditOwn,
		PermissionPostDeleteOwn,
		PermissionAttachmentUpload,
	} {
		if !slices.Contains(SeedMemberPermissions, key) {
			t.Fatalf("member missing %s", key)
		}
	}
	if slices.Contains(SeedMemberPermissions, PermissionAdminAccess) {
		t.Fatal("member must not receive admin.access by default")
	}
}

func TestDefaultPermissionsContainAdminAccess(t *testing.T) {
	found := false
	for _, permission := range SeedPermissions {
		if permission.Key == PermissionAdminAccess {
			found = true
		}
		if permission.Key == "" || permission.Module == "" {
			t.Fatalf("permission must have key and module: %#v", permission)
		}
	}
	if !found {
		t.Fatal("expected admin.access seed permission")
	}
}

func TestDefaultPermissionsContainTagManage(t *testing.T) {
	found := false
	for _, permission := range SeedPermissions {
		if permission.Key == PermissionTagManage {
			found = true
			if permission.Module != "forum" {
				t.Fatalf("expected tag.manage to belong to forum module, got %q", permission.Module)
			}
		}
	}
	if !found {
		t.Fatal("expected tag.manage seed permission")
	}
}

func TestModerationPermissionsRemainIndependent(t *testing.T) {
	found := map[string]bool{}
	for _, permission := range SeedPermissions {
		if permission.Module == "moderation" {
			found[permission.Key] = true
		}
	}

	if !found[PermissionModerationManage] {
		t.Fatal("expected moderation.manage seed permission")
	}
	if !found[PermissionModerationReview] {
		t.Fatal("expected moderation.review seed permission")
	}
	if !found[PermissionModerationViewIP] {
		t.Fatal("expected moderation.view_ip seed permission")
	}
	if PermissionModerationManage == PermissionModerationReview {
		t.Fatal("moderation permissions must remain independent")
	}
}

func TestSeedPermissionsCoverCurrentAdminAndForumSurfaces(t *testing.T) {
	required := []string{
		PermissionAdminAccess,
		PermissionRoleManage,
		PermissionUserView,
		PermissionUserManage,
		PermissionUserPermissionOverride,
		PermissionUserBan,
		PermissionCategoryManage,
		PermissionTagManage,
		PermissionBadgeView,
		PermissionBadgeManage,
		PermissionTopicCreate,
		PermissionTopicEditOwn,
		PermissionTopicEditAny,
		PermissionTopicRevisionViewAny,
		PermissionTopicDeleteOwn,
		PermissionTopicDeleteAny,
		PermissionTopicLock,
		PermissionTopicPin,
		PermissionPostCreate,
		PermissionPostEditOwn,
		PermissionPostEditAny,
		PermissionPostRevisionViewAny,
		PermissionPostDeleteOwn,
		PermissionPostDeleteAny,
		PermissionModerationManage,
		PermissionModerationReview,
		PermissionModerationViewIP,
		PermissionSettingsManage,
		PermissionSettingsSiteManage,
		PermissionSettingsMailManage,
		PermissionSettingsAvatarManage,
		PermissionSettingsAppearanceManage,
		PermissionSettingsNotificationsManage,
		PermissionForumSettingsManage,
		PermissionSEOManage,
		PermissionAttachmentUpload,
		PermissionAttachmentManage,
		PermissionAttachmentSettings,
		PermissionAttachmentUploadPolicyManage,
		PermissionExtensionManage,
		PermissionExtensionView,
		PermissionExtensionPluginManage,
		PermissionExtensionThemeManage,
		PermissionDatabaseManage,
		PermissionSearchManage,
		PermissionJobsView,
		PermissionJobsManage,
		PermissionEntityMetaManage,
		PermissionIdentityProviderManage,
		PermissionAIManage,
		PermissionTrustManage,
	}

	found := map[string]SeedPermission{}
	for _, permission := range SeedPermissions {
		if _, exists := found[permission.Key]; exists {
			t.Fatalf("duplicate seed permission key: %s", permission.Key)
		}
		found[permission.Key] = permission
	}

	for _, key := range required {
		permission, ok := found[key]
		if !ok {
			t.Fatalf("expected seed permission %s", key)
		}
		if permission.Module == "" || permission.Description == "" {
			t.Fatalf("permission %s must declare module and description", key)
		}
	}

	if len(found) != len(required) {
		t.Fatalf("seed catalog size drifted: got %d want %d", len(found), len(required))
	}
}
