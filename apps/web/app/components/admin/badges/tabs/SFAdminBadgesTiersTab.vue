<script setup lang="ts">
/**
 * 等级分区：列表（色点 + 图标 + 中英名 + 徽章数 + 启停）、上下调序、增删改入口。
 *
 * 位次就是语义：公开面与后台列表都按 `position` 升序展示，所以调序用「上移 / 下移」
 * 两个按钮而不是拖拽 —— 拖拽在 390 宽度下不可用，而按钮两端都有 aria-label。
 *
 * 停用等级仍然列出（否则无法重新启用），但不参与新授予与筛选。
 */
import SFAdminBadgeTierDialog from '~/components/admin/badges/SFAdminBadgeTierDialog.vue'
import SFAdminBadgeTierDeleteDialog from '~/components/admin/badges/SFAdminBadgeTierDeleteDialog.vue'
import type { AdminBadgesStore, AdminBadgeTierRow } from '~/composables/admin/badges/useAdminBadges'
import { isTierCustomColor, tierColorVar } from '~/utils/theme/tierColor'

const props = defineProps<{
  store: AdminBadgesStore
  tiers: AdminBadgeTierRow[]
  colorChoices: string[]
  canManage: boolean
}>()

const { t } = useI18n()
const toast = useToast()

const ordered = computed(() => [...props.tiers].sort((left, right) => left.position - right.position))

const dialogOpen = ref(false)
const editing = ref<AdminBadgeTierRow | null>(null)
const deleteOpen = ref(false)
const deleting = ref<AdminBadgeTierRow | null>(null)
const togglePendingKey = ref<string | null>(null)
const reorderPending = ref(false)

/** 迁移目标只能是**其它启用中**的等级：迁移到停用档等于把徽章挂到一个不展示的档上。 */
const migrateTargets = computed(() =>
  ordered.value.filter(tier => tier.key !== deleting.value?.key && tier.enabled)
)

/**
 * 停用最后一档等于清空全站最高级徽章（topBadgeOrderSQL 只认启用档）。
 * 后端已经按「至少保留一个启用档」拒绝，这里只是把入口关掉——不让运营点进一个
 * 必然 422 的操作。判据与删除路径同口径。
 */
const enabledCount = computed(() => ordered.value.filter(tier => tier.enabled).length)

function isLastEnabled(tier: AdminBadgeTierRow) {
  return tier.enabled && enabledCount.value <= 1
}

/**
 * 色点与图标共用的等级色变量。
 *
 * token（`var(--sf-tl-*)`）自带明暗两套取值；自定义 hex 只有一组，深色下按信任等级
 * 同一条公式提亮（`color-mix(in srgb, <hex> 74%, white)`）。两个值都下发、由 CSS
 * 按 `.dark` 挑一个——行内只能写一个值，写死就必然在另一套主题里失效。
 */
function tierAccentVars(tier: AdminBadgeTierRow): Record<string, string> {
  const accent = tierColorVar(tier.color)
  const dark = isTierCustomColor(accent) ? `color-mix(in srgb, ${accent} 74%, white)` : accent
  return {
    '--sf-tier-accent': accent,
    '--sf-tier-accent-dark': dark
  }
}

function openCreate() {
  editing.value = null
  dialogOpen.value = true
}

function openEdit(tier: AdminBadgeTierRow) {
  editing.value = tier
  dialogOpen.value = true
}

function openDelete(tier: AdminBadgeTierRow) {
  deleting.value = tier
  deleteOpen.value = true
}

function onSaved() {
  void props.store.load()
}

async function toggleEnabled(tier: AdminBadgeTierRow) {
  if (!props.canManage || togglePendingKey.value !== null || isLastEnabled(tier)) return
  togglePendingKey.value = tier.key
  try {
    await props.store.updateTier(tier.key, { enabled: !tier.enabled })
    toast.add({
      title: tier.enabled ? t('admin.badges.tiers.toast.disabled') : t('admin.badges.tiers.toast.enabled'),
      color: 'success',
      icon: 'i-lucide-check',
      duration: 5000
    })
  } catch {
    toast.add({ title: t('admin.badges.tiers.toast.saveFailed'), color: 'error', icon: 'i-lucide-triangle-alert', duration: 0 })
  } finally {
    togglePendingKey.value = null
  }
}

/** 调序发的是**全量 keys**：服务端按数组顺序重排，避免「只发两个 key」的歧义。 */
async function move(index: number, delta: -1 | 1) {
  if (!props.canManage || reorderPending.value) return
  const target = index + delta
  if (target < 0 || target >= ordered.value.length) return
  const keys = ordered.value.map(tier => tier.key)
  const moved = keys[index]
  const swapped = keys[target]
  if (!moved || !swapped) return
  keys[index] = swapped
  keys[target] = moved
  reorderPending.value = true
  try {
    await props.store.reorderTiers(keys)
  } catch {
    toast.add({ title: t('admin.badges.tiers.toast.orderFailed'), color: 'error', icon: 'i-lucide-triangle-alert', duration: 0 })
  } finally {
    reorderPending.value = false
  }
}

defineOptions({ name: 'SFAdminBadgesTiersTab' })
</script>

