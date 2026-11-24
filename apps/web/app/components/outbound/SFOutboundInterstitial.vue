<script setup lang="ts">
import { useContentLinkPolicy } from '~/composables/forum/useContentLinkPolicy'
import {
  decodeOutboundTarget,
  isOffsiteContentHref,
  isTrustedOutboundHost,
  outboundTargetHost
} from '~/utils/forum/forumOutboundLinks'

/**
 * 站外链接中转页（Host 安全面）。
 *
 * 形态契约：
 * - 「无需提示」的三种情况（运营关掉中转 / 命中免中转白名单 / 目标其实就是本站）
 *   在 SSR 阶段直接 302 放行，不渲染中间页；
 * - 其余情况必须由用户显式确认，「继续访问」是普通 <a>（无 JS 也能用），
 *   且为同标签页跳转——新标签页语义已在正文链接上生效；
 * - 页面自身不索引、不外链式转发，只展示目的地域名与完整地址。
 *
 * 视觉：单面板窄列，域名是唯一主角；面板内依次是图标方块 → 「将要前往」→ 域名
 * （锁/警示图标内联）→ meta（规则说明 + 站外药丸 + 协议标签）→ URL 块（含复制）
 * → 分隔线 → 三条检查项 → 通栏主按钮 → 纯文本「留在本站」→ 面板外脚注。
 */
const route = useRoute()
const localePath = useLocalePath()
const { t } = useI18n()
const toast = useToast()
const { contentLinkPolicy } = useContentLinkPolicy()

const target = computed(() => decodeOutboundTarget(route.query.u))
const targetHost = computed(() => (target.value ? outboundTargetHost(target.value) : ''))
const targetIsSecure = computed(() => Boolean(target.value?.startsWith('https://')))

/**
 * 是否需要停在提示页。默认 true（配置未加载时宁可多一次提示，也不静默放行）。
 */
const requiresNotice = computed(() => {
  if (!target.value) {
    return false
  }
  const policy = contentLinkPolicy.value
  if (!policy.interstitial) {
    return false
  }
  if (isTrustedOutboundHost(targetHost.value, policy.trustedDomains)) {
    return false
  }
  return isOffsiteContentHref(target.value, policy.internalHosts)
})

// 无需提示：服务端直接放行（302），客户端导航到本页时同样立刻跳转。
if (target.value && !requiresNotice.value) {
  await navigateTo(target.value, { external: true, redirectCode: 302 })
}

async function copyTarget() {
  const value = target.value
  if (!value || !import.meta.client) {
    return
  }
  try {
    await navigator.clipboard?.writeText(value)
    toast.add({
      color: 'neutral',
      icon: 'i-lucide-clipboard-check',
      title: t('outbound.copySuccessTitle'),
      duration: 5000
    })
  } catch {
    // 剪贴板被拒绝时保持页面可见，用户仍可手动选中地址复制。
  }
}
</script>

