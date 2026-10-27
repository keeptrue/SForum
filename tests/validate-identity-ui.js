const fs = require('fs');
const path = require('path');

const root = process.cwd();
// V3 presentation ownership：auth 路由壳 + Host body 岛（表单/ALTCHA 在岛上）。
const registerRoutePath = 'apps/web/app/pages/register.vue';
const loginRoutePath = 'apps/web/app/pages/login.vue';
const registerFormPath = 'apps/web/app/components/identity/SFRegisterFormPage.vue';
const loginFormPath = 'apps/web/app/components/identity/SFLoginFormPage.vue';
const adminUsersToolbarPath = 'apps/web/app/components/admin/identity/users/SFAdminUserListToolbar.vue';
// 用户管理已按路由壳 + 领域组件拆分：权限文案、分页、PATCH 保存与客户端 IP
// 清理分别落在 users/ 组件与 admin/identity composable 里，校验必须覆盖真实
// 拥有这些行为的文件，而不是逼着路由壳把实现字符串搬回来。
const adminUserFeatureDir = 'apps/web/app/components/admin/identity/users';
const adminUserFeaturePaths = [
  'apps/web/app/composables/admin/identity/useAdminUserList.ts',
  'apps/web/app/composables/admin/identity/useAdminUserDetailEditor.ts',
  'apps/web/app/composables/admin/identity/useAdminUserSecurity.ts',
  ...fs.readdirSync(path.resolve(root, adminUserFeatureDir))
    .filter(name => name.endsWith('.vue'))
    .sort()
    .map(name => `${adminUserFeatureDir}/${name}`)
];
const adminRoleGroupsTabPath = 'apps/web/app/components/admin/identity/roles/tabs/SFAdminRoleGroupsTab.vue';
const requiredFiles = [
  'apps/web/app/composables/identity/useAuthSession.ts',
  'apps/web/app/middleware/admin.ts',
  registerRoutePath,
  loginRoutePath,
  registerFormPath,
  loginFormPath,
  adminUsersToolbarPath,
  adminRoleGroupsTabPath,
  'apps/web/app/pages/admin/index.vue',
  'apps/web/app/pages/admin/roles.vue',
  'apps/web/app/pages/admin/users.vue',
  'apps/web/app/pages/admin/permissions.vue'
];

for (const file of requiredFiles) {
  if (!fs.existsSync(path.resolve(root, file))) {
    throw new Error(`Missing required identity UI file: ${file}`);
  }
}

const zh = JSON.parse(fs.readFileSync(path.resolve(root, 'apps/web/i18n/locales/zh-CN.json'), 'utf8'));
const en = JSON.parse(fs.readFileSync(path.resolve(root, 'apps/web/i18n/locales/en-US.json'), 'utf8'));
const registerRoute = fs.readFileSync(path.resolve(root, registerRoutePath), 'utf8');
const loginRoute = fs.readFileSync(path.resolve(root, loginRoutePath), 'utf8');
const registerPage = fs.readFileSync(path.resolve(root, registerFormPath), 'utf8');
const loginPage = fs.readFileSync(path.resolve(root, loginFormPath), 'utf8');
const requiredKeys = [
  ['auth', 'registerTitle'],
  ['auth', 'loginTitle'],
  ['admin', 'home', 'title'],
  ['admin', 'roles', 'title'],
  ['admin', 'roles', 'permissionEditor'],
  ['admin', 'users', 'permissionSection'],
  ['admin', 'users', 'overrideMode', 'deny'],
  ['admin', 'users', 'sortByLabel'],
  ['admin', 'users', 'sortOrderLabel'],
  ['admin', 'permissions', 'matrix'],
  ['admin', 'permissions', 'comparisonScope'],
  ['admin', 'permissions', 'roleSearchPlaceholder'],
  ['admin', 'permissions', 'onlyDifferences'],
  ['admin', 'permissions', 'clearFilters'],
  ['admin', 'permissions', 'noDifferences'],
  ['admin', 'permissionModules', 'identity'],
  ['admin', 'permissionModules', 'extension'],
  ['admin', 'permissionModules', 'search'],
  ['admin', 'permissionModules', 'jobs'],
  ['admin', 'permissionCatalog', 'admin', 'access', 'label'],
  ['admin', 'permissionCatalog', 'role', 'manage', 'description'],
  ['admin', 'permissionCatalog', 'post', 'delete_any', 'label'],
  ['admin', 'permissionCatalog', 'tag', 'manage', 'label'],
  ['admin', 'permissionCatalog', 'extension', 'manage', 'label'],
  ['admin', 'permissionCatalog', 'jobs', 'view', 'label'],
  ['admin', 'permissionCatalog', 'jobs', 'manage', 'description'],
  ['admin', 'permissionCatalog', 'moderation', 'manage', 'label'],
  ['admin', 'permissionCatalog', 'moderation', 'review', 'description'],
  ['admin', 'permissionCatalog', 'search', 'manage', 'label'],
  ['admin', 'permissionCatalog', 'identity', 'provider', 'manage', 'label'],
  ['admin', 'permissionCatalog', 'identity', 'provider', 'manage', 'description'],
  ['errors', 'permissionDenied']
];

