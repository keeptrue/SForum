# 徽章后台 i18n 文案表（`admin.badges.*`）

用途：上一刀（徽章 + 用户活动统计）漏了整个后台命名空间——`apps/web/app` 里引用了
`admin.badges.*`，两份 locale 里**一个定义都没有**，`/control-panel/badges` 的表头、表单、
Toast 全显示裸键。本表给出 zh-CN / en-US 文案，供 frontend 直接落 locale 与核对。
来源：reviewer 的 K-01 证据（`storage/qa/trust-levels/K/logs/K-01-*.txt`）指出缺失；
键清单与出现位置由本文件作者在 2026-11-11 09:28 +08:00 重跑得到（命令见 §1）。
语言文件：**`apps/web/i18n/locales/{zh-CN,en-US}.json`**（不是 `apps/web/app/lang/`——
该路径不存在，见 §4-4）。

## 1. 键数与命令

```bash
rg -o "admin\.badges\.[a-zA-Z0-9_.]+" apps/web/app -N --no-filename | sort -u | wc -l
# → 69

rg -n --no-heading -o "admin\.badges\.[a-zA-Z0-9_.]+" apps/web/app
# → 73 处引用，落在 4 个文件：
#   pages/admin/badges.vue
#   components/admin/badges/SFAdminBadgeTable.vue
#   components/admin/badges/SFAdminBadgeEditDialog.vue
#   components/admin/badges/SFAdminBadgeGrantDialog.vue

python3 -c "
import json
for loc in ('zh-CN', 'en-US'):
    d = json.load(open(f'apps/web/i18n/locales/{loc}.json'))
    print(loc, 'admin.badges keys =', sum(1 for k in d.get('admin', {}) if k == 'badges'))
"
# → zh-CN admin.badges keys = 0
# → en-US admin.badges keys = 0
# 判据只看这一条：两份 locale 的 `admin` 下都没有 `badges` 命名空间（0 个键），
# 而 `admin.*` 本身有数千个键——所以缺的是整个命名空间，不是零星几个。
```

**69 不是最终键数，是「字面出现的前缀数」。** 其中两个是**动态拼接前缀**（末尾带 `.`），
必须按枚举展开才完整：

| 前缀 | 展开来源 | 展开后 |
| --- | --- | --- |
| `admin.badges.grantMode.` | `['automatic','manual']`（`SFAdminBadgeEditDialog.vue:65`） | 2 个键 |
| `admin.badges.metrics.` | `BADGE_METRIC_OPTIONS`（`apps/web/app/utils/admin/adminBadges.ts:57-63`，5 项） | 5 个键 |

所以实际需要定义的是 **67 个字面键 + 7 个动态键 = 74 个键**。下表 74 行，逐键对应。

## 2. 文案表