<template>
  <main class="sforum-outbound" data-outbound-body="host">
    <!-- 非法/缺失目标：不展示任何可跳转内容，只给返回入口。 -->
    <section v-if="!target" class="sforum-outbound__panel" data-outbound-state="invalid">
      <span class="sforum-outbound__tile" aria-hidden="true">
        <UIcon name="i-lucide-unlink" />
      </span>
      <h1 class="sforum-outbound__invalid-title">{{ t('outbound.invalidTitle') }}</h1>
      <p class="sforum-outbound__invalid-body">{{ t('outbound.invalidBody') }}</p>
      <a
        class="sf-button sf-button--primary sf-button--md sf-button--block sforum-outbound__primary"
        :href="localePath('/')"
      >
        <span class="sf-button__label">{{ t('outbound.invalidBackLabel') }}</span>
      </a>
    </section>

    <template v-else>
      <section class="sforum-outbound__panel" data-outbound-state="notice">
        <span class="sforum-outbound__tile" aria-hidden="true">
          <UIcon name="i-lucide-globe" />
        </span>

        <p class="sforum-outbound__label">{{ t('outbound.destinationLabel') }}</p>
        <h1 class="sforum-outbound__host">
          <span
            class="sforum-outbound__lock"
            :class="{ 'sforum-outbound__lock--warn': !targetIsSecure }"
            aria-hidden="true"
          >
            <UIcon :name="targetIsSecure ? 'i-lucide-lock' : 'i-lucide-triangle-alert'" />
          </span>
          {{ targetHost }}
        </h1>
        <div class="sforum-outbound__meta">
          <span>{{ t('outbound.destinationRuleNote') }}</span>
          <span class="sforum-outbound__chip">{{ t('outbound.externalChip') }}</span>
          <span
            class="sforum-outbound__proto"
            :class="{ 'sforum-outbound__proto--warn': !targetIsSecure }"
          >
            {{ targetIsSecure ? 'HTTPS' : 'HTTP' }}
          </span>
        </div>

        <div class="sforum-outbound__url">
          <code class="sforum-outbound__url-text">{{ target }}</code>
          <button
            type="button"
            class="sforum-outbound__copy"
            :title="t('outbound.copyLabel')"
            :aria-label="t('outbound.copyLabel')"
            @click="copyTarget"
          >
            <UIcon name="i-lucide-copy" />
          </button>
        </div>

        <div class="sforum-outbound__divider" role="presentation" />

        <ul class="sforum-outbound__checks">
          <li>
            <UIcon
              :name="targetIsSecure ? 'i-lucide-shield-check' : 'i-lucide-shield-alert'"
              :class="['sforum-outbound__check-icon', targetIsSecure ? '' : 'sforum-outbound__check-icon--warn']"
            />
            {{ targetIsSecure ? t('outbound.checkSecure') : t('outbound.checkInsecure') }}
          </li>
          <li>
            <UIcon name="i-lucide-shield-check" class="sforum-outbound__check-icon" />
            {{ t('outbound.checkNoSession') }}
          </li>
          <li>
            <UIcon name="i-lucide-shield-check" class="sforum-outbound__check-icon" />
            {{ t('outbound.checkNoCredentials') }}
          </li>
        </ul>

        <!-- 同标签页跳转；新标签页语义已经在正文链接上生效。 -->
        <a
          class="sf-button sf-button--primary sf-button--md sf-button--block sforum-outbound__primary"
          :href="target"
          rel="nofollow noopener noreferrer"
        >
          <span class="sf-button__label">{{ t('outbound.continueLabel') }}</span>
          <span class="sf-button__icon"><UIcon name="i-lucide-external-link" /></span>
        </a>
        <a class="sforum-outbound__stay" :href="localePath('/')">{{ t('outbound.stayLabel') }}</a>
      </section>

      <p class="sforum-outbound__foot">
        {{ t('outbound.destinationPrivacyNote') }}
        <span aria-hidden="true">·</span>
        {{ t('outbound.footerNote') }}
      </p>
    </template>
  </main>
</template>

<style scoped>
/* 600 窄列：把注意力全部压在域名上，无侧栏。横向内边距由宿主公开内容列
   （SFHostPublicChrome__column）拥有，这里只管纵向留白。 */
.sforum-outbound {
  width: 100%;
  max-width: 600px;
  margin: 0 auto;
  padding: 64px 0 72px;
}

.sforum-outbound__panel {
  padding: 32px;
  border: 1px solid var(--sf-border);
  border-radius: var(--sf-theme-radius, 4px);
  background: var(--sf-card);
}

.sforum-outbound__tile {
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  border-radius: 6px;
  background: var(--sf-accent-soft);
  color: var(--sf-accent);
  font-size: 22px;
}

.sforum-outbound__label {
  margin: 20px 0 0;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.sforum-outbound__host {
  margin: 6px 0 0;
  font-family: var(--sf-public-heading-font-family, "Noto Sans SC", system-ui, sans-serif);
  font-size: 30px;
  font-weight: 750;
  line-height: 1.25;
  letter-spacing: -0.02em;
  word-break: break-all;
}

/* 锁图标内联在域名行首：安全状态与域名一起被读到，而不是另起一块。 */
.sforum-outbound__lock {
  display: inline-flex;
  margin-right: 8px;
  color: var(--sf-accent);
  font-size: 19px;
  transform: translateY(-1px);
  vertical-align: baseline;
}

.sforum-outbound__lock--warn {
  color: var(--sf-warning);
}

.sforum-outbound__meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 12px;
  margin-top: 8px;
  color: var(--sf-fg-secondary);
  font-size: 12px;
}