function valueAt(object, keyPath) {
  return keyPath.reduce((current, key) => current?.[key], object);
}

const adminUsersPage = fs.readFileSync(path.resolve(root, 'apps/web/app/pages/admin/users.vue'), 'utf8');
const adminUsersToolbar = fs.readFileSync(path.resolve(root, adminUsersToolbarPath), 'utf8');
const adminRolesPage = fs.readFileSync(path.resolve(root, 'apps/web/app/pages/admin/roles.vue'), 'utf8');
const adminPermissionsPage = fs.readFileSync(path.resolve(root, 'apps/web/app/pages/admin/permissions.vue'), 'utf8');
const adminRoleGroupsTab = fs.readFileSync(path.resolve(root, adminRoleGroupsTabPath), 'utf8');
const adminRolesSurface = `${adminRolesPage}\n${adminRoleGroupsTab}`;
const adminUsersSurface = [
  adminUsersPage,
  ...adminUserFeaturePaths.map(file => fs.readFileSync(path.resolve(root, file), 'utf8'))
].join('\n');

for (const [name, content] of [
  ['admin/users.vue', adminUsersPage],
  ['admin/roles.vue', `${adminRolesPage}\n${adminRoleGroupsTab}`],
  ['admin/permissions.vue', adminPermissionsPage]
]) {
  if (!content.includes("layout: 'admin'")) {
    throw new Error(`${name} should use the admin layout`);
  }
  if (!content.includes('UDashboardToolbar') && !(name === 'admin/users.vue' && adminUsersToolbar.includes('UDashboardToolbar'))) {
    throw new Error(`${name} should render a dashboard toolbar`);
  }
  if (!content.includes('i-lucide-')) {
    throw new Error(`${name} should use Nuxt Icon lucide icons`);
  }
}

for (const [name, content] of [
  ['admin/users.vue', adminUsersSurface],
  ['admin/roles.vue', `${adminRolesPage}\n${adminRoleGroupsTab}`],
  ['admin/permissions.vue', adminPermissionsPage]
]) {
  if (!content.includes('usePermissionText')) {
    throw new Error(`${name} should localize permission labels and descriptions through usePermissionText`);
  }
  if (content.includes('permission.description ||')) {
    throw new Error(`${name} should not render raw backend permission descriptions directly`);
  }
}

