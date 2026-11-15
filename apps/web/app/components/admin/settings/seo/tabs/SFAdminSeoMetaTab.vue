<script setup lang="ts">
import type { AdminWebOption, SEOTwitterCard } from '~/composables/useWebOptions'
import { normalizeSEOTwitterCard } from '~/composables/useWebOptions'
import { useAdminOptionForm } from '~/composables/admin/settings/useAdminOptionForm'
import SFAdminSeoTabFrame from '../SFAdminSeoTabFrame.vue'
import SFSEOImagePicker from './SFSEOImagePicker.vue'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

const props = defineProps<{ items: AdminWebOption[] }>()
const emit = defineEmits<{ saved: [items: AdminWebOption[]] }>()
const { t } = useI18n()
const form = useAdminOptionForm(
  toRef(props, 'items'),
  map => ({
    titleTemplate: map['seo.meta_title_template']?.value || '',
    description: map['seo.meta_description']?.value || '',
    keywords: map['seo.meta_keywords']?.value || '',
    imageUrl: map['seo.og_image_url']?.value || '',
    twitterCard: normalizeSEOTwitterCard(map['seo.twitter_card']?.value) as SEOTwitterCard,
    twitterSite: map['seo.twitter_site']?.value || ''
  }),
  value => [
    { name: 'seo.meta_title_template', value: value.titleTemplate },
    { name: 'seo.meta_description', value: value.description },
    { name: 'seo.meta_keywords', value: value.keywords },
    { name: 'seo.og_image_url', value: value.imageUrl },
    { name: 'seo.twitter_card', value: value.twitterCard },
    { name: 'seo.twitter_site', value: value.twitterSite }
  ],
  () => ({ titleTemplate: '', description: '', keywords: '', imageUrl: '', twitterCard: 'summary_large_image' as SEOTwitterCard, twitterSite: '' }),
  items => emit('saved', items),
  { saved: t('admin.seo.saved'), saveFailed: t('admin.seo.saveFailed'), reset: t('admin.seo.resetChanges'), restored: t('admin.seo.recommendedRestored') }
)
</script>

<template>
  <SFAdminSeoTabFrame tab="meta" :dirty="form.hasChanges.value" :saving="form.saving.value" @save="form.save" @reset="form.resetChanges">
    <div class="grid max-w-5xl gap-5">
      <div class="flex justify-end"><UButton type="button" color="neutral" variant="outline" icon="i-lucide-rotate-ccw" @click="form.restoreRecommended">{{ t('admin.seo.restoreRecommended') }}</UButton></div>
      <UFormField :label="t('admin.seo.metaTitleTemplate')" name="meta-title"><UInput v-model="form.form.titleTemplate" class="w-full" /></UFormField>
      <UFormField :label="t('admin.seo.metaDescription')" name="meta-description"><UTextarea v-model="form.form.description" :rows="3" class="w-full" /></UFormField>
      <UFormField :label="t('admin.seo.metaKeywords')" name="meta-keywords"><UInput v-model="form.form.keywords" class="w-full" /></UFormField>
      <SFSEOImagePicker v-model="form.form.imageUrl" context="default-og-image" :label="t('admin.seo.ogImageUrl')" recommended="1200 x 630" />
      <div class="grid gap-4 md:grid-cols-2">
        <UFormField :label="t('admin.seo.twitterCard')" name="twitter-card"><SFNativeSelect v-model="form.form.twitterCard" class="h-10 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option value="summary_large_image">{{ t('admin.seo.twitterCardLarge') }}</option><option value="summary">{{ t('admin.seo.twitterCardSummary') }}</option></SFNativeSelect></UFormField>
        <UFormField :label="t('admin.seo.twitterSite')" name="twitter-site"><UInput v-model="form.form.twitterSite" placeholder="@sforum" class="w-full" /></UFormField>
      </div>
    </div>
  </SFAdminSeoTabFrame>
</template>
