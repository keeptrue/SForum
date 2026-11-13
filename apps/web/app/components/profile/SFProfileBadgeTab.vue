<script setup lang="ts">
/**
 * 徽章墙容器，两个消费者共用同一份实现：
 *
 * - `variant="profile"`（默认）：资料页「徽章」tab，组内按 tier 降序，取数在 useProfileBadges。
 * - `variant="catalog"`：`/badges` 公共目录，组内按 position 升序，取数在 useBadgesCatalog。
 *
 * 单张卡已抽到 `components/forum/badges/SFBadgeCard.vue`（icon / 名称 / tier 标记 / earned 两态 /
 * 进度条 / aria-label 模板 / 整卡点击都在那边），详情弹窗在
 * `components/forum/badges/SFBadgeDetailModal.vue`。本文件只拥有**容器级**职责：分组 section、
 * 组内排序、组计数、空态 / 加载态 / 失败态、目录页内链、两个 variant 的差异，以及
 * 「哪一枚被选中」（`focusSlug` 由宿主与 URL 对齐，本文件只回发 `update:focusSlug`）。
 * 再出现新消费者时，加 variant 或复用容器；不要把卡片逻辑搬回来。
 */
import SFBadgeCard from '~/components/forum/badges/SFBadgeCard.vue'
import SFBadgeDetailModal from '~/components/forum/badges/SFBadgeDetailModal.vue'
import { badgeWornErrorKey, useBadgeWorn } from '~/composables/forum/badges/useBadgeWorn'
import { useBadgeWearToggle } from '~/composables/forum/badges/useBadgeWearToggle'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import {
  forumBadgeRequirementText,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups,
  type ProfileBadgeEntry,
  type ProfileBadgeGroup
} from '~/utils/forum/forumBadge'

type BadgeTabVariant = 'profile' | 'catalog'

const props = withDefaults(defineProps<{
  groups: ProfileBadgeGroup[]
  loading?: boolean
  failed?: boolean
  variant?: BadgeTabVariant
  /**
   * 是否本人视角。只有本人能佩戴：他人资料页与 `/badges` 目录页都不出现佩戴控件。
   * 取数仍由 useProfileBadges 拥有，佩戴态是独立的 `/badges/worn` 私有口径。
   */
  self?: boolean
  /**
   * 当前打开详情的徽章 slug（`?badge=` 的唯一来源在宿主，见 useBadgeDetailQuery）。
   * 空串 = 不弹窗；本文件不回退成「第一枚」，也不自己改 URL。
   */
  focusSlug?: string
  /**
   * 清单是否**真的到过服务端**（`useProfileBadges` / `useBadgesCatalog` 的 `loaded`）。
   * 深链守卫必须看它：`loading=false` + `groups=[]` 在「还没请求」和「请求回来是空」
   * 之间是同一个形状，只看 loading 会把还没到达的 `?badge=` 当未知 slug 清掉。
   */
  loaded?: boolean
}>(), {
  loading: false,
  failed: false,
  variant: 'profile',
  self: false,
  focusSlug: '',
  loaded: false
})

const emit = defineEmits<{ retry: [], 'update:focusSlug': [slug: string] }>()

const { t, locale } = useI18n()
const localePath = useLocalePath()
const toast = useToast()
const worn = useBadgeWorn()
// 佩戴状态条只回显「当前账号」的佩戴态：全局态带归属，账号切换后不能短暂显示上一账号。
const { user } = useAuthSession()
const viewerId = computed(() => Number(user.value?.id ?? 0))

const isCatalog = computed(() => props.variant === 'catalog')
// 佩戴控件只属于「本人的资料页」：目录页是目录，他人页是展示。
const canWear = computed(() => props.self && !isCatalog.value)

const isHidden = computed(() => worn.isHidden.value)

// 佩戴动作（乐观更新 + toast）与全局徽章弹窗共用一份实现。
const { toggleWear: toggleBadgeWear, hideWorn } = useBadgeWearToggle()

