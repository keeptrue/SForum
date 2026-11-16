import { useAuthSession } from '~/composables/identity/useAuthSession'
import { canWithUser } from '~/utils/identity/permissionCheck'
import type { CurrentUser } from './useAuthSession'

// 论坛权限键，与后端 seeds.go 常量保持一致。
export const FORUM_PERMISSIONS = {
  topicCreate: 'topic.create',
  topicEditOwn: 'topic.edit_own',
  topicEditAny: 'topic.edit_any',
  topicRevisionViewAny: 'topic.revision.view_any',
  topicDeleteOwn: 'topic.delete_own',
  topicDeleteAny: 'topic.delete_any',
  topicLock: 'topic.lock',
  topicPin: 'topic.pin',
  postCreate: 'post.create',
  postEditOwn: 'post.edit_own',
  postEditAny: 'post.edit_any',
  postRevisionViewAny: 'post.revision.view_any',
  postDeleteOwn: 'post.delete_own',
  postDeleteAny: 'post.delete_any',
  settingsManage: 'settings.manage',
  // 后台区域准入：与 API 侧 identity.PermissionAdminAccess 同一权威键，
  // 所有 /admin/* 端点均以它为准（super_admin 经 can() 绕过）。
  adminAccess: 'admin.access',
  settingsSiteManage: 'settings.site.manage',
  settingsMailManage: 'settings.mail.manage',
  settingsNotificationsManage: 'settings.notifications.manage',
  settingsAvatarManage: 'settings.avatar.manage',
  settingsAppearanceManage: 'settings.appearance.manage',
  attachmentUpload: 'attachment.upload',
  attachmentSettingsManage: 'attachment.settings.manage',
  attachmentUploadPolicyManage: 'attachment.upload_policy.manage',
  roleManage: 'role.manage',
  userView: 'user.view',
  userPermissionOverride: 'user.permission_override',
  forumSettingsManage: 'forum.settings.manage',
  moderationManage: 'moderation.manage',
  moderationReview: 'moderation.review',
  moderationViewIp: 'moderation.view_ip'
} as const

/**
 * 权限辅助 composable。基于当前登录用户的 permissions 数组判定。
 * 注意：前端权限判定只是 UX 辅助，后端 policy 检查才是权威。
 *
 * `permissions` 是后端下发的**已展开、已扣除**的最终集合，所以这里只查表，
 * 不再自己按父权限展开 —— 前端拿不到 denied 信息，自行展开会把管理员 deny
 * 掉的子权限放行（F-1）。语义与后端 `Actor.Can()` 同构。
 */
export function usePermissions() {
  const { user } = useAuthSession()

  function can(permission: string): boolean {
    return canWithUser(user.value, permission)
  }

  function canAny(permissions: string[]): boolean {
    return permissions.some((permission) => can(permission))
  }

  // 是否可编辑指定主题：版主任意编辑，或作者凭 topic.edit_own。
  function canEditTopic(topic: { authorUserId: number }): boolean {
    if (can(FORUM_PERMISSIONS.topicEditAny)) {
      return true
    }
    return Boolean(user.value?.id === topic.authorUserId && can(FORUM_PERMISSIONS.topicEditOwn))
  }

  // 是否可删除指定主题：版主凭 topic.delete_any，或作者凭 topic.delete_own。
  function canDeleteTopic(topic: { authorUserId: number }): boolean {
    if (can(FORUM_PERMISSIONS.topicDeleteAny)) {
      return true
    }
    return Boolean(user.value?.id === topic.authorUserId && can(FORUM_PERMISSIONS.topicDeleteOwn))
  }

  // 是否可编辑/删除评论：版主或作者。
  function canEditComment(comment: { authorUserId: number }): boolean {
    if (can(FORUM_PERMISSIONS.postEditAny)) {
      return true
    }
    return Boolean(user.value?.id === comment.authorUserId && can(FORUM_PERMISSIONS.postEditOwn))
  }

  function canDeleteComment(comment: { authorUserId: number }): boolean {
    if (can(FORUM_PERMISSIONS.postDeleteAny)) {
      return true
    }
    return Boolean(user.value?.id === comment.authorUserId && can(FORUM_PERMISSIONS.postDeleteOwn))
  }

  return {
    user,
    can,
    canAny,
    canEditTopic,
    canDeleteTopic,
    canEditComment,
    canDeleteComment
  }
}

export type PermissionChecker = ReturnType<typeof usePermissions>

export function hasPermission(user: CurrentUser | null, permission: string): boolean {
  return canWithUser(user, permission)
}
