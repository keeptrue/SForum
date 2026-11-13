<script setup lang="ts">
/**
 * 站内庆祝宿主：居中 modal，一次把所有待确认条目列清楚。
 *
 * 只在四个条件同时成立时存在：已登录、站点开启徽章功能、当前不是管理端路由、
 * 页面可见。渲染形态从「左下角浮卡」改成居中弹窗：浮卡会压住正文右下角的操作区，
 * 而且一次只能显示一条，多枚徽章同时到手时用户看不到全貌。
 *
 * 徽章类条目按 `params.badgeSlug` 去当前用户徽章墙里查一次，命中就用
 * `SFBadgeDetailBody`（与徽章详情弹窗同源）渲染「图标 + 名称 + 等级 + 分组 + 说明 +
 * 条件 + 进度/获得时间」；查不到（徽章停用/删除、旧通知没有 slug）才回落到纯文本行。
 * 取数留在本宿主里：信息块是纯展示，两个弹窗共用它，但不共用加载态。
 *
 * 数据与 ack 都在 `useCelebrations`；文案映射在 `celebrationPresentation`（纯函数）。
 * 关闭的三条路径（Esc、点遮罩、关闭按钮）都是同一件事：`dismiss()`（先收起本地队列，
 * 再 ack 已消费的最大条目 id）。主按钮「查看我的徽章」= 先 dismiss 再跳 `targetPath` ——
 * 返回时不该再看到同一批条目。没有条目时整棵 UModal 都不存在（不是 `open=false` 的空壳）。
 */
import SFBadgeDetailBody from '~/components/forum/badges/SFBadgeDetailBody.vue'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useCelebrations } from '~/composables/notifications/useCelebrations'
import { celebrationDialogModel } from '~/utils/notifications/celebrationPresentation'
import type { ProfileBadgeEntry, ProfileBadgeGroup } from '~/utils/forum/forumBadge'

type BadgeWallEntry = {
  badge: ProfileBadgeEntry
  groupName: string
}

const { t, te, locale } = useI18n()
const { user } = useAuthSession()
const { badgesEnabled } = useWebOptions()
const adminRoutes = useAdminRoutes()
const route = useRoute()
const { request } = useApiClient()
const { state, refresh, dismiss } = useCelebrations()

const documentVisible = ref(true)
const isAdminRoute = computed(() => adminRoutes.routeId(route.path) !== null)
const allowed = computed(() => Boolean(user.value)
  && badgesEnabled.value
  && !isAdminRoute.value
  && documentVisible.value)

const dialog = computed(() => (allowed.value
  ? celebrationDialogModel(state.value.items, state.value.total, {
    locale: String(locale.value || ''),
    t,
    te
  })
  : null))

/* ------------------------------------------------------------------------- */
/* 徽章墙映射：有带 slug 的条目才请求；缺的 slug 变了才补拉（不是每个弹窗都拉）  */
/* ------------------------------------------------------------------------- */

const badgeWall = ref(new Map<string, BadgeWallEntry>())
const wallPending = ref(false)

/** 条目 id → `params.badgeSlug`（后端 task-10 的字段；旧载荷没有这个键）。 */
const slugById = computed(() => {
  const map = new Map<number, string>()
  for (const item of state.value.items) {
    const raw = item?.params?.badgeSlug
    const slug = typeof raw === 'string' ? raw.trim() : ''
    if (slug) map.set(Math.trunc(Number(item.id) || 0), slug)
  }
  return map
})

/** 展示行 + 命中到的徽章。命中不了就是 `badge: null`，模板走回落行。 */
const rows = computed(() => (dialog.value?.rows ?? []).map((row) => {
  const slug = slugById.value.get(row.id) ?? ''
  const hit = slug ? badgeWall.value.get(slug) : undefined
  return {
    ...row,
    badge: hit?.badge ?? null,
    groupName: hit?.groupName ?? ''
  }
}))

/** 这一轮真正需要查的 slug（去重）：空数组 = 不该发请求。 */
const wantedSlugs = computed(() => {
  const slugs = new Set<string>()
  for (const row of rows.value) {
    const slug = slugById.value.get(row.id)
    if (slug) slugs.add(slug)
  }
  return [...slugs]
})

