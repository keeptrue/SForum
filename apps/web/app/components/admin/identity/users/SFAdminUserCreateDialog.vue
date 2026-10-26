<script setup lang="ts">
import type { AdminCreateUserResult, AdminUserDetail, Role } from '~/utils/admin/adminUsers'
import {
  buildAdminCreateUserPayload,
  validateAdminCreateUserForm,
  type AdminCreateUserForm
} from '~/utils/admin/adminUserCreate'
import { apiErrorFields, apiErrorMessage } from '~/composables/useApiClient'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { passwordPolicyRequirements } from '~/composables/useWebOptions'

const props = defineProps<{
  roles: Role[]
}>()

const emit = defineEmits<{
  created: [user: AdminUserDetail]
}>()

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const { request } = useApiClient()
const toast = useToast()
const { user: currentUser } = useAuthSession()
const { passwordPolicy } = useWebOptions()

const form = reactive<AdminCreateUserForm>({
  username: '',
  email: '',
  displayName: '',
  mode: 'manual',
  password: '',
  roleKeys: ['member'],
  emailVerified: false
})
const phase = ref<'form' | 'result'>('form')
const submitting = ref(false)
const errorMessage = ref('')
const fieldErrors = ref<Record<string, string[]>>({})
const generatedPassword = ref('')
const pendingCreated = ref<AdminUserDetail | null>(null)
const showPassword = ref(false)

const isSuperAdmin = computed(() => Boolean(currentUser.value?.roleKeys.includes('super_admin')))

const passwordModeItems = computed(() => [
  { value: 'manual', label: t('admin.users.createPasswordModeManual') },
  { value: 'generated', label: t('admin.users.createPasswordModeGenerated') }
])

const passwordRequirements = computed(() =>
  passwordPolicyRequirements(form.password, passwordPolicy.value)
)

const canSubmit = computed(() => {
  if (submitting.value) return false
  if (!form.username.trim() || !form.email.trim()) return false
  if (form.mode === 'manual' && !form.password) return false
  return form.roleKeys.some(key => key.trim())
})

watch(open, value => {
  if (value) {
    resetForm()
  }
})

function resetForm() {
  phase.value = 'form'
  submitting.value = false
  errorMessage.value = ''
  fieldErrors.value = {}
  generatedPassword.value = ''
  pendingCreated.value = null
  showPassword.value = false
  form.username = ''
  form.email = ''
  form.displayName = ''
  form.mode = 'manual'
  form.password = ''
  form.roleKeys = ['member']
  form.emailVerified = false
}

function close() {
  const created = pendingCreated.value
  pendingCreated.value = null
  open.value = false
  // 表单在下次打开时统一重置：关闭动画期间保留内容，避免闪一屏空字段。
  if (created) {
    emit('created', created)
  }
}

function clearFieldError(field: string) {
  if (!fieldErrors.value[field]) return
  const next = { ...fieldErrors.value }
  delete next[field]
  fieldErrors.value = next
}

function roleDisabled(role: Role) {
  return role.key === 'super_admin' && !isSuperAdmin.value
}

function toggleRole(key: string) {
  if (key === 'super_admin' && !isSuperAdmin.value) return
  form.roleKeys = form.roleKeys.includes(key)
    ? form.roleKeys.filter(item => item !== key)
    : [...form.roleKeys, key].sort()
  clearFieldError('roleKeys')
}

function requirementLabel(key: string) {
  switch (key) {
    case 'lowercase':
      return t('auth.passwordRequirementLowercase')
    case 'uppercase':
      return t('auth.passwordRequirementUppercase')
    case 'number':
      return t('auth.passwordRequirementNumber')
    case 'symbol':
      return t('auth.passwordRequirementSymbol')
    default:
      return t('auth.passwordRequirementLength', {
        min: passwordPolicy.value.minLength,
        max: passwordPolicy.value.maxLength
      })
  }
}

function applyValidationErrors() {
  const validation = validateAdminCreateUserForm(form)
  const next: Record<string, string[]> = {}
  if (validation.username) {
    next.username = [t('admin.users.createUsernameRequired')]
  }
  if (validation.email === 'required') {
    next.email = [t('admin.users.createEmailRequired')]
  } else if (validation.email === 'invalid') {
    next.email = [t('admin.users.createEmailInvalid')]
  }
  if (validation.password) {
    next.password = [t('admin.users.createPasswordRequired')]
  }
  if (validation.roles) {
    next.roleKeys = [t('admin.users.createRolesRequired')]
  }
  fieldErrors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!canSubmit.value) return
  errorMessage.value = ''
  if (!applyValidationErrors()) return

  submitting.value = true
  try {
    const result = await request<AdminCreateUserResult>('/users', {
      method: 'POST',
      body: buildAdminCreateUserPayload(form)
    })
    pendingCreated.value = result.user
    toast.add({
      title: t('admin.users.createSuccess'),
      color: 'success',
      icon: 'i-lucide-user-plus',
      duration: 5000
    })
    if (result.generatedPassword) {
      generatedPassword.value = result.generatedPassword
      phase.value = 'result'
      return
    }
    close()
  } catch (error) {
    const fields = apiErrorFields(error)
    fieldErrors.value = fields
    if (Object.keys(fields).length === 0) {
      errorMessage.value = apiErrorMessage(error) || t('admin.users.createFailed')
    }
  } finally {
    submitting.value = false
  }
}

