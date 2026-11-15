<script setup lang="ts">
import SFSEOImagePicker from './SFSEOImagePicker.vue'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

type Policy = { titleTemplate: string, descriptionSource: string, defaultImageUrl: string, indexMode: 'index' | 'noindex', includeInSitemap: boolean, schemaType: string }
type ContentType = 'category' | 'tag' | 'topic' | 'profile' | 'static'
const model = defineModel<Record<ContentType, Policy>>({ required: true })
const emit = defineEmits<{ restore: [] }>()
const { t } = useI18n()
const active = ref<ContentType>('topic')
const types: ContentType[] = ['category', 'tag', 'topic', 'profile', 'static']
const policy = computed(() => model.value[active.value])
function selectType(type: ContentType) { active.value = type }
</script>

<template>
  <div class="grid gap-5 lg:grid-cols-[180px_minmax(0,1fr)]">
    <nav class="flex gap-2 overflow-x-auto lg:flex-col">
      <UButton v-for="type in types" :key="type" type="button" :color="active === type ? 'primary' : 'neutral'" :variant="active === type ? 'solid' : 'ghost'" @click="selectType(type)">{{ t(`admin.seo.contentTypes.${type}`) }}</UButton>
    </nav>
    <div class="grid max-w-3xl gap-4">
      <div class="flex justify-end"><UButton type="button" color="neutral" variant="outline" icon="i-lucide-rotate-ccw" @click="emit('restore')">{{ t('admin.seo.restoreRecommended') }}</UButton></div>
      <UFormField :label="t('admin.seo.contentTitleTemplate')"><UInput v-model="policy.titleTemplate" class="w-full font-mono" /></UFormField>
      <UFormField :label="t('admin.seo.contentDescriptionSource')"><UInput v-model="policy.descriptionSource" class="w-full font-mono" /></UFormField>
      <div class="grid gap-4 sm:grid-cols-2">
        <UFormField :label="t('admin.seo.contentIndexMode')"><SFNativeSelect v-model="policy.indexMode" class="h-10 w-full border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option value="index">index</option><option value="noindex">noindex</option></SFNativeSelect></UFormField>
        <label class="flex items-center gap-3 pt-7"><UCheckbox v-model="policy.includeInSitemap" /><span class="text-sm">{{ t('admin.seo.contentIncludeSitemap') }}</span></label>
      </div>
      <SFSEOImagePicker v-model="policy.defaultImageUrl" :context="`${active}-default-image`" :label="t('admin.seo.contentDefaultImage')" recommended="1200 x 630" />
    </div>
  </div>
</template>
