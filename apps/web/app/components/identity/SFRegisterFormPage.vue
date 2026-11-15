<script setup lang="ts">
import { useExternalAuthFeedback } from '~/composables/identity/useExternalAuthFeedback'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useAuthReturnNavigation } from '~/composables/identity/useAuthReturnNavigation'
import { useAuthProviders } from '~/composables/identity/useAuthProviders'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import SFAuthProviderButtons from '~/components/identity/SFAuthProviderButtons.vue'
import SFAuthShell from '~/components/identity/auth/SFAuthShell.vue'
/**
 * 宿主 body 岛：auth.register（凭证表单仍为 Host 组件，不经主题可执行代码）。
 * 路由页保留 layout/middleware meta + fail-closed 回退。
 */
import type { AltchaWidgetElement } from 'altcha'
import type { CurrentUser } from '~/composables/identity/useAuthSession'
import type { PublicAuthProvider } from '~/composables/identity/useAuthProviders'
import { apiErrorFields, apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'
import { registerErrorMessage } from '~/utils/identity/registerErrors'
type RegistrationStatus = {
  // 公开端点恒定返回 false，避免向未认证访客暴露"下个注册者即 super_admin"
  // 的 bootstrap 窗口（knowledge/decisions/2026-07-09-security-audit.md M4）。
  // 首注册者改由注册响应内的 isInitialSuperAdmin 判定，该字段不再驱动 UI。
  nextUserIsInitialSuperAdmin: boolean
  registrationEnabled?: boolean
}
type ExternalRegistrationPreparation = {
  usernameHint: string
  emailHint: string
  displayName: string
  emailVerified: boolean
}
const { t, locale } = useI18n()
const toast = useToast()
const localePath = useLocalePath()
const route = useRoute()
const router = useRouter()
const { apiBaseUrl, request } = useApiClient()
const { setUser } = useAuthSession()
const { returnFromAuth, authPageLink, destination } = useAuthReturnNavigation()
const adminRoutes = useAdminRoutes()
const { humanVerificationEnabledFor, altchaWidgetSettings, passwordPolicy, webOption } = useWebOptions()
const {
  registrationProviders,
  redirectToProvider
} = useAuthProviders()
const {
  alertMessage: externalAlertMessage,
  alertVariant: externalAlertVariant
} = useExternalAuthFeedback()
// 固定 Host 续接：/register?ticket=…&redirect=…（opaque ticket，无 raw subject）。
const registrationTicket = computed(() => {
  const raw = route.query.ticket
  if (typeof raw !== 'string') {
    return ''
  }
  return raw.trim()
})
const isExternalTicketMode = computed(() => Boolean(registrationTicket.value))
const form = reactive({
  username: '',
  email: '',
  password: '',
  displayName: ''
})
const submitting = ref(false)
const providerStartingId = ref('')
const errorMessage = ref('')
const sessionUnavailable = ref(false)
const fieldErrors = ref<Record<string, string[]>>({})
const humanVerificationToken = ref('')
const altchaWidget = ref<AltchaWidgetElement | null>(null)
const surfaceError = computed(() => errorMessage.value || externalAlertMessage.value)
const surfaceErrorVariant = computed(() =>
  errorMessage.value ? 'danger' : (externalAlertVariant.value || 'danger')
)

const { data: externalRegistrationPreparation, pending: preparingExternalRegistration } = await useAsyncData(
  'auth-external-registration-preparation',
  async () => {
    if (!registrationTicket.value) {
      return null
    }
    try {
      return await request<ExternalRegistrationPreparation>('/auth/external-registration/prepare', {
        method: 'POST',
        body: { ticket: registrationTicket.value }
      })
    } catch (error) {
      errorMessage.value = registerErrorMessage(error, t)
        || apiErrorMessage(error)
        || t('auth.external.reasons.ticketInvalid')
      return null
    }
  },
  { watch: [registrationTicket] }
)

watch(externalRegistrationPreparation, (preparation) => {
  if (!preparation) {
    return
  }
  if (!form.username) {
    form.username = preparation.usernameHint || ''
  }
  if (!form.email && preparation.emailVerified) {
    form.email = preparation.emailHint || ''
  }
  if (!form.displayName) {
    form.displayName = preparation.displayName || ''
  }
}, { immediate: true })
const altchaConfiguration = computed(() => JSON.stringify({
  hideLogo: altchaWidgetSettings.value.hideLogo,
  hideFooter: altchaWidgetSettings.value.hideFooter,
  minDuration: altchaWidgetSettings.value.minDuration
}))
const altchaWidgetType = computed(() => altchaWidgetSettings.value.type)
const altchaWidgetAuto = computed(() => altchaWidgetSettings.value.auto)
const altchaWidgetDisplay = computed(() => altchaWidgetSettings.value.display)
const altchaWidgetWorkers = computed(() => altchaWidgetSettings.value.workers)
const altchaWidgetClass = computed(() => [
  'auth-altcha',
  `auth-altcha--${altchaWidgetDisplay.value}`,
  fieldError('humanVerification') ? 'auth-altcha--invalid' : ''
].filter(Boolean))
const altchaChallengeUrl = computed(() => {
  return `${apiBaseUrl}/human-verification/challenge?purpose=register`
})
const registerHumanVerificationEnabled = computed(() => {
  // 前端只负责体验开关；API verifier 仍按同一场景配置做权威校验。
  return humanVerificationEnabledFor('register')
})
const humanVerificationEnabled = computed(() => {
  return registerHumanVerificationEnabled.value || Boolean(fieldError('humanVerification'))
})
const { data: registrationStatus } = await useAsyncData('auth-registration-status', async () => {
  try {
    return await request<RegistrationStatus>('/auth/registration-status')
  } catch {
    // 状态接口失败时保守认为仍开放，避免安装期误锁；提交时 API 仍是权威。
    return { nextUserIsInitialSuperAdmin: false, registrationEnabled: true }
  }
})
// registrationEnabled 含 bootstrap 覆盖；缺字段时回退 true（兼容旧 API）。
const isRegistrationOpen = computed(() => registrationStatus.value?.registrationEnabled !== false)
// 显式第三方注册入口：Host 激活 registration 且站点开放注册、且非 ticket 续接页。
// 零用户站点仍可能展示入口：后端在事务内拒绝 bootstrap 外部注册并返回
// auth.external_bootstrap_required，由 registerErrorMessage 映射为说明文案。
const showExternalRegistrationProviders = computed(() =>
  isRegistrationOpen.value
  && !isExternalTicketMode.value
  && registrationProviders.value.length > 0
)
const emailVerificationRequired = computed(() =>
  webOption('identity.registration.require_email_verification', 'disabled') === 'enabled'
)

useSForumSeo({
  title: () => isExternalTicketMode.value
    ? t('auth.external.ticketModeTitle')
    : t('auth.registerTitle'),
  noindex: true
})

function handleAltchaVerified(event: Event) {
  const detail = (event as CustomEvent<{ payload?: string }>).detail
  humanVerificationToken.value = detail?.payload || ''
}

function handleAltchaStateChange(event: Event) {
  const detail = (event as CustomEvent<{ state?: string, payload?: string }>).detail
  if (detail?.state === 'verified' && detail.payload) {
    humanVerificationToken.value = detail.payload
    return
  }
  if (detail?.state === 'expired' || detail?.state === 'error' || detail?.state === 'unverified') {
    humanVerificationToken.value = ''
  }
}

function resetHumanVerification() {
  humanVerificationToken.value = ''
  altchaWidget.value?.reset()
}

function fieldError(name: string) {
  return fieldErrors.value[name]?.[0] || ''
}

function fieldDescription(name: string) {
  return fieldError(name) ? `${name}-error` : undefined
}

const passwordDescription = computed(() => {
  return ['password-hint', fieldDescription('password')].filter(Boolean).join(' ')
})
const passwordProgress = computed(() => passwordPolicyProgress(form.password, passwordPolicy.value))
// 进度条颜色档位：空=中性灰、弱=红、中=黄、强(100%)=主题色。
const passwordProgressLevel = computed(() => passwordPolicyProgressLevel(passwordProgress.value))
const passwordRequirementRows = computed(() => {
  return passwordPolicyRequirements(form.password, passwordPolicy.value).map(item => ({
    ...item,
    label: passwordRequirementLabel(item.key)
  }))
})

function passwordRequirementLabel(key: string) {
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

async function finishRegistration(currentUser: CurrentUser) {
  setUser(currentUser)
  // 首注册者即初始 super_admin：注册成功直接落到后台概览，省掉「注册完还要
  // 自己找入口」的断点。该落地优先于 ?redirect= 与 referrer —— 零用户站点
  // 没有已配置的插件/OAuth 回跳，bootstrap 引导优先；非首注册者仍走常规回跳。
  const landing = currentUser.isInitialSuperAdmin ? adminRoutes.path('/') : destination.value
  const needsEmailVerification = emailVerificationRequired.value && !currentUser.emailVerified
  toast.add({
    color: 'success',
    icon: needsEmailVerification ? 'i-lucide-mail-check' : 'i-lucide-check',
    title: needsEmailVerification ? t('auth.emailVerificationActionRequired') : t('auth.registerSuccess'),
    duration: 5000
  })
  // 首注册者即初始 super_admin（服务端在注册事务内判定）。提示只发给真正拿到
  // 权限的注册者，注册前不预告，因此不会向未认证访客暴露 bootstrap 窗口。
  if (currentUser.isInitialSuperAdmin) {
    toast.add({
      color: 'success',
      icon: 'i-lucide-shield-check',
      title: t('auth.initialSuperAdminGranted'),
      duration: 5000
    })
  }
  if (needsEmailVerification) {
    await router.replace({
      path: localePath('/email-verification'),
      query: landing === '/' ? undefined : { redirect: landing }
    })
    return
  }
  await returnFromAuth(landing)
}

async function submitPasswordRegister() {
  if (submitting.value) {
    return
  }
  if (!isRegistrationOpen.value) {
    errorMessage.value = t('auth.registerDisabledDescription')
    return
  }

  errorMessage.value = ''
  sessionUnavailable.value = false
  fieldErrors.value = {}
  submitting.value = true
  const submittedHumanVerificationToken = humanVerificationEnabled.value ? humanVerificationToken.value : ''
  const body: Record<string, unknown> = {
    username: form.username,
    email: form.email,
    password: form.password,
    displayName: form.displayName,
    locale: locale.value
  }
  if (humanVerificationEnabled.value) {
    body.humanVerification = {
      provider: 'altcha',
      token: submittedHumanVerificationToken
    }
  }

  let currentUser: CurrentUser | null = null
  try {
    currentUser = await request<CurrentUser>('/auth/register', {
      method: 'POST',
      body
    })
  } catch (error) {
    fieldErrors.value = apiErrorFields(error)
    sessionUnavailable.value = apiErrorReason(error) === 'auth.session_unavailable'
    if (humanVerificationEnabled.value && (submittedHumanVerificationToken || fieldError('humanVerification'))) {
      resetHumanVerification()
    }
    errorMessage.value = registerErrorMessage(error, t)
  } finally {
    submitting.value = false
  }

  if (!currentUser) {
    return
  }

  await finishRegistration(currentUser)
}

async function submitExternalRegister() {
  if (submitting.value) {
    return
  }
  if (!registrationTicket.value) {
    errorMessage.value = t('auth.external.reasons.ticketInvalid')
    return
  }
  if (!isRegistrationOpen.value) {
    errorMessage.value = t('auth.registerDisabledDescription')
    return
  }

  errorMessage.value = ''
  sessionUnavailable.value = false
  fieldErrors.value = {}
  submitting.value = true
  const submittedHumanVerificationToken = humanVerificationEnabled.value ? humanVerificationToken.value : ''
  // 外部注册不提交密码；凭证行由 Host 在 external-only 路径下保持缺省。
  const body: Record<string, unknown> = {
    ticket: registrationTicket.value,
    username: form.username,
    email: form.email,
    displayName: form.displayName,
    locale: locale.value
  }
  if (humanVerificationEnabled.value) {
    body.humanVerification = {
      provider: 'altcha',
      token: submittedHumanVerificationToken
    }
  }

  let currentUser: CurrentUser | null = null
  try {
    currentUser = await request<CurrentUser>('/auth/external-registration', {
      method: 'POST',
      body
    })
  } catch (error) {
    fieldErrors.value = apiErrorFields(error)
    const reason = apiErrorReason(error)
    sessionUnavailable.value = reason === 'auth.session_unavailable'
    if (humanVerificationEnabled.value && (submittedHumanVerificationToken || fieldError('humanVerification'))) {
      resetHumanVerification()
    }
    // 票据失效后去掉 ticket query，避免用户反复提交同一 opaque token。
    if (
      reason === 'auth.external_registration_ticket_invalid'
      || reason === 'auth.external_registration_ticket_expired'
    ) {
      const nextQuery = { ...route.query }
      delete nextQuery.ticket
      await router.replace({ path: route.path, query: nextQuery })
    }
    errorMessage.value = registerErrorMessage(error, t) || apiErrorMessage(error) || t('errors.registerFailed')
  } finally {
    submitting.value = false
  }

  if (!currentUser) {
    return
  }

  await finishRegistration(currentUser)
}

async function submitRegister() {
  if (isExternalTicketMode.value) {
    await submitExternalRegister()
    return
  }
  await submitPasswordRegister()
}

async function startExternalRegistration(provider: PublicAuthProvider) {
  if (submitting.value || providerStartingId.value || !showExternalRegistrationProviders.value) {
    return
  }
  errorMessage.value = ''
  providerStartingId.value = provider.id
  try {
    await redirectToProvider(provider.id, 'registration', {
      redirectHint: destination.value
    })
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('auth.providers.startFailed')
    providerStartingId.value = ''
  }
}
</script>

<template>

<SFAuthShell>
  <div class="auth-form-wrap">

        <!-- 页面导航 tab -->
        <nav class="auth-tabs" :aria-label="t('auth.loginOrRegister')">
          <NuxtLink :to="authPageLink('/login')" class="auth-tab">
            {{ t('auth.loginTitle') }}
          </NuxtLink>
          <NuxtLink :to="localePath('/register')" class="auth-tab auth-tab--active">
            {{ t('auth.registerTitle') }}
          </NuxtLink>
        </nav>

        <h2 class="auth-form-title">
          {{ isExternalTicketMode ? t('auth.external.ticketModeHeading') : t('auth.registerHeading') }}
        </h2>
        <p class="auth-form-sub">
          {{ isExternalTicketMode ? t('auth.external.ticketModeIntro') : t('auth.registerIntro') }}
        </p>

        <div v-if="!isRegistrationOpen" class="auth-closed">
          <SFAlert
            :title="t('auth.registerDisabledTitle')"
            variant="warning"
            compact
            class="auth-alert"
          >
            <p>{{ t('auth.registerDisabledDescription') }}</p>
            <NuxtLink :to="authPageLink('/login')" class="auth-alert-action">
              {{ t('auth.registerDisabledGoLogin') }}
            </NuxtLink>
          </SFAlert>
        </div>

        <form v-else @submit.prevent="submitRegister">
          <SFAlert
            v-if="isExternalTicketMode"
            :title="t('auth.external.ticketModeNotice')"
            variant="info"
            compact
            class="auth-alert"
          />

          <SFAlert
            v-if="surfaceError"
            :title="surfaceError"
            :variant="surfaceErrorVariant"
            compact
            class="auth-alert"
          >
            <NuxtLink
              v-if="sessionUnavailable"
              :to="authPageLink('/login')"
              class="auth-alert-action"
            >
              {{ t('auth.goLogin') }}
            </NuxtLink>
          </SFAlert>

          <div class="auth-field">
            <label class="auth-label" for="username-input">
              {{ t('auth.username') }}
            </label>
            <input
              id="username-input"
              v-model="form.username"
              :class="['auth-input', fieldError('username') ? 'auth-input--invalid' : '']"
              type="text"
              name="username"
              :placeholder="t('auth.usernamePlaceholder')"
              autocomplete="username"
              required
              :aria-invalid="fieldError('username') ? 'true' : undefined"
              :aria-describedby="fieldDescription('username')"
            />
            <p v-if="fieldError('username')" id="username-error" class="auth-field-message">
              {{ fieldError('username') }}
            </p>
          </div>

          <div class="auth-field">
            <label class="auth-label" for="email-input">
              {{ t('auth.email') }}
            </label>
            <input
              id="email-input"
              v-model="form.email"
              :class="['auth-input', fieldError('email') ? 'auth-input--invalid' : '']"
              type="email"
              name="email"
              :placeholder="t('auth.emailPlaceholder')"
              autocomplete="email"
              required
              :aria-invalid="fieldError('email') ? 'true' : undefined"
              :aria-describedby="fieldDescription('email')"
            />
            <p v-if="fieldError('email')" id="email-error" class="auth-field-message">
              {{ fieldError('email') }}
            </p>
          </div>

          <div class="auth-field">
            <label class="auth-label" for="displayname-input">
              {{ t('auth.displayName') }}
              <span class="auth-label-optional">（{{ t('auth.optional') }}）</span>
            </label>
            <input
              id="displayname-input"
              v-model="form.displayName"
              class="auth-input"
              type="text"
              name="displayName"
              :placeholder="t('auth.displayNamePlaceholder')"
            />
          </div>

          <div v-if="!isExternalTicketMode" class="auth-field">
            <label class="auth-label" for="reg-password-input">
              {{ t('auth.password') }}
            </label>
            <input
              id="reg-password-input"
              v-model="form.password"
              :class="['auth-input', fieldError('password') ? 'auth-input--invalid' : '']"
              type="password"
              name="password"
              :placeholder="t('auth.passwordPlaceholder')"
              autocomplete="new-password"
              required
              :aria-invalid="fieldError('password') ? 'true' : undefined"
              :aria-describedby="passwordDescription"
            />
            <div id="password-hint" class="auth-password-policy">
              <div class="auth-password-policy__header">
                <span>{{ t('auth.passwordStrength') }}</span>
                <span :class="['auth-password-policy__value', `is-${passwordProgressLevel}`]">{{ passwordProgress }}%</span>
              </div>
              <div class="auth-password-policy__bar" :class="[`is-${passwordProgressLevel}`]" aria-hidden="true">
                <span :style="{ width: `${passwordProgress}%` }" />
              </div>
              <p class="auth-field-hint">
                {{ t('auth.passwordPolicySummary', { min: passwordPolicy.minLength, max: passwordPolicy.maxLength }) }}
              </p>
              <ul class="auth-password-policy__list">
                <li v-for="item in passwordRequirementRows" :key="item.key" :class="{ 'is-met': item.met }">
                  <UIcon :name="item.met ? 'i-lucide-check' : 'i-lucide-circle'" class="auth-password-policy__icon" />
                  <span>{{ item.label }}</span>
                </li>
              </ul>
            </div>
            <p v-if="fieldError('password')" id="password-error" class="auth-field-message">
              {{ fieldError('password') }}
            </p>
          </div>

          <div v-if="humanVerificationEnabled" class="auth-field">
            <label id="human-verification-label" class="auth-label">
              {{ t('auth.humanVerification') }}
            </label>
            <ClientOnly>
              <altcha-widget
                ref="altchaWidget"
                :class="altchaWidgetClass"
                :challenge="altchaChallengeUrl"
                :configuration="altchaConfiguration"
                :auto="altchaWidgetAuto"
                :display="altchaWidgetDisplay"
                :language="locale === 'zh-CN' ? 'zh-cn' : 'en'"
                :type="altchaWidgetType"
                :workers="altchaWidgetWorkers"
                :aria-invalid="fieldError('humanVerification') ? 'true' : undefined"
                :aria-labelledby="'human-verification-label'"
                :aria-describedby="fieldDescription('humanVerification')"
                @verified="handleAltchaVerified"
                @expired="resetHumanVerification"
                @statechange="handleAltchaStateChange"
              />
              <template #fallback>
                <div class="auth-altcha-fallback">
                  {{ t('auth.humanVerificationLoading') }}
                </div>
              </template>
            </ClientOnly>
            <p v-if="fieldError('humanVerification')" id="humanVerification-error" class="auth-field-message">
              {{ fieldError('humanVerification') }}
            </p>
          </div>

          <button
            class="auth-btn"
            type="submit"
            :disabled="submitting || preparingExternalRegistration || Boolean(providerStartingId)"
          >
            {{
              submitting
                ? t('auth.registering')
                : (isExternalTicketMode ? t('auth.external.submitTicketRegister') : t('auth.submitRegister'))
            }}
          </button>
        </form>

        <SFAuthProviderButtons
          v-if="showExternalRegistrationProviders"
          :providers="registrationProviders"
          operation="registration"
          :starting-id="providerStartingId"
          :disabled="submitting"
          @start="startExternalRegistration"
        />

        <p class="auth-terms">
          {{ t('auth.agreeTo') }}
          <NuxtLink :to="localePath('/terms')">{{ t('auth.terms') }}</NuxtLink>
          {{ t('auth.and') }}
          <NuxtLink :to="localePath('/privacy')">{{ t('auth.privacy') }}</NuxtLink>
        </p>

        <p class="auth-switch">
          {{ t('auth.haveAccount') }}
          <NuxtLink :to="authPageLink('/login')">{{ t('auth.goLogin') }}</NuxtLink>
        </p>

  </div>
</SFAuthShell>
</template>

<style scoped>
.auth-form-wrap {
  width: 100%;
  max-width: 380px;
}

.auth-tabs {
  display: flex;
  border-bottom: 1px solid var(--sf-border);
  margin-bottom: 32px;
}

.auth-tab {
  padding: 10px 0;
  margin-right: 24px;
  margin-bottom: -1px;
  border-bottom: 2px solid transparent;
  color: var(--sf-fg-tertiary);
  font-size: 14px;
  font-weight: 600;
  text-decoration: none;
  transition: color 0.18s, border-color 0.18s;
}

.auth-tab:hover { color: var(--sf-fg-secondary); }

.auth-tab--active {
  color: var(--sf-fg);
  border-bottom-color: var(--sf-accent);
}

.auth-form-title {
  font-size: 20px;
  font-weight: 700;
  letter-spacing: -0.02em;
  color: var(--sf-fg);
  margin: 0 0 5px;
}

.auth-form-sub {
  font-size: 13px;
  color: var(--sf-fg-secondary);
  margin: 0 0 26px;
  line-height: 1.55;
}

.auth-alert {
  margin-bottom: 16px;
}

.auth-alert-action {
  display: inline-flex;
  margin-top: 8px;
  color: #b91c1c;
  font-size: 12px;
  font-weight: 700;
  text-decoration: underline;
  text-underline-offset: 3px;
}

.auth-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 14px;
}

.auth-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--sf-fg-secondary);
}

