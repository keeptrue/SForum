<script setup lang="ts">
import { usePermissions } from '~/composables/identity/usePermissions'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { forumListSortOptions, recommendedListDefaultSort } from '~/utils/admin/adminForum'
import type { ForumCategory, ForumSettings } from '~/utils/forum/forumTaxonomy'
import { useAdminForumSettingsTab } from '~/composables/admin/settings/useAdminForumSettingsTab'
import SFAdminForumTabFrame from '../SFAdminForumTabFrame.vue'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

const props = defineProps<{
  settings: ForumSettings
  categories: ForumCategory[]
  pending?: boolean
}>()
const emit = defineEmits<{ saved: [settings: ForumSettings], refresh: [] }>()
const { t } = useI18n()
const adminRoutes = useAdminRoutes()
const { can } = usePermissions()
// 只认细粒度子键：settings.manage 是后端的展开来源，会话里始终存在，
// 把它当候选会让管理员 deny 掉的 forum.settings.manage 重新放行（task-24）。
const canManageSettings = computed(() => can('forum.settings.manage'))
const canManageCategories = computed(() => can('category.manage'))
const publicCategories = computed(() => props.categories.filter(category => category.visibility === 'public'))
const keys = ['defaultCategorySlug', 'topicsPerPage', 'commentsPerPage', 'guestRead', 'listDefaultSort', 'listHotWindowDays'] as const
const tab = useAdminForumSettingsTab(toRef(props, 'settings'), keys, key => key === 'defaultCategorySlug' ? canManageCategories.value : canManageSettings.value, settings => emit('saved', settings))
</script>

<template>
  <SFAdminForumTabFrame tab="general" :dirty="tab.hasChanges.value" :validation-key="tab.validationKey.value" :saving="tab.saving.value" :restoring="tab.restoring.value" :can-save="tab.canSave.value" @save="tab.saveCurrent" @reset="tab.resetChanges" @restore="tab.restoreCurrent">
    <section v-if="!pending && publicCategories.length === 0" class="rounded-md border border-dashed border-slate-200 bg-slate-50/80 p-6 dark:border-zinc-700 dark:bg-zinc-950/40">
      <SFEmptyState icon-label="CAT" :title="t('admin.forum.settings.noPublicCategoriesTitle')" :description="t('admin.forum.settings.noPublicCategoriesDescription')" />
      <div class="mt-5 flex justify-center gap-2">
        <UButton icon="i-lucide-folder-tree" :to="adminRoutes.path('/forum/categories')">{{ t('admin.forum.settings.openCategories') }}</UButton>
        <UButton icon="i-lucide-rotate-cw" color="neutral" variant="subtle" @click="emit('refresh')">{{ t('admin.common.refresh') }}</UButton>
      </div>
    </section>
    <UFormField v-else :label="t('admin.forum.settings.defaultCategory')" name="default-category">
      <SFNativeSelect v-model="tab.form.defaultCategorySlug" :disabled="!canManageCategories" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950">
        <option v-for="category in publicCategories" :key="category.slug" :value="category.slug">{{ category.name }} (/c/{{ category.slug }})</option>
      </SFNativeSelect>
    </UFormField>
    <section class="grid gap-4 border-t border-slate-200 pt-5 dark:border-zinc-800 md:grid-cols-2">
      <UFormField :label="t('admin.forum.settings.topicsPerPage')" name="topics-per-page"><UInputNumber v-model="tab.form.topicsPerPage" :min="1" :max="100" :disabled="!canManageSettings" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.commentsPerPage')" name="comments-per-page"><UInputNumber v-model="tab.form.commentsPerPage" :min="1" :max="100" :disabled="!canManageSettings" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.guestRead')" name="guest-read">
        <SFNativeSelect v-model="tab.form.guestRead" :disabled="!canManageSettings" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option value="public">{{ t('admin.forum.settings.guestReadPublic') }}</option><option value="login_required">{{ t('admin.forum.settings.guestReadLogin') }}</option></SFNativeSelect>
      </UFormField>
      <UFormField :label="t('admin.forum.settings.listDefaultSort')" name="list-sort" :help="t('admin.forum.settings.listDefaultSortHelp')">
        <SFNativeSelect v-model="tab.form.listDefaultSort" :disabled="!canManageSettings" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950">
          <option v-for="option in forumListSortOptions" :key="option.value" :value="option.value">
            {{ t(option.labelKey) }}{{ option.value === recommendedListDefaultSort ? t('admin.forum.settings.recommended') : '' }}
          </option>
        </SFNativeSelect>
      </UFormField>
      <UFormField :label="t('admin.forum.settings.listHotWindowDays')" name="hot-window"><UInputNumber v-model="tab.form.listHotWindowDays" :min="1" :max="90" :disabled="!canManageSettings" class="w-full" /></UFormField>
    </section>
  </SFAdminForumTabFrame>
</template>