/**
 * 还缺哪些 slug（逗号串做 watch 键）：空串 = 手上的映射已经够用，不再请求。
 *
 * 用「缺哪些」而不是一个 `loaded` 布尔：同一页面第二次庆祝（隔了几分钟又来一枚新徽章）
 * 必须能补拉，否则会拿上一次的快照把「刚获得」渲染成「未获得 + 进度」。
 * 停用的徽章会一直留在 missing 里，但键是**字符串**、只在内容变化时触发，不会重试风暴
 * —— 直接 watch 数组会因为每次 fetch 都产出新对象而自激（无限请求）。
 */
const missingSlugKey = computed(() => wantedSlugs.value
  .filter(slug => !badgeWall.value.has(slug))
  .join(','))

/**
 * `{groups:[{name,badges:[]}]}` → `slug → {badge, groupName}`；同名 slug 先到先得。
 *
 * **只收 `earned` 的条目。** 这个端点是「全部启用徽章」，不是「我获得了哪些」：
 * 未获得的条目 `earned:false`、没有 `grantedAt`、还带 `current/threshold`。
 * 把它当命中素材的后果是：第二枚徽章到手时快照里早有这个 slug → 不补拉 →
 * 信息块按「未获得」渲染（冒出进度条、丢掉获得时间）。跳过未获得 = 让 slug 留在
 * `missingSlugKey` 里触发一次补拉；补拉后键不变，不会成风暴。
 *
 * 用 Map 而不是普通对象：slug 来自运营输入，别让 `constructor` 之类的原型键冒充命中。
 */
function indexBadgeWall(groups: ProfileBadgeGroup[] | undefined): Map<string, BadgeWallEntry> {
  const index = new Map<string, BadgeWallEntry>()
  for (const group of Array.isArray(groups) ? groups : []) {
    for (const badge of group?.badges ?? []) {
      const slug = String(badge?.slug || '').trim()
      // 未获得 = 还没有可信资料（没有获得时间，进度条也不该出现在庆祝里）。
      if (!slug || !badge?.earned || index.has(slug)) continue
      index.set(slug, { badge, groupName: String(group?.name || '') })
    }
  }
  return index
}

async function loadBadgeWall() {
  if (wallPending.value) return
  const name = String(user.value?.username || '').trim()
  if (!name) return
  wallPending.value = true
  try {
    const result = await request<{ groups: ProfileBadgeGroup[] }>(
      `/profiles/${encodeURIComponent(name)}/badges`
    )
    // 在途期间换了账号（登出再登录 / 切换用户）：这份响应属于上一个人，直接丢弃。
    // 只清空 Map 不够 —— 旧请求回来照样会把 A 的 `earned`/`grantedAt` 填进 B 的界面。
    if (String(user.value?.username || '').trim() !== name) return
    badgeWall.value = indexBadgeWall(result?.groups)
  } catch {
    // 静默：庆祝不是关键路径。查不到就全部走回落行，不 toast、不重试。
  } finally {
    wallPending.value = false
  }
}

// 只在「弹窗打开 + 确实有带 slug 的条目」时拉；缺的 slug 变了才补拉。
watch(missingSlugKey, (key) => {
  if (key) void loadBadgeWall()
}, { immediate: true })

// 换账号：上一位用户的徽章墙不能复用（否则会拿别人的说明/条件渲染）。
watch(() => user.value?.id ?? 0, () => {
  badgeWall.value = new Map()
})

const titleId = 'sf-celebration-title'

function syncVisibility() {
  if (typeof document === 'undefined') return
  documentVisible.value = document.visibilityState !== 'hidden'
}

onMounted(() => {
  syncVisibility()
  document.addEventListener('visibilitychange', syncVisibility)
  if (user.value) void refresh()
})

onBeforeUnmount(() => {
  if (typeof document === 'undefined') return
  document.removeEventListener('visibilitychange', syncVisibility)
})

/** Esc / 遮罩 / 关闭按钮统一走这里：关掉即 ack，不区分来源。 */
function onOpenChange(value: boolean) {
  if (value) return
  void dismiss()
}

/** 主按钮：先收起（ack 已消费的最大条目 id），再跳到目标页。 */
function viewTarget() {
  const target = dialog.value?.targetPath
  void dismiss()
  if (target) void navigateTo(target)
}
</script>