if (!adminUsersSurface.includes('/permission-overrides')) {
  throw new Error('Admin users page should manage per-user permission overrides');
}
if (!adminUsersSurface.includes('DEFAULT_PER_PAGE = 20')) {
  throw new Error('Admin users page should page users 20 per page');
}
if (!adminUsersSurface.includes('UPagination')) {
  throw new Error('Admin users page should render pagination controls');
}
if (!adminUsersSurface.includes("method: 'PATCH'")) {
  throw new Error('Admin users page should PATCH account/profile fields');
}
if (!adminUsersSurface.includes('saveAccount') || !adminUsersSurface.includes('saveProfile')) {
  throw new Error('Admin users page should expose account and profile save actions');
}
if (!adminUsersSurface.includes('client-ips/clear')) {
  throw new Error('Admin users page should support clearing stored client IPs');
}
if (!adminRolesSurface.includes('/permissions')) {
  throw new Error('Admin roles page should manage role permissions');
}
if (!adminRolesSurface.includes(':label="t(\'admin.roles.key\')"') || !adminRolesSurface.includes('name="role-key"')) {
  throw new Error('Admin roles page should show a visible label for the role key field');
}
if (!adminRolesSurface.includes(':label="t(\'admin.roles.alias\')"') || !adminRolesSurface.includes('name="role-alias"')) {
  throw new Error('Admin roles page should show a visible label for the role alias field');
}
if (!adminRolesSurface.includes('validateRoleForm')) {
  throw new Error('Admin roles page should validate required role fields before saving');
}
if (!adminRolesSurface.includes('ROLE_TEMPLATE_DEFINITIONS') || !adminRolesSurface.includes('applyRoleTemplate')) {
  throw new Error('Admin roles page should support applying built-in role permission templates');
}
if (!adminRolesSurface.includes('admin.roles.applyTemplate')) {
  throw new Error('Admin roles page should expose apply-template UI copy');
}
if (!adminPermissionsPage.includes('/permissions/matrix')) {
  throw new Error('Admin permissions page should load the permission matrix');
}
if (!adminPermissionsPage.includes('const ROLE_COMPARE_LIMIT = 5')) {
  throw new Error('Admin permissions page should cap the default role comparison width');
}
if (!adminPermissionsPage.includes('const roleSearch = ref')) {
  throw new Error('Admin permissions page should let admins search user groups before comparing permissions');
}
if (!adminPermissionsPage.includes('selectedRoleKeys')) {
  throw new Error('Admin permissions page should support an explicit user-group comparison selection');
}
if (!adminPermissionsPage.includes('showOnlyDifferences')) {
  throw new Error('Admin permissions page should support a differences-only permission audit mode');
}
if (!adminPermissionsPage.includes('visibleRoles')) {
  throw new Error('Admin permissions page should render the matrix against the filtered comparison role set');
}
if (!adminPermissionsPage.includes('filteredPermissionGroups')) {
  throw new Error('Admin permissions page should filter permission rows when differences-only mode is enabled');
}
if (adminPermissionsPage.includes('v-for="role in roles"')) {
  throw new Error('Admin permissions page should not render every user group as a matrix column by default');
}

for (const keyPath of requiredKeys) {
  if (!valueAt(zh, keyPath)) {
    throw new Error(`Missing zh-CN locale key: ${keyPath.join('.')}`);
  }
  if (!valueAt(en, keyPath)) {
    throw new Error(`Missing en-US locale key: ${keyPath.join('.')}`);
  }
}

// SeedPermissions 是权限目录权威列表；前后端文案必须覆盖每一个 key 与 module。
const seedsSource = fs.readFileSync(
  path.resolve(root, 'apps/api/app/Models/Identity/seeds.go'),
  'utf8'
);

