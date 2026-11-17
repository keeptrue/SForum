import { useApiClient } from '~/composables/useApiClient'

/**
 * 主题新增页面（`GET /admin/pages/added`）。
 *
 * 为什么单独有这条：`/admin/pages` 只遍历 `coreCatalog`，**不含**主题通过
 * `action: add` 贡献的页面；`Support/Pages.Registry.AddedPages()` 只经这个端点暴露。
 * 因此后台要看「主题到底加了哪些路由」只能读这里。只读：这些条目由主题包声明，
 * 编辑入口在主题包里，后台不提供改写。
 */
export type AdminAddedPage = {
  id: string
  action: 'add' | 'replace'
  target?: string
  path?: string
  template?: string
  label?: string
  view?: 'template' | 'component' | 'schema'
  componentId?: string
  schemaDescriptorFile?: string
  schemaContractRef?: string
  contract?: string
  access?: string
  permission?: string
  layout?: 'site' | 'bare'
  dataSource?: 'core' | 'plugin'
  dataRoute?: string
  dataSchema?: string
  extensionId: string
  version: string
  packageDigest?: string
  runtimeInstanceId?: string
  routeSignature?: string
}

export function useAdminAddedPages() {
  const { request } = useApiClient()

  function list() {
    return request<AdminAddedPage[]>('/admin/pages/added')
  }

  return { list }
}
