<script setup lang="ts">
import type { PageResolvePayload } from '~/utils/pageResolve'
import type { PublicFrontendComponentRef } from '~/runtime/public-extensions/pagePolicy'

/**
 * Page Outlet 的同步入口。
 * 普通页面交给异步 resolver；`resolvedPayload` 是保留的同步入口，
 * **当前全仓无调用方**（错误页走 `error.vue → SFSystemErrorPage → SFSystemThemeTemplate`，
 * 不经过本组件），不要以为它在错误渲染路径上被使用。
 */
defineProps<{
  page: string
  resolvedPayload?: PageResolvePayload | null
  /**
   * 页面级 L2 组件 refs（非区域来源，例如资料页 widget tab）。
   *
   * 接线：`SFPageOutletResolver` 路径（常规路由）与 `resolvedPayload` 分支的主题模板渲染
   * （后者当前无调用方）。
   *
   * 已知缺口：`resolvedPayload` + 非模板渲染（host chrome / 纯 slot）没有任何聚合点，
   * 传进来的 refs 会被静默忽略。将来启用该分支、或要给这类页面挂 widget 时，
   * 必须先在本组件补一次聚合，不能只把 refs 透传下去。
   */
  extraL2Refs?: PublicFrontendComponentRef[]
}>()
</script>

<template>
  <SFPageOutletRender
    v-if="resolvedPayload"
    :page="page"
    :resolved="resolvedPayload"
    :region-widget-refs="extraL2Refs"
  >
    <slot />
  </SFPageOutletRender>
  <SFPageOutletResolver v-else :page="page" :extra-l2-refs="extraL2Refs">
    <slot />
  </SFPageOutletResolver>
</template>
