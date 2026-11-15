<script setup lang="ts">
import { useExternalAuthFeedback } from '~/composables/identity/useExternalAuthFeedback'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useAuthReturnNavigation } from '~/composables/identity/useAuthReturnNavigation'
import { useAuthProviders } from '~/composables/identity/useAuthProviders'
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import SFAuthProviderButtons from '~/components/identity/SFAuthProviderButtons.vue'
import SFAuthShell from '~/components/identity/auth/SFAuthShell.vue'
/**
 * 宿主 body 岛：auth.login（凭证表单仍为 Host 组件，不经主题可执行代码）。
 * 路由页保留 layout/middleware meta + fail-closed 回退。
 */

import type { AltchaWidgetElement } from 'altcha'
import type { CurrentUser } from '~/composables/identity/useAuthSession'
import type { PublicAuthProvider } from '~/composables/identity/useAuthProviders'
import { apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const toast = useToast()
const localePath = useLocalePath()
const { apiBaseUrl, request } = useApiClient()
const { setUser } = useAuthSession()
const { returnFromAuth, authPageLink, destination } = useAuthReturnNavigation()
const { altchaWidgetSettings, loginRiskAlways } = useWebOptions()
const {
  loginProviders,
  redirectToProvider
} = useAuthProviders()
const continuationProviderId = computed(() =>
  typeof route.query.continuation_provider === 'string'
    ? route.query.continuation_provider.trim()
    : ''
)
const availableLoginProviders = computed(() =>
  loginProviders.value.filter(provider => provider.id !== continuationProviderId.value)
)
const {
  alertMessage: externalAlertMessage,
  alertVariant: externalAlertVariant
} = useExternalAuthFeedback()

type RegistrationStatus = {
  nextUserIsInitialSuperAdmin: boolean
  registrationEnabled?: boolean
}
const { data: registrationStatus } = await useAsyncData('auth-registration-status-login', async () => {
  try {
    return await request<RegistrationStatus>('/auth/registration-status')
  } catch {
    return { nextUserIsInitialSuperAdmin: false, registrationEnabled: true }
  }
})
// 关闭开放注册时隐藏注册入口；bootstrap（无用户）时接口会返回 true。
const showRegisterLinks = computed(() => registrationStatus.value?.registrationEnabled !== false)

const form = reactive({
  login: '',
  password: ''
})
const submitting = ref(false)
const providerStartingId = ref('')
const errorMessage = ref('')
const humanVerificationRequired = ref(false)
// 登录强化「总是验证」：进入页面即渲染 ALTCHA，而不是先吃一次 422。
watch(loginRiskAlways, (required) => {
  if (required) humanVerificationRequired.value = true
}, { immediate: true })
const humanVerificationToken = ref('')
const altchaWidget = ref<AltchaWidgetElement | null>(null)
const surfaceError = computed(() => errorMessage.value || externalAlertMessage.value)
const surfaceErrorVariant = computed(() =>
  errorMessage.value ? 'danger' : (externalAlertVariant.value || 'danger')
)
const emailVerifiedFeedback = computed(() =>
  route.query.reason === 'auth.email_verified' ? t('auth.emailVerificationConfirmed') : ''
)
const surfaceMessage = computed(() => surfaceError.value || emailVerifiedFeedback.value)
const surfaceVariant = computed(() => emailVerifiedFeedback.value && !surfaceError.value
  ? 'success'
  : surfaceErrorVariant.value
)
const altchaChallengeUrl = computed(() => `${apiBaseUrl}/human-verification/challenge?purpose=login_risk`)
const altchaConfiguration = computed(() => JSON.stringify({
  hideLogo: altchaWidgetSettings.value.hideLogo,
  hideFooter: altchaWidgetSettings.value.hideFooter,
  minDuration: altchaWidgetSettings.value.minDuration
}))

useSForumSeo({
  title: () => t('auth.loginTitle'),
  noindex: true
})

onMounted(() => {
  if (!emailVerifiedFeedback.value) return
  toast.add({
    color: 'success',
    icon: 'i-lucide-mail-check',
    title: emailVerifiedFeedback.value,
    duration: 5000
  })
  const nextQuery = { ...route.query }
  delete nextQuery.reason
  void router.replace({ path: route.path, query: nextQuery })
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

async function submitLogin() {
  errorMessage.value = ''
  submitting.value = true
  let currentUser: CurrentUser
  const submittedHumanVerificationToken = humanVerificationToken.value
  const body: Record<string, unknown> = {
    login: form.login,
    password: form.password
  }
  if (humanVerificationRequired.value) {
    body.humanVerification = {
      provider: 'altcha',
      token: submittedHumanVerificationToken
    }
  }

  try {
    currentUser = await request<CurrentUser>('/auth/login', {
      method: 'POST',
      body
    })
  } catch (error) {
    const reason = apiErrorReason(error)
    if (reason === 'human_verification.required') {
      humanVerificationRequired.value = true
      errorMessage.value = t('errors.humanVerificationRequired')
    } else {
      errorMessage.value = apiErrorMessage(error) || t('errors.loginFailed')
    }
    if (humanVerificationRequired.value && submittedHumanVerificationToken) {
      resetHumanVerification()
    }
    return
  } finally {
    submitting.value = false
  }

  setUser(currentUser)
  toast.add({
    color: 'success',
    icon: 'i-lucide-check',
    title: t('auth.loginSuccess'),
    duration: 5000
  })
  await returnFromAuth()
}

async function startExternalLogin(provider: PublicAuthProvider) {
  if (submitting.value || providerStartingId.value) {
    return
  }
  errorMessage.value = ''
  providerStartingId.value = provider.id
  try {
    // redirectHint 仅传已校验本地路径；Host 会再次校验后写入 callback 事务。
    await redirectToProvider(provider.id, 'login', {
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
          <NuxtLink :to="localePath('/login')" class="auth-tab auth-tab--active">
            {{ t('auth.loginTitle') }}
          </NuxtLink>
          <NuxtLink
            v-if="showRegisterLinks"
            :to="authPageLink('/register')"
            class="auth-tab"
          >
            {{ t('auth.registerTitle') }}
          </NuxtLink>
        </nav>

        <h2 class="auth-form-title">{{ t('auth.loginHeading') }}</h2>
        <p class="auth-form-sub">{{ t('auth.loginIntro') }}</p>

        <form @submit.prevent="submitLogin">
          <SFAlert
          v-if="surfaceMessage"
          :title="surfaceMessage"
          :variant="surfaceVariant"
            compact
            class="auth-alert"
          />

          <div class="auth-field">
            <label class="auth-label" for="login-input">
              {{ t('auth.loginName') }}
            </label>
            <input
              id="login-input"
              v-model="form.login"
              class="auth-input"
              type="text"
              name="login"
              :placeholder="t('auth.loginNamePlaceholder')"
              autocomplete="username"
              required
            />
          </div>

          <div class="auth-field">
            <div class="auth-field-head">
              <label class="auth-label" for="password-input">
                {{ t('auth.password') }}
              </label>
              <NuxtLink :to="localePath('/forgot-password')" class="auth-forgot">{{ t('auth.forgotPassword') }}</NuxtLink>
            </div>
            <input
              id="password-input"
              v-model="form.password"
              class="auth-input"
              type="password"
              name="password"
              placeholder="••••••••"
              autocomplete="current-password"
              required
            />
          </div>

          <div v-if="humanVerificationRequired" class="auth-field">
            <label id="login-human-verification-label" class="auth-label">
              {{ t('auth.humanVerification') }}
            </label>
            <ClientOnly>
              <altcha-widget
                ref="altchaWidget"
                class="auth-altcha"
                :challenge="altchaChallengeUrl"
                :configuration="altchaConfiguration"
                :auto="altchaWidgetSettings.auto"
                :display="altchaWidgetSettings.display"
                :language="locale === 'zh-CN' ? 'zh-cn' : 'en'"
                :type="altchaWidgetSettings.type"
                :workers="altchaWidgetSettings.workers"
                aria-labelledby="login-human-verification-label"
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
          </div>

          <button class="auth-btn" type="submit" :disabled="submitting || Boolean(providerStartingId)">
            {{ submitting ? t('auth.loggingIn') : t('auth.submitLogin') }}
          </button>
        </form>

        <SFAuthProviderButtons
          :providers="availableLoginProviders"
          operation="login"
          :starting-id="providerStartingId"
          :disabled="submitting"
          @start="startExternalLogin"
        />

        <p v-if="showRegisterLinks" class="auth-switch">
          {{ t('auth.needAccount') }}
          <NuxtLink :to="authPageLink('/register')">{{ t('auth.goRegister') }}</NuxtLink>
        </p>

  </div>
</SFAuthShell>
</template>

<style scoped>
.auth-form-wrap {
  width: 100%;
  max-width: 380px;
}

/* 页面 tab 导航 */
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

/* 当前页高亮：login 页永远高亮登录 tab */
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

/* 错误提示 */
.auth-alert {
  margin-bottom: 16px;
}

/* 表单字段 */
.auth-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 14px;
}

.auth-field-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.auth-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--sf-fg-secondary);
}

.auth-forgot {
  font-size: 12px;
  color: var(--sf-fg-tertiary);
  text-decoration: none;
  transition: color 0.15s;
}

.auth-forgot:hover { color: var(--sf-fg-secondary); }

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

/* 覆盖 WebKit 自动填充默认底色，保持登录表单跟随当前主题表面。 */
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

/* 提交按钮 */
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

/* 底部切换提示 */
.auth-switch {
  margin-top: 20px;
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
