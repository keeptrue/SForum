import {
  forumTopicExtensionActionLabel,
  type ForumCommentExtensionAction,
  type ForumTopicExtensionAction
} from './forumTaxonomy'

export type TopicActionTone = 'default' | 'danger'

export type TopicActionLabels = {
  edit: string
  delete: string
  lock: string
  unlock: string
  pin: string
  unpin: string
  hide: string
  restore: string
  report: string
}

export type TopicExtensionActionDescriptor = {
  extensionId: string
  id: string
  label: string
  icon?: string
  confirm?: boolean
}

export type TopicActionMenuItem = {
  id: string
  label: string
  icon: string
  tone?: TopicActionTone
  requiresConfirm?: boolean
  extension?: {
    extensionId: string
    actionId: string
  }
}

export type CommentActionMenuItem = {
  label: string
  value: string
  icon?: string
}

export type CommentActionMenuInput = {
  canReply: boolean
  canEdit: boolean
  canDelete: boolean
  canReport: boolean
  labels: {
    reply: string
    link: string
    quoteShortcode: string
    edit: string
    delete: string
    report: string
  }
  extensions: CommentActionMenuItem[]
}

export type TopicActionMenuInput = {
  canEdit: boolean
  canDelete: boolean
  canLock: boolean
  canPin: boolean
  canModerate: boolean
  canReport: boolean
  locked: boolean
  pinned: boolean
  hidden: boolean
  labels: TopicActionLabels
  extensions: TopicExtensionActionDescriptor[]
}

/**
 * 只把路由已经判定过的权限和状态转换成展示描述，不自行推断权限。
 */
export function buildTopicActionMenuItems(input: TopicActionMenuInput): TopicActionMenuItem[] {
  const items: TopicActionMenuItem[] = []

  if (input.canEdit) {
    items.push({ id: 'edit', label: input.labels.edit, icon: 'i-lucide-pencil' })
  }
  if (input.canLock) {
    items.push(input.locked
      ? { id: 'unlock', label: input.labels.unlock, icon: 'i-lucide-lock-open' }
      : { id: 'lock', label: input.labels.lock, icon: 'i-lucide-lock' })
  }
  if (input.canPin) {
    items.push(input.pinned
      ? { id: 'unpin', label: input.labels.unpin, icon: 'i-lucide-pin-off' }
      : { id: 'pin', label: input.labels.pin, icon: 'i-lucide-pin' })
  }
  if (input.canModerate) {
    items.push(input.hidden
      ? { id: 'restore', label: input.labels.restore, icon: 'i-lucide-eye' }
      : { id: 'hide', label: input.labels.hide, icon: 'i-lucide-eye-off' })
  }
  if (input.canDelete) {
    items.push({
      id: 'delete',
      label: input.labels.delete,
      icon: 'i-lucide-trash-2',
      tone: 'danger',
      requiresConfirm: true
    })
  }
  if (input.canReport) {
    items.push({ id: 'report', label: input.labels.report, icon: 'i-lucide-flag' })
  }

  for (const action of input.extensions) {
    items.push({
      id: `extension:${action.extensionId}:${action.id}`,
      label: action.label,
      icon: action.icon || 'i-lucide-plug',
      requiresConfirm: action.confirm,
      extension: {
        extensionId: action.extensionId,
        actionId: action.id
      }
    })
  }

  return items
}

/**
 * 权限仍由路由和 API 决定；这里仅统一评论操作的展示顺序。
 */
export function buildCommentActionMenuItems(input: CommentActionMenuInput): CommentActionMenuItem[] {
  const items: CommentActionMenuItem[] = []
  if (input.canReply) {
    // 评论区只保留回复；引用块由 replyTo 关系在展示层表达，不再提供「引用」入口。
    items.push({ label: input.labels.reply, value: 'reply', icon: 'i-lucide-reply' })
  }
  items.push({ label: input.labels.link, value: 'link', icon: 'i-lucide-link' })
  // 一键复制引用该评论的 shortcode（[comment comment_id="N"][/comment]），供粘贴进编辑器。
  items.push({ label: input.labels.quoteShortcode, value: 'quote-shortcode', icon: 'i-lucide-square-code' })
  if (input.canEdit) items.push({ label: input.labels.edit, value: 'edit', icon: 'i-lucide-pencil' })
  if (input.canDelete) items.push({ label: input.labels.delete, value: 'delete', icon: 'i-lucide-trash-2' })
  if (input.canReport) items.push({ label: input.labels.report, value: 'report', icon: 'i-lucide-flag' })
  return items.concat(input.extensions)
}

/** i18n 翻译函数签名：调用方传入 useI18n().t，保持文案表与菜单模型同层。 */
export type PresentationTranslate = (key: string) => string

export function buildTopicActionLabels(t: PresentationTranslate): TopicActionLabels {
  return {
    edit: t('topicDetail.edit'),
    delete: t('topicDetail.delete'),
    lock: t('topicDetail.lock'),
    unlock: t('topicDetail.unlock'),
    pin: t('topicDetail.pin'),
    unpin: t('topicDetail.unpin'),
    hide: t('topicDetail.hide'),
    restore: t('topicDetail.restore'),
    report: t('topicDetail.report')
  }
}

export function buildCommentActionLabels(
  t: PresentationTranslate,
  state: { deleting?: boolean } = {}
): CommentActionMenuInput['labels'] {
  return {
    reply: t('topicDetail.reply'),
    link: t('topicDetail.commentLink'),
    quoteShortcode: t('topicDetail.copyShortcode'),
    edit: t('topicDetail.edit'),
    delete: state.deleting ? t('topicDetail.deleting') : t('topicDetail.delete'),
    report: t('topicDetail.report')
  }
}

/**
 * 扩展动作的运行态标识：扩展 ID + 动作 ID。
 * 同一扩展内的动作 ID 唯一，加上扩展 ID 后即可全局唯一定位一个动作。
 */
export function topicExtensionActionKey(action: Pick<ForumTopicExtensionAction, 'extensionId' | 'id'>): string {
  return `${action.extensionId}:${action.id}`
}

/** 评论扩展动作与话题扩展动作共用同一套标识规则。 */
export function commentExtensionActionKey(action: Pick<ForumCommentExtensionAction, 'extensionId' | 'id'>): string {
  return `${action.extensionId}:${action.id}`
}

/**
 * 扩展动作的展示文案：按当前语言取值，缺省回落到动作 ID。
 * 语言由调用方传入（页面读 i18n 的 locale），工具层不依赖运行时。
 */
export function topicExtensionActionLabel(action: ForumTopicExtensionAction, locale = 'zh-CN'): string {
  return forumTopicExtensionActionLabel(action, locale)
}
