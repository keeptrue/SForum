<script setup lang="ts">
/**
 * 资料设置 · 地点与链接分区（所在地 / 网站）。
 *
 * 两项都可选；网站链接的最终 scheme 白名单在渲染层（safeUrl）兜底，
 * 这里只负责输入与服务端字段错误。
 */
defineProps<{
  locationDraft: string
  websiteDraft: string
  busy: boolean
  fieldErrors: Record<string, string[]>
}>()

const emit = defineEmits<{
  'update:locationDraft': [value: string]
  'update:websiteDraft': [value: string]
}>()

const { t } = useI18n()
</script>

<template>
  <div class="sforum-settings-profile__panel-body">
    <p class="sforum-settings-profile__panel-description">
      {{ t('profileSettings.sections.linksDescription') }}
    </p>

    <div class="sforum-settings-profile__field-grid">
      <div class="sforum-settings-profile__field">
        <label for="profile-settings-location">{{ t('profileSettings.location') }}</label>
        <input
          id="profile-settings-location"
          :value="locationDraft"
          type="text"
          maxlength="100"
          :class="{ 'is-invalid': fieldErrors.location }"
          :placeholder="t('profileSettings.locationPlaceholder')"
          :disabled="busy"
          :aria-invalid="fieldErrors.location ? 'true' : undefined"
          :aria-describedby="fieldErrors.location ? 'profile-settings-location-error' : undefined"
          @input="emit('update:locationDraft', ($event.target as HTMLInputElement).value)"
        >
        <p v-if="fieldErrors.location" id="profile-settings-location-error" class="sforum-settings-profile__field-error">
          {{ fieldErrors.location.join(', ') }}
        </p>
      </div>

      <div class="sforum-settings-profile__field">
        <label for="profile-settings-website">{{ t('profileSettings.website') }}</label>
        <input
          id="profile-settings-website"
          :value="websiteDraft"
          type="url"
          maxlength="200"
          :class="{ 'is-invalid': fieldErrors.websiteUrl }"
          :placeholder="t('profileSettings.websitePlaceholder')"
          :disabled="busy"
          :aria-invalid="fieldErrors.websiteUrl ? 'true' : undefined"
          :aria-describedby="fieldErrors.websiteUrl ? 'profile-settings-website-error profile-settings-website-hint' : 'profile-settings-website-hint'"
          @input="emit('update:websiteDraft', ($event.target as HTMLInputElement).value)"
        >
        <p v-if="fieldErrors.websiteUrl" id="profile-settings-website-error" class="sforum-settings-profile__field-error">
          {{ fieldErrors.websiteUrl.join(', ') }}
        </p>
        <p id="profile-settings-website-hint">
          {{ t('profileSettings.websiteHint') }}
        </p>
      </div>
    </div>
  </div>
</template>
