<script setup lang="ts">
import { useAdminExtensionInstall } from '~/composables/admin/useAdminExtensionInstall'
import { adminExtensionInstallErrorMessage } from '~/utils/admin/extensions/hostActions'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import type { AdminExtensionInstallResult } from '~/utils/admin/adminExtensions'
import {
  EXTENSION_ARCHIVE_ACCEPT,
  EXTENSION_ARCHIVE_MAX_BYTES,
  formatFileSize,
  normalizeRemoteArchiveUrl,
  normalizeSha256,
  remoteArchiveHost,
  validateArchiveFile
} from '~/utils/admin/extensions/extensionInstall'

defineOptions({ name: 'SFAdminExtensionInstallPanel' })

type InstallSource = 'upload' | 'url'

const { t } = useI18n()
const toast = useToast()
const adminRoutes = useAdminRoutes()
const { installArchive, installFromUrl } = useAdminExtensionInstall()

const fileInput = ref<HTMLInputElement | null>(null)
const selectedFile = ref<File | null>(null)
const dragging = ref(false)
const dragDepth = ref(0)
const uploading = ref(false)
const remoteUrl = ref('')
const remoteSha256 = ref('')
const fetching = ref(false)
const fileError = ref('')
const urlError = ref('')
const checksumError = ref('')
const actionError = ref('')
const result = ref<AdminExtensionInstallResult | null>(null)
const resultSource = ref<InstallSource>('upload')
const resultFileName = ref('')
const resultUrlHost = ref('')

const accept = EXTENSION_ARCHIVE_ACCEPT
const maxSizeLabel = formatFileSize(EXTENSION_ARCHIVE_MAX_BYTES)
const busy = computed(() => uploading.value || fetching.value)
const resultTypeLabel = computed(() => result.value?.extension.type === 'theme'
  ? t('admin.extensionInstall.result.theme')
  : t('admin.extensionInstall.result.plugin'))
const managementPath = computed(() => result.value?.extension.type === 'theme'
  ? '/extensions/themes'
  : '/extensions/plugins')
const sourceLabel = computed(() => {
  if (!result.value) return ''
  if (resultSource.value === 'upload') {
    return t('admin.extensionInstall.result.sourceUpload', { file: resultFileName.value || t('admin.extensionInstall.result.unknownSource') })
  }
  return resultUrlHost.value
    ? t('admin.extensionInstall.result.sourceUrl', { host: resultUrlHost.value })
    : t('admin.extensionInstall.result.sourceUrlGeneric')
})

function openFilePicker() {
  if (busy.value) return
  fileInput.value?.click()
}

function onFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0] || null
  input.value = ''
  selectFile(file)
}

function selectFile(file: File | null) {
  fileError.value = ''
  if (!file) return
  const validation = validateArchiveFile(file)
  if (!validation.ok) {
    selectedFile.value = null
    fileError.value = archiveFileError(validation.reason)
    return
  }
  selectedFile.value = file
}

function archiveFileError(reason: 'empty' | 'type' | 'size') {
  if (reason === 'type') return t('admin.extensionInstall.upload.errorType')
  if (reason === 'size') return t('admin.extensionInstall.upload.errorSize', { max: maxSizeLabel })
  return t('admin.extensionInstall.upload.errorEmpty')
}

function clearFile() {
  selectedFile.value = null
  fileError.value = ''
}

function onDragEnter(event: DragEvent) {
  event.preventDefault()
  if (busy.value) return
  dragDepth.value += 1
  dragging.value = true
}

function onDragOver(event: DragEvent) {
  event.preventDefault()
}

function onDragLeave(event: DragEvent) {
  event.preventDefault()
  dragDepth.value = Math.max(0, dragDepth.value - 1)
  if (dragDepth.value === 0) dragging.value = false
}

function onDrop(event: DragEvent) {
  event.preventDefault()
  dragDepth.value = 0
  dragging.value = false
  if (busy.value) return
  selectFile(event.dataTransfer?.files?.[0] || null)
}

