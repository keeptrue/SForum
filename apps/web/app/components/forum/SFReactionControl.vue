<script setup lang="ts">
/**
 * 点赞控件：心形按钮 + 头像组触发器 + 名单弹层。
 *
 * 状态策略：点击先乐观切换，服务端返回真值后回填；失败回滚并出错误 Toast。
 * 头像组首屏用父层传来的 likedBy 渲染（零请求），点开才由 SFReactionRoster 拉名单。
 * 匿名访客点击先跳登录并保留当前页面作为 redirect。
 */
import type { AvatarGroupSize } from '~/components/SFAvatarGroup.vue'
import type { ForumUserSummary } from '~/utils/forum/forumTaxonomy'
import SFReactionRoster from '~/components/forum/SFReactionRoster.vue'
import SFReactionStack from '~/components/forum/SFReactionStack.vue'
import { useForumApi } from '~/composables/forum/useForumApi'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import {
  forumReactionCanToggle,
  forumReactionFromResponse,
  forumReactionLoginTarget,
  forumReactionNeedsLogin,
  forumReactionPreviewAfterToggle,
  forumReactionPreviewItems,
  forumReactionRollback,
  forumReactionToggle
} from '~/utils/forum/forumReaction'

const props = withDefaults(defineProps<{
  targetType: 'topic' | 'comment'
  targetId: number
  count: number
  liked?: boolean
  /** 服务端预览：自己优先、其余最早，最多 3 个 */
  likedBy?: ForumUserSummary[]
  /** 头像组最多展示几个 */
  max?: number
  stackSize?: AvatarGroupSize
  /** 是否渲染 +N chip：计数已在心形旁时关掉 */
  chip?: boolean
  enabled?: boolean
  size?: 'sm' | 'md'
}>(), {
  liked: false,
  likedBy: () => [],
  max: 3,
  stackSize: 'xs',
  chip: true,
  enabled: true,
  size: 'sm'
})

const emit = defineEmits<{
  change: [liked: boolean, count: number, likedBy: ForumUserSummary[]]
}>()

const { t } = useI18n()
const localePath = useLocalePath()
const router = useRouter()
const route = useRoute()
const toast = useToast()
const api = useForumApi()
const { user } = useAuthSession()

const pending = ref(false)
const liked = ref(props.liked)
const count = ref(props.count)
const preview = ref<ForumUserSummary[]>(forumReactionPreviewItems(props.likedBy, props.max))
const rosterActive = ref(false)
// 弹层打开期间切换点赞会让名单过期：换 key 重挂载即重新拉第一页。
const rosterKey = ref(0)

watch(() => props.liked, (value) => { liked.value = value })
watch(() => props.count, (value) => { count.value = value })
watch(() => props.likedBy, (value) => {
  preview.value = forumReactionPreviewItems(value, props.max)
})

const selfSummary = computed<ForumUserSummary | null>(() => {
  const current = user.value
  if (!current) return null
  return { id: current.id, username: current.username, displayName: current.displayName, avatar: current.avatar }
})

const showStack = computed(() => count.value > 0 && (preview.value.length > 0 || props.chip))
const label = computed(() => liked.value
  ? t('forum.reaction.unlike')
  : t('forum.reaction.like'))

async function toggle() {
  if (!forumReactionCanToggle(props.enabled, pending.value)) return
  if (forumReactionNeedsLogin(Boolean(user.value))) {
    const target = forumReactionLoginTarget(route.fullPath)
    await router.push(localePath({ path: target.path, query: { redirect: target.redirect } }))
    return
  }
  const previous = { liked: liked.value, count: count.value }
  const previousPreview = preview.value
  const optimistic = forumReactionToggle(previous)
  liked.value = optimistic.liked
  count.value = optimistic.count
  preview.value = forumReactionPreviewAfterToggle(previousPreview, selfSummary.value, optimistic.liked, props.max)
  pending.value = true
  try {
    const state = props.targetType === 'topic'
      ? (optimistic.liked ? await api.likeTopic(props.targetId) : await api.unlikeTopic(props.targetId))
      : (optimistic.liked ? await api.likeComment(props.targetId) : await api.unlikeComment(props.targetId))
    const settled = forumReactionFromResponse(state.liked, state.likeCount)
    liked.value = settled.liked
    count.value = settled.count
    // 计数用服务端真值；头像组按服务端结果把自己置顶/摘掉。
    const settledPreview = forumReactionPreviewAfterToggle(previousPreview, selfSummary.value, settled.liked, props.max)
    preview.value = settledPreview
    if (rosterActive.value) {
      rosterKey.value += 1
    }
    emit('change', settled.liked, settled.count, settledPreview)
    toast.add({
      color: 'success', icon: 'i-lucide-check', duration: 5000,
      title: settled.liked ? t('forum.reaction.liked') : t('forum.reaction.unliked')
    })
  } catch {
    const restored = forumReactionRollback(previous)
    liked.value = restored.liked
    count.value = restored.count
    preview.value = previousPreview
    // 错误 Toast 不自动关闭（项目约定）
    toast.add({
      color: 'error', icon: 'i-lucide-alert-circle', duration: 0,
      title: t('forum.reaction.failed')
    })
  } finally {
    pending.value = false
  }
}