| key | zh-CN | en-US | 出现位置（文件:行）｜用途 |
| --- | --- | --- | --- |
| `admin.badges.actions.cancel` | 取消 | Cancel | SFAdminBadgeEditDialog.vue:251；SFAdminBadgeGrantDialog.vue:90｜UButton：编辑弹窗与授予弹窗的取消 |
| `admin.badges.actions.delete` | 删除 | Delete | SFAdminBadgeEditDialog.vue:246｜UButton：编辑弹窗左下的删除（未确认态） |
| `admin.badges.actions.disable` | 停用 | Disable | SFAdminBadgeTable.vue:193｜按钮 title：徽章启用时显示 |
| `admin.badges.actions.disableLabel` | 停用徽章 {name} | Disable badge {name} | SFAdminBadgeTable.vue:190｜按钮 aria-label（启用态） |
| `admin.badges.actions.edit` | 编辑 | Edit | SFAdminBadgeTable.vue:171｜按钮 title |
| `admin.badges.actions.editLabel` | 编辑徽章 {name} | Edit badge {name} | SFAdminBadgeTable.vue:170｜按钮 aria-label |
| `admin.badges.actions.enable` | 启用 | Enable | SFAdminBadgeTable.vue:193｜按钮 title：徽章停用时显示 |
| `admin.badges.actions.enableLabel` | 启用徽章 {name} | Enable badge {name} | SFAdminBadgeTable.vue:191｜按钮 aria-label（停用态） |
| `admin.badges.actions.grant` | 授予 | Grant | SFAdminBadgeTable.vue:180｜按钮 title |
| `admin.badges.actions.grantLabel` | 授予徽章 {name} | Grant badge {name} | SFAdminBadgeTable.vue:179｜按钮 aria-label |
| `admin.badges.actions.save` | 保存 | Save | SFAdminBadgeEditDialog.vue:253｜UButton：编辑弹窗提交 |
| `admin.badges.actions.toggleLabel` | 切换徽章 {name} 的启用状态 | Toggle badge {name} on or off | SFAdminBadgeTable.vue:158｜表格内 USwitch 的 aria-label |
| `admin.badges.columns.actions` | 操作 | Actions | SFAdminBadgeTable.vue:76｜表头 |
| `admin.badges.columns.enabled` | 启用 | Enabled | SFAdminBadgeTable.vue:71｜表头 |
| `admin.badges.columns.grantMode` | 授予方式 | Grant mode | SFAdminBadgeTable.vue:56｜表头 |
| `admin.badges.columns.grantedCount` | 已授予 | Granted | SFAdminBadgeTable.vue:66｜表头 |
| `admin.badges.columns.group` | 分组 | Group | SFAdminBadgeTable.vue:46｜表头 |
| `admin.badges.columns.name` | 名称 | Name | SFAdminBadgeTable.vue:41｜表头 |
| `admin.badges.columns.rule` | 规则 | Rule | SFAdminBadgeTable.vue:61｜表头 |
| `admin.badges.columns.tier` | 等级 | Tier | SFAdminBadgeTable.vue:51｜表头 |
| `admin.badges.confirmDeleteBody` | 删除后无法恢复。已授予该徽章的记录会一并删除，用户资料页的最高徽章也会被清空。 | This cannot be undone. Grants of this badge are deleted and the top badge on user profiles is cleared. | SFAdminBadgeEditDialog.vue:233｜删除二次确认的说明行 |
| `admin.badges.confirmDeleteTitle` | 确认删除 | Confirm delete | SFAdminBadgeEditDialog.vue:246｜删除按钮的第二次点击文案 |
| `admin.badges.create` | 新建徽章 | New badge | badges.vue:130｜UButton：页头右上 |
| `admin.badges.disabled` | 徽章功能已关闭。请在「站点设置 → 产品开关」中开启后再管理。 | Badges are turned off. Turn them on under Settings → Feature flags to manage them. | badges.vue:164｜UAlert：开关关闭（`forum.badges_disabled`） |
| `admin.badges.empty` | 还没有徽章。 | No badges yet. | SFAdminBadgeTable.vue:99｜UTable 空态 |
| `admin.badges.forbidden` | 你没有查看徽章后台的权限。 | You do not have permission to view the badge admin. | badges.vue:172｜UAlert：缺 `badge.view` |
| `admin.badges.form.createTitle` | 新建徽章 | New badge | SFAdminBadgeEditDialog.vue:52｜弹窗标题（新建） |
| `admin.badges.form.description` | 说明 | Description | SFAdminBadgeEditDialog.vue:182｜UFormField 标签 |
| `admin.badges.form.editTitle` | 编辑徽章 | Edit badge | SFAdminBadgeEditDialog.vue:52｜弹窗标题（编辑） |
| `admin.badges.form.enabled` | 启用 | Enabled | SFAdminBadgeEditDialog.vue:217；SFAdminBadgeEditDialog.vue:218｜UFormField 标签 + USwitch aria-label |
| `admin.badges.form.grantMode` | 授予方式 | Grant mode | SFAdminBadgeEditDialog.vue:214｜UFormField 标签 |
| `admin.badges.form.group` | 分组 | Group | SFAdminBadgeEditDialog.vue:187｜UFormField 标签 |
| `admin.badges.form.icon` | 图标 | Icon | SFAdminBadgeEditDialog.vue:195｜UFormField 标签 |
| `admin.badges.form.metric` | 指标 | Metric | SFAdminBadgeEditDialog.vue:223｜UFormField 标签（仅自动徽章） |
| `admin.badges.form.name` | 名称 | Name | SFAdminBadgeEditDialog.vue:178｜UFormField 标签 |
| `admin.badges.form.saveFailed` | 徽章保存失败，请稍后重试。 | The badge could not be saved. Try again in a moment. | SFAdminBadgeEditDialog.vue:134｜无字段级错误时的兜底错误行 |
| `admin.badges.form.slug` | 标识 | Slug | SFAdminBadgeEditDialog.vue:171｜UFormField 标签 |
| `admin.badges.form.slugHint` | 只保留小写字母、数字与连字符；下划线与空格会转成连字符。编辑时该字段锁定。 | Lowercase letters, digits, and hyphens only; underscores and spaces become hyphens. The field is locked while editing. | SFAdminBadgeEditDialog.vue:174｜标识字段 hint |
| `admin.badges.form.threshold` | 阈值 | Threshold | SFAdminBadgeEditDialog.vue:226｜UFormField 标签（仅自动徽章） |
| `admin.badges.form.tier` | 等级 | Tier | SFAdminBadgeEditDialog.vue:190｜UFormField 标签 |
| `admin.badges.grant.automaticNotGrantable` | 自动徽章按规则判定，不能手动授予。 | Automatic badges are granted by rules and cannot be granted manually. | SFAdminBadgeGrantDialog.vue:85｜授予弹窗的警示行：徽章是自动模式时显示（**键名已由 `grant.alreadyGranted` 改来**，见 §4-2） |
| `admin.badges.grant.failed` | 徽章授予失败，请稍后重试。 | The badge could not be granted. Try again in a moment. | SFAdminBadgeGrantDialog.vue:56｜无字段级错误时的兜底错误行 |
| `admin.badges.grant.reason` | 授予理由 | Reason | SFAdminBadgeGrantDialog.vue:82｜UFormField 标签 |
| `admin.badges.grant.reasonPlaceholder` | 可选，例如：活动奖励 | Optional, e.g. event reward | SFAdminBadgeGrantDialog.vue:83｜理由输入框 placeholder |
| `admin.badges.grant.submit` | 授予 | Grant | SFAdminBadgeGrantDialog.vue:92｜UButton：授予弹窗提交 |
| `admin.badges.grant.title` | 授予徽章 | Grant badge | SFAdminBadgeGrantDialog.vue:69｜弹窗标题 |
| `admin.badges.grant.user` | 用户 | User | SFAdminBadgeGrantDialog.vue:74｜UFormField 标签 |
| `admin.badges.grant.userPlaceholder` | 用户名 | Username | SFAdminBadgeGrantDialog.vue:78｜用户输入框 placeholder |
| `admin.badges.grantMode.automatic` | 自动 | Automatic | SFAdminBadgeEditDialog.vue:67（`grantMode.${mode}`）+ SFAdminBadgeTable.vue:134（`grantMode.${row.grantMode}`） |
| `admin.badges.grantMode.manual` | 手动 | Manual | 同上（`manual` 是后端 `grantMode` 的另一个取值） |
| `admin.badges.intro` | 管理徽章目录与授予规则。自动徽章按活跃度指标判定，手动徽章由管理员授予。 | Manage the badge catalog and grant rules. Automatic badges are evaluated from activity metrics; manual badges are granted by an admin. | badges.vue:127｜页头副标题 |
| `admin.badges.loadFailed` | 徽章列表加载失败。 | Badges could not be loaded. | badges.vue:180｜UAlert：列表请求失败 |
| `admin.badges.metrics.bookmark_count` | 收藏数 | Bookmarks | 同上 |
| `admin.badges.metrics.comment_count` | 回复数 | Replies | 同上 |
| `admin.badges.metrics.days_visited` | 访问天数 | Days visited | 同上 |
| `admin.badges.metrics.like_received_count` | 获赞数 | Likes received | 同上 |
| `admin.badges.metrics.topic_count` | 主题数 | Topics | SFAdminBadgeEditDialog.vue:72（`metrics.${metric}`），选项集来自 `utils/admin/adminBadges.ts:57-63` |
| `admin.badges.recompute.action` | 重算授予 | Recompute grants | badges.vue:153｜UButton：工具栏（未确认态） |
| `admin.badges.recompute.confirm` | 确认重算 | Confirm recompute | badges.vue:153｜UButton：工具栏（第一次点击后） |
| `admin.badges.refresh` | 刷新 | Refresh | badges.vue:143｜UButton：工具栏 |
| `admin.badges.rule.metricThreshold` | {metric} 达到 {threshold} 时授予 | Granted when {metric} reaches {threshold} | SFAdminBadgeTable.vue:141｜规则列：自动徽章（`{metric}` 目前传的是原始 slug，见 §4-1） |
| `admin.badges.rule.none` | 无规则（手动授予） | No rule (manual) | SFAdminBadgeTable.vue:147｜规则列：手动徽章 |
| `admin.badges.title` | 徽章 | Badges | SFAdminBadgeTable.vue:100；badges.vue:125｜页头 H1 + 表格 caption |
| `admin.badges.toast.created` | 徽章已创建。 | Badge created. | SFAdminBadgeEditDialog.vue:126｜Toast 成功 |
| `admin.badges.toast.deleteFailed` | 徽章删除失败，请稍后重试。 | The badge could not be deleted. Try again in a moment. | SFAdminBadgeEditDialog.vue:155｜错误行：删除失败 |
| `admin.badges.toast.deleted` | 徽章已删除。 | Badge deleted. | SFAdminBadgeEditDialog.vue:151｜Toast 成功 |
| `admin.badges.toast.disabled` | 徽章已停用。 | Badge turned off. | badges.vue:82｜Toast 成功：切换为停用 |
| `admin.badges.toast.enabled` | 徽章已启用。 | Badge turned on. | badges.vue:82｜Toast 成功：切换为启用 |
| `admin.badges.toast.granted` | 徽章已授予该用户。 | Badge granted to the user. | SFAdminBadgeGrantDialog.vue:49｜Toast 成功：授予 |
| `admin.badges.toast.recomputeFailed` | 徽章重算失败，请稍后重试。 | Badges could not be recomputed. Try again in a moment. | badges.vue:111｜Toast 错误 |
| `admin.badges.toast.recomputeStarted` | 已开始重算徽章授予。 | Recomputing badge grants. | badges.vue:104｜Toast 成功标题（description 目前直接塞数字，见 §4-3） |
| `admin.badges.toast.saveFailed` | 徽章保存失败，请稍后重试。 | The badge could not be saved. Try again in a moment. | badges.vue:88｜Toast 错误：启用/停用切换失败 |
| `admin.badges.toast.updated` | 徽章已更新。 | Badge updated. | SFAdminBadgeEditDialog.vue:123｜Toast 成功：编辑保存 |
| `admin.badges.toolbar` | 全部徽章 | All badges | badges.vue:138｜工具栏左侧文案（图标 + 文本），下方是完整列表 |
## 3. 风格约定（与既有后台文案对齐）

