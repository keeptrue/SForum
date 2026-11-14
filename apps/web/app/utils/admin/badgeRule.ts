/**
 * **兼容层**：实现已迁到域无关的 `rule.ts`（徽章与信任等级共用的规则编辑器）。
 *
 * 保留本文件是为了让尚未迁移的调用方继续 `import ... from '~/utils/admin/badgeRule'`，
 * 避免把一次文件移动变成几十处 import 的大 diff。新代码请直接 import `~/utils/admin/rule`；
 * 本文件不再承载任何实现，也不要在这里加「顺手搬一点」的新逻辑。
 */
export * from './rule'
