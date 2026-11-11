<script setup lang="ts">
/**
 * 分组 tab：分组列表 + 创建/编辑 + 删除两步确认。
 *
 * 后端 DELETE group 不拒绝有徽章的组：组内徽章由外键 SET NULL 落成未分组
 * （admin_service.go DeleteGroup）。两步确认文案按 badgeCount 分两种，让运营
 * 在确认前就知道删除的实际后果。
 */
import type { AdminBadgeGroupRow, useAdminBadges } from '~/composables/admin/badges/useAdminBadges'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { apiErrorMessage } from '~/composables/useApiClient'
import { validateBadgeGroupForm, type AdminBadgeGroupInput } from '~/utils/admin/adminBadges'

const props = defineProps<{
  store: ReturnType<typeof useAdminBadges>
  groups: AdminBadgeGroupRow[]
  pending: boolean
  canManage: boolean
}>()

const { t } = useI18n()
const toast = useToast()
const { can } = useAuthSession()

const canManage = computed(() => props.canManage && can('badge.manage'))

const dialogOpen = ref(false)
const editing = ref<AdminBadgeGroupRow | null>(null)
const form = reactive({
  key: '',
  name: '',
  description: '',
  position: 0
})
const submitting = ref(false)
const errorMessage = ref('')
const fieldErrors = ref<{ key?: string, name?: string }>({})

// 删除两步确认：第一次点击只确认，第二次才发 DELETE。按行独立记忆确认态。
const confirmDeleteId = ref<number | null>(null)
const deletingId = ref<number | null>(null)

const isEdit = computed(() => Boolean(editing.value))
const dialogTitle = computed(() =>
  isEdit.value ? t('admin.badges.groups.form.editTitle') : t('admin.badges.groups.form.createTitle')
)

watch(dialogOpen, (value) => {
  if (!value) return
  errorMessage.value = ''
  fieldErrors.value = {}
  const group = editing.value
  form.key = group?.key ?? ''
  form.name = group?.name ?? ''
  form.description = group?.description ?? ''
  form.position = group?.position ?? 0
})

function openCreate() {
  editing.value = null
  dialogOpen.value = true
}

function openEdit(group: AdminBadgeGroupRow) {
  editing.value = group
  dialogOpen.value = true
}

function closeDialog() {
  dialogOpen.value = false
}

async function submit() {
  const errors = validateBadgeGroupForm({ key: form.key, name: form.name })
  if (Object.keys(errors).length > 0 || submitting.value) {
    fieldErrors.value = errors
    return
  }
  const input: AdminBadgeGroupInput = {
    key: form.key.trim(),
    name: form.name.trim(),
    description: form.description.trim(),
    position: Number(form.position) || 0
  }
  submitting.value = true
  errorMessage.value = ''
  try {
    if (isEdit.value && editing.value) {
      await props.store.updateBadgeGroup(editing.value.id, input)
      toast.add({ title: t('admin.badges.groups.toast.updated'), color: 'success', icon: 'i-lucide-check', duration: 5000 })
    } else {
      await props.store.createBadgeGroup(input)
      toast.add({ title: t('admin.badges.groups.toast.created'), color: 'success', icon: 'i-lucide-check', duration: 5000 })
    }
    closeDialog()
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.badges.groups.toast.saveFailed')
  } finally {
    submitting.value = false
  }
}

function deleteTitle(group: AdminBadgeGroupRow): string {
  return group.badgeCount > 0
    ? t('admin.badges.groups.confirmDeleteBodyWithBadges', { count: group.badgeCount })
    : t('admin.badges.groups.confirmDeleteBody')
}

// 确认态跟着列表走：组被别处删掉后 find 会落空，非空断言会在渲染期炸掉整个分区。
const confirmGroup = computed(() => props.groups.find(group => group.id === confirmDeleteId.value) ?? null)

async function removeGroup(group: AdminBadgeGroupRow) {
  if (!canManage.value || deletingId.value !== null) return
  if (confirmDeleteId.value !== group.id) {
    confirmDeleteId.value = group.id
    return
  }
  deletingId.value = group.id
  try {
    await props.store.deleteBadgeGroup(group.id)
    confirmDeleteId.value = null
    toast.add({
      title: t('admin.badges.groups.toast.deleted'),
      color: 'success',
      icon: 'i-lucide-trash-2',
      duration: 5000
    })
  } catch {
    toast.add({
      title: t('admin.badges.groups.toast.deleteFailed'),
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      duration: 0
    })
  } finally {
    deletingId.value = null
  }
}

defineOptions({ name: 'SFAdminBadgesGroupsTab' })
</script>

