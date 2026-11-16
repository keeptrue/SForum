<script setup lang="ts">
import { usePermissions } from '~/composables/identity/usePermissions'
import type { ForumSettings } from '~/utils/forum/forumTaxonomy'
import { useAdminForumSettingsTab } from '~/composables/admin/settings/useAdminForumSettingsTab'
import SFAdminForumTabFrame from '../SFAdminForumTabFrame.vue'

const props = defineProps<{ settings: ForumSettings }>()
const emit = defineEmits<{ saved: [settings: ForumSettings] }>()
const { t } = useI18n()
const { can } = usePermissions()
// 只认细粒度子键：settings.manage 是后端的展开来源，会话里始终存在，
// 把它当候选会让管理员 deny 掉的 forum.settings.manage 重新放行（task-24）。
const canManage = computed(() => can('forum.settings.manage'))
const keys = ['topicTitleMinRunes', 'topicTitleMaxRunes', 'topicContentMinRunes', 'topicContentMaxRunes', 'topicEditWindowMinutes', 'topicCooldownSeconds', 'dailyTopicLimit'] as const
const tab = useAdminForumSettingsTab(toRef(props, 'settings'), keys, () => canManage.value, settings => emit('saved', settings))
</script>

<template>
  <SFAdminForumTabFrame tab="topics" :dirty="tab.hasChanges.value" :validation-key="tab.validationKey.value" :saving="tab.saving.value" :restoring="tab.restoring.value" :can-save="tab.canSave.value" @save="tab.saveCurrent" @reset="tab.resetChanges" @restore="tab.restoreCurrent">
    <section class="grid gap-4 md:grid-cols-2">
      <UFormField :label="t('admin.forum.settings.topicTitleMin')" name="topic-title-min"><UInputNumber v-model="tab.form.topicTitleMinRunes" :min="1" :max="200" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.topicTitleMax')" name="topic-title-max"><UInputNumber v-model="tab.form.topicTitleMaxRunes" :min="1" :max="200" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.topicContentMin')" name="topic-content-min"><UInputNumber v-model="tab.form.topicContentMinRunes" :min="0" :max="200000" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.topicContentMax')" name="topic-content-max"><UInputNumber v-model="tab.form.topicContentMaxRunes" :min="1" :max="200000" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.topicEditWindow')" name="topic-edit-window"><UInputNumber v-model="tab.form.topicEditWindowMinutes" :min="0" :max="10080" :disabled="!canManage" class="w-full" /><p class="mt-2 text-xs text-muted">{{ t('admin.forum.settings.zeroUnlimitedHelp') }}</p></UFormField>
      <UFormField :label="t('admin.forum.settings.topicCooldown')" name="topic-cooldown"><UInputNumber v-model="tab.form.topicCooldownSeconds" :min="0" :max="86400" :disabled="!canManage" class="w-full" /><p class="mt-2 text-xs text-muted">{{ t('admin.forum.settings.zeroUnlimitedHelp') }}</p></UFormField>
      <UFormField :label="t('admin.forum.settings.dailyTopicLimit')" name="daily-topic-limit"><UInputNumber v-model="tab.form.dailyTopicLimit" :min="0" :max="10000" :disabled="!canManage" class="w-full" /></UFormField>
    </section>
  </SFAdminForumTabFrame>
</template>
