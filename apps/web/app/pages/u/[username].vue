<script setup lang="ts">
import SFProfileShowPage from '~/components/profile/SFProfileShowPage.vue'
import { useProfileShowData } from '~/composables/profile/useProfileShowData'
import { collectProfileWidgetRefs } from '~/utils/profile/profileExtensionTab'
/** forum.profile.show 路由壳：outlet + fail-closed；呈现由主题 L1 + SFProfileShowPage 岛拥有。
 *  本壳是 profile 的唯一取数点（岛 inject 同一份 refs），只为 SSR 前聚合 widget tab 的 CSP。 */
definePageMeta({ public: true })
const route = useRoute()
const username = computed(() => String(route.params.username ?? ''))
const { profile } = await useProfileShowData(username)
const extraL2Refs = computed(() => collectProfileWidgetRefs(profile.value))
</script>

<template>
  <SFPageOutlet page="forum.profile.show" :extra-l2-refs="extraL2Refs">
    <SFProfileShowPage />
  </SFPageOutlet>
</template>