.auth-label-optional {
  font-weight: 400;
  color: var(--sf-fg-tertiary);
}

.auth-input {
  height: 40px;
  padding: 0 13px;
  border: 1px solid var(--sf-border);
  border-radius: 7px;
  background: var(--sf-card);
  color: var(--sf-fg);
  font-size: 14px;
  font-family: inherit;
  outline: none;
  transition: border-color 0.18s, box-shadow 0.18s;
  width: 100%;
}

.auth-input:hover { border-color: var(--sf-fg-tertiary); }

.auth-input:focus {
  border-color: var(--sf-accent);
  box-shadow: 0 0 0 3px var(--sf-accent-focus);
}

.auth-input--invalid,
.auth-input--invalid:hover,
.auth-input--invalid:focus {
  border-color: #dc2626;
}

.auth-input--invalid:focus {
  box-shadow: 0 0 0 3px rgba(220, 38, 38, 0.12);
}

.auth-field-message {
  margin: 0;
  color: #b91c1c;
  font-size: 12px;
  line-height: 1.45;
}

.auth-field-hint {
  margin: 0;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  line-height: 1.45;
}

.auth-password-policy {
  display: grid;
  gap: 7px;
  margin-top: 1px;
}

.auth-password-policy__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
}

.auth-password-policy__bar {
  height: 5px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--sf-muted);
  border: 1px solid var(--sf-border);
}

