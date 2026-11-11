<script setup lang="ts">
/**
 * 能力解锁（earnable）勾选矩阵 —— M10 / D33 A 路。
 *
 * 数据契约（只读，组件不自己请求、不自己保存）：
 * - `form`：宿主 `SFAdminTrustLevelsTab` 的表单模型，`earnable` 是 `Record<'1'..'4', string[]>`；
 * - `whitelist`：`useTrustLevelEarnableWhitelist()` 的四态，**唯一**可勾键来源（前端零硬编码，T22）；
 * - `canManage`：写权限 `trust.manage`。只有 `admin.access` 的账号能进这个 tab、能读、不能写。
 *
 * 三条不可退让的语义：
 * 1. **累积**：勾「TL2 的 topic.create」= TL2 及以上全部解锁（后端 `Earnable.Grants()`）。
 *    UI 因此只写「该档新增」，标题/说明/aria 三处都必须带这个语义（T21）。本组件不跨档复制键：
 *    复制会造出第二份配置，取消 TL2 后高档还留着键。
 * 2. **失败 ≠ 空**：`status === 'failed'` 时矩阵整块不渲染（换成锁定占位 + 错误条），
 *    空矩阵在运营眼里等于「后端说没有可勾的键」，那是谎。
 * 3. **键 → 人话复用既有权限文案**（`admin.permissionCatalog.<key>.label`），不新建映射表。
 *
 * 几何见 `reports/2026-11-11-trust-level-earnable-spec.md` §2：≥1280 表头 + 裸勾选框，
 * 768–1279 三列格内名，≤767 两列格内名；跨列判据是 `项数 % 列数 === 1`（余数），不是奇偶。
 *
 * 体量说明（AGENTS.md 的 500 行提示线）：本文件 ~550 行里约 300 行是几何 CSS（三档断点 +
 * 五档图标阶梯 + 锁定/加载占位），其余是模板；**派生状态已全部搬进
 * `useTrustLevelEarnableMatrix.ts`**（纯函数、可单测）。再拆只能拆成两个互相传 props 的组件，
 * 那会让「一个矩阵」变成两份契约，比长文件更糟，所以不拆。
 */
import { earnableOrphanText, earnableMatrixState } from '~/composables/admin/settings/useTrustLevelEarnableMatrix'
import type { TrustLevelWhitelistState, TrustLevelsForm } from '~/composables/admin/settings/useTrustLevelsThresholds'
import { earnableKeys, TRUST_LEVEL_ROWS } from '~/composables/admin/settings/useTrustLevelsThresholds'
import { usePermissionText } from '~/composables/identity/usePermissionText'
import { trustLevelIcon, trustLevelSlug } from '~/composables/profile/useTrustLevel'

const props = defineProps<{
  form: TrustLevelsForm
  whitelist: TrustLevelWhitelistState
  canManage: boolean
}>()

const emit = defineEmits<{
  toggle: [level: number, key: string]
  retry: []
}>()

const { t } = useI18n()
const { permissionLabel } = usePermissionText()

const state = computed(() => earnableMatrixState({
  form: props.form,
  whitelist: props.whitelist,
  canManage: props.canManage
}))
const orphanText = computed(() => earnableOrphanText(state.value.orphans))

function levelLabel(level: number) {
  return t(`admin.settings.trustLevels.level.${trustLevelSlug(level)}`)
}

function keyLabel(key: string) {
  return permissionLabel({ key, module: '' })
}

function isChecked(level: number, key: string) {
  return earnableKeys(props.form, level).includes(key)
}

/** 三元组顺序固定：档位 → 语义（新增解锁）→ 键名（规范 §7.1）。 */
function cellLabel(level: number, key: string) {
  return t('admin.settings.trustLevels.earnable.aria.cell', {
    level: levelLabel(level),
    permission: keyLabel(key)
  })
}

function toggle(level: number, key: string) {
  if (!state.value.editable) return
  emit('toggle', level, key)
}
</script>

