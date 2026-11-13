<script lang="ts">
import SFReactionControl from '~/components/forum/SFReactionControl.vue'
import SFBookmarkControl from '~/components/forum/SFBookmarkControl.vue'
// provide/inject key 与 renderer 类型：父页面 provide 一个"按 comment 渲染内联编辑器"的函数，
// 评论列表通过 inject 在评论原位渲染编辑器/回复框。
// 用字符串 key 方便父页面直接 provide，无需 import Symbol。详情页本地定义相同字符串即可。
export type CommentEditorRenderer = (comment: import('~/utils/forum/forumTaxonomy').ForumComment | null) => unknown
export const COMMENT_EDITOR_RENDERER_KEY = 'sforum-comment-editor-renderer'
</script>

<script setup lang="ts">
import SFCommentUserPreview from '~/components/forum/SFCommentUserPreview.vue'
import SFMentionContent from '~/components/forum/SFMentionContent.vue'
import SFCommentAIActivity from '~/components/forum/SFCommentAIActivity.vue'
import SFCommentAuthorRow from '~/components/forum/SFCommentAuthorRow.vue'
import {
  forumAuthorName,
  type ForumComment,
  type ForumUserSummary
} from '~/utils/forum/forumTaxonomy'
import { userDisplayName } from '~/utils/forum/userDisplay'
import {
  commentBranchPresentation,
  type CommentPresentationMode
} from '~/utils/forum/forumCommentPresentation'
import type { AvatarView } from '~/composables/profile/useProfileApi'

type CommentAction = {
  label: string
  value: string
  icon?: string
}

type CommentMenuEntry = {
  label: string
  icon?: string
  color?: 'error'
  onSelect: (event: Event) => void
}

const props = withDefaults(defineProps<{
  // 当前评论节点同时承载内联编辑器匹配和 tree 模式的递归数据。
  comment?: ForumComment
  presentation?: CommentPresentationMode
  depth?: number
  collapseFromDepth?: number
  author: string
  // content 为纯文本（组件预览/历史用法）；htmlContent 优先，渲染后端已 sanitize 的 HTML。
  content?: string
  htmlContent?: string
  authorLink?: string
  meta?: string
  floorLabel?: string
  avatar?: AvatarView | null
  // 被回复的评论引用：E3 方案用左侧 accent 竖条引用块展示，人名 + 内容预览分两行。
  // author 保持完整的 UserSummary 对象，渲染时统一走 userDisplayName，不再降级为 string。
  replyTo?: { id?: number; author?: ForumUserSummary; excerpt?: string }
  actions?: CommentAction[]
  commentMetaBuilder?: (comment: ForumComment) => string
  commentAuthorLinkBuilder?: (comment: ForumComment) => string
  commentActionsBuilder?: (comment: ForumComment) => CommentAction[]
  /** 正在加载更多回复的评论 id（详情页控制） */
  loadingMoreCommentId?: number | null
  /** 深链定位后的短暂强调高亮（详情页控制，约数秒后清除） */
  flash?: boolean
  /** 主题作者 user id：评论作者与其相同时显示「楼主」徽标 */
  opUserId?: number | null
}>(), {
  comment: undefined,
  presentation: 'flat',
  depth: 0,
  collapseFromDepth: 2,
  content: '',
  htmlContent: undefined,
  authorLink: undefined,
  meta: undefined,
  floorLabel: '',
  avatar: undefined,
  replyTo: undefined,
  actions: () => [
    { label: '回复', value: 'reply', icon: 'i-lucide-reply' }
  ],
  commentMetaBuilder: undefined,
  commentAuthorLinkBuilder: undefined,
  commentActionsBuilder: undefined,
  loadingMoreCommentId: null,
  flash: false,
  opUserId: null
})

const emit = defineEmits<{
  action: [value: string]
  actionComment: [comment: ForumComment, value: string]
  loadMoreReplies: [comment: ForumComment]
}>()