/**
 * 卡片/弹窗上的那颗按钮：未佩戴 → 佩戴；已佩戴 → **取消佩戴（不佩戴任何徽章）**。
 *
 * 「取消佩戴」不能走 `clear()`（那是「回到自动展示」）：合并成一个动作后用户就无法表达
 * 「我什么都不戴」，而且取消当前这枚会静默换戴另一枚。两个出口分开在底部状态条上。
 */
async function toggleWear(badge: ProfileBadgeEntry) {
  if (!canWear.value) return
  await toggleBadgeWear(badge)
}

/** 从已加载分组里反查佩戴 slug 的展示名（名字只由徽章清单提供，佩戴层不带文案）。 */
function badgeNameBySlug(slug: string): string {
  for (const group of sortedGroups.value) {
    const badge = group.badges.find(entry => entry.slug === slug)
    if (badge) return badge.name
  }
  return ''
}

/** 佩戴状态条的一句话：hidden 说「未佩戴」，manual 报当前那枚；auto 不重复回落提示。 */
const wornStatusText = computed(() => {
  if (isHidden.value) return t('badges.worn.hiddenEmpty')
  if (worn.mode.value === 'manual') {
    const name = badgeNameBySlug(worn.wornSlug.value)
    return name ? t('badges.worn.currentlyWearing', { name }) : ''
  }
  return ''
})

/** 自动展示徽章（`DELETE` 回 auto）：已经是 auto 时不重复发请求。 */
async function restoreAuto() {
  if (worn.mode.value === 'auto') return
  try {
    await worn.clear()
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('badges.worn.autoSuccess')
    })
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-alert-triangle',
      title: t(badgeWornErrorKey(error)),
      duration: 0
    })
  }
}

// 只有本人视角才需要佩戴态；切回他人页/目录页时不请求。
watch(canWear, (enabled) => {
  if (enabled) void worn.ensureLoaded()
}, { immediate: true })

const sortedGroups = computed(() => sortProfileBadgeGroups(props.groups).map((group) => {
  // 目录页是「管理员编排的清单」，所见即 position；资料页是「我的荣誉」，按 tier 降序。
  const badges = isCatalog.value
    ? [...group.badges].sort((left, right) => left.position - right.position)
    : sortProfileBadgeEntries(group.badges)
  return {
    key: group.key,
    name: group.name,
    badges,
    earned: group.badges.filter(badge => badge.earned).length,
    total: group.badges.length
  }
}))

const hasAny = computed(() => sortedGroups.value.some(group => group.total > 0))
const skeletonCount = computed(() => (isCatalog.value ? 8 : 3))

/** 当前打开详情的条目（带所属分组名）：slug 找不到就是 null，等于不弹窗。 */
const activeEntry = computed(() => {
  const slug = String(props.focusSlug || '').trim()
  if (!slug) return null
  for (const group of sortedGroups.value) {
    const badge = group.badges.find(entry => entry.slug === slug)
    if (badge) return { badge, groupName: group.name }
  }
  return null
})

const detailOpen = computed(() => Boolean(activeEntry.value))

function openBadge(badge: ProfileBadgeEntry) {
  emit('update:focusSlug', badge.slug)
}

function closeBadge() {
  emit('update:focusSlug', '')
}

// 深链的 slug 不在已加载清单里（停用 / 删除 / 换了 key）：静默清掉，不弹空弹窗。
// 三个前置条件缺一不可：`loaded` 为假时清单还不可信（宿主可能连请求都还没发），
// `loading` 期间同理，取数失败时更不该据此判定「这枚徽章不存在」。
watch(
  [() => props.focusSlug, sortedGroups, () => props.loading, () => props.failed, () => props.loaded],
  ([slug, , loading, failed, loaded]) => {
    if (!slug || loading || failed || !loaded) return
    if (activeEntry.value) return
    closeBadge()
  },
  { immediate: true }
)
</script>