.sforum-outbound__chip {
  display: inline-flex;
  align-items: center;
  height: 19px;
  padding: 0 7px;
  border: 1px solid color-mix(in srgb, var(--sf-warning) 32%, transparent);
  border-radius: 999px;
  background: var(--sf-warning-soft);
  color: var(--sf-warning);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.02em;
}

/* 协议标签：HTTP 与药丸同色，HTTPS 退回中性色，避免把安全目标也画成警告。 */
.sforum-outbound__proto {
  display: inline-flex;
  align-items: center;
  height: 19px;
  padding: 0 7px;
  border: 1px solid var(--sf-border);
  border-radius: 999px;
  color: var(--sf-fg-tertiary);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.02em;
}

.sforum-outbound__proto--warn {
  border-color: color-mix(in srgb, var(--sf-warning) 32%, transparent);
  background: var(--sf-warning-soft);
  color: var(--sf-warning);
}

.sforum-outbound__url {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-top: 18px;
  padding: 10px 12px;
  border: 1px solid var(--sf-border);
  border-radius: var(--sf-theme-radius, 4px);
  background: var(--sf-muted);
}

.sforum-outbound__url-text {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--sf-fg-secondary);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11.5px;
  line-height: 1.6;
  word-break: break-all;
}

.sforum-outbound__copy {
  display: grid;
  flex: 0 0 auto;
  place-items: center;
  width: 26px;
  height: 26px;
  margin: -3px -3px 0 0;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--sf-fg-tertiary);
  font-size: 14px;
  cursor: pointer;
}

.sforum-outbound__copy:hover {
  background: var(--sf-card);
  color: var(--sf-fg);
}

.sforum-outbound__copy:focus-visible {
  outline: none;
  box-shadow: 0 0 0 3px var(--sf-accent-focus);
}

.sforum-outbound__divider {
  height: 1px;
  margin: 22px 0;
  background: var(--sf-border);
}

.sforum-outbound__checks {
  display: flex;
  flex-direction: column;
  gap: 9px;
  margin: 0;
  padding: 0;
  color: var(--sf-fg-secondary);
  font-size: 12.5px;
  line-height: 1.5;
  list-style: none;
}

.sforum-outbound__checks li {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}

.sforum-outbound__check-icon {
  flex: 0 0 auto;
  margin-top: 2px;
  color: var(--sf-accent);
  font-size: 14px;
}

.sforum-outbound__check-icon--warn {
  color: var(--sf-warning);
}

.sforum-outbound__primary {
  height: 44px;
  margin-top: 24px;
  gap: 7px;
  font-size: 14px;
}

.sforum-outbound__primary:focus-visible {
  outline: none;
  box-shadow: 0 0 0 3px var(--sf-accent-focus);
}

.sforum-outbound__stay {
  display: block;
  margin-top: 14px;
  color: var(--sf-fg-secondary);
  font-size: 12.5px;
  font-weight: 600;
  text-align: center;
  text-decoration: none;
}

.sforum-outbound__stay:hover {
  color: var(--sf-accent);
  text-decoration: underline;
  text-underline-offset: 3px;
}

.sforum-outbound__stay:focus-visible {
  outline: none;
  border-radius: 4px;
  box-shadow: 0 0 0 3px var(--sf-accent-focus);
}

.sforum-outbound__invalid-title {
  margin: 20px 0 0;
  font-family: var(--sf-public-heading-font-family, "Noto Sans SC", system-ui, sans-serif);
  font-size: 22px;
  font-weight: 750;
  line-height: 1.3;
  letter-spacing: -0.01em;
}

.sforum-outbound__invalid-body {
  margin: 6px 0 0;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  line-height: 1.7;
}

.sforum-outbound__foot {
  margin: 20px 0 0;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  line-height: 1.7;
  text-align: center;
}

@media (max-width: 640px) {
  .sforum-outbound {
    padding: 32px 0 48px;
  }

  .sforum-outbound__panel {
    padding: 22px;
  }

  .sforum-outbound__host {
    font-size: 24px;
  }
}
</style>