const { t } = useI18n()
const { reactionsEnabled, bookmarksEnabled } = useWebOptions()

// 后端已用 bluemonday sanitize，前端可直接 v-html 渲染。
const showHtml = computed(() => Boolean(props.htmlContent))
const isDeletedComment = computed(() => props.comment?.status === 'deleted')
const displayFloorLabel = computed(() => props.floorLabel?.trim() || '')
const secondaryActions = computed(() => props.actions.filter(actionItem => !isPrimaryAction(actionItem)))
const mobileMenuItems = computed<CommentMenuEntry[][]>(() => [secondaryActions.value.map(actionItem => ({
  label: actionItem.label,
  icon: actionItem.icon,
  color: actionItem.value === 'delete' ? 'error' : undefined,
  onSelect: () => onAction(actionItem)
}))])
// 楼主徽标：仅详情页传入 opUserId 且评论节点作者匹配时显示。
const isOpAuthor = computed(() =>
  props.opUserId != null && props.comment?.authorUserId === props.opUserId
)

// 当前评论节点：用于内联编辑器/回复编辑器匹配（inject renderer 据此判断原位渲染）。
const commentNode = computed(() => props.comment ?? null)
const childComments = computed(() => props.comment?.children || [])
const branchPresentation = computed(() => commentBranchPresentation(
  props.presentation,
  props.depth,
  childComments.value,
  props.collapseFromDepth
))
const branchExpanded = ref(false)
const branchId = `sf-comment-branch-${useId()}`
const branchVisible = computed(() => !branchPresentation.value.collapsible || branchExpanded.value)
const userPreviewId = `sf-comment-user-preview-${useId()}`
const userPreviewOpen = ref(false)
const avatarPreviewTrigger = ref<HTMLElement | null>(null)
const userPreviewCard = ref<HTMLElement | null>(null)
const lastPreviewTrigger = ref<HTMLElement | null>(null)
const previewUsername = computed(() => props.comment?.author?.username?.trim() || '')
const canPreviewUser = computed(() => Boolean(props.authorLink && previewUsername.value))

function toggleBranch() {
  branchExpanded.value = !branchExpanded.value
}

// 预览按钮内已没有链接（作者名 `linked=false`、chip 也移到了按钮外），
// 因此不再需要捕获阶段拦截 NuxtLink 的导航，按钮的 click 直接开卡片。
function toggleUserPreview(event?: MouseEvent) {
  if (!canPreviewUser.value) {
    return
  }
  // 作者行把原生 click 透传上来（自定义组件事件不会自带 currentTarget），
  // 用 target.closest 定位触发按钮，关闭预览后焦点才能还回去。
  const target = event?.target ?? event?.currentTarget
  const trigger = target instanceof Element
    ? target.closest('.sf-comment__author-trigger, .sf-comment__avatar-trigger')
    : null
  if (trigger instanceof HTMLElement) {
    lastPreviewTrigger.value = trigger
  }
  userPreviewOpen.value = !userPreviewOpen.value
}

function closeUserPreview(restoreFocus = false) {
  if (!userPreviewOpen.value) {
    return
  }
  userPreviewOpen.value = false
  if (restoreFocus) {
    nextTick(() => lastPreviewTrigger.value?.focus())
  }
}

function onDocumentPointerDown(event: PointerEvent) {
  if (!userPreviewOpen.value || !(event.target instanceof Node)) {
    return
  }
  const target = event.target
  if (
    userPreviewCard.value?.contains(target)
    || avatarPreviewTrigger.value?.contains(target)
    // 作者行是独立组件，用它的稳定 class 判定「点在触发按钮内」，不跨组件拿 DOM 节点。
    || (target instanceof Element && Boolean(target.closest('.sf-comment__author-trigger')))
  ) {
    return
  }
  closeUserPreview()
}

function onDocumentKeyDown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    closeUserPreview(true)
  }
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocumentPointerDown)
  document.addEventListener('keydown', onDocumentKeyDown)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown)
  document.removeEventListener('keydown', onDocumentKeyDown)
})