async function submitUpload() {
  if (busy.value) return
  if (!selectedFile.value) {
    fileError.value = t('admin.extensionInstall.upload.errorEmpty')
    return
  }
  const validation = validateArchiveFile(selectedFile.value)
  if (!validation.ok) {
    fileError.value = archiveFileError(validation.reason)
    return
  }

  uploading.value = true
  actionError.value = ''
  try {
    const next = await installArchive(selectedFile.value)
    applyResult(next, 'upload', selectedFile.value.name, '')
    selectedFile.value = null
  } catch (error) {
    actionError.value = adminExtensionInstallErrorMessage(t, error, 'admin.extensionInstall.upload.failed')
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: actionError.value })
  } finally {
    uploading.value = false
  }
}

async function submitUrl() {
  if (busy.value) return
  urlError.value = ''
  checksumError.value = ''

  const urlValidation = normalizeRemoteArchiveUrl(remoteUrl.value)
  if (!urlValidation.ok) {
    urlError.value = remoteUrlError(urlValidation.reason)
    return
  }
  // 空摘要不是可选项：字段级拦下，避免把一个必然 422 的请求发出去。
  const digestValidation = normalizeSha256(remoteSha256.value)
  if (!digestValidation.ok) {
    checksumError.value = digestValidation.reason === 'empty'
      ? t('admin.extensionInstall.url.errorChecksumRequired')
      : t('admin.extensionInstall.url.errorChecksum')
    return
  }

  fetching.value = true
  actionError.value = ''
  try {
    const next = await installFromUrl(urlValidation.url, digestValidation.value)
    applyResult(next, 'url', '', remoteArchiveHost(urlValidation.url))
  } catch (error) {
    actionError.value = adminExtensionInstallErrorMessage(t, error, 'admin.extensionInstall.url.failed')
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: actionError.value })
  } finally {
    fetching.value = false
  }
}

function remoteUrlError(reason: 'empty' | 'length' | 'invalid' | 'scheme' | 'credentials') {
  if (reason === 'empty') return t('admin.extensionInstall.url.errorEmpty')
  if (reason === 'length') return t('admin.extensionInstall.url.errorLength')
  if (reason === 'scheme') return t('admin.extensionInstall.url.errorScheme')
  if (reason === 'credentials') return t('admin.extensionInstall.url.errorCredentials')
  return t('admin.extensionInstall.url.errorInvalid')
}

function applyResult(next: AdminExtensionInstallResult, source: InstallSource, fileName: string, urlHost: string) {
  result.value = next
  resultSource.value = source
  resultFileName.value = fileName
  resultUrlHost.value = urlHost
  toast.add({
    color: 'success',
    icon: 'i-lucide-package-check',
    title: next.upgraded
      ? t('admin.extensionInstall.result.upgradeStaged')
      : t('admin.extensionInstall.result.installed'),
    description: next.activationPending
      ? t('admin.extensionInstall.result.activationPending')
      : undefined,
    duration: 5000
  })
}

function resetResult() {
  result.value = null
  actionError.value = ''
}

async function copyDigest() {
  const digest = result.value?.extension.packageDigest
  if (!digest || typeof navigator === 'undefined' || !navigator.clipboard) return
  try {
    await navigator.clipboard.writeText(digest)
    toast.add({
      color: 'success',
      icon: 'i-lucide-copy-check',
      title: t('admin.extensionInstall.result.digestCopied'),
      duration: 5000
    })
  } catch {
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-triangle-alert',
      title: t('admin.extensionInstall.result.digestCopyFailed')
    })
  }
}
</script>