.auth-password-policy__bar span {
  display: block;
  height: 100%;
  min-width: 0;
  border-radius: inherit;
  background: var(--sf-muted);
  transition: width 0.18s ease, background-color 0.18s ease;
}
/* 进度条按合格度分档变色：弱=亮红、中=亮琥珀、强=主题色；空输入保持中性灰。
   弱/中档用亮色阶而非 --sf-danger/--sf-warning：后者偏暗、色相接近，
   大色块上难辨（红↔棕橙糊在一起）。 */
.auth-password-policy__bar.is-weak span {
  background: #ef4444;
}
.auth-password-policy__bar.is-medium span {
  background: #f59e0b;
}
.auth-password-policy__bar.is-strong span {
  background: var(--sf-accent);
}
/* 百分比文字与进度条同档变色，保持视觉一致。 */
.auth-password-policy__value.is-weak {
  color: var(--sf-danger);
}
.auth-password-policy__value.is-medium {
  color: var(--sf-warning);
}
.auth-password-policy__value.is-strong {
  color: var(--sf-accent);
}

.auth-password-policy__list {
  display: grid;
  gap: 5px;
  margin: 0;
  padding: 0;
  list-style: none;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  line-height: 1.4;
}

.auth-password-policy__list li {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 18px;
}

.auth-password-policy__list li.is-met {
  color: var(--sf-accent);
}