// UPopover 只发 update:open（Reka 语义），没有 open 事件。
function onRosterOpenChange(open: boolean) {
  rosterActive.value = open
}
</script>

<template>
  <!-- 开关关闭时不渲染任何控件：SSR 首屏即由 web-options 决定，避免水合后闪隐 -->
  <div v-if="props.enabled" class="sf-reaction" :class="`sf-reaction--${size}`">
    <button
      type="button"
      class="sf-reaction__button"
      :class="{ 'sf-reaction__button--active': liked }"
      :aria-pressed="liked"
      :aria-busy="pending"
      :aria-label="label"
      :title="label"
      :disabled="pending"
      @click="toggle"
    >
      <UIcon :name="liked ? 'i-tabler-heart-filled' : 'i-lucide-heart'" class="sf-reaction__icon" aria-hidden="true" />
      <span class="sf-reaction__count">{{ count }}</span>
    </button>
    <UPopover v-if="showStack" :content="{ align: 'center', sideOffset: 6, collisionPadding: 16 }" @update:open="onRosterOpenChange">
      <SFReactionStack
        :items="preview"
        :total="count"
        :max="props.max"
        :size="props.stackSize"
        :chip="props.chip"
        @open="rosterActive = true"
      />
      <template #content>
        <SFReactionRoster
          :key="rosterKey"
          :target-type="props.targetType"
          :target-id="props.targetId"
          :active="rosterActive"
          :count="count"
        />
      </template>
    </UPopover>
  </div>
</template>

<style scoped>
.sf-reaction {
  display: inline-flex; align-items: center; gap: 6px;
  /* 动作行坐在页面底色上：头像描边跟随画布，不用卡片色（深色下会偏亮） */
  --sf-avatar-ring-color: var(--sf-public-bg, #ffffff);
}

.sf-reaction__button {
  display: inline-flex; align-items: center; justify-content: center; gap: 0.25rem;
  border: 0; background: transparent; color: var(--sf-public-muted, #64748b);
  cursor: pointer; padding: 0.125rem 0.25rem; border-radius: 0.375rem;
  transition: color 120ms ease, background-color 120ms ease;
  /* 设计审计 A1：点赞热区至少 38×38，字号与行基线保持不变 */
  min-width: 2.375rem;
  min-height: 2.375rem;
}

.sf-reaction__button:hover { background: color-mix(in srgb, currentColor 8%, transparent); }

.sf-reaction__button:hover,
.sf-reaction__button--active { color: var(--sf-public-accent, #e11d48); }

.sf-reaction__button:disabled { cursor: default; opacity: 0.6; }

.sf-reaction--sm { font-size: 0.8125rem; }
.sf-reaction--md { font-size: 0.875rem; }
.sf-reaction__icon { width: 1rem; height: 1rem; }
.sf-reaction__count { font-variant-numeric: tabular-nums; }

/* ≤479px：头像组降到 2 个并隐藏 chip，整组约 116px，不与心形折行 */
@media (max-width: 479px) {
  .sf-reaction :deep(.sf-avatar-group__item:nth-child(n + 3)),
  .sf-reaction :deep(.sf-avatar-group__more) { display: none; }
}

@media (prefers-reduced-motion: reduce) {
  .sf-reaction,
  .sf-reaction__button { transition: none; animation: none; }
}
</style>