<template>
  <ClientOnly>
    <UModal
      v-if="dialog"
      :open="Boolean(dialog)"
      :title="dialog.title"
      :ui="{ content: 'sf-celebration-modal' }"
      @update:open="onOpenChange"
    >
      <template #content>
        <section class="sf-celebration" :aria-labelledby="titleId" data-sf-celebration>
          <header class="sf-celebration__head">
            <span class="sf-celebration__icon" aria-hidden="true">
              <UIcon :name="dialog.rows[0]?.icon || 'i-tabler-award'" class="sf-celebration__glyph" />
            </span>
            <h2 :id="titleId" class="sf-celebration__title">{{ dialog.title }}</h2>
            <button
              type="button"
              class="sf-celebration__close"
              :aria-label="t('celebrations.dismiss')"
              :title="t('celebrations.dismiss')"
              @click="onOpenChange(false)"
            >
              <UIcon name="i-tabler-x" class="sf-celebration__close-glyph" aria-hidden="true" />
            </button>
          </header>

          <p v-if="dialog.description" class="sf-celebration__description">{{ dialog.description }}</p>

          <ul class="sf-celebration__list">
            <li
              v-for="row in rows"
              :key="row.id"
              class="sf-celebration__row"
              :class="row.badge ? 'sf-celebration__row--badge' : 'sf-celebration__row--text'"
            >
              <!-- 命中徽章：信息块里已经有名称，不再重复「你获得了「X」徽章。」那句。 -->
              <SFBadgeDetailBody
                v-if="row.badge"
                :badge="row.badge"
                :group-name="row.groupName"
                variant="celebration"
              />
              <template v-else>
                <UIcon :name="row.icon" class="sf-celebration__row-glyph" aria-hidden="true" />
                <span class="sf-celebration__row-text">{{ row.text }}</span>
              </template>
            </li>
          </ul>

          <footer class="sf-celebration__footer">
            <button
              v-if="dialog.actionLabel"
              type="button"
              class="sf-celebration__primary"
              @click="viewTarget"
            >
              {{ dialog.actionLabel }}
            </button>
          </footer>
        </section>
      </template>
    </UModal>
  </ClientOnly>
</template>

<style scoped>
.sf-celebration {
  display: block;
  box-sizing: border-box;
  padding: 20px;
}

.sf-celebration__head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.sf-celebration__icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 1px solid color-mix(in srgb, var(--sf-accent) 34%, transparent);
  border-radius: 10px;
  background: color-mix(in srgb, var(--sf-accent) 12%, transparent);
  color: var(--sf-accent);
}

.sf-celebration__glyph {
  width: 18px;
  height: 18px;
}

.sf-celebration__title {
  flex: 1 1 auto;
  min-width: 0;
  margin: 0;
  color: var(--sf-fg);
  font-size: 16px;
  font-weight: 700;
  line-height: 1.35;
  overflow-wrap: anywhere;
}

.sf-celebration__close {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 8px;
  color: var(--sf-fg-tertiary);
}

.sf-celebration__close:hover {
  background: var(--sf-muted);
  color: var(--sf-fg);
}

.sf-celebration__close:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-celebration__close-glyph {
  width: 16px;
  height: 16px;
}

.sf-celebration__description {
  margin: 10px 0 0;
  color: var(--sf-fg-secondary);
  font-size: 12.5px;
  line-height: 1.5;
}

.sf-celebration__list {
  display: grid;
  gap: 8px;
  margin: 14px 0 0;
  padding: 0;
  list-style: none;
}

/* 回落行：一条灰底卡片（旧的纯文本形态）。 */
.sf-celebration__row--text {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--sf-border);
  border-radius: 10px;
  background: var(--sf-muted);
}

.sf-celebration__row-glyph {
  flex: 0 0 auto;
  width: 15px;
  height: 15px;
  margin-top: 2px;
  color: var(--sf-accent);
}

.sf-celebration__row-text {
  min-width: 0;
  color: var(--sf-fg);
  font-size: 12.5px;
  line-height: 1.45;
  overflow-wrap: anywhere;
}

/* 命中行：信息块自带层次，不再套一层卡片；多条之间用发丝线分隔。 */
.sf-celebration__row--badge {
  display: block;
  padding: 0;
}

.sf-celebration__row--badge + .sf-celebration__row--badge {
  padding-top: 14px;
  border-top: 1px solid var(--sf-border);
}

.sf-celebration__footer {
  display: flex;
  justify-content: flex-end;
  margin-top: 18px;
}

.sf-celebration__primary {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 7px 16px;
  border: 1px solid var(--sf-accent);
  border-radius: 999px;
  background: var(--sf-accent);
  color: var(--sf-public-surface);
  font-size: 12.5px;
  font-weight: 600;
  line-height: 1.4;
}

.sf-celebration__primary:hover {
  opacity: 0.9;
}

.sf-celebration__primary:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}
</style>
