<script setup lang="ts">
import { useActiveThemeSettings } from '~/composables/themes/useActiveThemeSettings'
import SFAnnouncementBanner from './notifications/SFAnnouncementBanner.vue'
import SFContentColumnFooter from './forum/SFContentColumnFooter.vue'
import SFNavbar from './SFNavbar.vue'

/**
 * 宿主公开页 chrome。
 *
 * Page Registry 插件公开页（主题未声明 L1 外壳）与 core 回退路径共用此处：
 * navbar / 页脚与宿主文件路由页面同一组件族（页脚走 SFContentColumnFooter，
 * 与 settings 页同形），只保证恰好一份 chrome，不做主题级定制。
 *
 * 几何契约：`__column` 是公开内容列的几何拥有者，与主题壳共用同一条
 * `--sf-public-container` 轨道和同一档 `--sf-public-edge-inset` 边距。页脚组件在
 * 桌面端用 -24px 负边距去贴内容列边沿，所以它必须和 slot 处在同一个带内边距的
 * 列里——脱离内容列时那对负边距会直接把页面撑出视口。
 * 自己声明三栏布局的页面（`.sforum-home` 等）仍由自身 layout 拥有几何，此时列
 * 退化为透明容器，避免出现双重内缩。
 */
const { layoutShowFooter, layoutShowAnnouncements } = useActiveThemeSettings()
</script>

<template>
  <div
    class="sf-host-public-chrome sf-host-public-chrome--fullwidth-3col"
    data-host-chrome="public"
    data-layout="fullwidth-3col"
  >
    <SFNavbar layout="fullwidth-3col" />
    <SFAnnouncementBanner v-if="layoutShowAnnouncements" />
    <div class="sf-host-public-chrome__body">
      <div class="sf-host-public-chrome__column">
        <slot />
        <SFContentColumnFooter v-if="layoutShowFooter" class="sf-host-public-chrome__footer" />
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 纵列外壳：页脚靠 margin-top:auto 贴底，移动端也成立（桌面端全局规则兜同一条链）。 */
.sf-host-public-chrome {
  display: flex;
  flex-direction: column;
}

.sf-host-public-chrome__body {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  min-width: 0;
}

/* 公开内容列：1200 轨道 + 边距，与主题壳同一条几何。 */
.sf-host-public-chrome__column {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  width: 100%;
  max-width: var(--sf-public-container, 1200px);
  min-width: 0;
  margin-inline: auto;
  padding-inline: 12px;
  box-sizing: border-box;
}

/* 页面自己拥有三栏几何时，列只做透明容器：内缩仍归页面 layout，不能叠加第二次。 */
.sf-host-public-chrome__column:has(
  > :is(
    .sforum-home,
    .sforum-topic-page,
    .sforum-topic-composer,
    .sforum-settings,
    .sforum-notifications,
    .sforum-moderation,
    [data-layout="fullwidth-3col"]
  )
) {
  max-width: none;
  padding-inline: 0;
}

@media (min-width: 981px) {
  .sf-host-public-chrome__column {
    padding-inline: var(--sf-public-edge-inset, 24px);
  }
}

/* 移动端底栏（60px，position:fixed）的安全区由全局 sforum-theme.css 统一提供，
   主题壳与回退壳共用同一条规则，这里不再重复声明。 */

/* 页面正文已自带内容列页脚（如插件页的 sf-content-column-footer 岛）时，
   chrome 级页脚退位，保证「每个渲染路径恰好一个可见页脚」。 */
.sf-host-public-chrome--fullwidth-3col:has(.sforum-settings .sforum-content-footer)
  .sf-host-public-chrome__footer {
  display: none;
}
</style>