.auth-password-policy__icon {
  width: 13px;
  height: 13px;
  flex: 0 0 13px;
}

/* 覆盖 WebKit 自动填充默认底色，保持注册表单跟随当前主题表面。 */
.auth-input:-webkit-autofill,
.auth-input:-webkit-autofill:hover,
.auth-input:-webkit-autofill:active {
  -webkit-box-shadow: 0 0 0 1000px var(--sf-card) inset;
  box-shadow: 0 0 0 1000px var(--sf-card) inset;
  -webkit-text-fill-color: var(--sf-fg);
  caret-color: var(--sf-fg);
  transition: background-color 9999s ease-out, color 9999s ease-out;
}

.auth-input:-webkit-autofill:focus {
  border-color: var(--sf-accent);
  -webkit-box-shadow: 0 0 0 1000px var(--sf-card) inset, 0 0 0 3px var(--sf-accent-focus);
  box-shadow: 0 0 0 1000px var(--sf-card) inset, 0 0 0 3px var(--sf-accent-focus);
  -webkit-text-fill-color: var(--sf-fg);
  caret-color: var(--sf-fg);
}

.auth-input::placeholder { color: var(--sf-fg-tertiary); }

.auth-altcha--invalid {
  --altcha-border-color: #dc2626;
}

.auth-altcha-fallback {
  min-height: 52px;
  display: flex;
  align-items: center;
  padding: 0 13px;
  border: 1px solid var(--sf-border);
  border-radius: 7px;
  color: var(--sf-fg-tertiary);
  font-size: 13px;
}