<template>
  <section
    class="sf-trust-earnable"
    aria-labelledby="sf-trust-earnable-title"
    data-sf-earnable-region
    :style="{ '--sf-earnable-cols': String(state.columnCount) }"
  >
    <div class="sf-trust-earnable__head">
      <h3 id="sf-trust-earnable-title" class="sf-trust-earnable__title">
        {{ t('admin.settings.trustLevels.earnable.title') }}
      </h3>
      <!-- 计数徽标：勾选后读屏能听到「新增项 3/28」；失败态不渲染（没有可解释的对象）。 -->
      <span
        v-if="state.ready"
        class="sf-trust-earnable__count"
        aria-live="polite"
        data-sf-earnable-count
      >{{ t('admin.settings.trustLevels.earnable.count', { checked: state.checkedCount, total: state.totalCells }) }}</span>
    </div>

    <!--
      ① 基线说明（前提）：无条件常驻，不依赖白名单数据，所以 loading / failed 下照常渲染。
      默认安装下这 7 个键已在默认用户组里 ⇒ 勾选是空操作、GrantSource 恒 static。
      不用 UAlert / 不用 warning 色：严重性通道留给白名单失败（error）与残留键（warning）。
    -->
    <div class="sf-trust-earnable__baseline" data-sf-earnable-baseline>
      <p class="sf-trust-earnable__baseline-title">
        <UIcon name="i-lucide-info" class="sf-trust-earnable__baseline-icon" aria-hidden="true" />
        <span>{{ t('admin.settings.trustLevels.earnable.baseline.title') }}</span>
      </p>
      <p class="sf-trust-earnable__baseline-text">{{ t('admin.settings.trustLevels.earnable.baseline.text') }}</p>
    </div>

    <!-- ② 机制说明（累积语义）：必须在按下勾选**之前**读得到（T21 / 规范 §3.1）。失败态不渲染。 -->
    <p v-if="state.ready" id="sf-trust-earnable-note" class="sf-trust-earnable__note">
      {{ t('admin.settings.trustLevels.earnable.note') }}
    </p>

    <!-- 无写权限：只有 admin.access 的账号能到这里，但保存会被后端 403。 -->
    <UAlert
      v-if="!canManage"
      class="sf-trust-earnable__alert"
      color="neutral"
      variant="soft"
      icon="i-lucide-lock"
      :title="t('admin.settings.trustLevels.earnable.readOnly.title')"
      :description="t('admin.settings.trustLevels.earnable.readOnly.description')"
      data-sf-earnable-readonly
    />

    <!--
      失败态（T22）：错误条 + 锁定占位，**整块不渲染矩阵**。
      错误不自动消失，只有重试成功才消失（AGENTS.md：error 不自动关闭）。
    -->
    <template v-if="state.failed">
      <UAlert
        class="sf-trust-earnable__alert"
        color="error"
        variant="soft"
        icon="i-lucide-triangle-alert"
        :title="t('admin.settings.trustLevels.earnable.loadFailed.title')"
        data-sf-earnable-error
      >
        <template #description>
          <p>{{ t('admin.settings.trustLevels.earnable.loadFailed.description') }}</p>
          <!-- 原因原样显示：翻译/美化会让 ops 无法定位。 -->
          <p class="sf-trust-earnable__reason" data-sf-earnable-reason>
            {{ t('admin.settings.trustLevels.earnable.loadFailed.reason', { reason: whitelist.error || 'request_failed' }) }}
          </p>
          <UButton
            class="sf-trust-earnable__retry"
            size="xs"
            color="neutral"
            variant="outline"
            leading-icon="i-lucide-refresh-cw"
            type="button"
            data-sf-earnable-retry
            @click="emit('retry')"
          >
            {{ t('admin.settings.trustLevels.earnable.loadFailed.retry') }}
          </UButton>
        </template>
      </UAlert>
      <div class="sf-trust-earnable__locked" data-sf-earnable-locked>
        <UIcon name="i-lucide-lock" class="sf-trust-earnable__locked-icon" aria-hidden="true" />
        <span>{{ t('admin.settings.trustLevels.earnable.loadFailed.locked') }}</span>
      </div>
    </template>

    <!-- 加载态：同尺寸占位，不加骨架微光（视觉规范禁动效）；SSR 与客户端首帧同形。 -->
    <div v-else-if="!state.ready" class="sf-trust-earnable__locked" aria-busy="true" data-sf-earnable-loading>
      <span>{{ t('admin.settings.trustLevels.earnable.loading') }}</span>
    </div>

    <template v-else>
      <!-- 表头：≥1280 显示（表头与单元格共用同一列模板，天然对齐）；窄屏 display:none。 -->
      <div class="sf-trust-earnable__columns" aria-hidden="true">
        <span v-for="key in state.keys" :key="`head-${key}`" class="sf-trust-earnable__column">
          <span class="sf-trust-earnable__column-name">{{ keyLabel(key) }}</span>
          <span class="sf-trust-earnable__column-key">{{ key }}</span>
        </span>
      </div>

      <fieldset
        v-for="level in TRUST_LEVEL_ROWS"
        :key="level"
        class="sf-trust-earnable__row"
        :data-level="String(level)"
        :data-readonly="level === 0 ? 'true' : 'false'"
        :aria-disabled="level === 0 ? 'true' : undefined"
      >
        <legend class="sf-trust-earnable__legend">
          <span class="sf-trust-earnable__icon" :data-level="String(level)" aria-hidden="true">
            <UIcon :name="trustLevelIcon(level)" class="sf-trust-earnable__glyph" />
          </span>
          <span class="sf-trust-earnable__name">{{ levelLabel(level) }}</span>
          <!-- 与资料页卡同一键：TL 编号是标识符不是文案。 -->
          <span class="sf-trust-earnable__code">{{ t('profile.trustLevel.code', { level }) }}</span>
        </legend>

        <!-- TL0 由 trust.new_user_* 决定，复用阈值行的既有说明键，不新写第二句。 -->
        <p v-if="level === 0" class="sf-trust-earnable__tl0">{{ t('admin.settings.trustLevels.tl0Hint') }}</p>

        <div
          class="sf-trust-earnable__cells"
          :data-span-two="state.spanTwo ? 'true' : 'false'"
          :data-span-three="state.spanThree ? 'true' : 'false'"
        >
          <div v-for="key in state.keys" :key="`${level}-${key}`" class="sf-trust-earnable__cell">
            <UCheckbox
              :model-value="isChecked(level, key)"
              :disabled="level === 0 || !state.editable"
              :aria-disabled="level === 0 || !state.editable ? 'true' : 'false'"
              :aria-label="cellLabel(level, key)"
              aria-describedby="sf-trust-earnable-note"
              @update:model-value="toggle(level, key)"
            />
            <!-- 窄屏格内展示名：可读名称仍来自 aria-label，这里只是视觉补充。 -->
            <span class="sf-trust-earnable__cell-name" aria-hidden="true">{{ keyLabel(key) }}</span>
          </div>
        </div>
      </fieldset>

      <!-- 空态：出厂状态（推荐默认 earnable = ∅），不是错误。矩阵照常渲染。 -->
      <p v-if="state.checkedCount === 0" class="sf-trust-earnable__empty" data-sf-earnable-empty>
        <UIcon name="i-lucide-info" class="sf-trust-earnable__empty-icon" aria-hidden="true" />
        <span>{{ t('admin.settings.trustLevels.earnable.empty') }}</span>
      </p>

      <!-- 白名单外残留：保存会移除，只披露不提供「保留」（后端必拒）。 -->
      <UAlert
        v-if="state.orphans.length > 0"
        class="sf-trust-earnable__alert"
        color="warning"
        variant="soft"
        icon="i-lucide-triangle-alert"
        :title="t('admin.settings.trustLevels.earnable.orphan.title', { count: state.orphans.length })"
        :description="t('admin.settings.trustLevels.earnable.orphan.description', { keys: orphanText })"
        data-sf-earnable-orphans
      />
    </template>
  </section>