// 操作按钮点击。
function onAction(actionItem: CommentAction) {
  emit('action', actionItem.value)
  if (props.comment) {
    emit('actionComment', props.comment, actionItem.value)
  }
}

function isPrimaryAction(actionItem: CommentAction) {
  return actionItem.value === 'reply' || actionItem.value === 'link'
}

function childAuthorName(comment: ForumComment) {
  return forumAuthorName(comment.author, comment.authorUserId)
}

function childMeta(comment: ForumComment) {
  return props.commentMetaBuilder?.(comment) || ''
}

function childAuthorLink(comment: ForumComment) {
  return props.commentAuthorLinkBuilder?.(comment) || ''
}

function childActions(comment: ForumComment) {
  return props.commentActionsBuilder?.(comment) || props.actions
}

function childReplyTo(comment: ForumComment) {
  if (!comment.replyTo) {
    return undefined
  }
  // 透传完整 author 对象，展示名由模板层统一解析。
  return {
    id: comment.replyTo.id,
    author: comment.replyTo.author,
    excerpt: comment.replyTo.excerpt
  }
}

function replyToAuthorName(replyTo: NonNullable<(typeof props)['replyTo']>) {
  return userDisplayName(replyTo.author)
}

function forwardChildAction(comment: ForumComment, value: string) {
  emit('actionComment', comment, value)
}

function forwardLoadMoreReplies(comment: ForumComment) {
  emit('loadMoreReplies', comment)
}

function onLoadMoreReplies() {
  if (props.comment) {
    emit('loadMoreReplies', props.comment)
  }
}

const showLoadMoreReplies = computed(() =>
  props.presentation === 'tree'
  && Boolean(props.comment?.hasMoreChildren)
)
const isLoadingMoreReplies = computed(() =>
  props.comment != null && props.loadingMoreCommentId === props.comment.id
)

// 内联编辑器渲染：优先用父级 provide 的 renderer（评论列表原位编辑/回复），
// 否则用本组件的 #editor slot（components.vue 预览页场景）。
const injectedEditorRenderer = inject<CommentEditorRenderer | null>(COMMENT_EDITOR_RENDERER_KEY, null)
const InlineEditorHost = () => {
  const node = commentNode.value
  if (!node || !injectedEditorRenderer) return null
  const rendered = injectedEditorRenderer(node)
  if (rendered == null) return null
  return h('div', { class: 'sf-comment__inline-editor' }, [rendered as never])
}
</script>