- zh-CN：短名词做表头/标签（「名称」「授予方式」）；按钮用动词（「刷新」「保存」「授予」）；
  Toast 成功句以「。」收尾（「徽章已更新。」）；失败句统一「X 失败，请稍后重试。」
  （对照 `admin.roles.*` / `admin.users.*` 的既有写法）。
- en-US：sentence case（`Grant mode`、`No badges yet.`）；失败句「The badge could not be
  saved. Try again in a moment.」与 `admin.roles.saveFailed` / `admin.users.saveAccountFailed` 同构。
- 无 emoji、无手写图标字符；`actions.*` 里的 `{name}` 是 vue-i18n 插值，会替换成徽章名称，
  只用于 `aria-label` / `title`，所以是完整句子而不是单词。
- 所有文案只描述代码里真实存在的控件与行为：例如「编辑时该字段锁定」对应
  `SFAdminBadgeEditDialog.vue:172` 的 `:disabled="isEdit"`；「已授予该徽章的记录会一并删除」
  对应 `user_badges.badge_id ... ON DELETE CASCADE`
  （`database/migrations/202611100002_badges_and_user_activity.sql:92`）。

## 4. 落 locale 前必须知道的三处代码问题（不是文案问题）

**4-1 `rule.metricThreshold` 的 `{metric}` 传的是原始 slug。**
`SFAdminBadgeTable.vue:141-144` 把 `ruleOf(row.original)?.metric` 直接塞进插值，而
`adminBadgeRule()`（`utils/admin/adminBadges.ts:28-35`）返回的就是 `topic_count` 这种后端值。
按本表文案渲染会得到「topic_count 达到 5 时授予」。修法（frontend 侧一行）：
把该参数改成 `t('admin.badges.metrics.' + metric)` 再传入，或让 `adminBadgeRule` 返回 i18n 键。
**在那之前，`rule.metricThreshold` 的文案只是占位正确的形状，渲染仍是 slug。**

