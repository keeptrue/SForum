<script setup lang="ts">
/**
 * 单张徽章卡：资料页「徽章」tab 与 `/badges` 公共目录共用的同一份实现。
 *
 * 职责边界：这里只渲染一格（图标砖 / 名称 / tier 标记 / earned 两态 / 进度条 / aria-label 模板）
 * 以及**整卡点击**。点击不打开路由也不取数：只 `emit('open', badge)`，详情弹窗由宿主拥有
 * （`SFProfileBadgeTab` → `SFBadgeDetailModal`）。
 * 分组容器、组内排序、空态 / 加载态 / 失败态、以及两个 variant 的容器级差异，
 * 全部由 `profile/SFProfileBadgeTab.vue` 拥有——不要在卡片里加取数或分组逻辑。
 */
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  type ProfileBadgeEntry
} from '~/utils/forum/forumBadge'

type BadgeCardVariant = 'profile' | 'catalog'

const props = withDefaults(defineProps<{
  badge: ProfileBadgeEntry
  variant?: BadgeCardVariant
  /**
   * 「获得条件」一句话的渲染 seam：宿主传 `forumBadgeRequirementText(badge, locale)`。
   * 句子本身由服务端生成（前端只选语言），这里只管排版 —— 最多两行，超出裁掉。
   * 空串表示没有条件文案，整行不渲染（不占位、不留空白行）。
   */
  description?: string
  /**
   * 整卡是否可点。默认 `true`：资料页与目录页都靠点卡片打开徽章详情。
   * 传 `false` 只用于确实不该响应点击的纯展示位 —— 那时卡片自己带上无障碍名字，
   * 也不再渲染覆盖按钮（避免出现一个点了没反应的假按钮）。
   */
  interactive?: boolean
}>(), {
  variant: 'profile',
  description: '',
  interactive: true
})

const emit = defineEmits<{ open: [badge: ProfileBadgeEntry] }>()

const { t, te, locale } = useI18n()
const { formatDateOnly } = useSiteDateTime()

const isCatalog = computed(() => props.variant === 'catalog')

// 等级名/色都优先取载荷（运营可增删等级），缺失才回退内置三档文案与中性色。
const tierName = computed(() => forumBadgeTierLabel(props.badge, locale.value, t, te))
const tierStyle = computed(() => forumBadgeTierStyle(props.badge))

function earnedDate() {
  return props.badge.grantedAt ? formatDateOnly(props.badge.grantedAt) : ''
}

function progressText() {
  return t('badges.card.progress', { current: props.badge.current ?? 0, threshold: props.badge.threshold ?? 0 })
}

function cardLabel() {
  if (props.badge.earned) {
    // catalog 的 wire 上没有 grantedAt（D37 的公开面白名单），所以不能复用带日期的模板，
    // 否则会读成「获得于 」（日期为空）。
    if (isCatalog.value) {
      return t('badges.catalog.ariaLabelEarned', { tier: tierName.value, name: props.badge.name })
    }
    return t('badges.card.ariaLabel', {
      tier: tierName.value,
      name: props.badge.name,
      date: earnedDate()
    })
  }
  if (forumBadgeHasProgress(props.badge)) {
    return t('badges.card.ariaLabelUngranted', {
      tier: tierName.value,
      name: props.badge.name,
      current: props.badge.current ?? 0,
      threshold: props.badge.threshold ?? 0
    })
  }
  // 未获得的手动徽章没有阈值：文案里不能出现进度，否则会读成「进度 0/0」。
  return t('badges.card.ariaLabelNotEarned', { tier: tierName.value, name: props.badge.name })
}

function open() {
  if (!props.interactive) return
  emit('open', props.badge)
}
</script>

