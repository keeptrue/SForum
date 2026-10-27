import {
  resolveForumHomeGreetingBucket,
  type ForumHomeGreetingBucket
} from '~/utils/forum/forumHomeGreeting'

/** 分档 → i18n key；键名集中写死，避免动态拼接出拼写错误。 */
const GREETING_KEYS = {
  morning: 'home.rightRail.greeting.morning',
  noon: 'home.rightRail.greeting.noon',
  afternoon: 'home.rightRail.greeting.afternoon',
  evening: 'home.rightRail.greeting.evening',
  lateNight: 'home.rightRail.greeting.lateNight'
} as const satisfies Record<ForumHomeGreetingBucket, string>

const REFRESH_INTERVAL_MS = 60_000

/**
 * 首页右栏问候语：SSR 与首帧按站点时区，挂载后改用访客本地时区。
 *
 * 首帧时间戳由 useState 固定，服务端值随 payload 下发，因此水合文本与 SSR 一致；
 * 访客时区只能在挂载后读到，晚一步修正不会产生 hydration mismatch。
 */
export function useForumHomeGreeting() {
  const { te } = useI18n()
  const { timezone } = useSiteDateTime()
  const now = useState<number>('forum-home-greeting-now', () => Date.now())
  // null = 尚未拿到访客时区（SSR/首帧），继续按站点时区计算。
  const visitorTimeZone = ref<string | null>(null)
  let timer: ReturnType<typeof setInterval> | null = null

  onMounted(() => {
    now.value = Date.now()
    visitorTimeZone.value = Intl.DateTimeFormat().resolvedOptions().timeZone || null
    timer = setInterval(() => {
      now.value = Date.now()
    }, REFRESH_INTERVAL_MS)
  })
  onBeforeUnmount(() => {
    if (timer) {
      clearInterval(timer)
    }
  })

  const bucket = computed<ForumHomeGreetingBucket>(() =>
    resolveForumHomeGreetingBucket(now.value, visitorTimeZone.value ?? timezone.value)
  )

  const greetingKey = computed(() => {
    const key = GREETING_KEYS[bucket.value]
    // 缺键时回退到中性文案，避免把 key 路径渲染到界面。
    return te(key) ? key : 'home.rightRail.welcomeUserTitle'
  })

  return { bucket, greetingKey }
}