// 内置角色模板：API seeds + migration + 前端 config + i18n 对齐。
const roleTemplatesSource = fs.readFileSync(
  path.resolve(root, 'apps/web/app/config/roleTemplates.ts'),
  'utf8'
);
const expectedTemplateKeys = ['moderator', 'operator', 'tech_admin'];
for (const key of expectedTemplateKeys) {
  if (!roleTemplatesSource.includes(`key: '${key}'`)) {
    throw new Error(`roleTemplates.ts should define built-in template ${key}`);
  }
  for (const locale of [
    ['zh-CN', zh],
    ['en-US', en]
  ]) {
    const [name, rootLocale] = locale;
    if (!valueAt(rootLocale, ['admin', 'roleCatalog', key, 'alias'])) {
      throw new Error(`${name} is missing admin.roleCatalog.${key}.alias`);
    }
    if (!valueAt(rootLocale, ['admin', 'roleCatalog', key, 'description'])) {
      throw new Error(`${name} is missing admin.roleCatalog.${key}.description`);
    }
  }
}
if (!seedsSource.includes('RoleModerator') || !seedsSource.includes('RoleOperator') || !seedsSource.includes('RoleTechAdmin')) {
  throw new Error('seeds.go should declare RoleModerator, RoleOperator, and RoleTechAdmin');
}
if (!seedsSource.includes('SeedRoleTemplates') || !seedsSource.includes('IsBuiltInSystemRole')) {
  throw new Error('seeds.go should export SeedRoleTemplates and IsBuiltInSystemRole');
}
for (const marker of ['RoleModerator', 'RoleOperator', 'RoleTechAdmin']) {
  // gofmt 可能在 Key: 后插入对齐空格。
  if (!new RegExp(`Key:\\s*${marker}\\b`).test(seedsSource)) {
    throw new Error(`SeedRoleTemplates should include ${marker}`);
  }
}
const migrationPath = 'apps/api/database/migrations/202607120002_builtin_role_templates.sql';
if (!fs.existsSync(path.resolve(root, migrationPath))) {
  throw new Error(`Missing built-in role template migration: ${migrationPath}`);
}
const migrationSql = fs.readFileSync(path.resolve(root, migrationPath), 'utf8');
for (const key of expectedTemplateKeys) {
  if (!migrationSql.includes(`'${key}'`)) {
    throw new Error(`Role template migration should seed role ${key}`);
  }
}
for (const keyPath of [
  ['admin', 'roles', 'applyTemplate'],
  ['admin', 'roles', 'templateApplied'],
  ['admin', 'roles', 'templateNone']
]) {
  if (!valueAt(zh, keyPath)) {
    throw new Error(`Missing zh-CN locale key: ${keyPath.join('.')}`);
  }
  if (!valueAt(en, keyPath)) {
    throw new Error(`Missing en-US locale key: ${keyPath.join('.')}`);
  }
}