<template>
  <article
    class="sf-profile-badge-card"
    :class="[
      `sf-profile-badge-card--${props.variant}`,
      {
        'sf-profile-badge-card--progress': forumBadgeHasProgress(props.badge),
        'sf-profile-badge-card--interactive': props.interactive
      }
    ]"
    :data-tier="forumBadgeTier(props.badge.tier)"
    :data-earned="props.badge.earned ? 'true' : 'false'"
    :data-badge-slug="props.badge.slug"
    :data-interactive="props.interactive ? 'true' : 'false'"
    :style="tierStyle"
    :aria-label="props.interactive ? undefined : cardLabel()"
  >
    <!--
      整卡点击：一个铺满卡片的真按钮承载，不把内容包进按钮里（按钮不能嵌套按钮，
      佩戴按钮就在同一张卡里）。名字挂在按钮上，卡片本身就不再重复一遍。
    -->
    <button
      v-if="props.interactive"
      type="button"
      class="sf-profile-badge-card__open"
      :aria-label="cardLabel()"
      @click="open"
    />

    <span class="sf-profile-badge-card__icon" aria-hidden="true">
      <UIcon :name="props.badge.icon || forumBadgeTierIcon(props.badge.tier)" class="sf-profile-badge-card__glyph" />
      <!-- 目录页的「已获得」勾章：形状线索，去色后仍与未获得可辨。 -->
      <span v-if="isCatalog && props.badge.earned" class="sf-profile-badge-card__seal">
        <UIcon name="i-lucide-check" class="sf-profile-badge-card__seal-glyph" />
      </span>
    </span>

    <div class="sf-profile-badge-card__body">
      <span class="sf-profile-badge-card__name">{{ props.badge.name }}</span>
      <span v-if="props.description" class="sf-profile-badge-card__description">{{ props.description }}</span>
      <span class="sf-profile-badge-card__meta">
        <span class="sf-profile-badge-card__tier">{{ tierName }}</span>
        <template v-if="props.badge.earned">
          <span aria-hidden="true">·</span>
          <span class="sf-profile-badge-card__date">
            {{ isCatalog ? t('badges.catalog.earned') : t('badges.card.earnedOn', { date: earnedDate() }) }}
          </span>
        </template>
        <template v-else>
          <span aria-hidden="true">·</span>
          <span class="sf-profile-badge-card__date">{{ t('badges.card.notEarned') }}</span>
        </template>
      </span>

      <div v-if="forumBadgeHasProgress(props.badge)" class="sf-profile-badge-card__progress">
        <div
          class="sf-profile-badge-card__track"
          role="progressbar"
          aria-valuemin="0"
          :aria-valuenow="props.badge.current ?? 0"
          :aria-valuemax="props.badge.threshold ?? 0"
          :aria-valuetext="progressText()"
        >
          <span
            class="sf-profile-badge-card__fill"
            :style="{ width: `${forumBadgeProgressPercent(props.badge.current, props.badge.threshold)}%` }"
          />
        </div>
        <span class="sf-profile-badge-card__progress-text">{{ progressText() }}</span>
      </div>

      <!--
        卡片动作位：只有资料页（profile 变体）且宿主真的传了内容才渲染。
        目录页的卡片不佩戴徽章，动作插槽在那里必须彻底消失，不能留一个空盒子。
        动作位浮在覆盖按钮之上（z-index 更高），按钮自己 `@click.stop`，所以点佩戴不会顺带开弹窗。
      -->
      <div v-if="!isCatalog && $slots.action" class="sf-profile-badge-card__action">
        <slot name="action" />
      </div>
    </div>
  </article>
</template>

<style scoped>
.sf-profile-badge-card {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
  box-sizing: border-box;
  min-height: 64px;
  padding: 12px;
  border: 1px solid var(--sf-border);
  border-radius: 12px;
  background: var(--sf-card);
}

.sf-profile-badge-card--progress {
  min-height: 84px;
}

/* 单列档横排：图标左、文本右，信息密度不降。 */
@media (max-width: 767px) {
  .sf-profile-badge-card {
    flex-direction: row;
    align-items: center;
  }

  .sf-profile-badge-card__body {
    flex: 1 1 auto;
    min-width: 0;
  }
}

/* 覆盖整卡的点击面：铺满圆角矩形，内容层仍在下层正常绘制。 */
.sf-profile-badge-card__open {
  position: absolute;
  inset: 0;
  z-index: 1;
  box-sizing: border-box;
  width: 100%;
  height: 100%;
  padding: 0;
  border: 0;
  border-radius: 12px;
  background: transparent;
  cursor: pointer;
}

