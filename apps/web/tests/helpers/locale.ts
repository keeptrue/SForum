import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * 真实 locale 的读取与查询工具。
 *
 * 组件挂载测试需要一个「真会翻译」的 `useI18n` 替身：只返回 key 的桩会让
 * 「文案是否落地」这类断言变成同义反复（key === key 永远成立）。这里直接读
 * `i18n/locales/*.json`，缺键就抛错，所以挂载级断言看到的就是用户看到的字。
 */
export const LOCALES = ['zh-CN', 'en-US'] as const
export type LocaleName = (typeof LOCALES)[number]

const LOCALES_ROOT = fileURLToPath(new URL('../../i18n/locales/', import.meta.url))

const cache = new Map<LocaleName, Record<string, unknown>>()

export function readLocale(locale: LocaleName): Record<string, unknown> {
  const cached = cache.get(locale)
  if (cached) return cached
  const parsed = JSON.parse(readFileSync(`${LOCALES_ROOT}${locale}.json`, 'utf8')) as Record<string, unknown>
  cache.set(locale, parsed)
  return parsed
}

/** 拍平 key path；嵌套命名空间与扁平 key 一视同仁。 */
export function flattenKeys(node: unknown, prefix = '', out = new Set<string>()): Set<string> {
  if (!node || typeof node !== 'object') return out
  for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      flattenKeys(value, path, out)
    } else {
      out.add(path)
    }
  }
  return out
}

export function lookupMessage(locale: LocaleName, key: string): string | undefined {
  const value = key.split('.').reduce<unknown>(
    (current, part) => (current && typeof current === 'object' ? (current as Record<string, unknown>)[part] : undefined),
    readLocale(locale)
  )
  return typeof value === 'string' ? value : undefined
}

/** vue-i18n 的最小可观测子集：`{name}` 插值；缺键抛错，不静默回退成 key。 */
export function translate(locale: LocaleName, key: string, params: Record<string, unknown> = {}): string {
  const template = lookupMessage(locale, key)
  if (template === undefined) {
    throw new Error(`missing locale key in ${locale}: ${key}`)
  }
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

/** `useI18n()` 的替身：调用方拿到某一份真实 locale 的翻译器。 */
export function useI18nFor(locale: LocaleName) {
  return () => ({
    t: (key: string, params?: Record<string, unknown>) => translate(locale, key, params as Record<string, unknown> | undefined),
    te: (key: string) => lookupMessage(locale, key) !== undefined,
    locale: { value: locale }
  })
}

/** 中日韩统一表意文字：用来断言「英文渲染里不该再出现中文」。 */
export const HAN_PATTERN = /[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]/
