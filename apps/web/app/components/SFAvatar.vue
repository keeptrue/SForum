<script setup lang="ts">
import { forumAvatarToneClass } from '~/utils/forum/forumListPresentation'

/** sm/md/lg 通用；list 为话题列表等密集行（36px）；xs/2xs 供动作行内的头像组 */
type AvatarSize = '2xs' | 'xs' | 'list' | 'sm' | 'md' | 'lg'
type AvatarShape = 'circle' | 'square'
type AvatarStatus = 'online' | 'idle' | 'offline'
type AvatarLoading = 'eager' | 'lazy'
type AvatarView = {
  kind: 'uploaded' | 'initials' | 'gravatar' | 'static'
  url: string
  attachmentId?: number | null
  alt: string
}

const props = withDefaults(defineProps<{
  name?: string
  src?: string
  avatar?: AvatarView | null
  alt?: string
  size?: AvatarSize
  shape?: AvatarShape
  status?: AvatarStatus
  loading?: AvatarLoading
}>(), {
  name: undefined,
  src: undefined,
  alt: undefined,
  size: 'md',
  shape: 'circle',
  status: undefined,
  loading: 'lazy'
})

const { t } = useI18n()

/** 无名称时也不能把中文写死：首字母、alt、色板种子都从这里取（RV-28）。 */
const displayName = computed(() => props.name?.trim() || t('common.anonymousUser'))

// NuxtImg width/height 像素预设（list 与视觉 size-9 对齐）
const sizePixels: Record<AvatarSize, number> = {
  '2xs': 18,
  xs: 24,
  list: 36,
  sm: 48,
  md: 96,
  lg: 256
}

const sizeClass: Record<AvatarSize, string> = {
  '2xs': 'size-[18px] text-[9px]',
  xs: 'size-[24px] text-[11px]',
  list: 'size-9 text-[13px]',
  sm: 'size-8 text-[0.72rem]',
  md: 'size-10 text-[0.85rem]',
  lg: 'size-14 text-[1.05rem]'
}

const shapeClass: Record<AvatarShape, string> = {
  circle: 'rounded-full',
  square: 'rounded-lg'
}

const statusClass: Record<AvatarStatus, string> = {
  online: 'bg-green-500',
  idle: 'bg-amber-500',
  offline: 'bg-slate-400'
}

const initials = computed(() => {
  const source = displayName.value.trim()
  if (!source) {
    return 'U'
  }
  // 中文名：取首字（种子用户N →「种」）
  const first = source[0] || 'U'
  if (/[\u3400-\u9fff\uf900-\ufaff]/.test(first)) {
    return first
  }
  return source
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part.slice(0, 1))
    .join('')
    .toUpperCase()
})

const toneSeed = computed(() => {
  const fromAvatar = props.avatar?.alt || ''
  return (fromAvatar || displayName.value || 'user').trim() || 'user'
})

// 原样使用 AvatarView / src
const imageSrc = computed(() => `${props.avatar?.url || props.src || ''}`.trim())
const isRemoteImage = computed(() => /^https?:\/\//i.test(imageSrc.value))
const bypassImageOptimization = computed(() => props.avatar?.kind === 'uploaded' || isRemoteImage.value)

const imageFailed = ref(false)
const showImage = computed(() => Boolean(imageSrc.value) && !imageFailed.value)

const avatarClass = computed(() => {
  // sf-avatar：稳定钩子；尺寸/色板走 Tailwind
  const classes = [
    'sf-avatar',
    'relative inline-flex shrink-0 select-none items-center justify-center overflow-hidden font-semibold leading-none',
    sizeClass[props.size],
    shapeClass[props.shape]
  ]
  if (showImage.value) {
    // 有图时不存在首字母文字：前景色只影响状态点边框，沿用强调色上的对比色。
    classes.push('bg-[var(--sf-accent)] text-[var(--sf-accent-contrast)]')
  } else {
    classes.push(forumAvatarToneClass(toneSeed.value))
  }
  return classes.join(' ')
})

/**
 * 可访问名的空串语义（与 `SFTabs.ariaLabel` 同一套约定）：
 *
 * - **未提供**（`undefined` / `null`）→ 回落到显示名（`name` 或「匿名用户」）；
 * - **调用方显式传 `alt=""`** → 装饰性头像：`aria-hidden` + `alt=""`，读屏直接跳过；
 * - **纯空白串** → 视同未提供（不产出「   」这种伪可访问名）。
 *
 * `name` 本身不承担「关掉可读名」的职责：`name=''` 只是没有名字，仍按匿名用户播报。
 *
 * **`avatar.alt` 的空串不算装饰**：那是服务端字段，`Alt()` 的契约是非空
 * （DisplayName → Username → "User"）。空值只可能来自旧/坏载荷，此时静默把头像
 * 变成 `aria-hidden` 会把「没名字」升级成「读屏跳过」，所以按未提供处理。
 */
const normalizedAlt = computed(() => {
  const explicit = props.alt
  if (explicit !== undefined && explicit !== null) {
    // 只有调用方显式空串是装饰语义；纯空白更像填错，按未提供处理。
    if (explicit === '') return ''
    const trimmed = explicit.trim()
    return trimmed === '' ? undefined : trimmed
  }
  const fromServer = props.avatar?.alt
  if (fromServer === undefined || fromServer === null) return undefined
  const trimmed = fromServer.trim()
  return trimmed === '' ? undefined : trimmed
})
const isDecorative = computed(() => normalizedAlt.value === '')
const imageAlt = computed(() => (isDecorative.value ? '' : normalizedAlt.value || displayName.value))

function resetImageFailure() {
  imageFailed.value = false
}

watch(imageSrc, resetImageFailure)
</script>

<template>
  <span :class="avatarClass" :aria-hidden="isDecorative ? 'true' : undefined">
    <!-- 上传头像由后端预处理，直接请求稳定媒体 URL；远程头像同样不经过 IPX。 -->
    <img
      v-if="showImage && imageSrc && bypassImageOptimization"
      class="size-full object-cover"
      :src="imageSrc"
      :alt="imageAlt"
      :width="sizePixels[size]"
      :height="sizePixels[size]"
      :loading="loading"
      decoding="async"
      referrerpolicy="no-referrer"
      @error="imageFailed = true"
    >
    <NuxtImg
      v-else-if="showImage && imageSrc"
      class="size-full object-cover"
      :src="imageSrc"
      :alt="imageAlt"
      :width="sizePixels[size]"
      :height="sizePixels[size]"
      :sizes="`${sizePixels[size]}px`"
      format="webp"
      :loading="loading"
      decoding="async"
      @error="imageFailed = true"
    />
    <span v-else class="grid size-full place-items-center">{{ initials }}</span>
    <span
      v-if="status"
      class="absolute right-0 bottom-0 size-2.5 rounded-full border-2 border-[var(--sf-card,#fff)]"
      :class="statusClass[status]"
      aria-hidden="true"
    />
  </span>
</template>
