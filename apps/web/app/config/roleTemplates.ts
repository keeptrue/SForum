/**
 * 内置角色模板权限包，与 apps/api/app/Models/Identity/seeds.go 的 SeedRoleTemplates 对齐。
 * 后台「应用模板」在创建自定义组时预填权限；系统模板角色本身由 migration seed。
 *
 * 注意：这里是**独立的一份定义**，不是从 seeds.go 生成的。新增或调整角色权限时必须
 * 同时改两处，否则后台模板与真实种子权限会漂移（模板给得比种子多 = 新建组凭空多权限）。
 * 当前对应关系（改 seeds.go 行号时同步更新本注释）：
 * - moderator：seeds.go:167 起，其中 `badge.view` 在 seeds.go:184
 * - operator：seeds.go:188 起，其中 `badge.view` / `badge.manage` 在 seeds.go:212-213
 */
export type RoleTemplateDefinition = {
  key: string
  /** i18n：admin.roleCatalog.<key>.alias */
  aliasKey: string
  /** i18n：admin.roleCatalog.<key>.description */
  descriptionKey: string
  permissionKeys: readonly string[]
}

export const ROLE_TEMPLATE_DEFINITIONS = [
  {
    key: 'moderator',
    aliasKey: 'admin.roleCatalog.moderator.alias',
    descriptionKey: 'admin.roleCatalog.moderator.description',
    permissionKeys: [
      'admin.access',
      'moderation.review',
      'moderation.view_ip',
      'topic.lock',
      'topic.pin',
      'topic.edit_any',
      'topic.revision.view_any',
      'topic.delete_any',
      'post.edit_any',
      'post.revision.view_any',
      'post.delete_any',
      'user.ban',
      // 只读：版主能看徽章目录与授予记录，不能改规则或手动授予。对应 seeds.go:184，改一边必须改另一边。
      'badge.view'
    ]
  },
  {
    key: 'operator',
    aliasKey: 'admin.roleCatalog.operator.alias',
    descriptionKey: 'admin.roleCatalog.operator.description',
    permissionKeys: [
      'admin.access',
      'user.view',
      'user.manage',
      'settings.site.manage',
      'settings.mail.manage',
      'settings.notifications.manage',
      'settings.avatar.manage',
      'settings.appearance.manage',
      'forum.settings.manage',
      'seo.manage',
      'category.manage',
      'tag.manage',
      'attachment.manage',
      'attachment.settings.manage',
      // 与 seeds.go SeedRoleTemplates operator 对齐：Host 登录方式激活/探测/重置。
      'identity.provider.manage',
      'entity_meta.manage',
      // 徽章是运营资产：站点运营可配置规则并手动授予。对应 seeds.go:212-213，改一边必须改另一边。
      'badge.view',
      'badge.manage',
      // 信任等级阈值是站点策略：与 badge.manage 同一批岗位（operator + super_admin）。
      // 对应迁移 202611100004_trust_manage_permission.sql，改一边必须改另一边。
      'trust.manage'
    ]
  },
  {
    key: 'tech_admin',
    aliasKey: 'admin.roleCatalog.tech_admin.alias',
    descriptionKey: 'admin.roleCatalog.tech_admin.description',
    permissionKeys: [
      'admin.access',
      'extension.view',
      'extension.plugin.manage',
      'extension.theme.manage',
      'jobs.view',
      'jobs.manage',
      'search.manage',
      'database.manage',
      'attachment.settings.manage'
    ]
  }
] as const satisfies readonly RoleTemplateDefinition[]

export function findRoleTemplate(key: string): RoleTemplateDefinition | undefined {
  return ROLE_TEMPLATE_DEFINITIONS.find(template => template.key === key)
}
