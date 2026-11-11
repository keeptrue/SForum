<script setup lang="ts">
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import SFAdminForumTaxonomyDeleteDialog from '~/components/admin/forum/SFAdminForumTaxonomyDeleteDialog.vue'
import {
  createAdminForumApi,
  createCategoryGroupPayload,
  createCategoryPayload,
  forumCategorySortChoices,
  forumTaxonomyDeleteErrorKey,
  forumVisibilityChoices,
  type AdminForumCategoryGroupPayload,
  type AdminForumCategoryPayload,
  type AdminForumTaxonomyDeleteTarget
} from '~/utils/admin/adminForum'
import { taxonomyColorInputValue, taxonomyIconColor } from '~/utils/forum/forumTaxonomy'
import type { ForumCategory, ForumCategoryGroup } from '~/utils/forum/forumTaxonomy'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminForumCategories'
})

const { t } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const forumApi = createAdminForumApi(request)
const adminPage = useAdminPage('/forum/categories')

const groupModalOpen = ref(false)
const categoryModalOpen = ref(false)
const editingGroupId = ref<number | null>(null)
const editingCategoryId = ref<number | null>(null)
const savingGroup = ref(false)
const savingCategory = ref(false)
const taxonomyDeleteTarget = ref<AdminForumTaxonomyDeleteTarget | null>(null)
const deletingTaxonomy = ref(false)

const taxonomyDeleteOpen = computed({
  get: () => taxonomyDeleteTarget.value !== null,
  set: (value: boolean) => {
    if (!value) {
      taxonomyDeleteTarget.value = null
    }
  }
})

const groupForm = reactive(createCategoryGroupPayload())
const categoryForm = reactive(createCategoryPayload())

const { data: groups, pending, error, refresh } = await useAsyncData(
  'admin-forum-category-groups',
  () => forumApi.listCategoryGroups(),
  { default: () => [] as ForumCategoryGroup[] }
)

useSeoMeta({
  title: t('admin.forum.categories.metaTitle')
})

const allCategories = computed(() => groups.value.flatMap((group) => group.categories || []))
const groupOptions = computed(() => groups.value.map((group) => ({ label: group.name, value: group.id })))

const selectedGroupName = computed(() => {
  return groups.value.find((group) => group.id === categoryForm.groupId)?.name || t('admin.forum.categories.noGroupSelected')
})

const defaultCategoryIcon = 'i-lucide-folder-open'

function categoryPreviewIcon(category: Pick<ForumCategory, 'icon'> | AdminForumCategoryPayload) {
  return category.icon || defaultCategoryIcon
}

function setCategoryColor(event: Event) {
  const target = event.target
  if (target instanceof HTMLInputElement) {
    categoryForm.iconColor = target.value.toLowerCase()
  }
}

function clearCategoryColor() {
  categoryForm.iconColor = ''
}

function openCreateGroup() {
  editingGroupId.value = null
  Object.assign(groupForm, createCategoryGroupPayload())
  groupModalOpen.value = true
}

function openEditGroup(group: ForumCategoryGroup) {
  editingGroupId.value = group.id
  Object.assign(groupForm, createCategoryGroupPayload(group))
  groupModalOpen.value = true
}

function closeGroupModal() {
  groupModalOpen.value = false
  editingGroupId.value = null
  Object.assign(groupForm, createCategoryGroupPayload())
}

function openCreateCategory(preferredGroupId?: number) {
  if (groups.value.length === 0) {
    toast.add({
      color: 'warning',
      icon: 'i-lucide-info',
      title: t('admin.forum.categories.noGroupsForCategoryTitle'),
      description: t('admin.forum.categories.noGroupsForCategoryDescription'),
      duration: 5000
    })
    return
  }

  editingCategoryId.value = null
  const groupId = preferredGroupId || groups.value[0]?.id || 0
  Object.assign(categoryForm, createCategoryPayload(groupId))
  categoryModalOpen.value = true
}

function openEditCategory(category: ForumCategory) {
  editingCategoryId.value = category.id
  Object.assign(categoryForm, createCategoryPayload(category.groupId, category))
  categoryModalOpen.value = true
}