</template>

<style scoped>
.sf-trust-earnable {
  min-width: 0;
  border-top: 1px solid var(--sf-border, var(--color-slate-200));
  padding-top: 20px;
}

.sf-trust-earnable__head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.sf-trust-earnable__title {
  color: inherit;
  font-size: 14px;
  font-weight: 600;
}

.sf-trust-earnable__count {
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

/* 常驻行内说明：不折叠、不悬浮、不进 Toast。 */
.sf-trust-earnable__note {
  margin-top: 6px;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 12px;
  line-height: 16px;
}

/* 基线说明：左边框 + 提一档字重表达「先读这段」，不占用严重性颜色通道（规范 §3.3.2）。 */
.sf-trust-earnable__baseline {
  margin-top: 8px;
  border-left: 2px solid var(--color-slate-300, #cbd5e1);
  padding-left: 12px;
}

.dark .sf-trust-earnable__baseline {
  border-left-color: var(--color-zinc-700, #3f3f46);
}

.sf-trust-earnable__baseline-title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 2px;
  color: var(--color-slate-900, #0f172a);
  font-size: 13px;
  font-weight: 600;
  line-height: 20px;
}

.dark .sf-trust-earnable__baseline-title {
  color: var(--color-zinc-100, #f4f4f5);
}

.sf-trust-earnable__baseline-icon {
  width: 14px;
  height: 14px;
  flex: 0 0 auto;
}

.sf-trust-earnable__baseline-text {
  color: var(--color-slate-600, #475569);
  font-size: 13px;
  line-height: 20px;
}

.dark .sf-trust-earnable__baseline-text {
  color: var(--color-zinc-300, #d4d4d8);
}

.sf-trust-earnable__alert {
  margin-top: 12px;
}

.sf-trust-earnable__reason {
  margin-top: 4px;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 11px;
}

.sf-trust-earnable__retry {
  margin-top: 8px;
}

/* 锁定/加载占位：min-height 固定，失败与加载不产生布局跳动。 */
.sf-trust-earnable__locked {
  display: flex;
  min-height: 180px;
  margin-top: 12px;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  border: 1px dashed var(--sf-border, var(--color-slate-200));
  border-radius: 8px;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 12px;
}

.sf-trust-earnable__locked-icon {
  width: 16px;
  height: 16px;
}

/* 列模板由 --sf-earnable-cols 驱动：白名单是动态的，不能把 7 写死在 CSS 里。 */
.sf-trust-earnable__columns {
  display: none;
  grid-template-columns: repeat(var(--sf-earnable-cols, 7), minmax(0, 1fr));
  align-items: end;
  gap: 8px;
  margin-top: 12px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--sf-border, var(--color-slate-200));
}

.sf-trust-earnable__column {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.sf-trust-earnable__column-name {
  color: inherit;
  font-size: 12px;
  font-weight: 600;
  line-height: 16px;
}

.sf-trust-earnable__column-key {
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 11px;
  line-height: 14px;
  overflow-wrap: anywhere;
}

.sf-trust-earnable__row {
  margin: 0;
  padding: 12px 0 0;
  border: 0;
  border-top: 1px solid var(--sf-border, var(--color-slate-200));
  margin-top: 12px;
}

.sf-trust-earnable__row:first-of-type {
  border-top: 0;
  margin-top: 0;
}

.sf-trust-earnable__legend {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  padding: 0;
}

/* 24×24 与阈值行同档（后台行密度优先）。 */
.sf-trust-earnable__icon {
  --tl-accent: var(--sf-accent);

  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: transparent;
  color: var(--sf-fg-tertiary);
}

.dark .sf-trust-earnable__icon {
  --tl-accent: var(--sf-accent-dark);
}

.sf-trust-earnable__glyph {
  width: 14px;
  height: 14px;
}

.sf-trust-earnable__icon[data-level="0"] {
  border-style: dashed;
}

.sf-trust-earnable__icon[data-level="1"] {
  color: var(--sf-fg-secondary);
}

.sf-trust-earnable__icon[data-level="2"] {
  border-color: color-mix(in srgb, var(--tl-accent) 34%, transparent);
  background: var(--sf-accent-soft);
  color: var(--tl-accent);
}

.sf-trust-earnable__icon[data-level="3"] {
  border: 2px solid color-mix(in srgb, var(--tl-accent) 55%, transparent);
  background: var(--sf-accent-soft);
  color: var(--tl-accent);
}

.sf-trust-earnable__icon[data-level="4"] {
  border-color: var(--tl-accent);
  background: var(--tl-accent);
  color: var(--sf-card);
}

.sf-trust-earnable__name {
  color: inherit;
  font-size: 14px;
  font-weight: 600;
}

.sf-trust-earnable__code {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.sf-trust-earnable__tl0 {
  margin-bottom: 8px;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 12px;
}

/* ≤767：2 列 + 格内名；`7 % 2 === 1` 时末项满宽（余数规则，不是奇偶）。 */
.sf-trust-earnable__cells {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.sf-trust-earnable__cells[data-span-two="true"] > :last-child {
  grid-column: span 2;
}

.sf-trust-earnable__cell {
  display: flex;
  min-width: 0;
  min-height: 32px;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
}

.sf-trust-earnable__cell-name {
  color: inherit;
  font-size: 12px;
  line-height: 16px;
  overflow-wrap: anywhere;
}

.sf-trust-earnable__cell :deep([data-slot="base"]) {
  border-radius: 4px;
}

.sf-trust-earnable__cell :deep([data-slot="base"]:focus-visible) {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

/* 768–1279：3 列 + 格内名；`7 % 3 === 1` 时末项满宽。 */
@media (min-width: 768px) {
  .sf-trust-earnable__cells {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .sf-trust-earnable__cells[data-span-two="true"] > :last-child {
    grid-column: auto;
  }

  .sf-trust-earnable__cells[data-span-three="true"] > :last-child {
    grid-column: span 3;
  }
}

/* ≥1280：表头 + 裸勾选框，列数 = 白名单长度；格内名让位给表头。 */
@media (min-width: 1280px) {
  .sf-trust-earnable__columns {
    display: grid;
  }

  .sf-trust-earnable__cells {
    grid-template-columns: repeat(var(--sf-earnable-cols, 7), minmax(0, 1fr));
  }

  .sf-trust-earnable__cells[data-span-two="true"] > :last-child,
  .sf-trust-earnable__cells[data-span-three="true"] > :last-child {
    grid-column: auto;
  }

  .sf-trust-earnable__cell {
    justify-content: center;
    gap: 0;
  }

  .sf-trust-earnable__cell-name {
    display: none;
  }
}

.sf-trust-earnable__empty {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin-top: 12px;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 12px;
  line-height: 16px;
}

.sf-trust-earnable__empty-icon {
  width: 14px;
  height: 14px;
  margin-top: 1px;
  flex: 0 0 auto;
}
</style>