<template>
  <section
    class="sf-profile-badges"
    :class="`sf-profile-badges--${props.variant}`"
    :aria-label="isCatalog ? t('badges.catalog.title') : t('profile.tabs.badges')"
  >
    <div
      v-if="props.loading"
      class="sf-badge-grid"
      :aria-busy="isCatalog ? 'true' : undefined"
      aria-hidden="true"
    >
      <div
        v-for="index in skeletonCount"
        :key="index"
        class="sf-profile-badge-card--skeleton"
      />
    </div>

    <p
      v-else-if="props.failed"
      class="sf-profile-badges__state"
      :role="isCatalog ? 'alert' : undefined"
    >
      {{ t('badges.loadFailed') }}
      <button type="button" class="sf-profile-badges__retry" @click="emit('retry')">
        <UIcon
          v-if="isCatalog"
          name="i-lucide-refresh-cw"
          class="sf-profile-badges__retry-icon"
          aria-hidden="true"
        />
        {{ t('badges.retry') }}
      </button>
    </p>

    <SFEmptyState
      v-else-if="!hasAny && isCatalog"
      :title="t('badges.catalog.emptyTitle')"
      :description="t('badges.catalog.emptyDescription')"
    />

    <p v-else-if="!hasAny" class="sf-profile-badges__state">{{ t('badges.empty') }}</p>

    <template v-else>
      <section v-for="group in sortedGroups" :key="group.key" class="sf-badge-group">
        <h2 class="sf-badge-group__title">
          <span class="sf-badge-group__name">{{ group.name }}</span>
          <span class="sf-badge-group__count">
            {{ isCatalog
              ? t('badges.catalog.groupTotal', { total: group.total })
              : t('badges.group.count', { earned: group.earned, total: group.total }) }}
          </span>
        </h2>

        <div class="sf-badge-grid">
          <SFBadgeCard
            v-for="badge in group.badges"
            :key="badge.slug"
            :badge="badge"
            :variant="props.variant"
            :description="forumBadgeRequirementText(badge, locale)"
            @open="openBadge"
          >
            <template v-if="canWear && badge.earned" #action>
              <!-- 可见文字就是动作：已佩戴时写「取消佩戴」（= 不佩戴任何徽章），
                   状态只由 aria-pressed 表达。 -->
              <button
                type="button"
                class="sf-profile-badges__wear"
                :class="{ 'sf-profile-badges__wear--active': worn.isWorn(badge.slug) }"
                :aria-pressed="worn.isWorn(badge.slug) ? 'true' : 'false'"
                :title="worn.isWorn(badge.slug) ? t('badges.worn.unwear') : t('badges.worn.wear')"
                @click.stop="toggleWear(badge)"
              >
                {{ worn.isWorn(badge.slug) ? t('badges.worn.unwear') : t('badges.worn.wear') }}
              </button>
            </template>
          </SFBadgeCard>
        </div>
      </section>
    </template>

    <!--
      佩戴状态条（仅本人资料页）：两个并列出口，文案写动作、状态只上 aria-pressed。
      - 「自动展示徽章」：mode=auto 时按下；点了走 DELETE 回 auto。
      - 「不佩戴徽章」：mode=hidden 时按下；点了走 PUT {hidden:true}。
      当前态那颗点了不发请求（handler guard），按钮保持可聚焦——不用 disabled。
    -->
    <div
      v-if="canWear && worn.loadedFor(viewerId) && !worn.failed.value"
      class="sf-profile-badges__worn-state"
    >
      <span v-if="wornStatusText" class="sf-profile-badges__worn-text">{{ wornStatusText }}</span>
      <button
        type="button"
        class="sf-profile-badges__wear sf-profile-badges__worn-auto"
        :class="{ 'sf-profile-badges__wear--active': worn.mode.value === 'auto' }"
        :aria-pressed="worn.mode.value === 'auto' ? 'true' : 'false'"
        @click="restoreAuto"
      >
        {{ t('badges.worn.auto') }}
      </button>
      <button
        type="button"
        class="sf-profile-badges__wear sf-profile-badges__worn-hide"
        :class="{ 'sf-profile-badges__wear--active': isHidden }"
        :aria-pressed="isHidden ? 'true' : 'false'"
        @click="hideWorn"
      >
        {{ t('badges.worn.hide') }}
      </button>
    </div>

    <!-- 目录页的站内入口：资料页是唯一能发现徽章的地方，没有这条链就没有入口。 -->
    <div v-if="!isCatalog" class="sf-profile-badges__more">
      <NuxtLink :to="localePath('/badges')" class="sf-profile-badges__more-link">
        {{ t('badges.catalog.viewAll') }}
      </NuxtLink>
    </div>

    <!-- 详情弹窗：开关与选中项都由宿主（`?badge=`）拥有，这里只回发。 -->
    <SFBadgeDetailModal
      :open="detailOpen"
      :badge="activeEntry?.badge ?? null"
      :group-name="activeEntry?.groupName ?? ''"
      :variant="props.variant"
      :self="props.self"
      :worn="activeEntry ? worn.isWorn(activeEntry.badge.slug) : false"
      @update:open="value => { if (!value) closeBadge() }"
      @toggle-wear="activeEntry && toggleWear(activeEntry.badge)"
    />
  </section>