function closeCategoryModal() {
  categoryModalOpen.value = false
  editingCategoryId.value = null
  Object.assign(categoryForm, createCategoryPayload(groups.value[0]?.id || 0))
}

async function saveGroup() {
  savingGroup.value = true
  try {
    if (editingGroupId.value) {
      await forumApi.updateCategoryGroup(editingGroupId.value, groupPayload())
      successToast(t('admin.forum.categories.groupUpdated'))
    } else {
      await forumApi.createCategoryGroup(groupPayload())
      successToast(t('admin.forum.categories.groupCreated'))
    }
    closeGroupModal()
    await refresh()
  } catch (error) {
    errorToast(error, t('admin.forum.categories.groupSaveFailed'))
  } finally {
    savingGroup.value = false
  }
}

async function saveCategory() {
  savingCategory.value = true
  try {
    if (editingCategoryId.value) {
      await forumApi.updateCategory(editingCategoryId.value, categoryPayload())
      successToast(t('admin.forum.categories.categoryUpdated'))
    } else {
      await forumApi.createCategory(categoryPayload())
      successToast(t('admin.forum.categories.categoryCreated'))
    }
    closeCategoryModal()
    await refresh()
  } catch (error) {
    errorToast(error, t('admin.forum.categories.categorySaveFailed'))
  } finally {
    savingCategory.value = false
  }
}

function groupPayload(): AdminForumCategoryGroupPayload {
  return {
    slug: groupForm.slug.trim(),
    name: groupForm.name.trim(),
    description: groupForm.description.trim(),
    visibility: groupForm.visibility,
    position: Number(groupForm.position) || 0
  }
}

function requestDeleteGroup(group: ForumCategoryGroup) {
  const count = group.categories?.length ?? 0
  taxonomyDeleteTarget.value = {
    kind: 'group',
    id: group.id,
    name: group.name,
    blockedReason: count > 0 ? t('admin.forum.categories.delete.groupBlockedReason', { count }) : ''
  }
}

function requestDeleteCategory(category: ForumCategory) {
  const count = category.topicCount ?? 0
  taxonomyDeleteTarget.value = {
    kind: 'category',
    id: category.id,
    name: category.name,
    blockedReason: count > 0 ? t('admin.forum.categories.delete.categoryBlockedReason', { count }) : ''
  }
}

function closeTaxonomyDelete() {
  taxonomyDeleteTarget.value = null
}

async function confirmTaxonomyDelete() {
  const target = taxonomyDeleteTarget.value
  if (!target || target.blockedReason) {
    return
  }
  deletingTaxonomy.value = true
  try {
    if (target.kind === 'group') {
      await forumApi.deleteCategoryGroup(target.id)
      successToast(t('admin.forum.categories.delete.groupDeleted'))
    } else {
      await forumApi.deleteCategory(target.id)
      successToast(t('admin.forum.categories.delete.categoryDeleted'))
    }
    closeTaxonomyDelete()
    await refresh()
  } catch (error) {
    const key = forumTaxonomyDeleteErrorKey(apiErrorMessage(error))
    if (key) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: t(`admin.forum.categories.delete.errors.${key}`)
      })
    } else {
      errorToast(error, t('admin.forum.categories.delete.failed'))
    }
  } finally {
    deletingTaxonomy.value = false
  }
}