.auth-btn {
  display: block;
  width: 100%;
  height: 40px;
  border: none;
  border-radius: 7px;
  background: var(--sf-accent);
  color: #fff;
  font-size: 14px;
  font-weight: 600;
  font-family: inherit;
  cursor: pointer;
  letter-spacing: 0.01em;
  transition: background 0.18s, box-shadow 0.18s;
  margin-top: 8px;
}

.auth-btn:hover:not(:disabled) {
  background: var(--sf-accent-hover);
  box-shadow: 0 4px 14px rgb(var(--sf-accent-rgb) / 0.25);
}

.auth-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.auth-terms {
  margin-top: 14px;
  font-size: 12px;
  color: var(--sf-fg-tertiary);
  text-align: center;
  line-height: 1.6;
}

.auth-terms a {
  color: var(--sf-fg-secondary);
  text-decoration: none;
  border-bottom: 1px solid var(--sf-border);
}

.auth-switch {
  margin-top: 14px;
  font-size: 13px;
  color: var(--sf-fg-tertiary);
  text-align: center;
}

.auth-switch a {
  color: var(--sf-fg-secondary);
  font-weight: 600;
  text-decoration: none;
  border-bottom: 1px solid var(--sf-border);
  transition: color 0.15s, border-color 0.15s;
}

.auth-switch a:hover {
  color: var(--sf-fg);
  border-bottom-color: var(--sf-fg-tertiary);
}

</style>
