<script setup lang="ts">
import { useLiveRegionState } from '~/utils/a11y/liveRegion'

/**
 * 全局 live region 渲染层（UX2-17）。
 *
 * 由壳层挂载一次：后台壳层负责「路由切换公告 + 各页面的异步结果」。
 * 两个节点都常驻 DOM（不能按需 v-if 插入，读屏不会为后插入的空节点建立监听）。
 */
const { politeMessage, assertiveMessage } = useLiveRegionState()
</script>

<template>
  <div class="sr-only" data-sf-live-region-root>
    <p role="status" aria-live="polite" aria-atomic="true" data-sf-live-region="polite">{{ politeMessage }}</p>
    <p role="alert" aria-live="assertive" aria-atomic="true" data-sf-live-region="assertive">{{ assertiveMessage }}</p>
  </div>
</template>