<template>
  <div
    :id="comment ? `comment-${comment.id}` : undefined"
    class="sf-comment"
    :class="[
      `sf-comment--${presentation}`,
      {
        'sf-comment--indented': branchPresentation.indentation === 1,
        'sf-comment--flash': flash
      }
    ]"
    :data-comment-depth="depth"
    :data-visual-depth="branchPresentation.indentation"
  >
    <article class="sf-comment__entry">
      <button
        v-if="canPreviewUser"
        ref="avatarPreviewTrigger"
        type="button"
        class="sf-comment__avatar-trigger"
        :aria-label="t('topicDetail.userPreview.open', { name: author })"
        aria-haspopup="dialog"
        :aria-expanded="userPreviewOpen"
        :aria-controls="userPreviewOpen ? userPreviewId : undefined"
        @click="toggleUserPreview"
      >
        <SFAvatar :name="author" :avatar="avatar" size="sm" />
      </button>
      <NuxtLink v-else-if="authorLink" :to="authorLink" class="sf-comment__avatar-link">
        <SFAvatar :name="author" :avatar="avatar" size="sm" />
      </NuxtLink>
      <SFAvatar v-else :name="author" :avatar="avatar" size="sm" />
      <div class="sf-comment__body">
        <header class="sf-comment__header">
          <SFCommentAuthorRow
            :author="author"
            :author-link="authorLink"
            :username="previewUsername"
            :display-name="comment?.author?.displayName || ''"
            :top-badge="comment?.topBadge || null"
            :is-op-author="isOpAuthor"
            :interactive="canPreviewUser"
            :preview-open="userPreviewOpen"
            :preview-id="userPreviewId"
            @preview-toggle="toggleUserPreview"
          />
          <!-- 时间与楼层合并为头部右侧的 meta 组；楼层保留 .sf-comment__floor 类与 #N 文本供进度条读取 -->
          <span v-if="meta || (comment && displayFloorLabel) || secondaryActions.length" class="sf-comment__meta-group">
            <span v-if="meta" class="sf-comment__meta">{{ meta }}</span>
            <span v-if="meta && comment && displayFloorLabel" class="sf-comment__meta-dot" aria-hidden="true" />
            <span class="sf-comment__floor-actions">
              <a
                v-if="comment && displayFloorLabel"
                class="sf-comment__floor"
                :href="`#comment-${comment.id}`"
                :aria-label="displayFloorLabel"
              >{{ displayFloorLabel }}</a>
              <UDropdownMenu
                v-if="secondaryActions.length"
                :items="mobileMenuItems"
                :content="{ align: 'end' }"
              >
                <button
                  type="button"
                  class="sf-comment__mobile-actions"
                  :aria-label="t('topicDetail.commentMoreActions')"
                  :title="t('topicDetail.commentMoreActions')"
                >
                  <UIcon name="i-lucide-ellipsis" class="size-4" aria-hidden="true" />
                </button>
              </UDropdownMenu>
            </span>
          </span>
        </header>

        <div
          v-if="userPreviewOpen && canPreviewUser"
          :id="userPreviewId"
          ref="userPreviewCard"
          class="sf-comment__user-preview-layer"
        >
          <SFCommentUserPreview
            :user="comment?.author ? { username: previewUsername, displayName: comment.author.displayName, avatar } : { username: previewUsername, avatar }"
            :profile-path="authorLink || ''"
            :top-badge="comment?.topBadge || null"
          />
        </div>

        <a
          v-if="replyTo"
          class="sf-comment__reply-to"
          :href="replyTo.id ? `#comment-${replyTo.id}` : undefined"
        >
          <span class="sf-comment__reply-to-label">
            <UIcon name="i-lucide-corner-up-left" class="sf-comment__reply-to-icon size-3.5 shrink-0" aria-hidden="true" />
            <span class="sf-comment__reply-to-author">
              {{ t('topicDetail.reply') }}<template v-if="replyToAuthorName(replyTo)"> {{ replyToAuthorName(replyTo) }}</template>
            </span>
          </span>
          <span v-if="replyTo.excerpt" class="sf-comment__reply-to-excerpt">{{ replyTo.excerpt }}</span>
        </a>

        <div
          v-if="isDeletedComment"
          class="sf-comment__deleted"
          role="status"
        >
          <span class="sf-comment__deleted-icon">
            <UIcon name="i-lucide-trash-2" class="size-4" aria-hidden="true" />
          </span>
          <span class="sf-comment__deleted-copy">
            <strong>{{ t('topicDetail.commentDeletedNotice') }}</strong>
            <span>{{ t('topicDetail.commentDeletedDescription') }}</span>
          </span>
        </div>
        <SFMentionContent
          v-else-if="showHtml"
          v-highlight
          class="sf-comment__content sf-prose"
          :data-sforum-image-gallery="`comment-${comment?.id || 'unknown'}`"
          data-selection-quote-source="comment"
          :data-selection-quote-comment-id="comment?.id"
          :html="sanitizeHtml(htmlContent)"
        />
        <p
          v-else
          class="sf-comment__content"
          data-selection-quote-source="comment"
          :data-selection-quote-comment-id="comment?.id"
        >
          {{ content }}
        </p>

        <!-- AI 产出评论的「AI 活动」折叠块：工具调用 + 依据。默认收起，
             收起时零请求；展开才去 GET /comments/{id}/citations。删除态不显示。 -->
        <SFCommentAIActivity
          v-if="commentNode && !isDeletedComment"
          :comment-id="commentNode.id"
        />

        <!-- 内联编辑器/回复编辑器 -->
        <InlineEditorHost v-if="injectedEditorRenderer" />
        <slot v-else name="editor" :comment="commentNode" />

        <div v-if="actions.length || (comment && (reactionsEnabled || bookmarksEnabled))" class="sf-comment__actions">
          <SFReactionControl
            v-if="comment"
            target-type="comment"
            :target-id="comment.id"
            :count="comment.likeCount"
            :liked="comment.likedByMe === true"
            :liked-by="comment.likedBy"
            :enabled="reactionsEnabled"
            size="sm"
            stack-size="2xs"
            :max="2"
            :chip="false"
          />
          <SFBookmarkControl
            v-if="comment"
            target-type="comment"
            :target-id="comment.id"
            :bookmarked="comment.bookmarkedByMe === true"
            :enabled="bookmarksEnabled"
            size="sm"
          />
          <button
            v-for="actionItem in actions"
            :key="actionItem.value"
            type="button"
            class="sf-comment__action"
            :class="{ 'sf-comment__action--secondary': !isPrimaryAction(actionItem) }"
            @click="onAction(actionItem)"
          >
            <UIcon v-if="actionItem.icon" :name="actionItem.icon" class="size-3.5" aria-hidden="true" />
            <span>{{ actionItem.label }}</span>
          </button>
        </div>
      </div>
    </article>

    <button
      v-if="branchPresentation.collapsible"
      type="button"
      class="sf-comment__disclosure"
      :aria-expanded="branchExpanded"
      :aria-controls="branchId"
      @click="toggleBranch"
    >
      <UIcon
        :name="branchExpanded ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
        class="size-4"
        aria-hidden="true"
      />
      <span>
        {{ branchExpanded
          ? t('topicDetail.collapse')
          : t('topicDetail.expand', { n: branchPresentation.followUpCount }) }}
      </span>
    </button>

    <div
      v-if="presentation === 'tree' && childComments.length"
      :id="branchId"
      class="sf-comment__branch"
      :class="{ 'sf-comment__branch--connected': branchPresentation.connectionRail }"
      :hidden="!branchVisible"
    >
      <SFComment
        v-for="child in childComments"
        :key="child.id"
        :comment="child"
        :presentation="presentation"
        :depth="depth + 1"
        :collapse-from-depth="collapseFromDepth"
        :author="childAuthorName(child)"
        :avatar="child.author?.avatar"
        :author-link="childAuthorLink(child)"
        :html-content="child.content.htmlContent"
        :meta="childMeta(child)"
        :reply-to="childReplyTo(child)"
        :actions="childActions(child)"
        :comment-meta-builder="commentMetaBuilder"
        :comment-author-link-builder="commentAuthorLinkBuilder"
        :comment-actions-builder="commentActionsBuilder"
        :loading-more-comment-id="loadingMoreCommentId"
        :op-user-id="opUserId"
        @load-more-replies="forwardLoadMoreReplies"
        @action-comment="forwardChildAction"
      />
    </div>

    <!-- D2：树子孙截断后通过 ListCommentReplies 加载更多 -->
    <button
      v-if="showLoadMoreReplies"
      type="button"
      class="sf-comment__load-more"
      :disabled="isLoadingMoreReplies"
      @click="onLoadMoreReplies"
    >
      <UIcon
        :name="isLoadingMoreReplies ? 'i-lucide-loader-2' : 'i-lucide-chevrons-down'"
        class="size-4"
        :class="{ 'animate-spin': isLoadingMoreReplies }"
        aria-hidden="true"
      />
      <span>{{ t('topicDetail.loadMoreReplies') }}</span>
    </button>
  </div>
</template>
