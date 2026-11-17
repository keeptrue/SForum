// @nuxtjs/i18n 的 useLocaleHead() 把 link / meta 都声明为 Record<string, string>[]
// （内部 nInfo.link / nInfo.meta: n[]，n = Record<string, string>）。unhead v3 的
// ResolvableLink / ResolvableMeta 都是按属性判别的联合类型（link 按 rel，meta 按
// charset / name / property / http-equiv），宽泛的 Record 无法自动收窄，直接塞进
// useHead 就会在调用处报类型错。
//
// 这里按 i18n 实际产出的条目重建对象：运行时值原样保留，只补上判别属性的字面量
// 类型。不用 `as any` 糊过去 —— 那会让 unhead 的 per-rel / per-meta 必填项校验
// （例如 preload 必须有 as）彻底失效。
//
// 现状提醒：app.vue 用 useLocaleHead({ seo: false })（no_prefix 下中英共用 URL，
// 不输出 hreflang 交替链接），因此当前 link / meta 都是空数组，这两个适配器只在
// 未来打开 seo 时才真正产出标签；保留它们是为了不把 i18n 的类型缺口留成坑。
type LocaleHeadEntry = Record<string, string>

function withHref(entries: LocaleHeadEntry[]) {
  return entries.filter((entry): entry is LocaleHeadEntry & { href: string } => Boolean(entry.href))
}

function withContent(entries: LocaleHeadEntry[]) {
  return entries.filter((entry): entry is LocaleHeadEntry & { content: string } => Boolean(entry.content))
}

// i18n 的 head.js 只会产出 rel="alternate"（含 x-default）与 rel="canonical" 两类
// link，其余 rel 不在此转换，保持不注入。
export function localeHeadLinks(links: LocaleHeadEntry[] = []) {
  const candidates = withHref(links)
  const alternates = candidates
    .filter(link => link.rel === 'alternate')
    .map(link => ({
      key: link.key,
      rel: 'alternate' as const,
      href: link.href,
      ...(link.hreflang ? { hreflang: link.hreflang } : {})
    }))
  const canonicals = candidates
    .filter(link => link.rel === 'canonical')
    .map(link => ({
      key: link.key,
      rel: 'canonical' as const,
      href: link.href
    }))
  return [...alternates, ...canonicals]
}

// i18n 的 seo meta 只有 og:url / og:locale / og:locale:alternate，统一是
// { key, property, content }；name 形态一并支持，避免以后加字段时静默丢弃。
export type LocaleHeadMetaEntry =
  | { key: string | undefined, name: string, content: string }
  | { key: string | undefined, property: string, content: string }

export function localeHeadMeta(metas: LocaleHeadEntry[] = []): LocaleHeadMetaEntry[] {
  const entries: LocaleHeadMetaEntry[] = []
  for (const meta of withContent(metas)) {
    if (meta.name) {
      entries.push({ key: meta.key, name: meta.name, content: meta.content })
      continue
    }
    if (meta.property) {
      entries.push({ key: meta.key, property: meta.property, content: meta.content })
    }
  }
  return entries
}