**4-2 旧键 `grant.alreadyGranted` 已改名为 `grant.automaticNotGrantable`（已落地，2026-11-11 09:40）。**
旧键名与实际用途不符：它只在**徽章是自动模式**时显示（`SFAdminBadgeGrantDialog.vue:85` 的
`v-if="!manual"`），语义是「自动徽章按规则判定，不能手动授予」，与「已授予」毫无关系——
叫 `alreadyGranted` 会让下一个人以为它是「该用户已经拿到这个徽章」的提示，从而写错文案
（甚至有人会顺手把 `v-if` 反过来「修」成已授予态）。
现名 `automaticNotGrantable` 直接说出条件与结论，`v-if="!manual"` 与它对得上。
**本表与 locale 已同步**（`apps/web/i18n/locales/{zh-CN,en-US}.json` 的
`admin.badges.grant.automaticNotGrantable`），照旧名补键会被 frontend 的 `extra` 断言打红。

**4-3 `toast.recomputeStarted` 的 description 是一个裸数字。**
`pages/admin/badges.vue:103-108` 把 `changed` 直接 `String(changed)` 当 description，
渲染出来是「已开始重算徽章授予。 / 12」。建议补一个键
`admin.badges.toast.recomputeChanged` = zh「本次影响 {count} 个用户。」/
en「{count} user(s) affected.」，并传 `{ count: changed }`。
本表没有把这个键算进 74 个（它现在不存在，写进表就成了第二份真值）。