async function copyPassword(): Promise<boolean> {
  try {
    if (!import.meta.client || !navigator.clipboard?.writeText) {
      throw new Error('clipboard unavailable')
    }
    await navigator.clipboard.writeText(generatedPassword.value)
    toast.add({
      title: t('admin.users.createCopied'),
      color: 'success',
      icon: 'i-lucide-clipboard-check',
      duration: 5000
    })
    return true
  } catch {
    toast.add({
      title: t('admin.users.createCopyFailed'),
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      duration: 0
    })
    return false
  }
}

async function copyAndClose() {
  if (await copyPassword()) {
    close()
  }
}

function copyPasswordFromButton() {
  void copyPassword()
}
</script>

<template>
  <UModal
    v-model:open="open"
    :ui="{ content: 'sm:max-w-xl' }"
    @update:open="(value) => { if (!value) close() }"
  >
    <template #content>
      <div class="flex max-h-[90vh] flex-col">
        <div class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ phase === 'result' ? t('admin.users.createResultTitle') : t('admin.users.createTitle') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ phase === 'result' ? t('admin.users.createResultIntro') : t('admin.users.createIntro') }}
            </p>
          </div>
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            icon="i-lucide-x"
            :aria-label="t('admin.users.close')"
            @click="close"
          />
        </div>

        <div class="flex-1 space-y-4 overflow-y-auto px-5 py-4">
          <UAlert
            v-if="errorMessage"
            color="error"
            variant="soft"
            icon="i-lucide-triangle-alert"
            :title="errorMessage"
          />

          <template v-if="phase === 'form'">
            <div class="grid gap-3 sm:grid-cols-2">
              <UFormField
                :label="t('admin.users.createUsernameLabel')"
                name="create-user-username"
                :error="fieldErrors.username?.[0]"
              >
                <UInput
                  v-model="form.username"
                  autocomplete="off"
                  class="w-full"
                  :disabled="submitting"
                  @update:model-value="clearFieldError('username')"
                />
              </UFormField>
              <UFormField
                :label="t('admin.users.createEmailLabel')"
                name="create-user-email"
                :error="fieldErrors.email?.[0]"
              >
                <UInput
                  v-model="form.email"
                  type="email"
                  autocomplete="off"
                  class="w-full"
                  :disabled="submitting"
                  @update:model-value="clearFieldError('email')"
                />
              </UFormField>
            </div>

            <UFormField
              :label="t('admin.users.createDisplayNameLabel')"
              name="create-user-display-name"
            >
              <UInput
                v-model="form.displayName"
                maxlength="80"
                :placeholder="t('admin.users.createDisplayNamePlaceholder')"
                class="w-full"
                :disabled="submitting"
              />
            </UFormField>

            <UFormField
              :label="t('admin.users.createPasswordLabel')"
              name="create-user-password"
              :error="fieldErrors.password?.[0]"
            >
              <URadioGroup
                v-model="form.mode"
                :items="passwordModeItems"
                value-key="value"
                color="primary"
                :disabled="submitting"
              />

              <div v-if="form.mode === 'manual'" class="mt-3 space-y-2">
                <div class="relative">
                  <UInput
                    v-model="form.password"
                    :type="showPassword ? 'text' : 'password'"
                    autocomplete="new-password"
                    :aria-label="t('admin.users.createPasswordLabel')"
                    class="w-full pr-10"
                    :disabled="submitting"
                    @update:model-value="clearFieldError('password')"
                  />
                  <button
                    type="button"
                    class="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-slate-500 transition hover:text-slate-700 dark:text-zinc-400 dark:hover:text-zinc-200"
                    :aria-label="showPassword ? t('admin.users.createHidePassword') : t('admin.users.createShowPassword')"
                    :title="showPassword ? t('admin.users.createHidePassword') : t('admin.users.createShowPassword')"
                    @click="showPassword = !showPassword"
                  >
                    <UIcon :name="showPassword ? 'i-lucide-eye-off' : 'i-lucide-eye'" class="size-4" />
                  </button>
                </div>
                <ul class="space-y-1 text-xs">
                  <li
                    v-for="item in passwordRequirements"
                    :key="item.key"
                    :class="item.met ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-500 dark:text-zinc-400'"
                  >
                    <UIcon :name="item.met ? 'i-lucide-circle-check' : 'i-lucide-circle'" class="mr-1 inline size-3" />
                    {{ requirementLabel(item.key) }}
                  </li>
                </ul>
                <p class="text-xs text-slate-500 dark:text-zinc-400">
                  {{ t('admin.users.createPasswordHint') }}
                </p>
              </div>
              <p v-else class="mt-2 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.users.createPasswordGeneratedHint') }}
              </p>
            </UFormField>

            <UFormField
              :label="t('admin.users.createRolesLabel')"
              name="create-user-roles"
              :error="fieldErrors.roleKeys?.[0]"
            >
              <div class="grid gap-2 sm:grid-cols-2">
                <label
                  v-for="role in props.roles"
                  :key="role.key"
                  class="flex min-h-14 items-start gap-3 rounded-md border border-slate-200 bg-white px-3 py-2 text-sm transition dark:border-zinc-800 dark:bg-zinc-950"
                  :class="roleDisabled(role) ? 'opacity-60' : 'cursor-pointer hover:border-[var(--sf-accent-soft-border)]'"
                >
                  <input
                    type="checkbox"
                    class="mt-1 size-4 accent-[var(--sf-accent)]"
                    :aria-label="role.alias || role.key"
                    :checked="form.roleKeys.includes(role.key)"
                    :disabled="roleDisabled(role) || submitting"
                    @change="toggleRole(role.key)"
                  >
                  <span class="min-w-0">
                    <span class="block font-semibold text-slate-900 dark:text-zinc-100">{{ role.alias || role.key }}</span>
                    <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">{{ role.key }}</span>
                    <span v-if="roleDisabled(role)" class="mt-1 block text-xs text-amber-600 dark:text-amber-400">
                      {{ t('admin.users.createSuperAdminLocked') }}
                    </span>
                  </span>
                </label>
              </div>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.users.createRolesHint') }}
              </p>
            </UFormField>

            <label class="flex items-start gap-2 rounded-md border border-slate-200 bg-white px-3 py-2 dark:border-zinc-800 dark:bg-zinc-950">
              <input
                v-model="form.emailVerified"
                type="checkbox"
                class="mt-0.5 size-4 accent-[var(--sf-accent)]"
                :aria-label="t('admin.users.createEmailVerifiedLabel')"
                :disabled="submitting"
              >
              <span class="min-w-0">
                <span class="block text-sm font-medium text-slate-700 dark:text-zinc-300">
                  {{ t('admin.users.createEmailVerifiedLabel') }}
                </span>
                <span class="block text-xs text-slate-500 dark:text-zinc-400">
                  {{ t('admin.users.createEmailVerifiedHint') }}
                </span>
              </span>
            </label>
          </template>

          <template v-else>
            <UAlert
              color="success"
              variant="soft"
              icon="i-lucide-check-circle"
              :title="t('admin.users.createResultTitle')"
              :description="t('admin.users.createResultIntro')"
            />

            <div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <p class="text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.users.createResultUsername') }}
              </p>
              <p class="mt-0.5 font-mono text-sm text-slate-900 dark:text-zinc-100">
                {{ pendingCreated?.username }}
              </p>

              <p class="mt-3 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.users.createResultPassword') }}
              </p>
              <div class="mt-0.5 flex items-center gap-2">
                <UInput
                  :model-value="generatedPassword"
                  readonly
                  class="w-full font-mono"
                />
                <UButton
                  type="button"
                  color="neutral"
                  variant="outline"
                  icon="i-lucide-copy"
                  :aria-label="t('admin.users.createCopyPassword')"
                  :title="t('admin.users.createCopyPassword')"
                  @click="copyPasswordFromButton"
                />
              </div>
            </div>

            <UAlert
              color="warning"
              variant="soft"
              icon="i-lucide-shield-alert"
              :title="t('admin.users.createResultWarning')"
            />
          </template>
        </div>

        <div class="flex items-center justify-end gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
          <template v-if="phase === 'form'">
            <UButton color="neutral" variant="ghost" :disabled="submitting" @click="close">
              {{ t('admin.users.createCancel') }}
            </UButton>
            <UButton color="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
              {{ submitting ? t('admin.users.createSubmitting') : t('admin.users.createSubmit') }}
            </UButton>
          </template>
          <template v-else>
            <UButton color="neutral" variant="ghost" @click="close">
              {{ t('admin.users.createResultSecondary') }}
            </UButton>
            <UButton color="primary" leading-icon="i-lucide-copy" @click="copyAndClose">
              {{ t('admin.users.createResultPrimary') }}
            </UButton>
          </template>
        </div>
      </div>
    </template>
  </UModal>
</template>
