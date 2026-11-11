<script setup lang="ts">
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import {
  createAdminForumApi,
  createTagPayload,
  forumTagStatusChoices,
  type AdminForumTagPayload
} from '~/utils/admin/adminForum'
import { taxonomyColorInputValue, taxonomyIconColor } from '~/utils/forum/forumTaxonomy'
import type { ForumTag, ForumTagStatus } from '~/utils/forum/forumTaxonomy'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminForumTags'
})

const { t } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const forumApi = createAdminForumApi(request)
const adminPage = useAdminPage('/forum/tags')

const activeStatus = ref<ForumTagStatus>('active')
const tagModalOpen = ref(false)
const editingTagId = ref<number | null>(null)
const saving = ref(false)
const updatingId = ref<number | null>(null)
const form = reactive(createTagPayload())

const { data: tags, pending, error, refresh } = await useAsyncData(
  'admin-forum-tags',
  () => forumApi.listTags(),
  { default: () => [] as ForumTag[] }
)

useSeoMeta({
  title: t('admin.forum.tags.metaTitle')
})

const tabs = computed(() => forumTagStatusChoices.map((status) => ({
  status,
  label: t(`admin.forum.tagStatus.${status}`),
  count: tags.value.filter((tag) => tag.status === status).length
})))

const filteredTags = computed(() => tags.value.filter((tag) => tag.status === activeStatus.value))

const defaultTagIcon = 'i-lucide-tag'

function defaultCreateStatus(): ForumTagStatus {
  // 在「已停用」列表点新建时，默认仍创建为启用，避免误建停用标签
  return activeStatus.value === 'disabled' ? 'active' : activeStatus.value
}

function tagPreviewIcon(tag: Pick<ForumTag, 'icon'> | AdminForumTagPayload) {
  return tag.icon || defaultTagIcon
}

function setTagColor(event: Event) {
  const target = event.target
  if (target instanceof HTMLInputElement) {
    form.iconColor = target.value.toLowerCase()
  }
}

function clearTagColor() {
  form.iconColor = ''
}

function openCreateTag() {
  editingTagId.value = null
  Object.assign(form, createTagPayload({ status: defaultCreateStatus() }))
  tagModalOpen.value = true
}

function openEditTag(tag: ForumTag) {
  editingTagId.value = tag.id
  Object.assign(form, createTagPayload(tag))
  tagModalOpen.value = true
}

function closeTagModal() {
  tagModalOpen.value = false
  editingTagId.value = null
  Object.assign(form, createTagPayload({ status: defaultCreateStatus() }))
}