**4-4 语言文件路径。** 实际是 `apps/web/i18n/locales/{zh-CN,en-US}.json`；
`apps/web/app/lang/` 不存在（`ls apps/web/app/lang/` 报 No such file or directory）。
`AGENTS.md` 与派工说明里的旧路径已过期，落键时按真实路径写。

### 4-5 可执行断言建议（给 reviewer 直接抄）

三条代码问题的共同形状是「文案写对了、渲染出来还是错的」，所以断言必须落在**渲染结果**上，
不能只断言 locale 里存在该键。落点建议统一放 `apps/web/tests/admin/adminBadgesI18n.test.ts`
（该目录是扁平命名，参考 `adminUserFieldErrors.test.ts` / `adminSurfaces.test.ts` 的写法）。

| 针对 | 断言（可执行） | 变异方向（先证明会红） |
| --- | --- | --- |
| 4-1 `{metric}` 传原始 slug | 挂载表格，构造 `{ grantMode: 'automatic', metric: 'topic_count', threshold: 5 }` 一行，取规则单元格 `innerText`：**不得**命中 `/topic_count|comment_count|like_received_count|bookmark_count|days_visited/`，且**必须**命中本地化值（zh 断言含「主题数」） | 把 `t('admin.badges.metrics.' + metric)` 改回 `metric` → 断言必红 |
| 4-2 `grant.automaticNotGrantable`（旧名 `alreadyGranted`） | ① 静态：两份 locale 该键的值不得命中 `/已授予|already granted/i`；② 行为：`manual: false` 时警示行可见且提交按钮 `disabled`，`manual: true` 时该行不存在 | 把文案改回「已授予」→ ①红；把 `v-if="!manual"` 改成 `v-if="manual"` → ②红 |
| 4-3 `toast.recomputeStarted` 裸数字 | mock `recompute()` 返回 `7`，捕获 `toast.add` 入参，断言 `String(description)` **不匹配** `/^\d+$/`；若采纳 §4-3 的补键方案，则改为断言 `title + description` 命中 `/\d+\s*(个用户|user)/` | 把 `description` 改回 `String(changed)` → 断言必红 |
| 4-4 旧路径 | `fs.existsSync('apps/web/i18n/locales/zh-CN.json')` 为真；`rg -n "apps/web/app/lang" apps packages docs` 零命中（**排除 `AGENTS.md`**——仓库级约定文件里仍有该旧路径，属本刀范围外，见 §6） | 把任一引用改回旧路径 → 红 |

