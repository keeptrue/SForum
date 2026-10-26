import { inject, provide, ref, shallowRef, type InjectionKey, type Ref } from 'vue'
import { useProfileApi, type PublicProfile } from '~/composables/profile/useProfileApi'

/**
 * 资料页 profile 的唯一取数点。
 *
 * 路由壳 `pages/u/[username].vue` 需要 profile 才能在 SSR 前聚合 widget tab 的页面 CSP，
 * 主题岛 `SFProfileShowPage` 需要同一份 profile 渲染正文。两边都调 `useAsyncData`
 * 会踩两个坑：同 key 第二次调用在 `payloadExtraction: false` 下读不到 payload（会重复请求），
 * 且两份 options 不同会在 dev 打出 `Incompatible options detected`。
 *
 * 所以改成「壳创建 + provide，岛 inject」：这个 key 全仓只有一处 `useAsyncData`，
 * 自然没有第二跳、没有 options 哈希差异，也不需要给任何一侧塞自定义 `getCachedData`
 * （那会让 Nuxt 的 `hasCustomGetCachedData` 为真、关掉 payload purge）。
 */
export type ProfileShowData = {
  profile: Ref<PublicProfile | null>
  pending: Ref<boolean>
  error: Ref<unknown>
}

export const PROFILE_SHOW_DATA: InjectionKey<ProfileShowData> = Symbol('sforum.profile-show-data')

/**
 * 首次调用（路由壳）创建并 provide；下游岛再次调用时直接拿到同一份 refs，不再取数。
 * 壳必须 await 到数据：`extraL2Refs` 要在 outlet 渲染前就位，否则 CSP 聚合会拿到空集合。
 */
export function useProfileShowData(username: Ref<string>): ProfileShowData | Promise<ProfileShowData> {
  const provided = inject(PROFILE_SHOW_DATA, null)
  if (provided) {
    return provided
  }
  return createProfileShowData(username)
}

async function createProfileShowData(username: Ref<string>): Promise<ProfileShowData> {
  const profileApi = useProfileApi()
  // provide 必须发生在第一个 await 之前：Vue 的 setup 上下文在 await 之后不再可靠
  // （并发的 async setup 会互相覆盖 currentInstance）。
  // 先 provide 占位容器，取数完成后再把 useAsyncData 的真实 refs 填进去——
  // 岛只会在壳 await 结束之后渲染，读到的永远是填好的那一份。
  const data: ProfileShowData = {
    profile: shallowRef<PublicProfile | null>(null),
    pending: ref(true),
    error: shallowRef<unknown>(undefined)
  }
  provide(PROFILE_SHOW_DATA, data)
  const asyncData = await useAsyncData(
    () => `forum-profile-${username.value}`,
    () => profileApi.getPublicProfile(username.value),
    { default: () => null as PublicProfile | null }
  )
  data.profile = asyncData.data
  data.pending = asyncData.pending
  data.error = asyncData.error
  return data
}