function categoryPayload(): AdminForumCategoryPayload {
  return {
    groupId: Number(categoryForm.groupId) || 0,
    slug: categoryForm.slug.trim(),
    name: categoryForm.name.trim(),
    description: categoryForm.description.trim(),
    icon: categoryForm.icon.trim(),
    iconColor: categoryForm.iconColor.trim(),
    visibility: categoryForm.visibility,
    position: Number(categoryForm.position) || 0,
    defaultSort: categoryForm.defaultSort
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
      {{ t('admin.forum.categories.title') }}
    </h2>
  </div>

  <UDashboardToolbar class="border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 rounded-lg px-4 py-2.5 mb-6 text-slate-500 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm text-slate-500 dark:text-zinc-400">
        <UIcon name="i-lucide-folder-tree" class="size-4" />
        <span class="truncate">{{ t('admin.forum.categories.intro') }}</span>
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
        <UButton
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-folder-plus"
          class="border-slate-200 dark:border-zinc-700"
          @click="openCreateGroup"
        >
          {{ t('admin.forum.categories.createGroup') }}
        </UButton>
        <UButton
          leading-icon="i-lucide-plus"
          :disabled="!pending && groups.length === 0"
          @click="openCreateCategory()"
        >
          {{ t('admin.forum.categories.createCategory') }}
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
      :title="t('admin.forum.categories.loadFailed')"
    />

    <UAlert
      color="primary"
      variant="soft"
      icon="i-lucide-info"
      :title="t('admin.forum.categories.defaultsTitle')"
      :description="t('admin.forum.categories.defaultsDescription')"
    />

    <UCard class="border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900">
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div>
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.forum.categories.structure') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.forum.categories.structureHelp') }}
            </p>
          </div>
          <UBadge color="neutral" variant="soft">
            {{ allCategories.length }}
          </UBadge>
        </div>
      </template>

      <div v-if="groups.length" class="grid gap-4">
        <section
          v-for="group in groups"
          :key="group.id"
          class="rounded-lg border border-slate-200 bg-slate-50/70 p-4 dark:border-zinc-800 dark:bg-zinc-950/40"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h4 class="font-bold text-slate-900 dark:text-zinc-50">
                  {{ group.name }}
                </h4>
                <UBadge :color="group.visibility === 'public' ? 'success' : 'neutral'" variant="soft">
                  {{ t(`admin.forum.visibility.${group.visibility}`) }}
                </UBadge>
                <UBadge color="neutral" variant="soft" class="font-mono">
                  {{ group.slug }}
                </UBadge>
              </div>
              <p v-if="group.description" class="mt-1 text-sm text-slate-500 dark:text-zinc-400">
                {{ group.description }}
              </p>
              <p class="mt-1 text-xs text-slate-400 dark:text-zinc-500">
                {{ t('admin.forum.categories.positionValue', { value: group.position }) }}
              </p>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <UButton
                color="neutral"
                variant="outline"
                size="sm"
                leading-icon="i-lucide-plus"
                @click="openCreateCategory(group.id)"
              >
                {{ t('admin.forum.categories.createCategory') }}
              </UButton>
              <UButton
                color="neutral"
                variant="outline"
                size="sm"
                leading-icon="i-lucide-pencil"
                @click="openEditGroup(group)"
              >
                {{ t('admin.common.edit') }}
              </UButton>
              <UButton
                color="error"
                variant="ghost"
                size="sm"
                leading-icon="i-lucide-trash-2"
                :aria-label="t('admin.forum.categories.delete.groupAction')"
                :title="t('admin.forum.categories.delete.groupAction')"
                @click="requestDeleteGroup(group)"
              >
                {{ t('admin.common.delete') }}
              </UButton>
            </div>
          </div>

          <div v-if="group.categories?.length" class="mt-4 overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
            <div
              v-for="category in group.categories"
              :key="category.id"
              class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-4 py-3 last:border-b-0 dark:border-zinc-800"
            >
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="inline-flex min-w-0 items-center gap-2 font-semibold text-slate-900 dark:text-zinc-100">
                    <UIcon
                      :name="categoryPreviewIcon(category)"
                      class="size-4 shrink-0"
                      :style="{ color: taxonomyIconColor(category.iconColor) }"
                      aria-hidden="true"
                    />
                    <span class="truncate">{{ category.name }}</span>
                  </span>
                  <UBadge :color="category.visibility === 'public' ? 'success' : 'neutral'" variant="soft">
                    {{ t(`admin.forum.visibility.${category.visibility}`) }}
                  </UBadge>
                  <UBadge color="neutral" variant="soft" class="font-mono">
                    /c/{{ category.slug }}
                  </UBadge>
                </div>
                <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                  {{ t('admin.forum.categories.categoryMeta', { topics: category.topicCount, comments: category.commentCount, sort: t(`admin.forum.sort.${category.defaultSort}`), position: category.position }) }}
                </p>
              </div>
              <div class="flex shrink-0 items-center gap-1">
                <UButton color="neutral" variant="ghost" size="sm" leading-icon="i-lucide-pencil" @click="openEditCategory(category)">
                  {{ t('admin.common.edit') }}
                </UButton>
                <UButton
                  color="error"
                  variant="ghost"
                  size="sm"
                  leading-icon="i-lucide-trash-2"
                  :aria-label="t('admin.forum.categories.delete.categoryAction')"
                  :title="t('admin.forum.categories.delete.categoryAction')"
                  @click="requestDeleteCategory(category)"
                >
                  {{ t('admin.common.delete') }}
                </UButton>
              </div>
            </div>
          </div>
          <SFEmptyState
            v-else
            icon-label="CAT"
            :title="t('admin.forum.categories.emptyCategories')"
            :description="t('admin.forum.categories.emptyCategoriesDescription')"
          />
        </section>
      </div>

      <SFEmptyState
        v-else
        icon-label="GRP"
        :title="t('admin.forum.categories.emptyGroups')"
        :description="t('admin.forum.categories.emptyGroupsDescription')"
      />
    </UCard>
  </div>

  <!-- 分组：新建 / 编辑 -->
  <UModal
    v-model:open="groupModalOpen"
    :ui="{ content: 'sm:max-w-xl' }"
    @update:open="(open) => { if (!open) closeGroupModal() }"
  >
    <template #content>
      <form class="flex max-h-[85vh] flex-col" @submit.prevent="saveGroup">
        <div class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ editingGroupId ? t('admin.forum.categories.editGroup') : t('admin.forum.categories.createGroup') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.forum.categories.groupHelp') }}
            </p>
          </div>
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            icon="i-lucide-x"
            :aria-label="t('admin.common.cancel')"
            @click="closeGroupModal"
          />
        </div>

        <div class="grid flex-1 gap-4 overflow-y-auto px-5 py-4">
          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.forum.categories.slug')" name="group-slug">
              <UInput v-model="groupForm.slug" icon="i-lucide-link" required class="w-full" placeholder="default" />
            </UFormField>
            <UFormField :label="t('admin.forum.categories.name')" name="group-name">
              <UInput v-model="groupForm.name" icon="i-lucide-folder" required class="w-full" />
            </UFormField>
          </div>

          <UFormField :label="t('admin.forum.categories.description')" name="group-description">
            <UTextarea v-model="groupForm.description" autoresize class="w-full" />
          </UFormField>

          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.forum.categories.visibility')" name="group-visibility">
              <select v-model="groupForm.visibility" class="h-10 w-full rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100">
                <option v-for="choice in forumVisibilityChoices" :key="choice" :value="choice">
                  {{ t(`admin.forum.visibility.${choice}`) }}
                </option>
              </select>
            </UFormField>
            <UFormField :label="t('admin.forum.categories.position')" name="group-position">
              <UInput v-model.number="groupForm.position" icon="i-lucide-list-ordered" type="number" step="1" class="w-full" />
            </UFormField>
          </div>
        </div>

        <div class="flex flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-4 dark:border-zinc-800">
          <UButton type="button" color="neutral" variant="ghost" @click="closeGroupModal">
            {{ t('admin.common.cancel') }}
          </UButton>
          <UButton type="submit" leading-icon="i-lucide-save" :loading="savingGroup">
            {{ t('admin.common.save') }}
          </UButton>
        </div>
      </form>
    </template>
  </UModal>

  <!-- 分类：新建 / 编辑 -->
  <UModal
    v-model:open="categoryModalOpen"
    :ui="{ content: 'sm:max-w-2xl' }"
    @update:open="(open) => { if (!open) closeCategoryModal() }"
  >
    <template #content>
      <form class="flex max-h-[85vh] flex-col" @submit.prevent="saveCategory">
        <div class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ editingCategoryId ? t('admin.forum.categories.editCategory') : t('admin.forum.categories.createCategory') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.forum.categories.categoryHelp', { group: selectedGroupName }) }}
            </p>
          </div>
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            icon="i-lucide-x"
            :aria-label="t('admin.common.cancel')"
            @click="closeCategoryModal"
          />
        </div>

        <div class="grid flex-1 gap-4 overflow-y-auto px-5 py-4">
          <UFormField :label="t('admin.forum.categories.group')" name="category-group">
            <select
              v-model.number="categoryForm.groupId"
              class="h-10 w-full rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100"
            >
              <option v-if="!categoryForm.groupId" value="0" disabled>
                {{ t('admin.forum.categories.groupPlaceholder') }}
              </option>
              <option v-for="group in groupOptions" :key="group.value" :value="group.value">
                {{ group.label }}
              </option>
            </select>
          </UFormField>

          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.forum.categories.slug')" name="category-slug">
              <UInput v-model="categoryForm.slug" icon="i-lucide-link" required class="w-full" placeholder="general" />
            </UFormField>
            <UFormField :label="t('admin.forum.categories.name')" name="category-name">
              <UInput v-model="categoryForm.name" icon="i-lucide-folder-open" required class="w-full" />
            </UFormField>
          </div>

          <UFormField :label="t('admin.forum.categories.description')" name="category-description">
            <UTextarea v-model="categoryForm.description" autoresize class="w-full" />
          </UFormField>

          <div class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_220px]">
            <LazySFIconPicker
              v-model="categoryForm.icon"
              :label="t('admin.forum.visual.icon')"
              :hint="t('admin.forum.visual.iconHelp')"
            />
            <UFormField :label="t('admin.forum.visual.iconColor')" name="category-icon-color">
              <div class="grid gap-2">
                <div class="flex items-center gap-2">
                  <input
                    :value="taxonomyColorInputValue(categoryForm.iconColor)"
                    type="color"
                    class="h-10 w-12 rounded-md border border-slate-200 bg-white p-1 dark:border-zinc-700 dark:bg-zinc-950"
                    :aria-label="t('admin.forum.visual.iconColor')"
                    @input="setCategoryColor"
                  >
                  <UInput v-model="categoryForm.iconColor" placeholder="#0f766e" class="min-w-0 flex-1" />
                </div>
                <div class="flex items-center justify-between gap-2">
                  <span class="inline-flex items-center gap-2 text-xs text-slate-500 dark:text-zinc-400">
                    <UIcon :name="categoryPreviewIcon(categoryForm)" class="size-4" :style="{ color: taxonomyIconColor(categoryForm.iconColor) }" />
                    {{ categoryForm.iconColor || t('admin.forum.visual.defaultAccent') }}
                  </span>
                  <UButton type="button" size="xs" color="neutral" variant="ghost" leading-icon="i-lucide-x" @click="clearCategoryColor">
                    {{ t('admin.forum.visual.clearColor') }}
                  </UButton>
                </div>
              </div>
            </UFormField>
          </div>

          <div class="grid gap-4 md:grid-cols-3">
            <UFormField :label="t('admin.forum.categories.visibility')" name="category-visibility">
              <select v-model="categoryForm.visibility" class="h-10 w-full rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100">
                <option v-for="choice in forumVisibilityChoices" :key="choice" :value="choice">
                  {{ t(`admin.forum.visibility.${choice}`) }}
                </option>
              </select>
            </UFormField>
            <UFormField :label="t('admin.forum.categories.defaultSort')" name="category-sort">
              <select v-model="categoryForm.defaultSort" class="h-10 w-full rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100">
                <option v-for="choice in forumCategorySortChoices" :key="choice" :value="choice">
                  {{ t(`admin.forum.sort.${choice}`) }}
                </option>
              </select>
            </UFormField>
            <UFormField :label="t('admin.forum.categories.position')" name="category-position">
              <UInput v-model.number="categoryForm.position" icon="i-lucide-list-ordered" type="number" step="1" class="w-full" />
            </UFormField>
          </div>
        </div>

        <div class="flex flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-4 dark:border-zinc-800">
          <UButton type="button" color="neutral" variant="ghost" @click="closeCategoryModal">
            {{ t('admin.common.cancel') }}
          </UButton>
          <UButton type="submit" leading-icon="i-lucide-save" :loading="savingCategory" :disabled="groups.length === 0">
            {{ t('admin.common.save') }}
          </UButton>
        </div>
      </form>
    </template>
  </UModal>

  <!-- 分组 / 分类：删除确认 -->
  <SFAdminForumTaxonomyDeleteDialog
    v-model:open="taxonomyDeleteOpen"
    :target="taxonomyDeleteTarget"
    :busy="deletingTaxonomy"
    @cancel="closeTaxonomyDelete"
    @confirm="confirmTaxonomyDelete"
  />
</template>