<template>
  <div>
    <div class="mb-4 flex items-center">
      <p class="min-w-0 truncate text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.badges.groups.listHint') }}</p>
      <UButton
        v-if="canManage"
        color="primary"
        icon="i-lucide-folder-plus"
        class="ml-auto"
        @click="openCreate"
      >
        {{ t('admin.badges.groups.create') }}
      </UButton>
    </div>

    <div v-if="groups.length === 0" class="rounded-lg border border-dashed border-slate-300 p-8 text-center dark:border-zinc-700">
      <UIcon name="i-lucide-folder-open" class="mx-auto size-8 text-slate-400 dark:text-zinc-500" aria-hidden="true" />
      <p class="mt-3 text-sm font-semibold text-slate-700 dark:text-zinc-200">{{ t('admin.badges.groups.empty') }}</p>
      <p class="mx-auto mt-1 max-w-md text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.badges.groups.emptyGuide') }}</p>
    </div>

    <UTable
      v-else
      :data="groups"
      :columns="[
        { id: 'name', header: t('admin.badges.groups.columns.name') },
        { id: 'key', header: t('admin.badges.groups.columns.key'), meta: { class: { th: 'w-[160px]', td: 'w-[160px]' } } },
        { id: 'badgeCount', header: t('admin.badges.groups.columns.badgeCount'), meta: { class: { th: 'w-[96px] text-right', td: 'w-[96px] text-right' } } },
        { id: 'position', header: t('admin.badges.groups.columns.position'), meta: { class: { th: 'w-[96px] text-right', td: 'w-[96px] text-right' } } },
        { id: 'actions', header: t('admin.badges.columns.actions'), meta: { class: { th: 'w-[96px]', td: 'w-[96px]' } } }
      ]"
      :loading="pending"
      :caption="t('admin.badges.groups.caption')"
    >
      <template #name-cell="{ row }">
        <div class="min-w-0">
          <div class="truncate font-semibold text-slate-900 dark:text-white">{{ row.original.name }}</div>
          <div v-if="row.original.description" class="truncate text-xs text-slate-500 dark:text-zinc-400">
            {{ row.original.description }}
          </div>
        </div>
      </template>

      <template #key-cell="{ row }">
        <code class="text-xs text-slate-500 dark:text-zinc-400">{{ row.original.key }}</code>
      </template>

      <template #badgeCount-cell="{ row }">
        <span class="tabular-nums text-sm text-slate-600 dark:text-zinc-300">{{ row.original.badgeCount }}</span>
      </template>

      <template #position-cell="{ row }">
        <span class="tabular-nums text-sm text-slate-600 dark:text-zinc-300">{{ row.original.position }}</span>
      </template>

      <template #actions-cell="{ row }">
        <div v-if="canManage" class="flex items-center gap-1">
          <UButton
            color="neutral"
            variant="ghost"
            size="xs"
            icon="i-lucide-pencil"
            :aria-label="t('admin.badges.groups.editLabel', { name: row.original.name })"
            :title="t('admin.badges.groups.edit')"
            @click="openEdit(row.original)"
          />
          <UButton
            color="neutral"
            variant="ghost"
            size="xs"
            :icon="confirmDeleteId === row.original.id ? 'i-lucide-triangle-alert' : 'i-lucide-trash-2'"
            :loading="deletingId === row.original.id"
            :aria-label="t('admin.badges.groups.deleteLabel', { name: row.original.name })"
            :title="confirmDeleteId === row.original.id
              ? t('admin.badges.groups.confirmDeleteTitle')
              : t('admin.badges.groups.delete')"
            @click="removeGroup(row.original)"
          />
        </div>
        <span v-else class="text-sm text-slate-400 dark:text-zinc-500">—</span>
      </template>
    </UTable>

    <p
      v-if="confirmGroup"
      class="mt-2 text-sm text-amber-600 dark:text-amber-400"
    >
      {{ deleteTitle(confirmGroup) }}
    </p>

    <UModal v-model:open="dialogOpen" :ui="{ content: 'sm:max-w-md' }" @update:open="(value) => { if (!value) closeDialog() }">
      <template #content>
        <div class="flex flex-col">
          <div class="border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
            <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ dialogTitle }}</h3>
          </div>

          <div class="space-y-3 px-5 py-4">
            <UFormField :label="t('admin.badges.groups.form.key')" :error="fieldErrors.key">
              <UInput v-model="form.key" :disabled="isEdit" class="w-full" />
              <template #hint>
                <span class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.badges.groups.form.keyHint') }}</span>
              </template>
            </UFormField>
            <UFormField :label="t('admin.badges.groups.form.name')" :error="fieldErrors.name">
              <UInput v-model="form.name" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.badges.groups.form.description')">
              <UTextarea v-model="form.description" :rows="2" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.badges.groups.form.position')">
              <UInput v-model.number="form.position" type="number" class="w-full" />
            </UFormField>
            <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
          </div>

          <div class="flex items-center justify-end gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
            <UButton color="neutral" variant="ghost" @click="closeDialog">{{ t('admin.badges.actions.cancel') }}</UButton>
            <UButton color="primary" :loading="submitting" @click="submit">{{ t('admin.badges.actions.save') }}</UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>