<template>
  <div>
    <div class="mb-4 flex flex-wrap items-center gap-2">
      <p class="min-w-0 flex-1 text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.badges.tiers.hint') }}</p>
      <UButton v-if="props.canManage" color="primary" icon="i-lucide-plus" @click="openCreate">
        {{ t('admin.badges.tiers.create') }}
      </UButton>
    </div>

    <div v-if="ordered.length === 0" class="rounded-lg border border-dashed border-slate-300 p-8 text-center dark:border-zinc-700">
      <UIcon name="i-lucide-layers" class="mx-auto size-8 text-slate-400 dark:text-zinc-500" aria-hidden="true" />
      <p class="mt-3 text-sm font-semibold text-slate-700 dark:text-zinc-200">{{ t('admin.badges.tiers.empty') }}</p>
      <p class="mx-auto mt-1 max-w-md text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.badges.tiers.emptyGuide') }}</p>
    </div>

    <ul v-else class="flex flex-col gap-2">
      <li
        v-for="(tier, index) in ordered"
        :key="tier.key"
        class="flex flex-wrap items-center gap-2 rounded-lg border border-slate-200 px-3 py-2 dark:border-zinc-800"
        :data-tier="tier.key"
        :data-enabled="tier.enabled ? 'true' : 'false'"
      >
        <span class="sf-tier-dot" :style="tierAccentVars(tier)" aria-hidden="true" />

        <!-- 图标与色点同色：等级的身份就是「色 + 图标 + 名字」，图标保持中性灰会让人以为它只是装饰。 -->
        <UIcon
          :name="tier.icon || 'i-lucide-award'"
          class="sf-tier-icon size-4 shrink-0"
          :style="tierAccentVars(tier)"
          aria-hidden="true"
        />

        <span class="min-w-0 flex-1">
          <span class="block truncate text-sm font-semibold text-slate-900 dark:text-white">{{ tier.name.zh }}</span>
          <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">{{ tier.name.en }} · <code>{{ tier.key }}</code></span>
        </span>

        <span class="whitespace-nowrap text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.badges.tiers.badgeCount', { count: tier.badgeCount }) }}
        </span>

        <USwitch
          :model-value="tier.enabled"
          :disabled="!props.canManage || togglePendingKey !== null || isLastEnabled(tier)"
          :aria-label="t('admin.badges.tiers.toggleLabel', { name: tier.name.zh })"
          :title="isLastEnabled(tier) ? t('admin.badges.tiers.lastEnabledHint') : undefined"
          @update:model-value="toggleEnabled(tier)"
        />

        <div class="flex items-center gap-1">
          <UButton
            color="neutral"
            variant="ghost"
            size="xs"
            icon="i-lucide-arrow-up"
            :disabled="!props.canManage || index === 0 || reorderPending"
            :aria-label="t('admin.badges.tiers.moveUpLabel', { name: tier.name.zh })"
            :title="t('admin.badges.tiers.moveUp')"
            @click="move(index, -1)"
          />
          <UButton
            color="neutral"
            variant="ghost"
            size="xs"
            icon="i-lucide-arrow-down"
            :disabled="!props.canManage || index === ordered.length - 1 || reorderPending"
            :aria-label="t('admin.badges.tiers.moveDownLabel', { name: tier.name.zh })"
            :title="t('admin.badges.tiers.moveDown')"
            @click="move(index, 1)"
          />
          <UButton
            color="neutral"
            variant="ghost"
            size="xs"
            icon="i-lucide-pencil"
            :disabled="!props.canManage"
            :aria-label="t('admin.badges.tiers.editLabel', { name: tier.name.zh })"
            :title="t('admin.badges.tiers.edit')"
            @click="openEdit(tier)"
          />
          <UButton
            color="error"
            variant="ghost"
            size="xs"
            icon="i-lucide-trash-2"
            :disabled="!props.canManage"
            :aria-label="t('admin.badges.tiers.deleteLabel', { name: tier.name.zh })"
            :title="t('admin.badges.tiers.delete')"
            @click="openDelete(tier)"
          />
        </div>
      </li>
    </ul>

    <SFAdminBadgeTierDialog
      v-model:open="dialogOpen"
      :tier="editing"
      :color-choices="props.colorChoices"
      :store="props.store"
      @saved="onSaved"
    />
    <SFAdminBadgeTierDeleteDialog
      v-model:open="deleteOpen"
      :tier="deleting"
      :targets="migrateTargets"
      :store="props.store"
      @deleted="onSaved"
    />
  </div>
</template>

<style scoped>
.sf-tier-dot {
  display: inline-flex;
  flex: 0 0 auto;
  width: 10px;
  height: 10px;
  border-radius: 999px;
  background: var(--sf-tier-accent);
}

.sf-tier-icon {
  color: var(--sf-tier-accent);
}

/* 深色：自定义 hex 用提亮后的值；token 的明暗由 `--sf-tl-*` 自己处理。 */
:global(.dark) .sf-tier-dot {
  background: var(--sf-tier-accent-dark, var(--sf-tier-accent));
}

:global(.dark) .sf-tier-icon {
  color: var(--sf-tier-accent-dark, var(--sf-tier-accent));
}
</style>