注意 4-1 的断言不能只查「等于某个常量」：那会退化成自证（测试断言自己的实现）。
必须断言**渲染文本里不出现后端枚举字面量**，这是唯一不依赖实现细节的判据。

## 5. TODO(待确认)

- `admin.badges.toolbar`（**判断，不是事实**）：已按 lead 要求重读 `pages/admin/badges.vue:133-145`
  的模板——`UDashboardToolbar` 的 `#left` 只有 `<UIcon name="i-lucide-award">` + 一个
  `<span>{{ t('admin.badges.toolbar') }}</span>`，`#right` 是「刷新」与「重算授予」两个按钮，
  **没有任何输入控件**；再对整页 `grep -nE "USelect|UInput|USwitch|filter|search"` **零命中**，
  说明这页没有筛选器，这一行不可能是「筛选」的标题。所以「全部徽章 / All badges」成立：
  它是表格上方的集合标签，下方紧接 `SFAdminBadgeTable` 的完整列表（`pages/admin/badges.vue:183`）。
  **仍保留 TODO 性质**：文案本身是从控件结构推出来的，不是代码里写明的意图；
  若设计要它表达别的（例如「徽章目录」），改这一行即可。
- （`admin.badges.disabled` 的入口页名已核实，不在此列：`features.badges` 的开关在
  `/control-panel/settings/features`，页面名 `admin.features.title` = 「产品开关 / Feature flags」
  （`adminModules.ts:148-154`）。）

## 6. 文档漂移（本刀不改，登记）

`AGENTS.md` 的 Stack 节仍写「`apps/web/app/lang/{zh-CN,en-US}.json`」，真实路径是
`apps/web/i18n/locales/{zh-CN,en-US}.json`。**本刀不改**：`AGENTS.md` 是仓库级共享约定文件，
改它超出本刀范围且会改变所有人的工作习惯；登记进 handoff 的「文档漂移」清单即可。
本表 §4-4 与 §4-5 的断言也按此口径：只约束 `apps/`、`packages/`、`docs/`，不约束 `AGENTS.md`。