const permissionConstants = Object.fromEntries(
  [...seedsSource.matchAll(/^\s*(Permission[A-Za-z]+)\s*=\s*"([^"]+)"/gm)].map(match => [match[1], match[2]])
);
const seedEntries = [...seedsSource.matchAll(/\{\s*Key:\s*(Permission[A-Za-z]+),\s*Module:\s*"([^"]+)"/g)]
  .map((match) => {
    const key = permissionConstants[match[1]];
    if (!key) {
      throw new Error(`Unknown permission constant in SeedPermissions: ${match[1]}`);
    }
    return { key, module: match[2] };
  });

if (seedEntries.length === 0) {
  throw new Error('Failed to parse SeedPermissions from seeds.go');
}

function catalogEntry(localeRoot, permissionKey) {
  return valueAt(localeRoot, ['admin', 'permissionCatalog', ...permissionKey.split('.')]);
}

const expectedModules = new Set(seedEntries.map(entry => entry.module));
for (const locale of [
  ['zh-CN', zh],
  ['en-US', en]
]) {
  const [name, rootLocale] = locale;
  for (const module of expectedModules) {
    if (!valueAt(rootLocale, ['admin', 'permissionModules', module])) {
      throw new Error(`${name} is missing permission module label: admin.permissionModules.${module}`);
    }
  }
  for (const entry of seedEntries) {
    const item = catalogEntry(rootLocale, entry.key);
    if (!item?.label || !item?.description) {
      throw new Error(`${name} is missing permission catalog text for ${entry.key}`);
    }
  }
  // 已废弃的 moderation.report_review 不应继续作为可配置权限文案入口。
  if (valueAt(rootLocale, ['admin', 'permissionCatalog', 'moderation', 'report_review'])) {
    throw new Error(`${name} still ships the retired moderation.report_review catalog entry`);
  }
}

// 路由壳：auth layout + guest middleware + Page Outlet + form island fail-closed。
for (const [name, route, pageId, island] of [
  ['login', loginRoute, 'auth.login', '<SFLoginFormPage'],
  ['register', registerRoute, 'auth.register', '<SFRegisterFormPage']
]) {
  if (!route.includes("layout: 'auth'") || !route.includes("middleware: 'guest'")) {
    throw new Error(`${name} route shell must use auth layout and guest middleware`);
  }
  if (!route.includes('SFPageOutlet') || !route.includes(pageId)) {
    throw new Error(`${name} route shell must mount SFPageOutlet for ${pageId}`);
  }
  if (!route.includes(island)) {
    throw new Error(`${name} route shell must fail-closed to Host form island ${island}`);
  }
}

if (!registerPage.includes(':configuration=')) {
  throw new Error('Registration ALTCHA widget should pass a configuration object');
}
if (!registerPage.includes('hideLogo: altchaWidgetSettings.value.hideLogo')) {
  throw new Error('Registration ALTCHA widget should read logo visibility from runtime settings');
}
if (!registerPage.includes('hideFooter: altchaWidgetSettings.value.hideFooter')) {
  throw new Error('Registration ALTCHA widget should read footer visibility from runtime settings');
}
if (!registerPage.includes('minDuration: altchaWidgetSettings.value.minDuration')) {
  throw new Error('Registration ALTCHA widget should read minimum duration from runtime settings');
}
if (!registerPage.includes("import type { AltchaWidgetElement } from 'altcha'")) {
  throw new Error('Registration ALTCHA widget should use the official AltchaWidgetElement type');
}
if (!registerPage.includes('const altchaWidget = ref<AltchaWidgetElement | null>(null)')) {
  throw new Error('Registration ALTCHA widget should keep a typed template ref');
}
if (!registerPage.includes('humanVerificationEnabled')) {
  throw new Error('Registration ALTCHA widget should be guarded by the configured provider');
}
if (!registerPage.includes("humanVerificationEnabledFor('register')")) {
  throw new Error('Registration ALTCHA widget should read register scenario availability from runtime web options');
}
if (registerPage.includes('public.humanVerificationProvider')) {
  throw new Error('Registration ALTCHA widget should not read provider from Nuxt public runtime config');
}
if (!registerPage.includes('ref="altchaWidget"')) {
  throw new Error('Registration ALTCHA widget should bind the template ref');
}
if (!registerPage.includes('altchaWidget.value?.reset()')) {
  throw new Error('Registration ALTCHA widget should reset the widget instance, not only the token');
}
if (!registerPage.includes("const submittedHumanVerificationToken = humanVerificationEnabled.value ? humanVerificationToken.value : ''")) {
  throw new Error('Registration should only submit ALTCHA tokens when human verification is enabled');
}
if (!registerPage.includes('body.humanVerification')) {
  throw new Error('Registration should omit the human verification payload when ALTCHA is disabled');
}
if (!registerPage.includes("humanVerificationEnabled.value && (submittedHumanVerificationToken || fieldError('humanVerification'))")) {
  throw new Error('Registration should reset ALTCHA after any failed submission that included a token');
}

const altchaWidgetTag = registerPage.match(/<altcha-widget[\s\S]*?\/>/)?.[0] || '';
if (altchaWidgetTag.includes(':key=') || altchaWidgetTag.includes('v-if=')) {
  throw new Error('Registration ALTCHA widget should reset in place instead of remounting');
}

// Autofill 覆盖在 Host 表单岛上（非薄路由壳）。
for (const [name, content] of [
  ['SFLoginFormPage.vue', loginPage],
  ['SFRegisterFormPage.vue', registerPage]
]) {
  if (!content.includes('.auth-input:-webkit-autofill')) {
    throw new Error(`${name} should override browser autofill input background`);
  }
  if (!content.includes('-webkit-box-shadow')) {
    throw new Error(`${name} autofill override should preserve the white input surface`);
  }
  if (!content.includes('-webkit-text-fill-color')) {
    throw new Error(`${name} autofill override should preserve text color`);
  }
}

console.log('Identity UI validation passed.');
