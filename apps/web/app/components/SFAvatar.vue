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
  name: '匿名用户',
  src: undefined,
  alt: undefined,
  size: 'md',
  shape: 'circle',
  status: undefined,
  loading: 'lazy'
})

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
  const source = props.name.trim()
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
  return (fromAvatar || props.name || 'user').trim() || 'user'
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
    'relative inline-flex shrink-0 select-none items-center justify-center overflow-hidden font-semibold leading-none text-white',
    sizeClass[props.size],
    shapeClass[props.shape]
  ]
  if (showImage.value) {
    classes.push('bg-[var(--sf-accent)]')
  } else {
    classes.push(forumAvatarToneClass(toneSeed.value))
  }
  return classes.join(' ')
})

const imageAlt = computed(() => props.alt ?? props.avatar?.alt ?? props.name)
const isDecorative = computed(() => props.alt === '')

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
