<script setup lang="ts">
/**
 * 全局徽章说明弹窗宿主：挂在 `app.vue` 的 `</NuxtLayout>` 之后，与庆祝宿主同层。
 *
 * 入口是三处昵称旁的 chip（资料页头部、主题作者行、评论作者行）；弹窗本身复用
 * `SFBadgeDetailModal`，不新建第二个弹窗。取数/缓存/选中项都在 `useForumBadgeDialog`。
 *
 * 加载期与失败态：`SFBadgeDetailModal` 对 `badge=null` 只渲染空 section，所以这里
 * **先不开详情弹窗**，而是开一个轻量 UModal 显示骨架/失败文案——点了必须有反应。
 * 失败时只给文案与关闭，不拿 chip 的 fallback 拼一颗缺说明、缺条件的假徽章。
 *
 * 佩戴动作与资料页徽章墙共用 `useBadgeWearToggle`（乐观更新 + toast 只有一份）；
 * `worn` 的判定按账号归属走 `loadedFor(viewerId)`，账号切换后不会显示上一账号的佩戴态。
 */
import SFBadgeDetailModal from '~/components/forum/badges/SFBadgeDetailModal.vue'
import { useForumBadgeDialog } from '~/composables/forum/badges/useForumBadgeDialog'
import { useBadgeWearToggle } from '~/composables/forum/badges/useBadgeWearToggle'
import { useBadgeWorn } from '~/composables/forum/badges/useBadgeWorn'
import { useAuthSession } from '~/composables/identity/useAuthSession'

const { t } = useI18n()
const { state, entry, groupName, loading, closeBadgeDialog } = useForumBadgeDialog()
const { user } = useAuthSession()
const worn = useBadgeWorn()
const { toggleWear } = useBadgeWearToggle()

const viewerId = computed(() => Number(user.value?.id ?? 0))
const ownerIsViewer = computed(() => {
  const username = state.value.username
  return Boolean(username) && username === String(user.value?.username || '')
})
// 佩戴按钮只属于「本人的、已获得的」徽章；他人页与未获得都只读。
const canWear = computed(() => ownerIsViewer.value && Boolean(entry.value?.earned))
const wornCurrent = computed(() => Boolean(
  entry.value
  && ownerIsViewer.value
  && worn.loadedFor(viewerId.value)
  && worn.isWorn(entry.value.slug)
))
// 墙已到但 slug 不在（徽章停用/删除）与请求失败是同一个出口：不伪造对象。
const unavailable = computed(() => state.value.open && !entry.value && !loading.value)

watch(ownerIsViewer, (self) => {
  if (self) void worn.ensureLoaded()
}, { immediate: true })

function onUpdateOpen(open: boolean) {
  if (!open) closeBadgeDialog()
}

async function onToggleWear() {
  const badge = entry.value
  if (!badge || !canWear.value) return
  await toggleWear(badge)
}
</script>

<template>
  <SFBadgeDetailModal
    v-if="state.open && entry"
    :open="state.open"
    :badge="entry"
    :group-name="groupName"
    variant="profile"
    :self="ownerIsViewer"
    :worn="wornCurrent"
    @update:open="onUpdateOpen"
    @toggle-wear="onToggleWear"
  />

  <!-- 数据未就绪：轻量弹窗承载加载/失败，避免点 chip 后毫无反应。 -->
  <UModal
    v-else-if="state.open"
    :open="state.open"
    :title="t('badges.detail.title')"
    :ui="{ content: 'sf-badge-dialog-state' }"
    @update:open="onUpdateOpen"
  >
    <template #content>
      <section class="sf-badge-dialog-state__body">
        <template v-if="!unavailable">
          <p class="sf-badge-dialog-state__text">{{ t('badges.dialog.loading') }}</p>
          <SFSkeleton :lines="4" />
        </template>
        <p v-else class="sf-badge-dialog-state__text">{{ t('badges.dialog.failed') }}</p>
        <!-- 加载与失败都必须有可见关闭出口：请求挂住时不能只剩 ESC 一条路。 -->
        <button type="button" class="sf-badge-dialog-state__close" @click="closeBadgeDialog">
          {{ t('common.close') }}
        </button>
      </section>
    </template>
  </UModal>
</template>

<style scoped>
.sf-badge-dialog-state__body {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 20px;
}

.sf-badge-dialog-state__text {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 13px;
  line-height: 1.6;
}

.sf-badge-dialog-state__close {
  align-self: flex-end;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  padding: 6px 12px;
  color: var(--sf-fg);
  font-size: 13px;
  font-weight: 650;
}
</style>
