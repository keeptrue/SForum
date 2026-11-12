/**
 * 徽章编辑弹窗里**有专属展示位**的字段错误键。
 *
 * 后端 422 的 `data.fields` 可能点名任何列，包括已经没有控件的 legacy 列（`metric` /
 * `threshold`）。有落点的键由各自的 `UFormField` 显示；不在本清单里的键会被合并进
 * 弹窗的通用错误提示——静默丢掉一条 422 等于让运营对着「没反应」的保存按钮猜。
 *
 * 清单必须与模板里的 `fieldErrors.<key>` 逐一对应（含 Section 渲染的 `rule`）；
 * `adminBadgeEditDialogRule.test.ts` 有一条静态一致性测试从 SFC 源码比对，多一个少一个都红。
 *
 * 放在独立模块而不是弹窗 SFC 里：SFC 的具名导出既不会被 Vue 编译保留，也无法被
 * 测试直接 import（测试只能拿到编译后的组件对象）。
 */
export const FIELD_SLOT_KEYS: ReadonlySet<string> = new Set([
  'slug',
  'name',
  'description',
  'groupId',
  'tier',
  'icon',
  'grantMode',
  'rule'
])