<template>
  <div class="flex min-w-0 flex-col gap-4">
    <div class="grid gap-4 xl:grid-cols-2">
      <UCard
        data-testid="extension-install-upload-card"
        class="min-w-0 border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900"
      >
        <template #header>
          <div class="flex items-start gap-3">
            <span class="flex size-9 shrink-0 items-center justify-center rounded-md bg-[var(--sf-accent)]/10 text-[var(--sf-accent)]">
              <UIcon name="i-lucide-file-archive" class="size-5" />
            </span>
            <div class="min-w-0">
              <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                {{ t('admin.extensionInstall.upload.title') }}
              </h3>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.extensionInstall.upload.description') }}
              </p>
            </div>
          </div>
        </template>

        <div class="flex flex-col gap-3">
          <div
            data-testid="extension-drop-zone"
            role="button"
            tabindex="0"
            :aria-label="t('admin.extensionInstall.upload.dropTitle')"
            :aria-disabled="busy"
            class="flex min-w-0 cursor-pointer flex-col items-center justify-center rounded-lg border-2 border-dashed px-4 py-8 text-center transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)]"
            :class="[
              dragging ? 'border-[var(--sf-accent)] bg-[var(--sf-accent)]/5' : 'border-slate-300 hover:border-[var(--sf-accent)] dark:border-zinc-700',
              busy ? 'cursor-not-allowed opacity-60' : ''
            ]"
            @click="openFilePicker"
            @keydown.enter.prevent="openFilePicker"
            @keydown.space.prevent="openFilePicker"
            @dragenter.prevent="onDragEnter"
            @dragover.prevent="onDragOver"
            @dragleave.prevent="onDragLeave"
            @drop.prevent="onDrop"
          >
            <UIcon name="i-lucide-cloud-upload" class="size-8 text-[var(--sf-accent)]" />
            <p class="mt-3 text-sm font-medium text-slate-900 dark:text-zinc-100">
              {{ t('admin.extensionInstall.upload.dropTitle') }}
            </p>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensionInstall.upload.dropHint', { max: maxSizeLabel }) }}
            </p>
          </div>

          <input
            ref="fileInput"
            data-testid="extension-archive-input"
            class="hidden"
            type="file"
            :accept="accept"
            @change="onFileChange"
          >

          <div
            v-if="selectedFile"
            class="flex min-w-0 items-center gap-3 rounded-md border border-slate-200 bg-slate-50 px-3 py-2 dark:border-zinc-800 dark:bg-zinc-950"
          >
            <UIcon name="i-lucide-file-archive" class="size-4 shrink-0 text-[var(--sf-accent)]" />
            <div class="min-w-0 flex-1">
              <p class="truncate text-sm font-medium text-slate-900 dark:text-zinc-100">
                {{ selectedFile.name }}
              </p>
              <p class="text-xs text-slate-500 dark:text-zinc-400">
                {{ formatFileSize(selectedFile.size) }}
              </p>
            </div>
            <UButton
              color="neutral"
              variant="ghost"
              icon="i-lucide-x"
              :aria-label="t('admin.extensionInstall.upload.remove')"
              :title="t('admin.extensionInstall.upload.remove')"
              :disabled="busy"
              @click.stop="clearFile"
            />
          </div>

          <UAlert
            v-if="fileError"
            color="error"
            variant="subtle"
            icon="i-lucide-triangle-alert"
            :title="fileError"
          />

          <UButton
            data-testid="extension-archive-submit"
            class="w-full justify-center"
            icon="i-lucide-upload"
            :loading="uploading"
            :disabled="busy || !selectedFile"
            @click="submitUpload"
          >
            {{ uploading ? t('admin.extensionInstall.upload.uploading') : t('admin.extensionInstall.upload.action') }}
          </UButton>
        </div>
      </UCard>

      <UCard class="min-w-0 border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
        <template #header>
          <div class="flex items-start gap-3">
            <span class="flex size-9 shrink-0 items-center justify-center rounded-md bg-[var(--sf-accent)]/10 text-[var(--sf-accent)]">
              <UIcon name="i-lucide-cloud-download" class="size-5" />
            </span>
            <div class="min-w-0">
              <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                {{ t('admin.extensionInstall.url.title') }}
              </h3>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.extensionInstall.url.description') }}
              </p>
            </div>
          </div>
        </template>

        <form data-testid="extension-url-form" class="flex flex-col gap-3" @submit.prevent="submitUrl">
          <UFormField :label="t('admin.extensionInstall.url.label')" :error="urlError || undefined">
            <UInput
              v-model="remoteUrl"
              data-testid="extension-url-input"
              type="url"
              inputmode="url"
              autocomplete="off"
              spellcheck="false"
              icon="i-lucide-link"
              :placeholder="t('admin.extensionInstall.url.placeholder')"
              :disabled="busy"
              class="w-full"
            />
          </UFormField>

          <UFormField
            :label="t('admin.extensionInstall.url.checksumLabel')"
            :description="t('admin.extensionInstall.url.checksumHint')"
            :error="checksumError || undefined"
          >
            <UInput
              v-model="remoteSha256"
              data-testid="extension-url-checksum"
              autocomplete="off"
              spellcheck="false"
              :placeholder="t('admin.extensionInstall.url.checksumPlaceholder')"
              :disabled="busy"
              class="w-full font-mono text-xs"
            />
          </UFormField>

          <p class="text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.extensionInstall.url.hint') }}
          </p>

          <UButton
            data-testid="extension-url-submit"
            type="submit"
            class="w-full justify-center"
            icon="i-lucide-cloud-download"
            :loading="fetching"
            :disabled="busy"
          >
            {{ fetching ? t('admin.extensionInstall.url.fetching') : t('admin.extensionInstall.url.action') }}
          </UButton>

          <p v-if="fetching" class="text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.extensionInstall.url.fetchingHint') }}
          </p>
        </form>
      </UCard>
    </div>

    <UAlert
      v-if="actionError"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :title="actionError"
    />

    <UCard
      v-if="result"
      data-testid="extension-install-result"
      class="min-w-0 border-emerald-200 bg-emerald-50/40 dark:border-emerald-900/60 dark:bg-emerald-950/20"
    >
      <div class="flex min-w-0 flex-col gap-4">
        <div class="flex min-w-0 items-start gap-3">
          <span class="flex size-9 shrink-0 items-center justify-center rounded-full bg-emerald-500/15 text-emerald-700 dark:text-emerald-300">
            <UIcon name="i-lucide-circle-check" class="size-5" />
          </span>
          <div class="min-w-0 flex-1">
            <p class="text-base font-semibold text-slate-900 dark:text-zinc-100">
              {{ result.upgraded ? t('admin.extensionInstall.result.upgradeStaged') : t('admin.extensionInstall.result.installed') }}
            </p>
            <p class="mt-1 break-words text-sm text-slate-600 dark:text-zinc-300">
              {{ result.extension.name }} · {{ result.extension.version }}
            </p>
          </div>
          <UBadge color="success" variant="subtle">
            {{ resultTypeLabel }}
          </UBadge>
        </div>

        <dl class="grid gap-3 sm:grid-cols-2">
          <div class="min-w-0">
            <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensionInstall.result.extensionId') }}
            </dt>
            <dd class="mt-1 break-all font-mono text-xs text-slate-900 dark:text-zinc-100">
              {{ result.extension.id }}
            </dd>
          </div>
          <div class="min-w-0">
            <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensionInstall.result.source') }}
            </dt>
            <dd class="mt-1 break-all text-xs text-slate-900 dark:text-zinc-100">
              {{ sourceLabel }}
            </dd>
          </div>
          <div class="min-w-0 sm:col-span-2">
            <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensionInstall.result.packageDigest') }}
            </dt>
            <dd class="mt-1 flex min-w-0 flex-wrap items-center gap-2">
              <code class="min-w-0 break-all rounded bg-white/70 px-2 py-1 font-mono text-xs text-slate-700 dark:bg-zinc-950/60 dark:text-zinc-200">
                {{ result.extension.packageDigest || '—' }}
              </code>
              <UButton
                v-if="result.extension.packageDigest"
                color="neutral"
                variant="ghost"
                size="xs"
                icon="i-lucide-copy"
                :aria-label="t('admin.extensionInstall.result.copyDigest')"
                :title="t('admin.extensionInstall.result.copyDigest')"
                @click="copyDigest"
              />
            </dd>
          </div>
        </dl>

        <!-- 摘要校验徽标只对链接安装成立：上传没有摘要可校验，所以归因跟着 resultSource 走。 -->
        <UAlert
          v-if="resultSource === 'url'"
          color="success"
          variant="subtle"
          icon="i-lucide-shield-check"
          :title="t('admin.extensionInstall.result.checksumVerified')"
        />
        <UAlert
          v-if="result.activationPending"
          color="warning"
          variant="subtle"
          icon="i-lucide-clock"
          :title="t('admin.extensionInstall.result.activationPending')"
        />

        <div class="flex flex-wrap items-center gap-2">
          <UButton :to="adminRoutes.path(managementPath)" icon="i-lucide-external-link">
            {{ t('admin.extensionInstall.result.openManagement') }}
          </UButton>
          <UButton color="neutral" variant="outline" icon="i-lucide-plus" @click="resetResult">
            {{ t('admin.extensionInstall.result.installAnother') }}
          </UButton>
        </div>
      </div>
    </UCard>

    <UAlert
      color="neutral"
      variant="subtle"
      icon="i-lucide-info"
      :title="t('admin.extensionInstall.note.title')"
      :description="t('admin.extensionInstall.note.body')"
    />
  </div>
</template>
