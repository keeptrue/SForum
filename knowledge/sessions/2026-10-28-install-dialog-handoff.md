# 2026-10-28 安装弹窗收尾：信任入口按需出现、跳转前先卸载

**起因**：商城（`sforum.store`）的「安装并启用」在真机上装完一个**早已信任**的插件后，
宿主弹窗仍是「已安装并升级」，底部却还挂着「去插件页完成信任与启用」；点它跳到插件页时，
弹窗也不一定跟着消失。

**范围**：`bridge.installExtension` / `bridge.installAndEnableExtension` 的宿主弹窗
（`SFAdminPageInstallDialog.vue` + `useAdminPageHostInstall.ts`）。
**不在范围**：商城侧详情弹窗（见 `sforum-app-store` 的 `2026-10-28-store-dialog-handoff.md`）、
信任判定本身、`SFAdminExtensionInstallPanel`（非 bridge 流程）。

## 1 按钮 = 剩余动作，不是包的类型

`managementKey` 原先只判 `result.type`，与 `outcome` / `stagedReason` 无关，于是「已经在跑」
和「还要人去信任」共用一句话。现在换成 `managementAction`（返回 `{ key } | null`）：

| 结果 | 按钮 |
| --- | --- |
| `enabled` / `upgraded` | **不渲染**，只剩「关闭」 |
| `staged` + `theme` | `openThemes`「去主题页完成激活」 |
| `staged` + `super_admin` / `declined` | `openPlugins`「去插件页完成信任与启用」 |
| `staged` + `stage`（旧宿主只暂存） | `openPluginsEnable`「去插件页启用」（新增 key） |

规则：按钮存在当且仅当管理页还有事要做；只有信任确实待办时文案才出现「信任」。
旧宿主只暂存时，装完之前说不清制品是否已信任（`requiresTrust` 只表示「有可执行后端」），
所以只说启用。

## 2 跳转前必须先卸载

`openManagement()` 原来是 `reset()` 紧接着 `navigate()`。`reset()` 只改 ref，DOM 要到
Vue 的渲染微任务才更新；而动态扩展页是 **KeepAlive 缓存**的（`app.vue` 的
`:keepalive="{ include: cachedTabNames }"`，`[...pagePath].vue` 注册
`AdminExtensionDynamicPage`），弹窗又 teleport 到 body —— 同帧换页时遮罩与 body 上的焦点锁
有概率留在新页面上。

现在：`reset()` → `await nextTick()` → `navigate()`。另外补 `dismissOnDeactivate()`：
页面被 KeepAlive 停用（切 tab / 换路由）时按 `close()` 的同一套语义收尾（信任审阅没确认
算「已暂存」、其余算取消），进行中的安装不动作 —— 那条 promise 由 unmount 的
`abortOnUnmount` 收尾。由 `SFTrustedAdminPageComponent` 的 `onDeactivated` 调用。

## 3 焦点归还加 `isConnected` 守卫

商城在宿主回报「已在跑」后会关掉自己的详情弹窗（触发元素随之脱离文档），
`previous.focus()` 会静默失败并把焦点丢给 body。只在元素仍挂载时归还。

## 证据

- 源码：`apps/web/app/components/extensions/admin/SFAdminPageInstallDialog.vue`
  （`managementAction`、footer `v-if`、`syncFocus`）、
  `apps/web/app/composables/admin/useAdminPageHostInstall.ts`（`openManagement`、
  `dismissOnDeactivate`）、`apps/web/app/components/extensions/admin/SFTrustedAdminPageComponent.vue`
  （`onDeactivated`）、`apps/web/i18n/locales/{zh-CN,en-US}.json`（`openPluginsEnable`）。
- 用例：`tests/admin/adminPageInstallDialog.test.ts`（running 不渲染管理按钮、四种暂存文案、
  焦点守卫）、`tests/admin/extensionHostInstall.test.ts`（跳转时弹窗已撤、失活收尾、
  失活不取消进行中的安装、信任审阅失活算暂存）。
- `cd apps/web && bun test`：**1809 pass / 213 files，0 fail**；`bun run typecheck` 0。

## 未验证（下一步）

原始现象是用户真机观察，未留 DOM 证据。上面两条机制（跳转同帧、KeepAlive 停用）都能造成
「弹窗留在新页面」，但**哪一条是当时的主因仍未验证**。真机复核步骤：

1. 在 `127.0.0.1:3000` 用「已安装的插件 + 有更新」的产品点一次升级；
2. 成功态应只有「关闭」一枚按钮；
3. 若是暂存态，按钮文案应是「去插件页启用」（旧宿主）或「去插件页完成信任与启用」（超管/拒绝）；
4. 点跳转后，插件页可以立刻点击（`document.body` 无 `pointer-events: none`），
   页面里查不到 `[data-testid="admin-page-install-dialog"]`。