</template>

<style scoped>
.sf-profile-badges {
  display: block;
}

.sf-profile-badges__state {
  margin: 32px 0;
  color: var(--sf-fg-secondary);
  font-size: 13px;
}

.sf-profile-badges__retry {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-left: 8px;
  color: var(--sf-accent);
  font-size: 13px;
  text-decoration: underline;
}

.sf-profile-badges__retry:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-profile-badges__retry-icon {
  width: 14px;
  height: 14px;
}

.sf-profile-badges__more {
  margin-top: 20px;
}

.sf-profile-badges__more-link {
  color: var(--sf-accent);
  font-size: 13px;
  font-weight: 600;
  text-decoration: none;
}

.sf-profile-badges__more-link:hover {
  text-decoration: underline;
}

.sf-profile-badges__more-link:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-badge-group + .sf-badge-group {
  margin-top: 28px;
}

.sf-badge-group:first-child {
  margin-top: 4px;
}

.sf-badge-group__title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 0;
  color: var(--sf-fg);
  font-size: 15px;
  font-weight: 700;
  line-height: 1.3;
}

.sf-badge-group__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-badge-group__count {
  flex: 0 0 auto;
  margin-left: auto;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  font-weight: 400;
  font-variant-numeric: tabular-nums;
}

/* 佩戴按钮：已佩戴用实心 accent 表明「这就是当前展示的那枚」。 */
.sf-profile-badges__wear {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 4px 10px;
  border: 1px solid var(--sf-border);
  border-radius: 999px;
  background: transparent;
  color: var(--sf-fg-secondary);
  font-size: 11.5px;
  font-weight: 600;
  line-height: 1.4;
}

.sf-profile-badges__wear:hover {
  border-color: var(--sf-accent);
  color: var(--sf-accent);
}

.sf-profile-badges__wear:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-profile-badges__wear--active {
  border-color: var(--sf-accent);
  background: var(--sf-accent);
  color: var(--sf-public-surface);
}

.sf-profile-badges__wear--active:hover {
  color: var(--sf-public-surface);
  opacity: 0.9;
}

/* 佩戴状态条：一行文案 + 两个并列出口，独立于卡片网格。390 档两个按钮换行，不许横向溢出。 */
.sf-profile-badges__worn-state {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  column-gap: 8px;
  row-gap: 8px;
  margin-top: 16px;
}

/* 状态行是 flex item：默认 min-width:auto 会被一个超长英文徽章名撑破 390 容器，
   所以显式允许收缩并让长词断行（中文本来就能断，英文单词不能）。 */
.sf-profile-badges__worn-text {
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--sf-fg-secondary);
  font-size: 12.5px;
}

/* 单列 → 4 列：390 档硬塞两列会让名称与进度条一起挤坏，规范明确走单列。 */
.sf-badge-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 12px;
  margin: 10px 0 0;
}

@media (min-width: 768px) {
  .sf-badge-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1024px) {
  .sf-badge-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (min-width: 1280px) {
  .sf-badge-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}

/* 骨架不是卡片实例（它没有 badge），基础形状在容器这里自己给，别去借卡片的样式。 */
.sf-profile-badge-card--skeleton {
  box-sizing: border-box;
  min-height: 64px;
  border: 1px solid transparent;
  border-radius: 12px;
  background: var(--sf-muted);
}
</style>