.sf-profile-badge-card__open:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: -2px;
}

.sf-profile-badge-card__icon {
  position: relative;
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 1px solid color-mix(in srgb, var(--sf-tier-hue) 34%, transparent);
  border-radius: 10px;
  background: color-mix(in srgb, var(--sf-tier-hue) 12%, transparent);
  color: var(--sf-tier-hue);
}

.sf-profile-badge-card__glyph {
  width: 18px;
  height: 18px;
}

/* 勾章绝对定位，不参与布局：有无勾章不改变卡片高度。 */
.sf-profile-badge-card__seal {
  position: absolute;
  top: -4px;
  right: -4px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 12px;
  height: 12px;
  border-radius: 999px;
  background: var(--sf-accent);
  color: var(--sf-public-surface);
}

.sf-profile-badge-card__seal-glyph {
  width: 8px;
  height: 8px;
}

.sf-profile-badge-card__body {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.sf-profile-badge-card__name {
  overflow: hidden;
  color: var(--sf-fg);
  font-size: 13px;
  font-weight: 700;
  line-height: 1.3;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 条件句子是自然语言，单行 nowrap 会吃掉大半句：改成两行 clamp，不写死高度
   （行数由 line-height 决定，空句时整行不渲染由模板的 v-if 负责）。 */
.sf-profile-badge-card__description {
  display: -webkit-box;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  color: var(--sf-fg-secondary);
  font-size: 11px;
  line-height: 1.35;
  overflow-wrap: anywhere;
  white-space: normal;
}

.sf-profile-badge-card__meta {
  display: flex;
  align-items: center;
  gap: 4px;
  overflow: hidden;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  white-space: nowrap;
}

.sf-profile-badge-card__tier {
  color: var(--sf-fg-secondary);
  font-weight: 600;
}

.sf-profile-badge-card__date {
  overflow: hidden;
  font-variant-numeric: tabular-nums;
  text-overflow: ellipsis;
}

.sf-profile-badge-card__progress {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 2px;
}

.sf-profile-badge-card__track {
  flex: 1 1 auto;
  height: 4px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--sf-muted);
}

.sf-profile-badge-card__fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--sf-accent);
}

.sf-profile-badge-card__progress-text {
  flex: 0 0 auto;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

/* 动作位只占内容列，不改变卡片的其它几何；宿主条件为假时不留空盒子。
   `position/z-index` 让佩戴按钮浮在覆盖按钮之上，点它不会顺带打开详情。 */
.sf-profile-badge-card__action {
  position: relative;
  z-index: 2;
  display: flex;
  margin-top: 6px;
}

.sf-profile-badge-card__action:empty {
  display: none;
}

/* 统一配色：hue = 等级色，填充 12%，环线 34%；名称永远是 --sf-fg。
   等级色成对下发（明/暗），这里按主题挑一个 —— 没有 [data-tier] 变体规则。 */
.sf-profile-badge-card {
  --sf-tier-hue: var(--sf-tier-hue-light);
}

.dark .sf-profile-badge-card {
  --sf-tier-hue: var(--sf-tier-hue-dark);
}

.sf-profile-badge-card[data-earned="true"] {
  border-color: color-mix(in srgb, var(--sf-tier-hue) 34%, transparent);
  background: color-mix(in srgb, var(--sf-tier-hue) 12%, var(--sf-card));
}

/* 未获得：不降透明度，只把名称降到 secondary、图标降到 tertiary。 */
.sf-profile-badge-card[data-earned="false"] .sf-profile-badge-card__name {
  color: var(--sf-fg-secondary);
}

/* hover 反馈只给可点的卡片；不可点时边框变色是假承诺。 */
.sf-profile-badge-card--interactive:hover {
  border-color: var(--sf-public-border-strong);
}

/* 目录页：虚线只表示「未获得」，tier 不改 border-style。 */
.sf-profile-badge-card--catalog[data-earned="false"] {
  border-style: dashed;
}
</style>