async function saveTag() {
  saving.value = true
  try {
    if (editingTagId.value) {
      await forumApi.updateTag(editingTagId.value, tagPayload())
      successToast(t('admin.forum.tags.updated'))
    } else {
      await forumApi.createTag(tagPayload())
      successToast(t('admin.forum.tags.created'))
    }
    closeTagModal()
    await refresh()
  } catch (error) {
    errorToast(error, t('admin.forum.tags.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function setTagStatus(tag: ForumTag, status: ForumTagStatus) {
  updatingId.value = tag.id
  try {
    await forumApi.updateTag(tag.id, { status })
    successToast(status === 'active' ? t('admin.forum.tags.approved') : t('admin.forum.tags.disabled'))
    await refresh()
  } catch (error) {
    errorToast(error, t('admin.forum.tags.statusFailed'))
  } finally {
    updatingId.value = null
  }
}

function setActiveStatus(status: ForumTagStatus) {
  activeStatus.value = status
}

function tagPayload(): AdminForumTagPayload {
  return {
    slug: form.slug.trim(),
    name: form.name.trim(),
    description: form.description.trim(),
    icon: form.icon.trim(),
    iconColor: form.iconColor.trim(),
    status: form.status
  }
}

function successToast(title: string) {
  toast.add({
    color: 'success',
    icon: 'i-lucide-check',
    title,
    duration: 5000
  })
}

function errorToast(error: unknown, fallback: string) {
  toast.add({
    color: 'error',
    icon: 'i-lucide-triangle-alert',
    title: apiErrorMessage(error) || fallback
  })
}
</script>

<template>
  <div class="mb-4">
    <h2 class="text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.forum.tags.title') }}
    </h2>
  </div>

  <UDashboardToolbar class="border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 rounded-lg px-4 py-2.5 mb-6 text-slate-500 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm text-slate-500 dark:text-zinc-400">
        <UIcon name="i-lucide-tags" class="size-4" />
        <span class="truncate">{{ t('admin.forum.tags.intro') }}</span>
      </div>
    </template>
    <template #right>
      <div class="flex flex-wrap items-center gap-2">
        <UButton
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-refresh-cw"
          :loading="pending"
          class="border-slate-200 dark:border-zinc-700"
          @click="refresh()"
        >
          {{ t('admin.common.refresh') }}
        </UButton>
        <UButton leading-icon="i-lucide-plus" @click="openCreateTag">
          {{ t('admin.forum.tags.create') }}
        </UButton>
      </div>
    </template>
  </UDashboardToolbar>

  <div class="grid gap-4">
    <UAlert
      v-if="error"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      :title="t('admin.forum.tags.loadFailed')"
    />

    <UAlert
      color="primary"
      variant="soft"
      icon="i-lucide-info"
      :title="t('admin.forum.tags.policyTitle')"
      :description="t('admin.forum.tags.policyDescription')"
    />

    <UCard class="border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.forum.tags.listTitle') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.forum.tags.listHelp') }}
            </p>
          </div>
          <div class="flex flex-wrap gap-2">
            <UButton
              v-for="tab in tabs"
              :key="tab.status"
              type="button"
              size="sm"
              :color="activeStatus === tab.status ? 'primary' : 'neutral'"
              :variant="activeStatus === tab.status ? 'solid' : 'outline'"
              @click="setActiveStatus(tab.status)"
            >
              {{ tab.label }}
              <UBadge color="neutral" variant="soft" class="ml-1">
                {{ tab.count }}
              </UBadge>
            </UButton>
          </div>
        </div>
      </template>

      <div v-if="filteredTags.length" class="overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800">
        <div
          v-for="tag in filteredTags"
          :key="tag.id"
          class="grid gap-3 border-b border-slate-100 px-4 py-3 last:border-b-0 dark:border-zinc-800 lg:grid-cols-[1fr_auto]"
        >
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <span class="inline-flex min-w-0 items-center gap-2 font-semibold text-slate-900 dark:text-zinc-100">
                <UIcon
                  :name="tagPreviewIcon(tag)"
                  class="size-4 shrink-0"
                  :style="{ color: taxonomyIconColor(tag.iconColor) }"
                  aria-hidden="true"
                />
                <span class="truncate">#{{ tag.name }}</span>
              </span>
              <UBadge color="neutral" variant="soft" class="font-mono">
                {{ tag.slug }}
              </UBadge>
              <UBadge :color="tag.status === 'active' ? 'success' : tag.status === 'pending' ? 'warning' : 'neutral'" variant="soft">
                {{ t(`admin.forum.tagStatus.${tag.status}`) }}
              </UBadge>
            </div>
            <p v-if="tag.description" class="mt-1 text-sm text-slate-500 dark:text-zinc-400">
              {{ tag.description }}
            </p>
            <p class="mt-1 text-xs text-slate-400 dark:text-zinc-500">
              {{ t('admin.forum.tags.topicCount', { count: tag.topicCount }) }}
            </p>
          </div>
          <div class="flex flex-wrap items-center justify-end gap-2">
            <UButton color="neutral" variant="ghost" size="sm" leading-icon="i-lucide-pencil" @click="openEditTag(tag)">
              {{ t('admin.common.edit') }}
            </UButton>
            <UButton
              v-if="tag.status === 'pending'"
              color="success"
              variant="outline"
              size="sm"
              leading-icon="i-lucide-check"
              :loading="updatingId === tag.id"
              @click="setTagStatus(tag, 'active')"
            >
              {{ t('admin.forum.tags.approve') }}
            </UButton>
            <UButton
              v-if="tag.status !== 'disabled'"
              color="neutral"
              variant="outline"
              size="sm"
              leading-icon="i-lucide-ban"
              :loading="updatingId === tag.id"
              @click="setTagStatus(tag, 'disabled')"
            >
              {{ t('admin.forum.tags.disable') }}
            </UButton>
          </div>
        </div>
      </div>

      <SFEmptyState
        v-else
        icon-label="TAG"
        :title="t('admin.forum.tags.empty')"
        :description="t('admin.forum.tags.emptyDescription')"
      />
    </UCard>
  </div>

  <!-- 标签：新建 / 编辑 -->
  <UModal
    v-model:open="tagModalOpen"
    :ui="{ content: 'sm:max-w-2xl' }"
    @update:open="(open) => { if (!open) closeTagModal() }"
  >
    <template #content>
      <form class="flex max-h-[85vh] flex-col" @submit.prevent="saveTag">
        <div class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ editingTagId ? t('admin.forum.tags.edit') : t('admin.forum.tags.create') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.forum.tags.formHelp') }}
            </p>
          </div>
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            icon="i-lucide-x"
            :aria-label="t('admin.common.cancel')"
            @click="closeTagModal"
          />
        </div>

        <div class="grid flex-1 gap-4 overflow-y-auto px-5 py-4">
          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.forum.tags.slug')" name="tag-slug">
              <UInput v-model="form.slug" icon="i-lucide-link" required class="w-full" placeholder="nuxt" />
            </UFormField>
            <UFormField :label="t('admin.forum.tags.name')" name="tag-name">
              <UInput v-model="form.name" icon="i-lucide-tag" required class="w-full" />
            </UFormField>
          </div>

          <UFormField :label="t('admin.forum.tags.description')" name="tag-description">
            <UInput v-model="form.description" icon="i-lucide-file-text" class="w-full" />
          </UFormField>

          <UFormField :label="t('admin.forum.tags.status')" name="tag-status">
            <select v-model="form.status" class="h-10 w-full rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100">
              <option v-for="status in forumTagStatusChoices" :key="status" :value="status">
                {{ t(`admin.forum.tagStatus.${status}`) }}
              </option>
            </select>
          </UFormField>

          <div class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_220px]">
            <LazySFIconPicker
              v-model="form.icon"
              :label="t('admin.forum.visual.icon')"
              :hint="t('admin.forum.visual.iconHelp')"
            />
            <UFormField :label="t('admin.forum.visual.iconColor')" name="tag-icon-color">
              <div class="grid gap-2">
                <div class="flex items-center gap-2">
                  <input
                    :value="taxonomyColorInputValue(form.iconColor)"
                    type="color"
                    class="h-10 w-12 rounded-md border border-slate-200 bg-white p-1 dark:border-zinc-700 dark:bg-zinc-950"
                    :aria-label="t('admin.forum.visual.iconColor')"
                    @input="setTagColor"
                  >
                  <UInput v-model="form.iconColor" placeholder="#2563eb" class="min-w-0 flex-1" />
                </div>
                <div class="flex items-center justify-between gap-2">
                  <span class="inline-flex items-center gap-2 text-xs text-slate-500 dark:text-zinc-400">
                    <UIcon :name="tagPreviewIcon(form)" class="size-4" :style="{ color: taxonomyIconColor(form.iconColor) }" />
                    {{ form.iconColor || t('admin.forum.visual.defaultAccent') }}
                  </span>
                  <UButton type="button" size="xs" color="neutral" variant="ghost" leading-icon="i-lucide-x" @click="clearTagColor">
                    {{ t('admin.forum.visual.clearColor') }}
                  </UButton>
                </div>
              </div>
            </UFormField>
          </div>
        </div>

        <div class="flex flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-4 dark:border-zinc-800">
          <UButton type="button" color="neutral" variant="ghost" @click="closeTagModal">
            {{ t('admin.common.cancel') }}
          </UButton>
          <UButton type="submit" leading-icon="i-lucide-save" :loading="saving">
            {{ t('admin.common.save') }}
          </UButton>
        </div>
      </form>
    </template>
  </UModal>
</template>
