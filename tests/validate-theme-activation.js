const fs = require('fs')
const path = require('path')

const root = path.resolve(__dirname, '..')
const themesPagePath = path.join(root, 'apps/web/app/pages/admin/extensions/themes.vue')
const overviewPagePath = path.join(root, 'apps/web/app/pages/admin/extensions/index.vue')
const zhLocalePath = path.join(root, 'apps/web/i18n/locales/zh-CN.json')
const enLocalePath = path.join(root, 'apps/web/i18n/locales/en-US.json')
// 名称与简介由共享身份组件渲染；列表页只负责把 localized display 传下去。
const identityPath = path.join(root, 'apps/web/app/components/admin/extensions/SFAdminExtensionIdentity.vue')

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

const themesPage = fs.readFileSync(themesPagePath, 'utf8')
const extensionIdentity = fs.readFileSync(identityPath, 'utf8')
const themesSurface = `${themesPage}\n${extensionIdentity}`
assert(themesPage.includes('activateTheme(item)'), 'themes page must activate synchronously')
assert(!themesPage.includes('setInterval'), 'themes page must not poll a build pipeline')
assert(!themesPage.includes('UProgress'), 'themes page must not render build progress')
assert(themesPage.includes('extensionLocalizedDisplay'), 'themes list must resolve localized copy')
assert(themesSurface.includes('display.description') && themesSurface.includes('display.name'), 'themes list must show localized name and description')
assert(themesPage.includes('locale.value'), 'themes list must recompute with the UI locale')

const pluginsPage = fs.readFileSync(path.join(root, 'apps/web/app/pages/admin/extensions/plugins.vue'), 'utf8')
assert(pluginsPage.includes('extensionLocalizedDisplay'), 'plugins list must resolve localized copy')
assert(`${pluginsPage}\n${extensionIdentity}`.includes('display.description') && `${pluginsPage}\n${extensionIdentity}`.includes('display.name'), 'plugins list must show localized name and description')
assert(pluginsPage.includes('locale.value'), 'plugins list must recompute with the UI locale')

const overviewPage = fs.readFileSync(overviewPagePath, 'utf8')
assert(overviewPage.includes('activateTheme(item)'), 'extensions overview must activate synchronously')
assert(!overviewPage.includes('setInterval'), 'extensions overview must not poll a build pipeline')
assert(!overviewPage.includes('UProgress'), 'extensions overview must not render build progress')
assert(overviewPage.includes('extensionLocalizedDisplay'), 'extensions overview must resolve localized copy')

for (const localePath of [zhLocalePath, enLocalePath]) {
  const locale = fs.readFileSync(localePath, 'utf8')
  assert(!locale.includes('"themeProgress"'), `${path.basename(localePath)} must not define build progress copy`)
}

console.log('Synchronous theme activation validation passed.')
