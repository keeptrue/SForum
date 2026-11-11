<script setup lang="ts">
/**
 * 前台信任等级徽章（视觉规格 `knowledge/reports/2026-11-11-trust-level-badge-spec.md` §9 裁决口径）。
 *
 * 形态：6px（列表/回复）或 7px（详情作者栏）圆点 + 同色 `TL{n}` 文字；无底色、无描边、无悬停态。
 * TL0 用**同尺寸盒子 + `inset 0 0 0 1px` 空心环**：盒子大小与其它档一致，作者名不会因为档位
 * 变化而重新折行（规格 9.3 的防抖动要求）。
 *
 * 只读装饰：不可聚焦、无指针手型、不带计数/进度、不提供第二个「档位说明」入口
 * （那属于个人主页的等级卡），也不把任何含义只放在 tooltip 里。
 *
 * 降级（规格 §5，从可信到不可信，任何一步都不猜色）：
 * 1. `trustLevel` 缺席或没有 `level` → **不渲染**（不留空容器、不留 gap）；
 * 2. `level` 超出 0–8 → 隐藏（绝不取模回落到 slate）；
 * 3. `levelSlug` 不是已知档位身份（旧数据 / 运营删过档位）→ 纯文字 `TL{n}`，`--sf-fg-tertiary`，无圆点；
 * 4. `color` 为空或不在 9 色白名单 → 按 `level` 序号取默认 token（0=slate … 8=amber）。
 *
 * 颜色只经**冻结白名单字典**映射到 `--sf-tl-*`（见下方 `--sf-tier-badge-token-*` 类），
 * 绝不把服务端字符串拼进 `style`。
 */
import { TRUST_LEVEL_SLUGS } from '~/composables/profile/useTrustLevel'
import { TIER_COLOR_TOKENS, defaultTierColorName, isTierColorToken } from '~/utils/profile/trustLevelColors'
import type { ForumAuthorTrustLevel } from '~/utils/forum/forumTaxonomy'

const props = withDefaults(defineProps<{
  trustLevel?: ForumAuthorTrustLevel | null
  /** 两个密度档：列表/回复 `row`，详情作者栏 `author`（规格 §2）。 */
  size?: 'row' | 'author'
}>(), {
  trustLevel: null,
  size: 'row'
})

const { t } = useI18n()

/** 合法档位上界：TL0..TL8（9 色）。越界即隐藏，不做取模。 */
const MAX_LEVEL = TIER_COLOR_TOKENS.length - 1

const level = computed<number | null>(() => {
  const raw = props.trustLevel?.level
  if (raw === null || raw === undefined) return null
  const parsed = Number(raw)
  if (!Number.isInteger(parsed) || parsed < 0 || parsed > MAX_LEVEL) return null
  return parsed
})

const slug = computed(() => String(props.trustLevel?.levelSlug ?? '').trim())

/**
 * 档位身份是否已知。
 *
 * 判据是**纯本地白名单**（内置五档 slug 或运营新增档的 `tier_<n>`），刻意不查目录、不查接口。
 *
 * 为什么这样就够：服务端的 `levelSlug` 是由**当前 policy 现算**的（`Policy.TierID(rank)`），
 * 所以它永远不会下发一个「已删档的 slug」——下面这个降级分支只是防御**旧缓存 payload**
 * （请求级字段不进缓存，但客户端可能拿着上一次会话的响应）。为一条永不到达的分支
 * 给每一条列表项多发一次目录请求不划算；这里保持零请求是**裁定过的取舍**，不是漏实现。
 */
const slugKnown = computed(() =>
  (TRUST_LEVEL_SLUGS as readonly string[]).includes(slug.value) || /^tier_[0-9]+$/.test(slug.value)
)

/** 有效色：白名单命中就用它，否则按 level 序号兜底（唯一允许的「按位次配色」）。 */
const token = computed(() => {
  const stored = props.trustLevel?.color
  return isTierColorToken(stored) ? stored : defaultTierColorName(level.value ?? 0)
})

const visible = computed(() => level.value !== null)
/** 未知 slug = 纯文字降级：不画圆点、不上档位色。 */
const plain = computed(() => visible.value && !slugKnown.value)
</script>

<template>
  <span
    v-if="visible"
    class="sf-tier-badge"
    :class="[
      `sf-tier-badge--${size}`,
      plain ? 'sf-tier-badge--unknown' : `sf-tier-badge--token-${token}`
    ]"
    :data-tier-level="String(level)"
    data-sf-trust-tier-badge
    role="img"
    :aria-label="t('topicDetail.trustBadge', { level })"
  >
    <span v-if="!plain" class="sf-tier-badge__dot" aria-hidden="true" />
    <!-- 可见文字对 AT 隐藏：读屏只读 role="img" 的 aria-label「信任等级 n」，不读成 "TL 1"。 -->
    <span class="sf-tier-badge__label" aria-hidden="true">TL{{ level }}</span>
  </span>
</template>

<style scoped>
/*
 * 两个密度档（规格 §2）：列表/回复 11px，详情作者栏 12px；间距沿用容器行内 gap，
 * 不额外加外边距（9.2）。`flex: 0 0 auto` + `nowrap`：昵称截断时 TL 不缩不换行（9.4）。
 */
.sf-tier-badge {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: baseline;
  gap: 4px;
  color: var(--sf-fg-tertiary);
  font-weight: 600;
  line-height: 14px;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
  letter-spacing: 0.02em;
  /* 复制作者名时不该把 TL 带进剪贴板。 */
  user-select: none;
  /* 只读装饰：不可聚焦、不是按钮，光标保持文本态。 */
  cursor: text;
}

.sf-tier-badge--author {
  gap: 5px;
  line-height: 16px;
  font-size: 12px;
}

.sf-tier-badge--row {
  font-size: 11px;
}

.sf-tier-badge__label {
  color: inherit;
}

/* 圆点与数字同色同强度；垂直方向用 center 微调，不参与行高计算。 */
.sf-tier-badge__dot {
  display: inline-block;
  align-self: center;
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  border-radius: 999px;
  background: currentColor;
}

.sf-tier-badge--author .sf-tier-badge__dot {
  width: 7px;
  height: 7px;
}

/* TL0：同尺寸盒子 + 内描边空心环（外径仍是 6/7px，绝不加 padding）。 */
.sf-tier-badge[data-tier-level="0"] .sf-tier-badge__dot {
  background: transparent;
  box-shadow: inset 0 0 0 1px currentColor;
}

/* 未知档位（历史数据）：纯文字，无圆点，`--sf-fg-tertiary`，不猜档位色。 */
.sf-tier-badge--unknown {
  color: var(--sf-fg-tertiary);
}

/*
 * 冻结白名单 → `--sf-tl-*` 的唯一映射。服务端字符串只用来选类名，
 * 不认识的值在脚本里已经落回按位次默认色，这里没有兜底分支。
 */
.sf-tier-badge--token-slate { color: var(--sf-tl-slate); }
.sf-tier-badge--token-teal { color: var(--sf-tl-teal); }
.sf-tier-badge--token-emerald { color: var(--sf-tl-emerald); }
.sf-tier-badge--token-sky { color: var(--sf-tl-sky); }
.sf-tier-badge--token-indigo { color: var(--sf-tl-indigo); }
.sf-tier-badge--token-violet { color: var(--sf-tl-violet); }
.sf-tier-badge--token-fuchsia { color: var(--sf-tl-fuchsia); }
.sf-tier-badge--token-rose { color: var(--sf-tl-rose); }
.sf-tier-badge--token-amber { color: var(--sf-tl-amber); }
</style>
